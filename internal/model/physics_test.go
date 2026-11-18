package model

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func physicsTestChunk(name string, data []byte, reversed bool) []byte {
	out := make([]byte, 8)
	if reversed {
		name = string([]byte{name[3], name[2], name[1], name[0]})
	}
	copy(out, name)
	le.PutUint32(out[4:], uint32(len(data)))
	return append(out, data...)
}

func physicsFixture() []byte {
	capsule, shape, bodies, shoulder, joint := make([]byte, 28), make([]byte, 32), make([]byte, 96), make([]byte, 124), make([]byte, 16)
	putFloat(capsule, 0, 0.05)
	putFloat(capsule, 12, 0.4)
	putFloat(capsule, 24, 0.025)
	le.PutUint16(shape, 1)
	putFloat(shape, 16, 1000)
	for i := range 2 {
		o := i * 48
		le.PutUint16(bodies[o:], uint16(i))
		le.PutUint16(bodies[o+2:], uint16(i))
		putFloat(bodies, o+24, 1)
		putFloat(bodies, o+28, 1)
		putFloat(bodies, o+32, 2)
		putFloat(bodies, o+36, 3)
		putFloat(bodies, o+40, 0.5)
	}
	le.PutUint16(bodies[44:], 0x8000)
	putFloat(bodies, 48+12, 1)
	le.PutUint32(bodies[48+20:], 1)
	frame := Rotation(Quat{0, sin(math.Pi / 4), 0, cos(math.Pi / 4)})
	for i := range 2 {
		for c := range 3 {
			for r := range 3 {
				putFloat(shoulder, i*48+(c*3+r)*4, frame[c*4+r])
			}
		}
	}
	putFloat(shoulder, 44, 1)
	putFloat(shoulder, 96, -20)
	putFloat(shoulder, 100, 20)
	putFloat(shoulder, 104, 120)
	le.PutUint32(joint[4:], 1)
	le.PutUint16(joint[12:], 1)
	var out []byte
	for _, chunk := range []struct {
		name string
		data []byte
	}{
		{"PHYS", []byte{6, 0}}, {"PHYT", []byte{4, 0, 0, 0}},
		{"CAPS", capsule}, {"SHP2", shape}, {"BDY4", bodies}, {"SHJ2", shoulder}, {"JOIN", joint},
	} {
		out = append(out, physicsTestChunk(chunk.name, chunk.data, true)...)
	}
	return append(out, make([]byte, (16-len(out)%16)%16)...)
}

func physicsModel(t *testing.T) *Model {
	t.Helper()
	m := &Model{Stand: -1, Bones: []Bone{
		{Parent: -1}, {Parent: 0, Flags: 0x400, Pivot: Vec3{0, 0, 1}},
		{Parent: 1, Pivot: Vec3{0.4, 0, 1}},
	}}
	if err := m.SetPhysics(physicsFixture()); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPhysicsHangsInWorldGravity(t *testing.T) {
	m := physicsModel(t)
	for _, world := range []Mat4{
		Identity(),
		Multiply(Translate(Vec3{2, -3, 5}), Multiply(Rotation(Quat{sin(math.Pi / 4), 0, 0, cos(math.Pi / 4)}), Scale(Vec3{2, 2, 2}))),
	} {
		pose, _ := m.Pose(0)
		p := &physicsSystem{model: m}
		p.update(0, world, pose)
		anchor := transformPoint(Multiply(world, pose[1]), m.Bones[1].Pivot)
		wantAnchor := transformPoint(world, m.Bones[1].Pivot)
		tip := transformPoint(Multiply(world, pose[1]), Vec3{0.4, 0, 1})
		down := sub(tip, anchor)
		if physicsLength(sub(anchor, wantAnchor)) > 0.002 || down[2] > -0.38*p.scale || physicsLength(down) < 0.39*p.scale {
			t.Fatalf("body did not hang below its fixed anchor: anchor=%v want=%v direction=%v", anchor, wantAnchor, down)
		}
		if pose[0] != Identity() || pose[2] != pose[1] {
			t.Fatal("physics changed the root or failed to propagate to an unanimated child")
		}
	}
	pose, _ := m.Pose(0)
	if pose[1] != Identity() {
		t.Fatal("scene simulation modified the shared model's animation")
	}
}

func TestPhysicsUsesGravityScale(t *testing.T) {
	m := &Model{physics: &physicsData{bodies: []physicsBody{{dynamic: true, gravity: 0.85}}}}
	p := &physicsSystem{model: m, states: []physicsState{{rotation: Quat{0, 0, 0, 1}, invMass: 1}}}
	p.step(0.01, []physicsTarget{{}})
	if math.Abs(float64(p.states[0].velocity[2]+0.085)) > 1e-5 {
		t.Fatalf("default WoW gravity or authored gravityScale lost: velocity=%v", p.states[0].velocity)
	}
}

func TestPhysicsFollowsAnimationAndResets(t *testing.T) {
	m := physicsModel(t)
	m.Stand, m.Sequences = 0, []Sequence{{Duration: 1000, Flags: 0x20}}
	m.Bones[0].Flags = 0x200
	m.Bones[0].Rotation = Track{Loop: -1, Interpolation: 1, Times: []uint32{0, 500, 1000},
		Values: []Quat{{0, sin(0.1), 0, cos(0.1)}, {0, sin(-0.1), 0, cos(-0.1)}, {0, sin(0.1), 0, cos(0.1)}}}
	sc := SingleScene(&Asset{Model: m}, "pendulum")
	first, err := sc.Animate(0)
	if err != nil {
		t.Fatal(err)
	}
	var last Frame
	var tip Vec3
	for ms := uint32(0); ms < 4000; ms += 16 {
		frame, err := sc.Animate(ms)
		if err != nil {
			t.Fatal(err)
		}
		animated, _ := m.Pose(ms)
		anchor := transformPoint(frame.Poses[0][1], m.Bones[1].Pivot)
		want := transformPoint(animated[0], m.Bones[1].Pivot)
		now := transformPoint(frame.Poses[0][1], Vec3{0.4, 0, 1})
		if frame.Poses[0][0] != animated[0] || physicsLength(sub(anchor, want)) > 0.002 || (ms > 0 && physicsLength(sub(now, tip)) > 0.04) {
			t.Fatalf("physics detached or snapped at %d ms: anchor=%v want=%v tip=%v previous=%v", ms, anchor, want, now, tip)
		}
		for _, matrix := range frame.Poses[0] {
			for _, value := range matrix {
				if !finite(value) {
					t.Fatal("non-finite physics pose")
				}
			}
		}
		tip, last = now, frame
	}
	paused, _ := sc.Animate(sc.physics[0].last)
	if !reflect.DeepEqual(paused, last) {
		t.Fatal("physics continued moving while animation time was paused")
	}
	reset, _ := sc.Animate(0)
	if !reflect.DeepEqual(reset, first) {
		t.Fatal("restarting animation did not reset the simulation")
	}
	other := SingleScene(&Asset{Model: m}, "independent")
	other.Animate(0)
	if other.physics[0] == sc.physics[0] {
		t.Fatal("instances share mutable physics state")
	}
}

func TestPhysicsCapsulesDoNotCross(t *testing.T) {
	m := physicsModel(t)
	m.Bones = append(m.Bones, Bone{Parent: 0, Flags: 0x400, Pivot: Vec3{0, 0.01, 1}})
	body := m.physics.bodies[1]
	body.bone, body.position = 3, m.Bones[3].Pivot
	m.physics.bodies = append(m.physics.bodies, body)
	joint := m.physics.joints[0]
	joint.b, joint.frameA[13] = 2, 0.01
	m.physics.joints = append(m.physics.joints, joint)
	p := &physicsSystem{model: m}
	pose, _ := m.Pose(0)
	p.update(0, Identity(), pose)
	shape := body.shapes[0]
	a := p.states[1]
	b := p.states[2]
	point := func(s physicsState, v Vec3) Vec3 {
		return add(s.position, physicsRotate(s.rotation, sub(v, s.center)))
	}
	x, y := physicsClosestSegments(point(a, shape.a), point(a, shape.b), point(b, shape.a), point(b, shape.b))
	if distance := physicsLength(sub(x, y)); distance < shape.radius*2-0.007 {
		t.Fatalf("hanging bodies overlap: separation=%v radii=%v", distance, shape.radius*2)
	}
}

type physicsFiles map[uint32][]byte

func (f physicsFiles) File(_ context.Context, id uint32) ([]byte, error) {
	if data, ok := f[id]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("unexpected file %d", id)
}

func TestLoadPhysicsInlineAndExternal(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprint("inline=", inline), func(t *testing.T) {
			data := fixtureM2()
			bones := physicsModel(t).Bones
			offset := len(data)
			data = append(data, make([]byte, len(bones)*88)...)
			descriptor(data, 0x2c, len(bones), offset)
			for i, bone := range bones {
				o := offset + i*88
				le.PutUint32(data[o+4:], bone.Flags)
				le.PutUint16(data[o+8:], uint16(bone.Parent))
				for k, value := range bone.Pivot {
					putFloat(data, o+76+k*4, value)
				}
				for _, off := range []int{16, 36, 56} {
					le.PutUint16(data[o+off+2:], 0xffff)
				}
			}
			flags := uint32(0x20)
			if inline {
				flags |= 0x1000000
			}
			le.PutUint32(data[0x10:], flags)
			raw := physicsTestChunk("MD21", data, false)
			raw = append(raw, physicsTestChunk("SFID", []byte{2, 0, 0, 0}, false)...)
			files := physicsFiles{2: skinFixture(), 3: physicsFixture()}
			id := byte(3)
			if inline {
				raw = append(raw, physicsTestChunk("PFDC", physicsFixture(), false)...)
				id = 99 // PFDC takes precedence when 0x1000000 is set.
			}
			raw = append(raw, physicsTestChunk("PFID", []byte{id, 0, 0, 0}, false)...)
			files[1] = raw
			asset, err := LoadWith(context.Background(), files, 1, Options{})
			if err != nil || asset.Model.physics == nil {
				t.Fatalf("physics was not loaded: asset=%+v err=%v", asset, err)
			}
			if !inline {
				files[3] = []byte("bad physics")
				asset, err = LoadWith(context.Background(), files, 1, Options{})
				if err != nil || asset.Model.physics != nil || !strings.Contains(strings.Join(asset.Warnings, "\n"), "physics skipped") {
					t.Fatal("invalid physics must warn and preserve animated geometry", asset, err)
				}
			}
		})
	}
}

func TestPhysicsRejectsMalformedData(t *testing.T) {
	bones := physicsModel(t).Bones
	for _, raw := range [][]byte{
		nil, []byte("SYHP"), physicsTestChunk("PHYS", []byte{7, 0}, true),
		physicsFixture()[:20],
		append(physicsFixture(), 1),
	} {
		if _, err := parsePhysics(raw, bones); err == nil {
			t.Fatal("malformed physics accepted")
		}
	}
}
