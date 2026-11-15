package tact

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

type Key [16]byte

func ParseKey(value string) (Key, error) {
	var key Key
	data, err := hex.DecodeString(value)
	if err != nil || len(data) != len(key) {
		return key, fmt.Errorf("expected a 32-character hexadecimal key")
	}
	copy(key[:], data)
	return key, nil
}

func (k Key) String() string { return hex.EncodeToString(k[:]) }

type Encoding struct {
	data     []byte
	pages    int
	pageSize int
	start    int
	index    int
}

// Encoding retains the paged table rather than allocating millions of map entries.
func ParseEncoding(data []byte) (*Encoding, error) {
	if len(data) < 22 || string(data[:2]) != "EN" || data[2] != 1 || data[3] != 16 || data[4] != 16 {
		return nil, fmt.Errorf("unsupported encoding table")
	}
	e := &Encoding{data: data, pages: int(binary.BigEndian.Uint32(data[9:13])),
		pageSize: int(binary.BigEndian.Uint16(data[5:7])) * 1024,
		index:    22 + int(binary.BigEndian.Uint32(data[18:22]))}
	if e.pageSize == 0 || e.index > len(data) || e.pages > (len(data)-e.index)/32 {
		return nil, fmt.Errorf("encoding index out of bounds")
	}
	e.start = e.index + e.pages*32
	if e.pages > (len(data)-e.start)/e.pageSize {
		return nil, fmt.Errorf("encoding pages out of bounds")
	}
	return e, nil
}

func (e *Encoding) Lookup(key Key) ([]Key, error) {
	i := sort.Search(e.pages, func(i int) bool {
		return bytes.Compare(e.data[e.index+i*32:e.index+i*32+16], key[:]) > 0
	}) - 1
	if i < 0 {
		return nil, fmt.Errorf("content key %s not in encoding", key)
	}
	page := e.data[e.start+i*e.pageSize : e.start+(i+1)*e.pageSize]
	checksum := md5.Sum(page)
	if !bytes.Equal(checksum[:], e.data[e.index+i*32+16:e.index+(i+1)*32]) {
		return nil, fmt.Errorf("encoding page checksum mismatch")
	}
	for pos := 0; pos < len(page) && page[pos] != 0; {
		n := int(page[pos])
		length := 22 + n*16
		if length > len(page)-pos {
			return nil, fmt.Errorf("truncated encoding entry")
		}
		if bytes.Equal(page[pos+6:pos+22], key[:]) {
			keys := make([]Key, n)
			for j := range keys {
				copy(keys[j][:], page[pos+22+j*16:pos+38+j*16])
			}
			return keys, nil
		}
		pos += length
	}
	return nil, fmt.Errorf("content key %s not in encoding", key)
}

type Root map[uint32]Key

func ParseRoot(data []byte) (Root, error) {
	root := make(Root)
	offset, version, modern := 0, uint32(0), false
	if len(data) >= 12 && string(data[:4]) == "TSFM" {
		modern, offset = true, 12
		if len(data) >= 20 && (binary.LittleEndian.Uint32(data[8:12]) == 1 || binary.LittleEndian.Uint32(data[8:12]) == 2) {
			version = binary.LittleEndian.Uint32(data[8:12])
			offset = int(binary.LittleEndian.Uint32(data[4:8]))
			if offset < 20 || offset > len(data) {
				return nil, fmt.Errorf("invalid root header size")
			}
		}
	}
	for offset < len(data) {
		headerSize := 12
		if version == 2 {
			headerSize = 17
		}
		if len(data)-offset < headerSize {
			return nil, fmt.Errorf("truncated root block")
		}
		block := data[offset:]
		count := int(binary.LittleEndian.Uint32(block))
		flags, locale := binary.LittleEndian.Uint32(block[4:8]), binary.LittleEndian.Uint32(block[8:12])
		if version == 2 {
			locale = binary.LittleEndian.Uint32(block[4:8])
			flags = binary.LittleEndian.Uint32(block[8:12]) | binary.LittleEndian.Uint32(block[12:16]) | uint32(block[16])<<17
		}
		offset += headerSize
		stride := 28
		if modern && flags&0x10000000 != 0 {
			stride = 20
		}
		if count > (len(data)-offset)/stride {
			return nil, fmt.Errorf("root entries exceed bounds")
		}
		ids, keys := offset, offset+count*4
		var id uint64
		for i := 0; i < count; i++ {
			id += uint64(binary.LittleEndian.Uint32(data[ids+4*i:]))
			if id > 0xffffffff {
				return nil, fmt.Errorf("root file ID overflow")
			}
			if flags&(0x80|0x100) == 0 && (locale == 0 || locale&2 != 0) {
				var key Key
				keyOffset := keys + i*16
				if !modern {
					keyOffset = keys + i*24
				}
				copy(key[:], data[keyOffset:keyOffset+16])
				root[uint32(id)] = key
			}
			id++
		}
		offset += count * stride
	}
	if len(root) == 0 {
		return nil, fmt.Errorf("root contains no enUS or locale-neutral assets")
	}
	return root, nil
}

func ParseConfig(data []byte) map[string][]string {
	result := make(map[string][]string)
	for _, line := range strings.Split(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		name, value, ok := strings.Cut(line, "=")
		if ok {
			result[strings.TrimSpace(name)] = strings.Fields(value)
		}
	}
	return result
}

func TableRow(data []byte, field, value string) (map[string]string, error) {
	var columns []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		if columns == nil {
			for _, column := range parts {
				name, _, _ := strings.Cut(column, "!")
				columns = append(columns, name)
			}
			continue
		}
		if len(parts) != len(columns) {
			continue
		}
		row := make(map[string]string)
		for i, column := range columns {
			row[column] = parts[i]
		}
		if row[field] == value {
			return row, nil
		}
	}
	return nil, fmt.Errorf("patch metadata has no %s=%s row", field, value)
}
