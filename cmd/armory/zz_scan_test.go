package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jrsmile/armory/internal/model"
)

func TestZZScan(t *testing.T) {
	home, _ := os.UserHomeDir()
	ctx := context.Background()
	s := &session{cache: filepath.Join(home, ".cache/armory/cdn"), region: "eu", log: func(string) {}}
	if err := s.open(ctx); err != nil {
		t.Fatal(err)
	}
	mfd, _ := s.db.Table(ctx, "CreatureModelData")
	hits := 0
	for r := mfd.Len() - 1; r >= 0 && r > mfd.Len()-3000 && hits < 2; r -= 7 {
		id := uint32(mfd.Int(r, "FileDataID"))
		raw, err := s.client.File(ctx, id)
		if err != nil {
			continue
		}
		m, err := model.ParseM2(raw)
		if err != nil || m.EffectAnimID == 0 {
			continue
		}
		before := m.Effects(500)
		a, err := model.LoadWith(ctx, s.client, id, model.Options{Cache: &model.TextureCache{}})
		if err != nil {
			t.Logf("%d: %v", id, err)
			continue
		}
		after := a.Model.Effects(500)
		hits++
		t.Logf("model %d anim %d warnings %v emitters %d\n before %+v\n after %+v", id, m.EffectAnimID, a.Warnings, len(a.Model.Emitters), before, after)
	}
}
