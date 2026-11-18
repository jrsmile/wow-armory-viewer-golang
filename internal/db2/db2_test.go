package db2

import (
	"bytes"
	"encoding/binary"
	"testing"
)

const testDBD = `COLUMNS
int ID
int Value
int Kind
float Scale
int ParentID
int Other // not in this layout

LAYOUT 11111111
Other

LAYOUT 0BADF00D, 1234ABCD
BUILD 12.0.0.1
$noninline,id$ID<32>
Value<16>
Kind<u32>
Scale
$noninline,relation$ParentID<32>
`

func TestParseDBD(t *testing.T) {
	fields, err := ParseDBD(testDBD, "1234ABCD")
	if err != nil {
		t.Fatal(err)
	}
	want := []Field{
		{Name: "ID", Bits: 32, Signed: true, Count: 1, ID: true},
		{Name: "Value", Bits: 16, Signed: true, Count: 1, Inline: true},
		{Name: "Kind", Bits: 32, Count: 1, Inline: true},
		{Name: "Scale", Bits: 32, Float: true, Count: 1, Inline: true},
		{Name: "ParentID", Bits: 32, Signed: true, Count: 1, Relation: true},
	}
	if len(fields) != len(want) {
		t.Fatalf("got %+v", fields)
	}
	for i := range want {
		if fields[i] != want[i] {
			t.Errorf("field %d: got %+v want %+v", i, fields[i], want[i])
		}
	}
	if _, err := ParseDBD(testDBD, "DEADBEEF"); err == nil {
		t.Error("unknown layout accepted")
	}
}

func TestParseDBDStringTypes(t *testing.T) {
	fields, err := ParseDBD(`COLUMNS
string Name
locstring Text

LAYOUT 12345678
Name
Text`, "12345678")
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || !fields[0].String || !fields[1].String || fields[0].Float || fields[1].Float {
		t.Fatalf("string types not preserved: %+v", fields)
	}
}

// buildWDC3 writes a one-section WDC3 file with records {ID 5, 9} plus a copy
// of 9 as 12: Value is inline signed 16-bit, Kind a 2-bit pallet index, Scale a
// common-data float (default 1.5, ID 9 = 2.5) and ParentID relationship data.
func buildWDC3() []byte {
	var b bytes.Buffer
	w := func(v ...any) {
		for _, x := range v {
			binary.Write(&b, binary.LittleEndian, x)
		}
	}
	const recordSize, fields = 4, 3
	pallet := []uint32{100, 200, 300}
	common := [][2]uint32{{9, 0x40200000}}
	records := []byte{0xFE, 0xFF, 2, 0, 7, 0, 1, 0} // Value -2 / Kind 300, Value 7 / Kind 200
	ids := []uint32{5, 9}
	copies := [][2]uint32{{12, 9}}

	header := 72 + 40 + 4*fields + 24*fields + 4*len(pallet) + 8*len(common)
	b.WriteString("WDC3")
	w(uint32(2), uint32(fields), uint32(recordSize), uint32(0), uint32(0xAAAA), uint32(0x1234ABCD),
		uint32(5), uint32(12), uint32(0), uint16(4), uint16(0), uint32(fields), uint32(0), uint32(0),
		uint32(24*fields), uint32(8*len(common)), uint32(4*len(pallet)), uint32(1))
	relSize := 12 + 8*2
	w(uint64(0), uint32(header), uint32(2), uint32(0), uint32(0), uint32(4*len(ids)), uint32(relSize), uint32(0), uint32(len(copies)))
	for range fields {
		w(int16(0), uint16(0))
	}
	w(uint16(0), uint16(16), uint32(0), uint32(storageNone), uint32(0), uint32(0), uint32(0))
	w(uint16(16), uint16(2), uint32(4*len(pallet)), uint32(storagePallet), uint32(0), uint32(0), uint32(0))
	w(uint16(18), uint16(0), uint32(8*len(common)), uint32(storageCommon), uint32(0x3FC00000), uint32(0), uint32(0))
	w(pallet, common)
	if b.Len() != header {
		panic("header size")
	}
	w(records, ids, copies)
	w(uint32(2), uint32(0), uint32(0), [2]uint32{77, 0}, [2]uint32{88, 1})
	return b.Bytes()
}

func TestParseWDC3(t *testing.T) {
	fields, err := ParseDBD(testDBD, "1234ABCD")
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := Parse("Test", buildWDC3(), fields)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		value, kind, parent int64
		scale               float32
	}
	want := map[uint32]row{5: {-2, 300, 77, 1.5}, 9: {7, 200, 88, 2.5}, 12: {7, 200, 88, 2.5}}
	if tbl.Len() != len(want) {
		t.Fatalf("got %d rows", tbl.Len())
	}
	for id, w := range want {
		r := tbl.Lookup(id)
		if r < 0 {
			t.Fatalf("id %d missing", id)
		}
		got := row{tbl.Int(r, "Value"), tbl.Int(r, "Kind"), tbl.Int(r, "ParentID"), tbl.Float(r, "Scale", 0)}
		if got != w || tbl.Int(r, "ID") != int64(id) {
			t.Errorf("id %d: got %+v want %+v", id, got, w)
		}
	}
	if rows := tbl.Where("Kind", 200); len(rows) != 2 {
		t.Errorf("Where(Kind=200) = %v", rows)
	}
	if _, err := Parse("Test", buildWDC3()[:100], fields); err == nil {
		t.Error("truncated file accepted")
	}
}

// WoW Forever ships some tables with no records and no storage info.
func TestParseEmptyTable(t *testing.T) {
	fields, err := ParseDBD(testDBD, "1234ABCD")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	b.WriteString("WDC3")
	binary.Write(&b, binary.LittleEndian, [17]uint32{0, 2, 0, 0, 0xAAAA, 0x1234ABCD, 0, 0, 0xFFFFFFFF, 4, 2})
	tbl, err := Parse("Empty", b.Bytes(), fields)
	if err != nil || tbl.Len() != 0 || len(tbl.Where("Kind", 0)) != 0 || tbl.Lookup(1) != -1 {
		t.Fatal(tbl, err)
	}
}
