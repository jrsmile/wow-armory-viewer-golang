package model

import (
	"fmt"
	"math"
	"sort"
)

// Material shading follows the client's M2 combiner model as reimplemented
// by WebWowViewerCpp (the renderer behind wow.tools): a batch's shader_id and
// texture count select a vertex shader (UV / environment-map generation) and
// a pixel shader (how up to four textures are combined into a diffuse colour
// plus an additive, unlit "specular" glow).

// m2Shaders is the client's M2ShaderTable: {pixel, vertex} for shader IDs
// with the 0x8000 bit set.
var m2Shaders = [...][2]int{
	{12, 3}, {13, 3}, {14, 3}, {15, 6}, {16, 3}, {13, 7}, {16, 7}, {17, 3}, {18, 3}, {19, 6},
	{20, 7}, {21, 3}, {22, 3}, {23, 3}, {23, 7}, {20, 2}, {24, 3}, {25, 6}, {26, 0}, {33, 9},
	{27, 11}, {6, 12}, {28, 2}, {29, 7}, {25, 11}, {33, 13}, {30, 14}, {31, 2}, {32, 14}, {34, 7},
	{35, 15}, {35, 16}, {0, 0}, {7, 12}, {1, 9}, {36, 12},
}

// PixelShader returns the M2 pixel shader (combiner) index for a batch.
func PixelShader(textureCount int, shaderID uint16) int {
	if shaderID&0x8000 != 0 {
		if i := int(shaderID & 0x7FFF); i < len(m2Shaders) {
			return m2Shaders[i][0]
		}
		return 0
	}
	if textureCount <= 1 {
		if shaderID&0x70 != 0 {
			return 1
		}
		return 0
	}
	if shaderID&0x70 != 0 {
		return [...]int{11, 6, 6, 8, 7, 6, 9, 10}[shaderID&7]
	}
	return [...]int{5, 2, 2, 13, 3, 2, 4, 13}[shaderID&7]
}

// VertexShader returns the M2 vertex shader (texture coordinate mode).
func VertexShader(textureCount int, shaderID uint16) int {
	if shaderID&0x8000 != 0 {
		if i := int(shaderID & 0x7FFF); i < len(m2Shaders) {
			return m2Shaders[i][1]
		}
		return 0
	}
	if textureCount <= 1 {
		switch {
		case shaderID&0x80 != 0:
			return 1
		case shaderID&0x4000 != 0:
			return 10
		}
		return 0
	}
	if shaderID&0x80 == 0 {
		switch {
		case shaderID&8 != 0:
			return 3
		case shaderID&0x4000 != 0:
			return 2
		}
		return 7
	}
	if shaderID&8 != 0 {
		return 5
	}
	return 4
}

// TextureTransform animates texture coordinates (scrolling, rotating glows).
type TextureTransform struct {
	Translation, Rotation, Scale Track
}

// ColorTrack is an animated mesh colour (rgb) and opacity.
type ColorTrack struct {
	Color, Alpha Track
}

// Effects holds the animated material state of a model at one time.
type Effects struct {
	Colors   [][4]float32
	Weights  []float32
	Textures []Mat4
}

// effectTracks are the model's own material and particle animations. They
// are keyed by the model file's sequences and global loops, which differ
// from the skeleton's when the model uses an external .skel file.
type effectTracks struct {
	colors     []ColorTrack
	weights    []Track
	transforms []TextureTransform
	loops      []uint32
	duration   uint32
	seq        int    // the model file's Stand sequence; -1 if it has none
	inModel    bool   // seq's keyframes are stored in the model file
	values     []byte // keyframe buffer of an external seq (.anim file)
}

const (
	trackUint8      = 1
	trackFixed16    = 2
	trackFloat      = 4
	trackVec3       = 12
	trackQuat       = 16
	trackGravity    = 0x104 // CompressedParticleGravity: int8 x, y; int16 z
	trackKindStride = 0xff
)

// readEffects locates the model file's Stand sequence and parses colours,
// texture weights, texture transforms and particle emitters. When Stand's
// keyframes live in an external .anim file, EffectAnimID names it and only
// global-loop tracks animate until SetEffectAnimation supplies the file.
func (m *Model) readEffects() {
	fx := &m.effects
	fx.loops = append([]uint32(nil), m.loops...)
	fx.seq = -1
	for i, s := range m.Sequences {
		if s.ID != 0 || s.Variation != 0 {
			continue
		}
		for hops := 0; s.Flags&0x40 != 0 && hops < len(m.Sequences); hops++ {
			if i = int(s.Alias); i >= len(m.Sequences) {
				break
			}
			s = m.Sequences[i]
		}
		if i < len(m.Sequences) && s.Flags&0x40 == 0 && s.Duration > 0 {
			fx.seq, fx.duration, fx.inModel = i, s.Duration, s.Flags&0x20 != 0
			if !fx.inModel {
				m.EffectAnimID = m.AnimationIDs[uint32(s.ID)<<16|uint32(s.Variation)]
			}
		}
		break
	}
	m.parseEffects()
}

// SetEffectAnimation supplies the .anim file holding the model's Stand
// keyframes for material and particle tracks (see EffectAnimID).
func (m *Model) SetEffectAnimation(raw []byte) error {
	values := raw
	if len(raw) >= 4 && string(raw[:4]) == "AFM2" {
		chunks, err := Chunks(raw)
		if err != nil {
			return err
		}
		if values = chunks["AFM2"]; len(values) == 0 {
			return fmt.Errorf("animation file lacks AFM2 data")
		}
	}
	m.effects.values = values
	m.parseEffects()
	return nil
}

func (m *Model) effectTrack(offset, kind int) Track {
	fx := &m.effects
	t, err := readEffectTrack(m.data, fx.values, offset, kind, fx.seq, fx.inModel, fx.loops)
	if err != nil {
		return Track{Loop: -1}
	}
	return t
}

func (m *Model) parseEffects() {
	fx, data := &m.effects, m.data
	fx.colors, fx.weights, fx.transforms = nil, nil, nil
	track := m.effectTrack
	if raw, err := array(data, 0x48, 40); err == nil {
		base := int(le.Uint32(data[0x4c:]))
		for i := range len(raw) / 40 {
			o := base + i*40
			fx.colors = append(fx.colors, ColorTrack{track(o, trackVec3), track(o+20, trackFixed16)})
		}
	}
	if raw, err := array(data, 0x58, 20); err == nil {
		base := int(le.Uint32(data[0x5c:]))
		for i := range len(raw) / 20 {
			fx.weights = append(fx.weights, track(base+i*20, trackFixed16))
		}
	}
	if raw, err := array(data, 0x60, 60); err == nil {
		base := int(le.Uint32(data[0x64:]))
		for i := range len(raw) / 60 {
			o := base + i*60
			fx.transforms = append(fx.transforms, TextureTransform{track(o, trackVec3), track(o+20, trackQuat), track(o+40, trackVec3)})
		}
	}
	m.Emitters = m.readEmitters()
}

// readEffectTrack reads an M2Track from the model file for sequence seq (or
// its global loop). Keyframes of an external sequence come from values (the
// .anim file); without it the track is left empty. Spline keys keep their
// value and drop the tangents.
func readEffectTrack(data, values []byte, offset, kind, seq int, inModel bool, loops []uint32) (Track, error) {
	if offset < 0 || offset+20 > len(data) {
		return Track{Loop: -1}, fmt.Errorf("track header exceeds bounds")
	}
	t := Track{Interpolation: le.Uint16(data[offset:]), Loop: int16(le.Uint16(data[offset+2:]))}
	size := kind & trackKindStride
	stride := size
	if t.Interpolation > 1 {
		stride, t.Interpolation = size*3, 1
	}
	times, err := array(data, offset+4, 8)
	if err != nil {
		return t, err
	}
	keys, err := array(data, offset+12, 8)
	if err != nil {
		return t, err
	}
	index, source := seq, values
	if inModel {
		source = data
	}
	if t.Loop >= 0 {
		if int(t.Loop) >= len(loops) || loops[t.Loop] == 0 {
			t.Loop = -1
			return t, nil
		}
		index, source = 0, data
	}
	if index < 0 || index >= len(times)/8 || index >= len(keys)/8 || source == nil {
		return t, nil
	}
	tsData, err := nested(source, times[index*8:], 4)
	if err != nil {
		return t, err
	}
	vsData, err := nested(source, keys[index*8:], stride)
	if err != nil {
		return t, err
	}
	if len(tsData)/4 != len(vsData)/stride {
		return t, fmt.Errorf("track time and value counts differ")
	}
	t.Times = make([]uint32, len(tsData)/4)
	t.Values = make([]Quat, len(t.Times))
	for i := range t.Times {
		t.Times[i] = le.Uint32(tsData[i*4:])
		if i > 0 && t.Times[i] <= t.Times[i-1] {
			return t, fmt.Errorf("track timestamps are not increasing")
		}
		v := vsData[i*stride:]
		switch kind {
		case trackUint8:
			t.Values[i][0] = float32(v[0])
		case trackFixed16:
			t.Values[i][0] = float32(int16(le.Uint16(v))) / 32767
		case trackFloat:
			t.Values[i][0] = f32(v, 0)
		case trackGravity:
			g := compressedGravity(int8(v[0]), int8(v[1]), int16(le.Uint16(v[2:])))
			t.Values[i] = Quat{g[0], g[1], g[2], 0}
		case trackVec3:
			t.Values[i] = Quat{f32(v, 0), f32(v, 4), f32(v, 8), 0}
		case trackQuat:
			t.Values[i] = Quat{f32(v, 0), f32(v, 4), f32(v, 8), f32(v, 12)}
		}
		for _, c := range t.Values[i] {
			if !finite(c) {
				return t, fmt.Errorf("non-finite effect value")
			}
		}
	}
	return t, nil
}

// compressedGravity decodes a particle gravity vector: x, y are the
// direction's horizontal components in 1/128 units and z its signed
// magnitude (as in WebWowViewerCpp's convertHelper).
func compressedGravity(x, y int8, z int16) Vec3 {
	dx, dy := float32(x)/128, float32(y)/128
	dz := float32(math.Sqrt(math.Max(0, float64(1-dx*dx-dy*dy))))
	mag := float32(z) * 0.04238648
	if mag < 0 {
		dz, mag = -dz, -mag
	}
	return Vec3{dx * mag, dy * mag, dz * mag}
}

func nested(data, descriptor []byte, stride int) ([]byte, error) {
	count, off := uint64(le.Uint32(descriptor)), uint64(le.Uint32(descriptor[4:]))
	length := count * uint64(stride)
	if off > uint64(len(data)) || length > uint64(len(data))-off {
		return nil, fmt.Errorf("track exceeds bounds")
	}
	return data[off : off+length], nil
}

// sampleTrack interpolates a track; global-loop tracks wrap at their loop.
func sampleTrack(t Track, ms uint32, loops []uint32, fallback Quat, rotation bool) Quat {
	if len(t.Times) == 0 {
		return fallback
	}
	if t.Loop >= 0 && int(t.Loop) < len(loops) && loops[t.Loop] > 0 {
		ms %= loops[t.Loop]
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

// sampleEffect evaluates a model-file track at ms: sequence tracks loop over the
// model's Stand, global-loop tracks over their own loop.
func (m *Model) sampleEffect(t Track, ms uint32, fallback Quat, rotation bool) Quat {
	fx := &m.effects
	if t.Loop < 0 && fx.duration > 0 {
		ms %= fx.duration
	}
	return sampleTrack(t, ms, fx.loops, fallback, rotation)
}

// Effects evaluates mesh colours, texture weights and texture matrices at
// ms (the model's Stand loop and global sequences).
func (m *Model) Effects(ms uint32) Effects {
	fx := &m.effects
	at := func(t Track, fallback Quat, rotation bool) Quat { return m.sampleEffect(t, ms, fallback, rotation) }
	out := Effects{Colors: make([][4]float32, len(fx.colors)), Weights: make([]float32, len(fx.weights)), Textures: make([]Mat4, len(fx.transforms))}
	for i, c := range fx.colors {
		rgb, a := at(c.Color, Quat{1, 1, 1, 0}, false), at(c.Alpha, Quat{1}, false)
		out.Colors[i] = [4]float32{rgb[0], rgb[1], rgb[2], a[0]}
	}
	for i, w := range fx.weights {
		out.Weights[i] = at(w, Quat{1}, false)[0]
	}
	pivot, back := Translate(Vec3{0.5, 0.5, 0}), Translate(Vec3{-0.5, -0.5, 0})
	for i, tt := range fx.transforms {
		mat := Identity()
		if len(tt.Rotation.Times) > 0 {
			mat = Multiply(mat, Multiply(pivot, Multiply(Rotation(normalize(at(tt.Rotation, Quat{0, 0, 0, 1}, true))), back)))
		}
		if len(tt.Scale.Times) > 0 {
			s := at(tt.Scale, Quat{1, 1, 1, 0}, false)
			mat = Multiply(mat, Multiply(pivot, Multiply(Scale(Vec3{s[0], s[1], s[2]}), back)))
		}
		if len(tt.Translation.Times) > 0 {
			v := at(tt.Translation, Quat{}, false)
			mat = Multiply(mat, Translate(Vec3{v[0], v[1], v[2]}))
		}
		out.Textures[i] = mat
	}
	return out
}
