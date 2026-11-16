package main

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/jrsmile/armory/internal/model"
	_ "fmt"
)

func TestZZEnchant(t *testing.T) {
	home, _ := os.UserHomeDir()
	ctx := context.Background()
	s := &session{cache: filepath.Join(home, ".cache/armory/cdn"), region: "eu", log: func(string) {}}
	if err := s.open(ctx); err != nil {
		t.Fatal(err)
	}
	se, err := s.db.Table(ctx, "SpellItemEnchantment")
	if err != nil {
		t.Fatal(err)
	}
	iv, err := s.db.Table(ctx, "ItemVisuals")
	if err != nil {
		t.Fatal(err)
	}
	ti, err := s.db.Table(ctx, "TransmogIllusion")
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint32
	for _, e := range []uint32{7981, 213} {
		if r := ti.Lookup(e); r >= 0 {
			t.Logf("illusion %d -> SIE %d", e, ti.Int(r, "SpellItemEnchantmentID"))
			ids = append(ids, uint32(ti.Int(r, "SpellItemEnchantmentID")))
		} else {
			t.Logf("illusion %d missing", e)
		}
	}
	for _, v := range []uint32{7981, 213} {
		if r := iv.Lookup(v); r >= 0 {
			t.Logf("ItemVisuals %d: %v %v %v %v %v", v, iv.At(r, "ModelFileID", 0), iv.At(r, "ModelFileID", 1), iv.At(r, "ModelFileID", 2), iv.At(r, "ModelFileID", 3), iv.At(r, "ModelFileID", 4))
		}
	}
	for r := 0; r < se.Len(); r++ {
		if se.Int(r, "ItemVisual") == 213 {
			t.Logf("SIE %d has visual 213 name? effect %d", se.ID(r), se.At(r, "Effect", 0))
		}
	}
	for _, e := range ids {
		r := se.Lookup(e)
		if r < 0 {
			t.Logf("enchant %d missing", e)
			continue
		}
		vis := se.Int(r, "ItemVisual")
		vr := iv.Lookup(uint32(vis))
		t.Logf("enchant %d visual %d row %d", e, vis, vr)
		if vr < 0 {
			continue
		}
		for i := range 5 {
			id := uint32(iv.At(vr, "ModelFileID", i))
			if id == 0 {
				continue
			}
			raw, err := s.client.File(ctx, id)
			if err != nil {
				t.Logf("  [%d] %d: %v", i, id, err)
				continue
			}
			md := raw
			_ = model.Chunks
			if string(raw[:4]) != "MD20" {
				c, _ := model.Chunks(raw)
				md = c["MD21"]
			}
			t.Logf("  [%d] %d ribbons %d particles %d", i, id,
				binary.LittleEndian.Uint32(md[0x120:]), binary.LittleEndian.Uint32(md[0x128:]))
		}
	}
}
