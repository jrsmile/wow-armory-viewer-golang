package db2

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func hotfixFixture(build uint32, entries ...hotfixRecord) []byte {
	var b bytes.Buffer
	b.WriteString("XFTH")
	binary.Write(&b, binary.LittleEndian, uint32(9))
	binary.Write(&b, binary.LittleEndian, build)
	b.Write(make([]byte, 32))
	for _, e := range entries {
		b.WriteString("XFTH")
		binary.Write(&b, binary.LittleEndian, uint32(0)) // region
		binary.Write(&b, binary.LittleEndian, e.pushID)
		binary.Write(&b, binary.LittleEndian, uint32(0)) // unique ID
		binary.Write(&b, binary.LittleEndian, e.tableHash)
		binary.Write(&b, binary.LittleEndian, e.recordID)
		binary.Write(&b, binary.LittleEndian, uint32(len(e.data)))
		if e.valid {
			b.WriteByte(1)
		} else {
			b.WriteByte(0)
		}
		b.Write([]byte{0, 0, 0})
		b.Write(e.data)
	}
	return b.Bytes()
}

func hotfixRow(value int16, scale float32) []byte {
	b := make([]byte, 6)
	binary.LittleEndian.PutUint16(b, uint16(value))
	binary.LittleEndian.PutUint32(b[2:], math.Float32bits(scale))
	return b
}

func TestParseHotfix(t *testing.T) {
	raw := hotfixFixture(70961,
		hotfixRecord{pushID: 2, tableHash: 0x1234, recordID: 7, valid: true, data: hotfixRow(-3, 1.5)},
	)
	h, err := parseHotfix(raw, 70961)
	if err != nil {
		t.Fatal(err)
	}
	if h.version != 9 || h.build != 70961 || len(h.tables[0x1234]) != 1 {
		t.Fatalf("unexpected hotfix header or table map: %+v", h)
	}
	r := h.tables[0x1234][0]
	if r.pushID != 2 || r.recordID != 7 || !r.valid || !bytes.Equal(r.data, hotfixRow(-3, 1.5)) {
		t.Fatalf("unexpected entry: %+v", r)
	}
	if _, err := parseHotfix(raw, 70960); err == nil {
		t.Fatal("mismatched build accepted")
	}
	for _, bad := range [][]byte{nil, raw[:20], append([]byte(nil), raw[:4]...)} {
		if _, err := parseHotfix(bad, 70961); err == nil {
			t.Errorf("malformed DBCache accepted: %x", bad)
		}
	}
}

func TestApplyHotfixes(t *testing.T) {
	table := &Table{
		Name: "Test", stride: 3,
		columns: map[string]column{
			"ID":    {offset: 0, count: 1},
			"Value": {offset: 1, count: 1},
			"Scale": {offset: 2, count: 1, float: true},
		},
		ids:    []uint32{1, 3},
		values: []int64{1, 10, int64(math.Float32bits(1)), 3, 30, int64(math.Float32bits(3))},
		byID:   map[uint32]int{1: 0, 3: 1},
	}
	fields := []Field{
		{Name: "ID", Bits: 32, Signed: true, Count: 1, ID: true, Inline: false},
		{Name: "Value", Bits: 16, Signed: true, Count: 1, Inline: true},
		{Name: "Scale", Bits: 32, Float: true, Count: 1, Inline: true},
	}
	records := []hotfixRecord{
		{pushID: 20, recordID: 1, valid: true, data: hotfixRow(17, 1.75)},
		{pushID: 30, recordID: 3, valid: false},
		{pushID: 15, recordID: 2, valid: true, data: hotfixRow(-2, 2.5)},
		{pushID: 10, recordID: 1, valid: true, data: hotfixRow(15, 1.5)},
	}
	n, err := applyHotfixes(table, fields, records)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || table.Len() != 2 {
		t.Fatalf("applied %d records, table has %d rows", n, table.Len())
	}
	r := table.Lookup(1)
	if r < 0 || table.Int(r, "ID") != 1 || table.Int(r, "Value") != 17 || table.Float(r, "Scale", 0) != 1.75 {
		t.Fatalf("latest update for row 1 not applied: row=%d", r)
	}
	r = table.Lookup(2)
	if r < 0 || table.Int(r, "ID") != 2 || table.Int(r, "Value") != -2 || table.Float(r, "Scale", 0) != 2.5 {
		t.Fatalf("new row 2 not applied: row=%d", r)
	}
	if table.Lookup(3) >= 0 {
		t.Fatal("deleted row 3 remains")
	}
	if rows := table.Where("Value", -2); len(rows) != 1 || rows[0] != r {
		t.Fatalf("indexes were not invalidated and rebuilt: %v", rows)
	}
}

func TestApplyHotfixesIsAtomicOnUnsupportedRow(t *testing.T) {
	table := &Table{
		Name: "Test", stride: 1,
		columns: map[string]column{"Value": {offset: 0, count: 1}},
		ids:     []uint32{1}, values: []int64{6}, byID: map[uint32]int{1: 0},
	}
	fields := []Field{{Name: "Value", Bits: 32, Count: 1, Inline: true}}
	records := []hotfixRecord{
		{pushID: 1, recordID: 1, valid: true, data: []byte{7, 0, 0, 0}},
		{pushID: 2, recordID: 2, valid: true, data: []byte{1}},
	}
	if _, err := applyHotfixes(table, fields, records); err == nil {
		t.Fatal("truncated hotfix row accepted")
	}
	if table.Len() != 1 || table.Int(0, "Value") != 6 || table.Lookup(2) >= 0 {
		t.Fatal("partial changes were applied despite invalid row")
	}
}

func TestDecodeHotfixRowConsumesStrings(t *testing.T) {
	rec := hotfixRecord{recordID: 1, data: []byte{'x', 0, 9, 0, 0, 0}}
	fields := []Field{
		{Name: "Name", Bits: 32, String: true, Count: 1, Inline: true},
		{Name: "Value", Bits: 32, Count: 1, Inline: true},
	}
	row, err := decodeHotfixRow(rec, fields, 2)
	if err != nil {
		t.Fatal(err)
	}
	if row[0] != 0 || row[1] != 9 {
		t.Fatalf("string field did not preserve following numeric value: %v", row)
	}
	rec.data = []byte{'x'}
	if _, err := decodeHotfixRow(rec, fields, 2); err == nil {
		t.Fatal("unterminated hotfix string accepted")
	}
}

func TestLoadHotfixesDownloadsProgressAndReusesCache(t *testing.T) {
	raw := hotfixFixture(70961, hotfixRecord{pushID: 1, tableHash: 4, recordID: 5, valid: true, data: []byte{1}})
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/70961/enUS/DBCache.bin" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Write(raw)
	}))
	defer srv.Close()

	cache := t.TempDir()
	var reports int
	db := New(nil, cache)
	db.HTTP = srv.Client()
	db.hotfixURL = func(uint32) string { return srv.URL + "/70961/enUS/DBCache.bin" }
	if err := db.LoadHotfixes(context.Background(), 70961, func(url string, done, total int64, err error) {
		if url != srv.URL+"/70961/enUS/DBCache.bin" {
			t.Errorf("reported wrong URL %q", url)
		}
		if err == nil && total >= 0 && done == total {
			reports++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || reports == 0 || db.hotfix == nil {
		t.Fatalf("download requests=%d final progress reports=%d hotfix=%v", requests, reports, db.hotfix)
	}
	if _, err := os.Stat(filepath.Join(cache, "hotfix", "70961", "DBCache.bin")); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	cached := New(nil, cache)
	cached.hotfixURL = func(uint32) string { return srv.URL + "/must-not-download" }
	if err := cached.LoadHotfixes(context.Background(), 70961, nil); err != nil {
		t.Fatal(err)
	}
	if cached.hotfix == nil {
		t.Fatal("cached DBCache was not loaded")
	}
}
