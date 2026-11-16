package model

import "testing"

func TestSceneSharesSkeletonAndAttaches(t *testing.T) {
	lift := Track{Loop: -1, Times: []uint32{0}, Values: []Quat{{0, 0, 2, 0}}}
	root := &Model{Stand: -1, Bones: []Bone{
		{CRC: 10, Parent: -1},
		{CRC: 20, Parent: 0, Flags: 0x200, Pivot: Vec3{1, 0, 0}, Translation: lift},
	}}
	// The cloth model has its own copy of bone 20 (different index), plus a
	// bone the root lacks, which must keep its own animation.
	cloth := &Model{Stand: -1, Bones: []Bone{
		{CRC: 99, Parent: -1},
		{CRC: 20, Parent: -1, Pivot: Vec3{1, 0, 0}},
	}}
	helm := &Model{Stand: -1}
	bm := MapBones(cloth, root)
	if bm[0] != -1 || bm[1] != 1 {
		t.Fatalf("MapBones = %v", bm)
	}
	s := &Scene{Instances: []*Instance{
		{Asset: &Asset{Model: root}},
		{Asset: &Asset{Model: cloth}, BoneMap: bm},
		{Asset: &Asset{Model: helm}, Attached: true, AttachBone: 1, AttachPos: Vec3{0, 0, 1}},
		{Asset: &Asset{Model: &Model{Stand: -1}}, Attached: true, AttachPos: Vec3{1, 0, 0}, Parent: 3},
	}}
	f, err := s.Animate(500)
	if err != nil {
		t.Fatal(err)
	}
	if got := Transform(f.Poses[0][1], Vec3{}); got != (Vec3{0, 0, 2}) {
		t.Fatalf("root bone not animated: %v", got)
	}
	if f.Poses[1][1] != f.Poses[0][1] || f.Poses[1][0] != Identity() {
		t.Error("cloth did not follow the shared root bone")
	}
	if got := Transform(f.Models[2], Vec3{}); got != (Vec3{0, 0, 3}) {
		t.Errorf("attachment at %v, want bone transform plus offset", got)
	}
	if got := Transform(f.Models[3], Vec3{}); got != (Vec3{1, 0, 3}) {
		t.Errorf("effect attached to the helm at %v, want helm transform plus offset", got)
	}
	if f.Models[0] != Identity() || f.Models[1] != Identity() {
		t.Error("unattached instances must use the identity model matrix")
	}
}

func TestInstanceVisible(t *testing.T) {
	all := &Instance{}
	some := &Instance{Geosets: map[uint16]bool{0: true, 1301: false}}
	if !all.Visible(1302) || !some.Visible(0) || some.Visible(1301) || some.Visible(5) {
		t.Error("geoset filtering")
	}
}
