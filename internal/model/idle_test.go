package model

import "testing"

func idleFixture() *Model {
	constant := func(x float32) Track {
		return Track{Interpolation: 1, Loop: -1, Times: []uint32{0}, Values: []Quat{{x, 0, 0, 0}}}
	}
	return &Model{
		Stand:     0,
		Sequences: []Sequence{{Duration: 1000, Flags: 0x20}, {Variation: 1, Duration: 500, Flags: 0x20}, {Variation: 2, Duration: 700, Flags: 0x20}},
		Idles:     []Clip{{Sequence: 1, Duration: 500}, {Sequence: 2, Duration: 700}},
		Bones: []Bone{{Parent: -1, Flags: 0x200, Translation: constant(0), Idle: []BoneTracks{
			{Translation: constant(1)}, {Translation: constant(2)},
		}}},
	}
}

func TestIdleCycleRotatesFidgetsBetweenStand(t *testing.T) {
	c := NewIdleCycle(idleFixture(), 1)
	var order []int
	last := -1
	for ms := uint32(0); ms < 60000; ms += 10 {
		c.At(ms)
		if clip := c.Clip(); clip != last {
			order = append(order, clip)
			last = clip
		}
	}
	if order[0] != 0 || len(order) < 5 {
		t.Fatal("cycle did not start in Stand or never played fidgets", order)
	}
	for i := 1; i < len(order); i++ {
		if (order[i] == 0) == (order[i-1] == 0) {
			t.Fatal("fidgets must alternate with Stand", order)
		}
	}
	if order[1] == order[3] {
		t.Fatal("a fidget repeated before the other one played", order)
	}
}

func TestIdleCycleCrossFadesAndRestarts(t *testing.T) {
	m := idleFixture()
	c := NewIdleCycle(m, 1)
	c.At(0)
	ms := (c.standUntil + 999) / 1000 * 1000 // first Stand loop end at or after standUntil
	before, after := c.At(ms-1), c.At(ms+1)
	if before.Weight != 0 || before.From.Clip != 0 || after.To.Clip == 0 || after.Weight <= 0 || after.Weight > 0.01 {
		t.Fatal("expected a fade starting at the clip boundary", before, after)
	}
	pose, err := m.PoseBlend(Blend{From: ClipTime{Clip: 0}, To: ClipTime{Clip: 2}, Weight: 0.5}, 0, nil)
	if err != nil || pose[0][12] != 1 {
		t.Fatal("blend should average Stand (0) and fidget (2)", pose[0][12], err)
	}
	if b := c.At(5); b.From.Clip != 0 || b.Weight != 0 || c.Clip() != 0 {
		t.Fatal("going back in time should restart in Stand", b)
	}
}

func TestFidgetWithoutKeysKeepsStandTrack(t *testing.T) {
	m := idleFixture()
	m.Bones[0].Translation.Values[0][0] = 3
	m.Bones[0].Idle[0].Translation = Track{Loop: -1}
	pose, err := m.PoseBlend(Blend{From: ClipTime{Clip: 1, Time: 100}}, 0, nil)
	if err != nil || pose[0][12] != 3 {
		t.Fatal("empty fidget track should fall back to Stand, not the bind pose", pose[0][12], err)
	}
}
