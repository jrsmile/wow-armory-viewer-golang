package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jrsmile/armory/internal/character"
	"github.com/jrsmile/armory/internal/model"
)

func TestZZParticles(t *testing.T) {
	home, _ := os.UserHomeDir()
	ctx := context.Background()
	s := &session{cache: filepath.Join(home, ".cache/armory/cdn"), region: "eu", log: func(string) {}}
	if err := s.open(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{166033} {
		a, err := model.LoadWith(ctx, s.client, id, model.Options{Cache: &model.TextureCache{}})
		if err != nil {
			t.Fatal(err)
		}
		m := a.Model
		t.Logf("model %d verts %d emitters %d effectAnim %d warnings %v", id, len(m.Vertices), len(m.Emitters), m.EffectAnimID, a.Warnings)
		for i, e := range m.Emitters {
			t.Logf(" e%d flags %x type %d blend %d tex %d (fid %d) rows %d cols %d bone %d pos %v", i, e.Flags, e.Type, e.Blend, e.Texture, m.Textures[e.Texture].FileID, e.Rows, e.Cols, e.Bone, e.Position)
			t.Logf("   rate %v life %v speed %v area %v/%v grav %v", e.Rate.Values, e.Lifespan.Values, e.Speed.Values, e.AreaLength.Values, e.AreaWidth.Values, e.Gravity.Values)
			t.Logf("   color %v alpha %v scale %v twinkle %v %v %v-%v", e.Color.Values, e.Alpha.Values, e.Scale.Values, e.TwinkleSpeed, e.TwinklePercent, e.TwinkleMin, e.TwinkleMax)
		}
		ps := model.NewParticleSystem(m)
		mats := make([]model.Mat4, len(m.Emitters))
		for i := range mats {
			mats[i] = model.Identity()
		}
		ps.Update(0, mats)
		t.Logf(" particles after prewarm %d", ps.Count())
	}
}

func TestZZScene(t *testing.T) {
	home, _ := os.UserHomeDir()
	ctx := context.Background()
	s := &session{cache: filepath.Join(home, ".cache/armory/cdn"), region: "eu", log: func(string) {}}
	if err := s.open(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(os.Getenv("APP"))
	app, err := character.ParseArmory(raw)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := s.character(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	fr, _ := sc.Animate(0)
	for i, in := range sc.Instances {
		m := in.Asset.Model
		if len(m.Emitters) == 0 && in.Parent == 0 && m.EffectAnimID == 0 {
			continue
		}
		mm := fr.Models[i]
		t.Logf("%d %s parent %d bone %d pos %v emitters %d effAnim %d at %v", i, in.Name, in.Parent, in.AttachBone, in.AttachPos, len(m.Emitters), m.EffectAnimID, [3]float32{mm[12], mm[13], mm[14]})
		for k, e := range m.Emitters {
			ps := model.NewParticleSystem(m)
			mats := make([]model.Mat4, len(m.Emitters))
			for j := range mats {
				mats[j] = fr.Emitter(i, m.Emitters[j])
			}
			ps.Update(0, mats)
			t.Logf("   e%d type %d flags %x blend %d tex %d rate %v life %v scale %v count %d", k, e.Type, e.Flags, e.Blend, e.Texture, e.Rate.Values, e.Lifespan.Values, e.Scale.Values, ps.Count())
		}
	}
}
