package db2

import (
	"encoding/binary"
	"fmt"
	"math"
)

var le = binary.LittleEndian

const (
	storageNone = iota
	storageBitpacked
	storageCommon
	storagePallet
	storagePalletArray
	storageBitpackedSigned
)

// Header holds the parts of a WDC header needed to pick a DBD layout.
type Header struct {
	Magic      string
	TableHash  uint32
	LayoutHash uint32
}

func (h Header) Layout() string { return fmt.Sprintf("%08X", h.LayoutHash) }

// ReadHeader decodes the identifying fields of a WDC3/4/5 file.
func ReadHeader(data []byte) (Header, error) {
	if len(data) < 72 {
		return Header{}, fmt.Errorf("db2 too short")
	}
	h := Header{Magic: string(data[:4])}
	base := 4
	switch h.Magic {
	case "WDC3", "WDC4":
	case "WDC5":
		base = 4 + 4 + 128
	default:
		return h, fmt.Errorf("unsupported db2 format %q", h.Magic)
	}
	if len(data) < base+68 {
		return h, fmt.Errorf("db2 header truncated")
	}
	h.TableHash = le.Uint32(data[base+16:])
	h.LayoutHash = le.Uint32(data[base+20:])
	return h, nil
}

type column struct {
	offset int // index of the first element in a row slice
	count  int
	float  bool
}

// Table is a fully decoded DB2 table. Values are stored as int64 (float fields
// keep their IEEE-754 bit pattern).
type Table struct {
	Name    string
	columns map[string]column
	stride  int
	values  []int64
	ids     []uint32
	byID    map[uint32]int
	groups  map[string]map[int64][]int
}

func (t *Table) Len() int          { return len(t.ids) }
func (t *Table) ID(row int) uint32 { return t.ids[row] }

// Row returns the row index for a record ID.
func (t *Table) Row(id uint32) (int, bool) {
	r, ok := t.byID[id]
	return r, ok
}

func (t *Table) col(name string) column {
	c, ok := t.columns[name]
	if !ok {
		panic(fmt.Sprintf("db2: %s has no field %s", t.Name, name))
	}
	return c
}

// Has reports whether the decoded layout contains a field.
func (t *Table) Has(name string) bool { _, ok := t.columns[name]; return ok }

// Int returns element 0 of a field.
func (t *Table) Int(row int, name string) int64 { return t.At(row, name, 0) }

// At returns element i of an array field.
func (t *Table) At(row int, name string, i int) int64 {
	c := t.col(name)
	if i < 0 || i >= c.count {
		return 0
	}
	return t.values[row*t.stride+c.offset+i]
}

func (t *Table) Float(row int, name string, i int) float32 {
	return math.Float32frombits(uint32(t.At(row, name, i)))
}

// Lookup returns the row for id or -1.
func (t *Table) Lookup(id uint32) int {
	if r, ok := t.byID[id]; ok {
		return r
	}
	return -1
}

// Where returns all rows whose field equals value, using a lazily built index.
func (t *Table) Where(name string, value int64) []int {
	g, ok := t.groups[name]
	if !ok {
		c := t.col(name)
		g = make(map[int64][]int)
		for r := range t.ids {
			v := t.values[r*t.stride+c.offset]
			g[v] = append(g[v], r)
		}
		if t.groups == nil {
			t.groups = map[string]map[int64][]int{}
		}
		t.groups[name] = g
	}
	return g[value]
}

type fieldInfo struct {
	offsetBits, sizeBits int
	additional           int
	storage              uint32
	val                  [3]uint32
	pallet               []uint32
	common               map[uint32]uint32
}

// Parse decodes a WDC3/4/5 table with the given DBD fields.
func Parse(name string, data []byte, fields []Field) (t *Table, err error) {
	defer func() {
		if r := recover(); r != nil {
			t, err = nil, fmt.Errorf("db2 %s: malformed file: %v", name, r)
		}
	}()
	h, err := ReadHeader(data)
	if err != nil {
		return nil, err
	}
	p := 4
	if h.Magic == "WDC5" {
		p = 136
	}
	u32 := func() int { v := int(le.Uint32(data[p:])); p += 4; return v }
	recordCount := u32()
	fieldCount := u32()
	recordSize := u32()
	u32() // string table size
	u32() // table hash
	u32() // layout hash
	u32() // min id
	u32() // max id
	u32() // locale
	flags := le.Uint16(data[p:])
	idIndex := int(le.Uint16(data[p+2:]))
	p += 4
	u32() // total field count
	u32() // bitpacked data offset
	u32() // lookup column count
	storageInfoSize := u32()
	commonSize := u32()
	palletSize := u32()
	sectionCount := u32()
	if flags&1 != 0 {
		return nil, fmt.Errorf("db2 %s: sparse tables are not supported", name)
	}
	type section struct {
		tactKey                                                      uint64
		offset, records, strings, idList, relations, offsetMap, copy int
	}
	sections := make([]section, sectionCount)
	for i := range sections {
		s := &sections[i]
		s.tactKey = le.Uint64(data[p:])
		p += 8
		s.offset = u32()
		s.records = u32()
		s.strings = u32()
		u32() // offset records end
		s.idList = u32()
		s.relations = u32()
		s.offsetMap = u32()
		s.copy = u32()
	}
	p += 4 * fieldCount
	infos := make([]fieldInfo, storageInfoSize/24)
	for i := range infos {
		f := &infos[i]
		f.offsetBits = int(le.Uint16(data[p:]))
		f.sizeBits = int(le.Uint16(data[p+2:]))
		f.additional = int(le.Uint32(data[p+4:]))
		f.storage = le.Uint32(data[p+8:])
		f.val = [3]uint32{le.Uint32(data[p+12:]), le.Uint32(data[p+16:]), le.Uint32(data[p+20:])}
		p += 24
	}
	end := p + palletSize
	for i := range infos {
		f := &infos[i]
		if f.storage == storagePallet || f.storage == storagePalletArray {
			f.pallet = make([]uint32, f.additional/4)
			for j := range f.pallet {
				f.pallet[j] = le.Uint32(data[p:])
				p += 4
			}
		}
	}
	if p != end {
		return nil, fmt.Errorf("db2 %s: pallet size mismatch", name)
	}
	end = p + commonSize
	for i := range infos {
		f := &infos[i]
		if f.storage == storageCommon {
			f.common = make(map[uint32]uint32, f.additional/8)
			for j := 0; j < f.additional/8; j++ {
				f.common[le.Uint32(data[p:])] = le.Uint32(data[p+4:])
				p += 8
			}
		}
	}
	if p != end {
		return nil, fmt.Errorf("db2 %s: common data size mismatch", name)
	}

	// Map DBD fields to storage columns.
	t = &Table{Name: name, columns: map[string]column{}, byID: map[uint32]int{}}
	type binding struct {
		col      column
		field    Field
		info     int // storage index, -1 for non-inline
		idList   bool
		relation bool
		// overrideRelation replaces an inline value with relationship data
		overrideRelation bool
	}
	var binds []binding
	idCol, storage := -1, 0
	for _, f := range fields {
		c := column{offset: t.stride, count: f.Count, float: f.Float}
		t.stride += f.Count
		t.columns[f.Name] = c
		b := binding{col: c, field: f, info: -1}
		if f.ID {
			idCol = c.offset
		}
		switch {
		case !f.Inline && f.ID:
			b.idList = true
		case !f.Inline && f.Relation:
			b.relation = true
		case !f.Inline:
		default:
			if storage >= len(infos) {
				return nil, fmt.Errorf("db2 %s: definition has more inline fields than file", name)
			}
			b.info = storage
			storage++
		}
		binds = append(binds, b)
	}
	hasRelation := false
	for _, s := range sections {
		hasRelation = hasRelation || s.relations > 0
	}
	if hasRelation {
		nonInline := false
		for _, b := range binds {
			nonInline = nonInline || b.relation
		}
		// Some DBDs label an inline field as the relation while the file stores
		// the foreign key in relationship data; the file is authoritative.
		for i := range binds {
			if !nonInline && binds[i].field.Relation && binds[i].info >= 0 {
				binds[i].overrideRelation = true
			}
		}
	}
	if storage != len(infos) || storage != fieldCount {
		return nil, fmt.Errorf("db2 %s: definition has %d inline fields, file has %d", name, storage, len(infos))
	}
	if idCol < 0 && idIndex < len(infos) {
		for _, b := range binds {
			if b.info == idIndex {
				idCol = b.col.offset
			}
		}
	}

	padded := make([]byte, recordSize+8)
	t.values = make([]int64, 0, recordCount*t.stride)
	var copies [][2]uint32
	for _, s := range sections {
		if s.records == 0 {
			continue
		}
		recordsAt := s.offset
		q := recordsAt + s.records*recordSize + s.strings
		ids := make([]uint32, s.idList/4)
		for i := range ids {
			ids[i] = le.Uint32(data[q:])
			q += 4
		}
		copyAt := q
		q += s.copy * 8
		q += s.offsetMap * 6
		relations := map[int]uint32{}
		if s.relations > 0 {
			n := int(le.Uint32(data[q:]))
			if 12+8*n <= s.relations {
				for i := 0; i < n; i++ {
					e := q + 12 + 8*i
					relations[int(le.Uint32(data[e+4:]))] = le.Uint32(data[e:])
				}
			}
		}
		if s.tactKey != 0 && allZero(data[recordsAt:recordsAt+s.records*recordSize]) {
			continue // encrypted with an unknown key
		}
		for i := 0; i < s.copy; i++ {
			e := copyAt + 8*i
			copies = append(copies, [2]uint32{le.Uint32(data[e:]), le.Uint32(data[e+4:])})
		}
		for r := 0; r < s.records; r++ {
			copy(padded, data[recordsAt+r*recordSize:recordsAt+(r+1)*recordSize])
			row := make([]int64, t.stride)
			var id uint32
			if r < len(ids) {
				id = ids[r]
			}
			for _, b := range binds {
				switch {
				case b.idList:
					row[b.col.offset] = int64(id)
				case b.relation:
					row[b.col.offset] = int64(relations[r])
				case b.info >= 0:
					readField(row[b.col.offset:b.col.offset+b.col.count], padded, &infos[b.info], b.field, id)
				}
			}
			for _, b := range binds {
				if b.overrideRelation {
					if v, ok := relations[r]; ok {
						row[b.col.offset] = int64(v)
					}
				}
			}
			if len(ids) == 0 && idCol >= 0 {
				id = uint32(row[idCol])
				// common-data fields keyed by ID need the real ID
				for _, b := range binds {
					if b.info >= 0 && infos[b.info].storage == storageCommon {
						readField(row[b.col.offset:b.col.offset+b.col.count], padded, &infos[b.info], b.field, id)
					}
				}
			} else if idCol >= 0 {
				row[idCol] = int64(id)
			}
			if id == 0 {
				continue
			}
			t.byID[id] = len(t.ids)
			t.ids = append(t.ids, id)
			t.values = append(t.values, row...)
		}
	}
	for _, c := range copies {
		src, ok := t.byID[c[1]]
		if !ok || c[0] == 0 {
			continue
		}
		row := append([]int64(nil), t.values[src*t.stride:(src+1)*t.stride]...)
		for _, b := range binds {
			if b.field.ID {
				row[b.col.offset] = int64(c[0])
			}
		}
		t.byID[c[0]] = len(t.ids)
		t.ids = append(t.ids, c[0])
		t.values = append(t.values, row...)
	}
	return t, nil
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

func bits(record []byte, offset, size int) uint64 {
	if size <= 0 {
		return 0
	}
	if size > 57 {
		// Wide fields are always byte aligned.
		v := le.Uint64(record[offset>>3:])
		if size == 64 {
			return v
		}
		return v & (1<<uint(size) - 1)
	}
	v := le.Uint64(record[offset>>3:]) >> uint(offset&7)
	return v & (1<<uint(size) - 1)
}

func extend(v uint64, size int, signed bool) int64 {
	if size >= 64 {
		return int64(v)
	}
	v &= 1<<uint(size) - 1
	if signed && size > 0 && v&(1<<uint(size-1)) != 0 {
		return int64(v) - 1<<uint(size)
	}
	return int64(v)
}

func readField(out []int64, record []byte, info *fieldInfo, f Field, id uint32) {
	switch info.storage {
	case storageNone:
		elem := info.sizeBits / len(out)
		if elem == 0 {
			elem = f.Bits
		}
		for i := range out {
			out[i] = extend(bits(record, info.offsetBits+i*elem, elem), elem, f.Signed && !f.Float)
		}
	case storageBitpacked, storageBitpackedSigned:
		v := bits(record, info.offsetBits, info.sizeBits)
		if info.storage == storageBitpackedSigned {
			out[0] = extend(v, info.sizeBits, true)
		} else {
			out[0] = extend(v, f.Bits, f.Signed && !f.Float && info.sizeBits >= f.Bits)
		}
	case storageCommon:
		v, ok := info.common[id]
		if !ok {
			v = info.val[0]
		}
		out[0] = extend(uint64(v), f.Bits, f.Signed && !f.Float)
	case storagePallet, storagePalletArray:
		index := int(bits(record, info.offsetBits, info.sizeBits))
		n := 1
		if info.storage == storagePalletArray {
			n = int(info.val[2])
		}
		for i := range out {
			if i >= n {
				break
			}
			if j := index*n + i; j < len(info.pallet) {
				out[i] = extend(uint64(info.pallet[j]), f.Bits, f.Signed && !f.Float)
			}
		}
	}
}
