package model

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

var le = binary.LittleEndian

type Vertex struct {
	Position Vec3
	Weights  [4]byte
	Bones    [4]byte
	Normal   Vec3
	UV       [2]float32
}

type Texture struct {
	Type   uint32
	Flags  uint32
	FileID uint32
}

type Batch struct {
	Start   int
	Count   int
	Geoset  uint16
	Texture int
	Blend   uint16
	Flags   uint16
}

type Sequence struct {
	ID        uint16
	Variation uint16
	Duration  uint32
	Flags     uint32
	Alias     uint16
}

type Track struct {
	Interpolation uint16
	Loop          int16
	Times         []uint32
	Values        []Quat
}

type Bone struct {
	CRC         uint32 // boneNameCRC, used to share skeletons between models
	Parent      int16
	Flags       uint32
	Pivot       Vec3
	Translation Track
	Rotation    Track
	Scale       Track
}

// Attachment is a named mount point (helmet, shoulders, hands, ...) on a bone.
type Attachment struct {
	ID       uint32
	Bone     int
	Position Vec3
}

type Model struct {
	Attachments  []Attachment
	Vertices     []Vertex
	Indices      []uint32
	Textures     []Texture
	Batches      []Batch
	Bones        []Bone
	Sequences    []Sequence
	SkinIDs      []uint32
	SkeletonID   uint32
	AnimationIDs map[uint32]uint32
	Stand        int
	StandError   string
	Min, Max     Vec3
	data         []byte
	chunks       map[string][]byte
	loops        []uint32
	boneData     []byte
	boneHeader   int
}

func Chunks(data []byte) (map[string][]byte, error) {
	chunks := make(map[string][]byte)
	for pos := 0; pos < len(data); {
		if len(data)-pos < 8 {
			return nil, fmt.Errorf("truncated chunk header")
		}
		size := int(le.Uint32(data[pos+4 : pos+8]))
		if size > len(data)-pos-8 {
			return nil, fmt.Errorf("chunk exceeds file bounds")
		}
		chunks[string(data[pos:pos+4])] = data[pos+8 : pos+8+size]
		pos += 8 + size
	}
	return chunks, nil
}

func array(data []byte, header, stride int) ([]byte, error) {
	if header < 0 || header+8 > len(data) {
		return nil, fmt.Errorf("array descriptor exceeds bounds")
	}
	count, offset := uint64(le.Uint32(data[header:])), uint64(le.Uint32(data[header+4:]))
	length := count * uint64(stride)
	if offset > uint64(len(data)) || length > uint64(len(data))-offset {
		return nil, fmt.Errorf("array exceeds file bounds")
	}
	return data[int(offset):int(offset+length)], nil
}
func f32(data []byte, offset int) float32 { return math.Float32frombits(le.Uint32(data[offset:])) }
func vec(data []byte, offset int) Vec3 {
	return Vec3{f32(data, offset), f32(data, offset+4), f32(data, offset+8)}
}
func finite(v float32) bool { return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) }
func uints(data []byte) ([]uint32, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("misaligned FileDataID chunk")
	}
	out := make([]uint32, len(data)/4)
	for i := range out {
		out[i] = le.Uint32(data[i*4:])
	}
	return out, nil
}

func ParseM2(raw []byte) (*Model, error) {
	m := &Model{Stand: -1, AnimationIDs: make(map[uint32]uint32), Min: Vec3{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32}, Max: Vec3{-math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}}
	if len(raw) < 4 {
		return nil, fmt.Errorf("truncated M2")
	}
	data := raw
	if string(raw[:4]) != "MD20" {
		var err error
		m.chunks, err = Chunks(raw)
		if err != nil {
			return nil, err
		}
		data = m.chunks["MD21"]
	}
	if len(data) < 0x130 || string(data[:4]) != "MD20" {
		return nil, fmt.Errorf("missing MD20 model data")
	}
	version := le.Uint32(data[4:8])
	if version < 264 || version > 274 {
		return nil, fmt.Errorf("unsupported M2 version %d", version)
	}
	m.data, m.boneData, m.boneHeader = data, data, 0x2c
	vertices, err := array(data, 0x3c, 48)
	if err != nil {
		return nil, err
	}
	if len(vertices) == 0 || len(vertices)/48 > 1_000_000 {
		return nil, fmt.Errorf("invalid vertex count")
	}
	m.Vertices = make([]Vertex, len(vertices)/48)
	for i := range m.Vertices {
		raw := vertices[i*48 : (i+1)*48]
		v := &m.Vertices[i]
		v.Position, v.Normal = vec(raw, 0), vec(raw, 20)
		copy(v.Weights[:], raw[12:16])
		copy(v.Bones[:], raw[16:20])
		v.UV = [2]float32{f32(raw, 32), f32(raw, 36)}
		for j, p := range v.Position {
			if !finite(p) || !finite(v.Normal[j]) {
				return nil, fmt.Errorf("non-finite vertex")
			}
			m.Min[j] = min(m.Min[j], p)
			m.Max[j] = max(m.Max[j], p)
		}
		if !finite(v.UV[0]) || !finite(v.UV[1]) {
			return nil, fmt.Errorf("non-finite UV")
		}
	}
	textures, err := array(data, 0x50, 16)
	if err != nil {
		return nil, err
	}
	m.Textures = make([]Texture, len(textures)/16)
	textureIDs, err := uints(m.chunks["TXID"])
	if err != nil {
		return nil, err
	}
	for i := range m.Textures {
		t := &m.Textures[i]
		t.Type, t.Flags = le.Uint32(textures[i*16:]), le.Uint32(textures[i*16+4:])
		if i < len(textureIDs) {
			t.FileID = textureIDs[i]
		}
	}
	m.SkinIDs, err = uints(m.chunks["SFID"])
	if err != nil {
		return nil, err
	}
	if sk := m.chunks["SKID"]; len(sk) >= 4 {
		m.SkeletonID = le.Uint32(sk)
	}
	if err := m.readAnimationIDs(m.chunks["AFID"]); err != nil {
		return nil, err
	}
	if err := m.readSequences(data, 0x14, 0x1c); err != nil {
		return nil, err
	}
	if m.Attachments, err = readAttachments(data, 0xF0); err != nil {
		return nil, err
	}
	return m, nil
}

func readAttachments(data []byte, header int) ([]Attachment, error) {
	raw, err := array(data, header, 40)
	if err != nil {
		return nil, fmt.Errorf("attachments: %w", err)
	}
	out := make([]Attachment, len(raw)/40)
	for i := range out {
		r := raw[i*40:]
		out[i] = Attachment{ID: le.Uint32(r), Bone: int(le.Uint16(r[4:])), Position: vec(r, 8)}
	}
	return out, nil
}

// FindAttachment returns the attachment with the given ID.
func (m *Model) FindAttachment(id uint32) (Attachment, bool) {
	for _, a := range m.Attachments {
		if a.ID == id && a.Bone < max(1, len(m.Bones)) {
			return a, true
		}
	}
	return Attachment{}, false
}

func (m *Model) readAnimationIDs(data []byte) error {
	if len(data)%8 != 0 {
		return fmt.Errorf("truncated AFID")
	}
	for i := 0; i < len(data); i += 8 {
		key := uint32(le.Uint16(data[i:]))<<16 | uint32(le.Uint16(data[i+2:]))
		m.AnimationIDs[key] = le.Uint32(data[i+4:])
	}
	return nil
}

func (m *Model) readSequences(data []byte, loopsHeader, sequencesHeader int) error {
	raw, err := array(data, loopsHeader, 4)
	if err != nil {
		return err
	}
	m.loops, err = uints(raw)
	if err != nil {
		return err
	}
	raw, err = array(data, sequencesHeader, 64)
	if err != nil {
		return err
	}
	m.Sequences = make([]Sequence, len(raw)/64)
	for i := range m.Sequences {
		r := raw[i*64:]
		m.Sequences[i] = Sequence{le.Uint16(r), le.Uint16(r[2:]), le.Uint32(r[4:]), le.Uint32(r[12:]), le.Uint16(r[62:])}
	}
	return nil
}

func (m *Model) SetSkeleton(data []byte) (uint32, error) {
	chunks, err := Chunks(data)
	if err != nil {
		return 0, err
	}
	bones, seq := chunks["SKB1"], chunks["SKS1"]
	if len(bones) < 16 || len(seq) < 24 {
		return 0, fmt.Errorf("skeleton lacks SKB1 or SKS1")
	}
	m.boneData, m.boneHeader = bones, 0
	if err := m.readSequences(seq, 0, 8); err != nil {
		return 0, err
	}
	if att := chunks["SKA1"]; len(att) >= 16 {
		if m.Attachments, err = readAttachments(att, 0); err != nil {
			return 0, err
		}
	}
	if err := m.readAnimationIDs(chunks["AFID"]); err != nil {
		return 0, err
	}
	var parent uint32
	if data := chunks["SKPD"]; len(data) >= 12 {
		parent = le.Uint32(data[8:])
	}
	return parent, nil
}

func (m *Model) MergeParentAnimations(data []byte) (uint32, error) {
	chunks, err := Chunks(data)
	if err != nil {
		return 0, err
	}
	if err := m.readAnimationIDs(chunks["AFID"]); err != nil {
		return 0, err
	}
	if data := chunks["SKPD"]; len(data) >= 12 {
		return le.Uint32(data[8:]), nil
	}
	return 0, nil
}

func (m *Model) StandFileID() (uint32, error) {
	m.Stand = -1
	for i, s := range m.Sequences {
		if s.ID == 0 && s.Variation == 0 {
			m.Stand = i
			break
		}
	}
	if m.Stand < 0 {
		return 0, fmt.Errorf("model has no Stand (animation 0) sequence")
	}
	seen := make(map[int]bool)
	for m.Sequences[m.Stand].Flags&0x40 != 0 {
		if seen[m.Stand] {
			return 0, fmt.Errorf("cyclic animation alias")
		}
		seen[m.Stand] = true
		m.Stand = int(m.Sequences[m.Stand].Alias)
		if m.Stand >= len(m.Sequences) {
			return 0, fmt.Errorf("animation alias exceeds bounds")
		}
	}
	s := m.Sequences[m.Stand]
	if s.Duration == 0 {
		return 0, fmt.Errorf("Stand has zero duration")
	}
	if s.Flags&0x20 != 0 {
		return 0, nil
	}
	id := m.AnimationIDs[uint32(s.ID)<<16|uint32(s.Variation)]
	if id == 0 {
		return 0, fmt.Errorf("Stand is external but has no AFID mapping")
	}
	return id, nil
}

// PrepareStand reads bones and their Stand animation tracks. With m.Stand < 0
// it reads the bind pose (plus global-sequence tracks) only.
func (m *Model) PrepareStand(external []byte) error {
	valueData := m.boneData
	if m.Stand >= 0 && m.Sequences[m.Stand].Flags&0x20 == 0 {
		if len(external) == 0 {
			return fmt.Errorf("Stand animation file is missing")
		}
		valueData = external
		if len(external) >= 4 && string(external[:4]) == "AFM2" {
			chunks, err := Chunks(external)
			if err != nil {
				return err
			}
			valueData = chunks["AFM2"]
		}
	}
	raw, err := array(m.boneData, m.boneHeader, 88)
	if err != nil {
		return err
	}
	if len(raw)/88 > 256 {
		return fmt.Errorf("GPU skinning supports at most 256 bones")
	}
	m.Bones = make([]Bone, len(raw)/88)
	for i := range m.Bones {
		offset := int(le.Uint32(m.boneData[m.boneHeader+4:])) + i*88
		r := raw[i*88:]
		b := &m.Bones[i]
		b.Flags, b.Parent, b.Pivot = le.Uint32(r[4:]), int16(le.Uint16(r[8:])), vec(r, 76)
		b.CRC = le.Uint32(r[12:])
		if b.Parent < -1 || int(b.Parent) >= len(m.Bones) {
			return fmt.Errorf("invalid bone parent")
		}
		for _, p := range b.Pivot {
			if !finite(p) {
				return fmt.Errorf("non-finite bone pivot")
			}
		}
		b.Translation, err = m.readTrack(offset+16, 12, valueData)
		if err != nil {
			return fmt.Errorf("bone %d translation: %w", i, err)
		}
		b.Rotation, err = m.readTrack(offset+36, 8, valueData)
		if err != nil {
			return fmt.Errorf("bone %d rotation: %w", i, err)
		}
		b.Scale, err = m.readTrack(offset+56, 12, valueData)
		if err != nil {
			return fmt.Errorf("bone %d scale: %w", i, err)
		}
	}
	for _, v := range m.Vertices {
		for j, w := range v.Weights {
			if w != 0 && int(v.Bones[j]) >= len(m.Bones) {
				return fmt.Errorf("vertex references missing bone")
			}
		}
	}
	_, err = m.Pose(0)
	return err
}

func (m *Model) readTrack(offset, stride int, external []byte) (Track, error) {
	data := m.boneData
	t := Track{Interpolation: le.Uint16(data[offset:]), Loop: int16(le.Uint16(data[offset+2:]))}
	if t.Interpolation > 1 {
		return Track{}, nil // spline tracks: keep bind pose
	}
	times, err := array(data, offset+4, 8)
	if err != nil {
		return t, err
	}
	values, err := array(data, offset+12, 8)
	if err != nil {
		return t, err
	}
	index := m.Stand
	valueData := external
	if index >= 0 && m.Sequences[index].Flags&0x20 != 0 {
		valueData = data
	}
	if t.Loop >= 0 {
		if int(t.Loop) >= len(m.loops) || m.loops[t.Loop] == 0 {
			return Track{}, nil
		}
		index = 0
		valueData = data
	}
	if (len(times) == 0 && len(values) == 0) || index < 0 {
		return t, nil
	}
	if index >= len(times)/8 || index >= len(values)/8 {
		return t, fmt.Errorf("animation track sequence missing")
	}
	readNested := func(descriptor []byte, stride int) ([]byte, error) {
		count, off := uint64(le.Uint32(descriptor)), uint64(le.Uint32(descriptor[4:]))
		length := count * uint64(stride)
		if off > uint64(len(valueData)) || length > uint64(len(valueData))-off {
			return nil, fmt.Errorf("external animation track exceeds bounds")
		}
		return valueData[off : off+length], nil
	}
	ts, err := readNested(times[index*8:], 4)
	if err != nil {
		return t, err
	}
	vs, err := readNested(values[index*8:], stride)
	if err != nil {
		return t, err
	}
	if len(ts)/4 != len(vs)/stride {
		return t, fmt.Errorf("track time and value counts differ")
	}
	t.Times = make([]uint32, len(ts)/4)
	t.Values = make([]Quat, len(t.Times))
	for i := range t.Times {
		t.Times[i] = le.Uint32(ts[i*4:])
		if i > 0 && t.Times[i] <= t.Times[i-1] {
			return t, fmt.Errorf("track timestamps are not increasing")
		}
		for j := range 4 {
			if stride == 8 {
				v := int32(int16(le.Uint16(vs[i*8+j*2:])))
				if v < 0 {
					t.Values[i][j] = float32(v+32768) / 32767
				} else {
					t.Values[i][j] = float32(v-32767) / 32767
				}
			} else if j < 3 {
				t.Values[i][j] = f32(vs, i*stride+j*4)
			}
			if !finite(t.Values[i][j]) {
				return t, fmt.Errorf("non-finite animation value")
			}
		}
	}
	return t, nil
}

func (m *Model) sample(t Track, ms uint32, fallback Quat, rotation bool) Quat {
	if len(t.Times) == 0 {
		return fallback
	}
	if t.Loop >= 0 {
		ms %= m.loops[t.Loop]
	}
	i := sort.Search(len(t.Times), func(i int) bool { return t.Times[i] > ms }) - 1
	if i < 0 {
		return t.Values[0]
	}
	if i == len(t.Times)-1 || t.Interpolation == 0 {
		return t.Values[i]
	}
	f := float32(ms-t.Times[i]) / float32(t.Times[i+1]-t.Times[i])
	if rotation {
		return interpolateQuat(t.Values[i], t.Values[i+1], f)
	}
	var result Quat
	for j := range result {
		result[j] = t.Values[i][j] + (t.Values[i+1][j]-t.Values[i][j])*f
	}
	return result
}

func (m *Model) Pose(milliseconds uint32) ([]Mat4, error) {
	return m.PoseWith(milliseconds, nil)
}

// PoseWith computes bone matrices; bones for which override returns true use
// the given matrix instead (used to drive a model from another skeleton).
func (m *Model) PoseWith(milliseconds uint32, override func(int) (Mat4, bool)) ([]Mat4, error) {
	pose := make([]Mat4, max(1, len(m.Bones)))
	if len(m.Bones) == 0 {
		pose[0] = Identity()
		return pose, nil
	}
	ms := milliseconds
	if m.Stand >= 0 {
		ms %= m.Sequences[m.Stand].Duration
	}
	state := make([]byte, len(m.Bones))
	var visit func(int) error
	visit = func(i int) error {
		if state[i] == 2 {
			return nil
		}
		if state[i] == 1 {
			return fmt.Errorf("cyclic bone hierarchy")
		}
		state[i] = 1
		if override != nil {
			if fixed, ok := override(i); ok {
				pose[i] = fixed
				state[i] = 2
				return nil
			}
		}
		b := m.Bones[i]
		transform := Identity()
		if b.Flags&0x200 != 0 {
			trackTime := func(t Track) uint32 {
				if t.Loop >= 0 {
					return milliseconds
				}
				return ms
			}
			t := m.sample(b.Translation, trackTime(b.Translation), Quat{}, false)
			s := m.sample(b.Scale, trackTime(b.Scale), Quat{1, 1, 1, 0}, false)
			r := m.sample(b.Rotation, trackTime(b.Rotation), Quat{0, 0, 0, 1}, true)
			transform = Multiply(Translate(Vec3{b.Pivot[0] + t[0], b.Pivot[1] + t[1], b.Pivot[2] + t[2]}),
				Multiply(Rotation(normalize(r)), Multiply(Scale(Vec3{s[0], s[1], s[2]}), Translate(Vec3{-b.Pivot[0], -b.Pivot[1], -b.Pivot[2]}))))
		}
		if b.Parent >= 0 {
			if err := visit(int(b.Parent)); err != nil {
				return err
			}
			transform = Multiply(pose[b.Parent], transform)
		}
		pose[i] = transform
		state[i] = 2
		return nil
	}
	for i := range m.Bones {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return pose, nil
}

func (m *Model) SetSkin(data []byte) error {
	if len(data) < 48 || string(data[:4]) != "SKIN" {
		return fmt.Errorf("invalid SKIN")
	}
	lookup, err := array(data, 4, 2)
	if err != nil {
		return err
	}
	indices, err := array(data, 12, 2)
	if err != nil {
		return err
	}
	if len(indices)%6 != 0 {
		return fmt.Errorf("SKIN index count is not triangular")
	}
	m.Indices = make([]uint32, len(indices)/2)
	for i := range m.Indices {
		index := int(le.Uint16(indices[i*2:]))
		if index >= len(lookup)/2 {
			return fmt.Errorf("SKIN local index exceeds bounds")
		}
		m.Indices[i] = uint32(le.Uint16(lookup[index*2:]))
		if int(m.Indices[i]) >= len(m.Vertices) {
			return fmt.Errorf("SKIN vertex exceeds bounds")
		}
	}
	sections, err := array(data, 28, 48)
	if err != nil {
		return err
	}
	batches, err := array(data, 36, 24)
	if err != nil {
		return err
	}
	materials, err := array(m.data, 0x70, 4)
	if err != nil {
		return err
	}
	textureLookup, err := array(m.data, 0x80, 2)
	if err != nil {
		return err
	}
	for i := 0; i < len(batches); i += 24 {
		batch := batches[i:]
		section := int(le.Uint16(batch[4:]))
		material := int(le.Uint16(batch[10:]))
		texture := int(le.Uint16(batch[16:]))
		if section >= len(sections)/48 || material >= len(materials)/4 || texture >= len(textureLookup)/2 {
			return fmt.Errorf("SKIN batch reference exceeds bounds")
		}
		s := sections[section*48:]
		start := int(le.Uint16(s[8:])) | int(le.Uint16(s[2:]))<<16
		count := int(le.Uint16(s[10:]))
		texture = int(le.Uint16(textureLookup[texture*2:]))
		if start > len(m.Indices) || count > len(m.Indices)-start || count%3 != 0 || texture >= len(m.Textures) {
			return fmt.Errorf("SKIN batch range exceeds bounds")
		}
		m.Batches = append(m.Batches, Batch{start, count, le.Uint16(s), texture, le.Uint16(materials[material*4+2:]), le.Uint16(materials[material*4:])})
	}
	return nil
}
