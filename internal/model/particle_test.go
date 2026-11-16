package model

import (
	"math"
	"testing"
)

func constTrack(v float32) Track { return Track{Loop: -1, Times: []uint32{0}, Values: []Quat{{v}}} }

func TestEffectTracksFromExternalAnimation(t *testing.T) {
	data := fixtureM2()
	le.PutUint32(data[0x130+48*3+12:], 0) // Stand keyframes live in a .anim file
	weight := len(data)
	data = append(data, make([]byte, 20)...)
	descriptor(data, 0x58, 1, weight)
	values := make([]byte, 4)
	le.PutUint16(values[2:], 32767)
	data = appendTrack(data, weight, values)
	anim := append([]byte(nil), data...)
	le.PutUint16(data[len(data)-2:], 0) // the model's copy must not be used
	m, err := ParseM2(data)
	if err != nil {
		t.Fatal(err)
	}
	if w := m.Effects(500).Weights[0]; w != 1 {
		t.Fatalf("weight without .anim = %v, want default 1", w)
	}
	chunk := append([]byte("AFM2\x00\x00\x00\x00"), anim...)
	le.PutUint32(chunk[4:], uint32(len(anim)))
	if err := m.SetEffectAnimation(chunk); err != nil {
		t.Fatal(err)
	}
	if w := m.Effects(1500).Weights[0]; math.Abs(float64(w-0.5)) > 0.01 {
		t.Fatalf("weight from .anim = %v, want 0.5", w)
	}
}

func TestCompressedGravity(t *testing.T) {
	if g := compressedGravity(0, 0, 100); math.Abs(float64(g[2]-4.238648)) > 1e-4 || g[0] != 0 {
		t.Errorf("up gravity = %v", g)
	}
	if g := compressedGravity(0, 0, -100); math.Abs(float64(g[2]+4.238648)) > 1e-4 {
		t.Errorf("negative magnitude must point down: %v", g)
	}
}

func TestParticleSystemSpawnsAgesAndBuildsQuads(t *testing.T) {
	e := Emitter{
		Type: 1, Blend: 4, Rows: 2, Cols: 2, Bone: 0,
		Rate: constTrack(10), Lifespan: constTrack(1), Speed: constTrack(1), Enabled: Track{Loop: -1},
		Alpha: PartTrack{Times: []float32{0, 1}, Values: []Quat{{1}, {0}}},
		Scale: PartTrack{Times: []float32{0}, Values: []Quat{{0.5, 0.5}}},
	}
	m := &Model{Emitters: []Emitter{e}}
	ps := NewParticleSystem(m)
	at := Translate(Vec3{0, 0, 5})
	ps.Update(0, []Mat4{at})
	if n := ps.Count(); n < 7 || n > 12 {
		t.Fatalf("pre-warmed particles = %d, want about rate*lifespan = 10", n)
	}
	for _, q := range ps.states[0].particles {
		if q.age >= q.life || q.pos[2] < 5 {
			t.Fatalf("particle %+v outside its lifespan or emitted below the emitter", q)
		}
	}
	ps.Update(5000, []Mat4{at})
	if n := ps.Count(); n > 12 {
		t.Fatalf("expired particles not removed: %d live", n)
	}
	b := ps.Batches(Identity(), []Mat4{at})
	if len(b) != 1 || len(b[0].Vertices) != ps.Count()*54 || b[0].Blend != 4 {
		t.Fatalf("batches = %d, want one quad (6 x 9 floats) per particle", len(b))
	}
	v := b[0].Vertices
	if w := v[18] - v[0]; math.Abs(float64(w-1)) > 1e-4 {
		t.Errorf("quad width = %v, want 2 x scale", w)
	}
	if u := v[21] - v[3]; math.Abs(float64(u-0.5)) > 1e-4 {
		t.Errorf("atlas cell width = %v, want 1/cols", u)
	}
	m.Emitters[0].Enabled = constTrack(0)
	for ms := uint32(5500); ms <= 7000; ms += 500 {
		ps.Update(ms, []Mat4{at})
	}
	if ps.Count() != 0 {
		t.Errorf("disabled emitter kept spawning: %d", ps.Count())
	}
}
