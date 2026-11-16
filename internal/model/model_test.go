package model

import (
	"encoding/binary"
	"image/color"
	"math"
	"strings"
	"testing"
)

func putFloat(data []byte, offset int, value float32) {
	le.PutUint32(data[offset:], math.Float32bits(value))
}
func descriptor(data []byte, header, count, offset int) {
	le.PutUint32(data[header:], uint32(count))
	le.PutUint32(data[header+4:], uint32(offset))
}

func fixtureM2() []byte {
	data := make([]byte, 0x130+48*3+64+88+16+4+2+8*2+4*2+12*2)
	copy(data, "MD20")
	le.PutUint32(data[4:], 274)
	vertices := 0x130
	descriptor(data, 0x3c, 3, vertices)
	for i := 0; i < 3; i++ {
		p := vertices + i*48
		putFloat(data, p, float32(i))
		putFloat(data, p+28, 1)
		data[p+12] = 255
	}
	sequence := vertices + 48*3
	descriptor(data, 0x1c, 1, sequence)
	le.PutUint32(data[sequence+4:], 1000)
	le.PutUint32(data[sequence+12:], 0x20)
	bone := sequence + 64
	descriptor(data, 0x2c, 1, bone)
	le.PutUint32(data[bone+4:], 0x200)
	le.PutUint16(data[bone+8:], 0xffff)
	texture := bone + 88
	descriptor(data, 0x50, 1, texture)
	material := texture + 16
	descriptor(data, 0x70, 1, material)
	texLookup := material + 4
	descriptor(data, 0x80, 1, texLookup)
	timesArray := texLookup + 2
	valuesArray := timesArray + 8
	times := valuesArray + 8
	values := times + 8
	for _, off := range []int{bone + 16, bone + 36, bone + 56} {
		le.PutUint16(data[off+2:], 0xffff)
	}
	le.PutUint16(data[bone+16:], 1)
	descriptor(data, bone+20, 1, timesArray)
	descriptor(data, bone+28, 1, valuesArray)
	descriptor(data, timesArray, 2, times)
	descriptor(data, valuesArray, 2, values)
	le.PutUint32(data[times+4:], 1000)
	putFloat(data, values+12, 2)
	return data
}

func skinFixture() []byte {
	data := make([]byte, 48+6+6+48+24)
	copy(data, "SKIN")
	descriptor(data, 4, 3, 48)
	descriptor(data, 12, 3, 54)
	descriptor(data, 28, 1, 60)
	descriptor(data, 36, 1, 108)
	for i := 0; i < 3; i++ {
		le.PutUint16(data[48+i*2:], uint16(i))
		le.PutUint16(data[54+i*2:], uint16(i))
	}
	le.PutUint16(data[60+10:], 3)
	return data
}

func TestActualStandTrackAndLoop(t *testing.T) {
	m, err := ParseM2(fixtureM2())
	if err != nil {
		t.Fatal(err)
	}
	if id, err := m.StandFileID(); err != nil || id != 0 {
		t.Fatal(id, err)
	}
	if err := m.PrepareStand(nil); err != nil {
		t.Fatal(err)
	}
	if err := m.SetSkin(skinFixture()); err != nil {
		t.Fatal(err)
	}
	pose, err := m.Pose(500)
	if err != nil || math.Abs(float64(pose[0][12]-1)) > 1e-5 {
		t.Fatal("wrong authentic track interpolation", pose, err)
	}
	loop, err := m.Pose(1500)
	if err != nil || loop[0] != pose[0] {
		t.Fatal("Stand did not loop recorded duration", loop, err)
	}
	if len(m.Indices) != 3 || m.Batches[0].Count != 3 {
		t.Fatal("wrong skin")
	}
}

func TestMalformedM2SkinAndHierarchy(t *testing.T) {
	data := fixtureM2()
	le.PutUint32(data[0x40:], 0xffffffff)
	if _, err := ParseM2(data); err == nil {
		t.Fatal("accepted invalid vertex offset")
	}

	m, err := ParseM2(fixtureM2())
	if err != nil {
		t.Fatal(err)
	}
	skin := skinFixture()
	le.PutUint16(skin[54:], 10)
	if err := m.SetSkin(skin); err == nil {
		t.Fatal("accepted invalid skin index")
	}
	m.Bones = []Bone{{Parent: 0}}
	if _, err := m.Pose(0); err == nil {
		t.Fatal("accepted cyclic bone hierarchy")
	}
}

func TestExternalStandAndIndependentGlobalLoop(t *testing.T) {
	data := fixtureM2()
	sequence := 0x130 + 48*3
	le.PutUint32(data[sequence+12:], 0)
	m, err := ParseM2(data)
	if err != nil {
		t.Fatal(err)
	}
	m.AnimationIDs[0] = 99
	if id, err := m.StandFileID(); err != nil || id != 99 {
		t.Fatal(id, err)
	}
	if err := m.PrepareStand(data); err != nil {
		t.Fatal(err)
	}
	pose, err := m.Pose(500)
	if err != nil || pose[0][12] != 1 {
		t.Fatal("external Stand data was not used", pose, err)
	}
	m.loops = []uint32{2000}
	m.Bones[0].Translation = Track{
		Interpolation: 1, Loop: 0,
		Times: []uint32{0, 2000}, Values: []Quat{{}, {2, 0, 0, 0}},
	}
	pose, err = m.Pose(1500)
	if err != nil || pose[0][12] != 1.5 {
		t.Fatal("global animation loop incorrectly followed Stand duration", pose, err)
	}
}

func TestChunkOffsetsAndExternalStandRequired(t *testing.T) {
	data := fixtureM2()
	sequence := 0x130 + 48*3
	le.PutUint32(data[sequence+12:], 0)
	chunk := make([]byte, 8+len(data)+12)
	copy(chunk, "MD21")
	le.PutUint32(chunk[4:], uint32(len(data)))
	copy(chunk[8:], data)
	copy(chunk[8+len(data):], "SFID")
	le.PutUint32(chunk[12+len(data):], 4)
	le.PutUint32(chunk[16+len(data):], 99)
	m, err := ParseM2(chunk)
	if err != nil || len(m.SkinIDs) != 1 || m.SkinIDs[0] != 99 {
		t.Fatal(m, err)
	}
	if _, err := m.StandFileID(); err == nil || !strings.Contains(err.Error(), "AFID") {
		t.Fatal("missing external data silently accepted", err)
	}
}

func TestBLPFormatsAndBounds(t *testing.T) {
	blp := func(encoding, alpha, mode byte, size int) []byte {
		data := make([]byte, 148+size)
		copy(data, "BLP2")
		le.PutUint32(data[4:], 1)
		data[8], data[9], data[10] = encoding, alpha, mode
		le.PutUint32(data[12:], 1)
		le.PutUint32(data[16:], 1)
		le.PutUint32(data[20:], 148)
		le.PutUint32(data[84:], uint32(size))
		return data
	}
	data := blp(3, 8, 0, 4)
	copy(data[148:], []byte{10, 20, 30, 40})
	img, err := DecodeBLP(data)
	if err != nil || img.NRGBAAt(0, 0) != (color.NRGBA{30, 20, 10, 40}) {
		t.Fatal(img, err)
	}
	for _, mode := range []byte{0, 1, 7} {
		size := 8
		if mode != 0 {
			size = 16
		}
		data := blp(2, 8, mode, size)
		off := 148
		if mode != 0 {
			off += 8
		}
		binary.LittleEndian.PutUint16(data[off:], 0xf800)
		if mode == 1 {
			data[148] = 0x0f
		}
		if mode == 7 {
			data[148] = 255
		}
		img, err := DecodeBLP(data)
		if err != nil || img.NRGBAAt(0, 0) != (color.NRGBA{255, 0, 0, 255}) {
			t.Fatal(mode, img, err)
		}
		if _, err := DecodeBLP(data[:len(data)-1]); err == nil {
			t.Fatal("accepted truncated texture")
		}
	}
	data = blp(3, 8, 0, 4)
	le.PutUint32(data[12:], 0xffffffff)
	if _, err := DecodeBLP(data); err == nil {
		t.Fatal("accepted huge texture")
	}
	palette := make([]byte, 1174)
	copy(palette, blp(1, 4, 0, 0))
	le.PutUint32(palette[20:], 1172)
	le.PutUint32(palette[84:], 2)
	copy(palette[148+4:], []byte{10, 20, 30, 0})
	palette[1172], palette[1173] = 1, 8
	img, err = DecodeBLP(palette)
	if err != nil || img.NRGBAAt(0, 0) != (color.NRGBA{30, 20, 10, 136}) {
		t.Fatal("incorrect palette/4-bit alpha", img, err)
	}
}

func FuzzModel(f *testing.F) {
	f.Add(fixtureM2())
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		m, err := ParseM2(data)
		if err == nil {
			_, err = m.StandFileID()
			if err == nil {
				_ = m.PrepareStand(nil)
			}
		}
	})
}

func TestShaderIDMapping(t *testing.T) {
	for _, c := range []struct {
		count       int
		id          uint16
		pixel, vert int
	}{
		{1, 0x8000, 12, 3}, {1, 0x8003, 15, 6}, {2, 0x4011, 6, 2},
		{2, 0x4014, 7, 2}, {1, 0x10, 1, 0}, {2, 0xe, 4, 3},
	} {
		if p, v := PixelShader(c.count, c.id), VertexShader(c.count, c.id); p != c.pixel || v != c.vert {
			t.Errorf("shader %#x/%d = ps %d vs %d, want ps %d vs %d", c.id, c.count, p, v, c.pixel, c.vert)
		}
	}
}

// appendTrack appends an M2Track with two keys at 0 and 1000 ms to data and
// writes its header at header.
func appendTrack(data []byte, header int, values []byte) []byte {
	base := len(data)
	data = append(data, make([]byte, 8+8+8)...)
	data = append(data, values...)
	le.PutUint16(data[header:], 1)
	le.PutUint16(data[header+2:], 0xffff)
	descriptor(data, header+4, 1, base)
	descriptor(data, header+12, 1, base+8)
	descriptor(data, base, 2, base+16)
	descriptor(data, base+8, 2, base+24)
	le.PutUint32(data[base+20:], 1000)
	return data
}

func TestEffectWeightAndTextureScroll(t *testing.T) {
	data := fixtureM2()
	weight := len(data)
	data = append(data, make([]byte, 20+60)...)
	transform := weight + 20
	descriptor(data, 0x58, 1, weight)
	descriptor(data, 0x60, 1, transform)
	for _, off := range []int{transform + 20, transform + 40} {
		le.PutUint16(data[off+2:], 0xffff)
	}
	weights := make([]byte, 4)
	le.PutUint16(weights[2:], 32767)
	data = appendTrack(data, weight, weights)
	translation := make([]byte, 24)
	putFloat(translation, 12, 1)
	data = appendTrack(data, transform, translation)
	m, err := ParseM2(data)
	if err != nil {
		t.Fatal(err)
	}
	fx := m.Effects(1500) // wraps to 500 ms in the 1000 ms Stand loop
	if len(fx.Weights) != 1 || math.Abs(float64(fx.Weights[0]-0.5)) > 0.01 {
		t.Fatalf("weights = %v, want [0.5]", fx.Weights)
	}
	if len(fx.Textures) != 1 || math.Abs(float64(fx.Textures[0][12]-0.5)) > 0.01 {
		t.Fatalf("texture matrix = %v, want u offset 0.5", fx.Textures)
	}
}
