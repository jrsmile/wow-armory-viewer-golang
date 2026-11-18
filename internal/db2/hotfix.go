package db2

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	raidbotsHotfixBaseURL = "https://storage.googleapis.com/raidbots-static/wow/"
	maxHotfixFileSize     = 128 << 20
)

var hotfixCacheMu sync.Mutex

type HotfixProgress func(url string, done, total int64, err error)

type hotfixFile struct {
	version uint32
	build   uint32
	tables  map[uint32][]hotfixRecord
}

type hotfixRecord struct {
	pushID    int32
	tableHash uint32
	recordID  uint32
	valid     bool
	data      []byte
}

func raidbotsHotfixURL(build uint32) string {
	return fmt.Sprintf("%s%d/enUS/DBCache.bin", raidbotsHotfixBaseURL, build)
}

// LoadHotfixes loads the verified Raidbots DBCache for the exact game build.
// A valid cached copy is reused; download or format failures are returned so
// callers can warn and continue with the base CDN tables.
func (d *DB) LoadHotfixes(ctx context.Context, build uint32, progress HotfixProgress) error {
	if build == 0 {
		return fmt.Errorf("invalid zero game build")
	}
	var path string
	if d.CacheDir != "" {
		path = filepath.Join(d.CacheDir, "hotfix", fmt.Sprint(build), "DBCache.bin")
		if info, err := os.Stat(path); err == nil && info.Size() <= maxHotfixFileSize {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if h, parseErr := parseHotfix(b, build); parseErr == nil {
				d.hotfix = h
				return nil
			} else if d.Log != nil {
				d.Log(fmt.Sprintf("cached hotfix file ignored: %v", parseErr))
			}
		} else if err == nil && d.Log != nil {
			d.Log(fmt.Sprintf("cached hotfix file ignored: exceeds %d bytes", maxHotfixFileSize))
		}
	}

	url := d.hotfixURL(build)
	b, err := d.downloadHotfix(ctx, url, progress)
	if err != nil {
		return err
	}
	h, err := parseHotfix(b, build)
	if err != nil {
		return err
	}
	d.hotfix = h
	if path != "" {
		if err := writeHotfixCache(path, b); err != nil && d.Warn != nil {
			d.Warn(fmt.Sprintf("could not cache online hotfixes: %v", err))
		}
	}
	return nil
}

func (d *DB) downloadHotfix(ctx context.Context, url string, progress HotfixProgress) ([]byte, error) {
	if progress != nil {
		progress(url, 0, -1, nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		reportHotfixProgress(progress, url, 0, -1, err)
		return nil, err
	}
	resp, err := d.HTTP.Do(req)
	if err != nil {
		reportHotfixProgress(progress, url, 0, -1, err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("Raidbots hotfix cache: HTTP %d", resp.StatusCode)
		reportHotfixProgress(progress, url, 0, -1, err)
		return nil, err
	}
	if resp.ContentLength > maxHotfixFileSize {
		err := fmt.Errorf("Raidbots hotfix cache exceeds %d bytes", maxHotfixFileSize)
		reportHotfixProgress(progress, url, 0, resp.ContentLength, err)
		return nil, err
	}
	var b []byte
	buf := make([]byte, 32<<10)
	total := resp.ContentLength
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if len(b)+n > maxHotfixFileSize {
				err := fmt.Errorf("Raidbots hotfix cache exceeds %d bytes", maxHotfixFileSize)
				reportHotfixProgress(progress, url, int64(len(b)), total, err)
				return nil, err
			}
			b = append(b, buf[:n]...)
			if progress != nil {
				progress(url, int64(len(b)), total, nil)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			reportHotfixProgress(progress, url, int64(len(b)), total, readErr)
			return nil, readErr
		}
	}
	if progress != nil {
		n := int64(len(b))
		progress(url, n, n, nil)
	}
	return b, nil
}

func reportHotfixProgress(progress HotfixProgress, url string, done, total int64, err error) {
	if progress != nil {
		progress(url, done, total, err)
	}
}

func writeHotfixCache(path string, data []byte) error {
	hotfixCacheMu.Lock()
	defer hotfixCacheMu.Unlock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".DBCache-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmp, path)
}

func parseHotfix(data []byte, expectedBuild uint32) (*hotfixFile, error) {
	if len(data) < 44 || string(data[:4]) != "XFTH" {
		return nil, fmt.Errorf("invalid DBCache header")
	}
	version := binary.LittleEndian.Uint32(data[4:8])
	build := binary.LittleEndian.Uint32(data[8:12])
	if version != 9 {
		return nil, fmt.Errorf("unsupported DBCache version %d (want 9)", version)
	}
	if build != expectedBuild {
		return nil, fmt.Errorf("DBCache build %d does not match game build %d", build, expectedBuild)
	}
	h := &hotfixFile{version: version, build: build, tables: map[uint32][]hotfixRecord{}}
	for pos := 44; pos < len(data); {
		if pos+32 > len(data) || string(data[pos:pos+4]) != "XFTH" {
			return nil, fmt.Errorf("invalid DBCache entry at offset %d", pos)
		}
		pos += 4
		pushID := int32(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		tableHash := binary.LittleEndian.Uint32(data[pos+12 : pos+16])
		recordID := binary.LittleEndian.Uint32(data[pos+16 : pos+20])
		dataSize := binary.LittleEndian.Uint32(data[pos+20 : pos+24])
		valid := data[pos+24] == 1
		pos += 28
		if uint64(pos)+uint64(dataSize) > uint64(len(data)) {
			return nil, fmt.Errorf("truncated DBCache row at offset %d", pos)
		}
		row := append([]byte(nil), data[pos:pos+int(dataSize)]...)
		pos += int(dataSize)
		h.tables[tableHash] = append(h.tables[tableHash], hotfixRecord{
			pushID: pushID, tableHash: tableHash, recordID: recordID, valid: valid, data: row,
		})
	}
	return h, nil
}

// applyHotfixes stages all changed rows before replacing table contents, so an
// unsupported/malformed record leaves the parsed CDN table untouched.
func applyHotfixes(t *Table, fields []Field, records []hotfixRecord) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	ordered := append([]hotfixRecord(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].pushID < ordered[j].pushID })

	rows := make(map[uint32][]int64, t.Len())
	for i, id := range t.ids {
		rows[id] = append([]int64(nil), t.values[i*t.stride:(i+1)*t.stride]...)
	}
	applied := 0
	for _, rec := range ordered {
		if rec.valid && len(rec.data) > 0 {
			row, err := decodeHotfixRow(rec, fields, t.stride)
			if err != nil {
				return 0, fmt.Errorf("record %d: %w", rec.recordID, err)
			}
			rows[rec.recordID] = row
		} else {
			delete(rows, rec.recordID)
		}
		applied++
	}
	ids := make([]uint32, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	values := make([]int64, 0, len(ids)*t.stride)
	byID := make(map[uint32]int, len(ids))
	for _, id := range ids {
		byID[id] = len(byID)
		values = append(values, rows[id]...)
	}
	t.ids, t.values, t.byID, t.groups = ids, values, byID, nil
	return applied, nil
}

func decodeHotfixRow(rec hotfixRecord, fields []Field, stride int) ([]int64, error) {
	row := make([]int64, stride)
	pos, offset := 0, 0
	for _, f := range fields {
		if f.Count < 1 || offset+f.Count > stride {
			return nil, fmt.Errorf("field %s has invalid width", f.Name)
		}
		if f.String {
			for i := 0; i < f.Count; i++ {
				end := pos
				for end < len(rec.data) && rec.data[end] != 0 {
					end++
				}
				if end == len(rec.data) {
					return nil, fmt.Errorf("unterminated string field %s", f.Name)
				}
				pos = end + 1
			}
			offset += f.Count
			continue
		}
		if !f.Inline && f.ID {
			if f.Count != 1 {
				return nil, fmt.Errorf("non-inline ID array %s is unsupported", f.Name)
			}
			row[offset] = int64(rec.recordID)
			offset += f.Count
			continue
		}
		if !f.Inline && !f.Relation {
			offset += f.Count
			continue
		}
		if f.Bits != 8 && f.Bits != 16 && f.Bits != 32 && f.Bits != 64 {
			return nil, fmt.Errorf("field %s has unsupported width %d", f.Name, f.Bits)
		}
		if f.Float && f.Bits != 32 {
			return nil, fmt.Errorf("float field %s has unsupported width %d", f.Name, f.Bits)
		}
		bytesPerValue := f.Bits / 8
		for i := 0; i < f.Count; i++ {
			if pos+bytesPerValue > len(rec.data) {
				return nil, fmt.Errorf("truncated field %s", f.Name)
			}
			var value uint64
			switch f.Bits {
			case 8:
				value = uint64(rec.data[pos])
			case 16:
				value = uint64(binary.LittleEndian.Uint16(rec.data[pos:]))
			case 32:
				value = uint64(binary.LittleEndian.Uint32(rec.data[pos:]))
			case 64:
				value = binary.LittleEndian.Uint64(rec.data[pos:])
			}
			pos += bytesPerValue
			if f.Float {
				row[offset+i] = int64(uint32(value))
			} else if f.Signed {
				row[offset+i] = signExtend(value, f.Bits)
			} else {
				row[offset+i] = int64(value)
			}
		}
		offset += f.Count
	}
	if offset != stride {
		return nil, fmt.Errorf("definition describes %d values, table has stride %d", offset, stride)
	}
	return row, nil
}

func signExtend(v uint64, bits int) int64 {
	if bits == 64 {
		return int64(v)
	}
	shift := 64 - bits
	return int64(v<<shift) >> shift
}
