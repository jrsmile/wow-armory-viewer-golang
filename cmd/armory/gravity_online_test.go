package main

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/jrsmile/armory/internal/blizzard"
	"github.com/jrsmile/armory/internal/model"
)

// Regression for Theradras/Taddie's belt (waist item 189811): its two
// ribbon ends are PHYS rigid bodies and must hang with world gravity, not
// stick out in their authored bind pose, throughout the idle animation.
// Needs network access; enable with ARMORY_ONLINE_TESTS=1.
func TestOnlineTaddieBeltHangsUnderGravity(t *testing.T) {
	if os.Getenv("ARMORY_ONLINE_TESTS") != "1" {
		t.Skip("set ARMORY_ONLINE_TESTS=1 to download the character from Blizzard")
	}
	cache := os.Getenv("ARMORY_CACHE")
	if cache == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		cache = filepath.Join(dir, "armory", "cdn")
	}
	ctx := context.Background()
	data, err := fetchAppearance(ctx, "https://worldofwarcraft.blizzard.com/de-de/worldsoul/eu/armory/character/theradras/taddie", blizzard.New("", ""))
	if err != nil {
		t.Fatal(err)
	}
	g := &games{cache: cache, region: "eu", log: func(string) {}}
	scene, err := g.character(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	belt := -1
	for i, in := range scene.Instances {
		if len(in.Asset.Model.PhysicsBones()) > 0 {
			if belt >= 0 {
				t.Fatalf("instances %d and %d both simulate; expected only the belt", belt, i)
			}
			belt = i
		}
	}
	if belt < 0 {
		t.Fatalf("no instance loaded PHYS data; warnings: %v", scene.Warnings)
	}
	m := scene.Instances[belt].Asset.Model
	type end struct {
		bone, parent int
		tip          model.Vec3
	}
	var ends []end
	for _, b := range m.PhysicsBones() {
		e := end{bone: b, parent: int(m.Bones[b].Parent)}
		best := float32(-1)
		for _, v := range m.Vertices {
			for j, w := range v.Weights {
				if w >= 200 && int(v.Bones[j]) == b {
					if d := distance(v.Position, m.Bones[b].Pivot); d > best {
						best, e.tip = d, v.Position
					}
				}
			}
		}
		if best <= 0 || e.parent < 0 {
			t.Fatalf("bone %d has no skinned ribbon or anchor", b)
		}
		ends = append(ends, e)
	}
	if len(ends) != 2 {
		t.Fatalf("belt simulates %d bones, want its 2 ribbon ends", len(ends))
	}

	first := make([]model.Vec3, len(ends))
	moved := make([]float32, len(ends))
	for ms := uint32(0); ms <= 20000; ms += 33 {
		f, err := scene.Animate(ms)
		if err != nil {
			t.Fatal(err)
		}
		for k, e := range ends {
			sim := model.Multiply(f.Models[belt], f.Poses[belt][e.bone])
			rigid := model.Multiply(f.Models[belt], f.Poses[belt][e.parent])
			pivot := m.Bones[e.bone].Pivot
			down := direction(apply(sim, pivot), apply(sim, e.tip))[2]
			bind := direction(apply(rigid, pivot), apply(rigid, e.tip))[2]
			if down > -0.85 {
				t.Fatalf("t=%dms bone %d: ribbon direction z=%.3f, want hanging down (<= -0.85)", ms, e.bone, down)
			}
			if down > bind-0.05 {
				t.Fatalf("t=%dms bone %d: simulated z=%.3f not lower than bind pose z=%.3f", ms, e.bone, down, bind)
			}
			tip := apply(sim, e.tip)
			if ms == 0 {
				first[k] = tip
			} else if d := distance(tip, first[k]); d > moved[k] {
				moved[k] = d
			}
		}
	}
	for k, e := range ends {
		if moved[k] < 0.001 {
			t.Errorf("bone %d tip moved only %.4f yd during idle animation; physics is not following motion", e.bone, moved[k])
		}
		t.Logf("bone %d tip travel %.3f yd", e.bone, moved[k])
	}
}

func apply(m model.Mat4, p model.Vec3) model.Vec3 {
	return model.Vec3{
		m[0]*p[0] + m[4]*p[1] + m[8]*p[2] + m[12],
		m[1]*p[0] + m[5]*p[1] + m[9]*p[2] + m[13],
		m[2]*p[0] + m[6]*p[1] + m[10]*p[2] + m[14],
	}
}

func distance(a, b model.Vec3) float32 {
	x, y, z := b[0]-a[0], b[1]-a[1], b[2]-a[2]
	return float32(math.Sqrt(float64(x*x + y*y + z*z)))
}

func direction(a, b model.Vec3) model.Vec3 {
	x, y, z := b[0]-a[0], b[1]-a[1], b[2]-a[2]
	l := float32(math.Sqrt(float64(x*x + y*y + z*z)))
	if l == 0 {
		return model.Vec3{}
	}
	return model.Vec3{x / l, y / l, z / l}
}
