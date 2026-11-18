package model

import (
	"fmt"
	"math"
)

// PHYS stores rigid bodies, not a cloth mesh. Only bones marked 0x400 may
// be driven by them. Layouts and units: https://wowdev.wiki/PHYS.
type physicsData struct {
	kind   uint32
	bodies []physicsBody
	joints []physicsJoint
}

type physicsBody struct {
	bone, group                      int
	dynamic                          bool
	position, center                 Vec3
	invMass                          float32
	invInertia                       physicsMatrix
	gravity, linear, angular, follow float32
	attachment                       uint16
	shapes                           []physicsCapsule
}

type physicsCapsule struct {
	a, b   Vec3
	radius float32
}

type physicsJoint struct {
	a, b               int
	frameA, frameB     Mat4
	lower, upper, cone float32
	weld               bool
}

type physicsShape struct {
	kind, index int
	density     float32
}

// SetPhysics loads an inline PFDC payload or a standalone PFID file. A file
// with unsupported constraints is left entirely unsimulated, not partly bent.
func (m *Model) SetPhysics(raw []byte) error {
	p, err := parsePhysics(raw, m.Bones)
	if err != nil {
		return err
	}
	m.physics = p
	return nil
}

// PhysicsBones lists the bones posed by simulated (dynamic) bodies.
func (m *Model) PhysicsBones() []int {
	if m.physics == nil {
		return nil
	}
	var out []int
	for _, b := range m.physics.bodies {
		if b.dynamic {
			out = append(out, b.bone)
		}
	}
	return out
}

func parsePhysics(raw []byte, bones []Bone) (*physicsData, error) {
	chunks := map[string][]byte{}
	for len(raw) > 0 {
		// PFDC is padded to 16 bytes.
		zero := true
		for _, b := range raw[:min(8, len(raw))] {
			zero = zero && b == 0
		}
		if zero {
			for _, b := range raw {
				if b != 0 {
					return nil, fmt.Errorf("invalid PHYS padding")
				}
			}
			break
		}
		if len(raw) < 8 {
			return nil, fmt.Errorf("truncated PHYS chunk")
		}
		name := string([]byte{raw[3], raw[2], raw[1], raw[0]})
		n := uint64(le.Uint32(raw[4:]))
		if n > uint64(len(raw)-8) {
			return nil, fmt.Errorf("truncated PHYS %s", name)
		}
		if _, exists := chunks[name]; exists {
			return nil, fmt.Errorf("duplicate PHYS %s", name)
		}
		chunks[name], raw = raw[8:8+n], raw[8+n:]
	}
	header := chunks["PHYS"]
	if len(header) != 2 || le.Uint16(header) > 6 {
		return nil, fmt.Errorf("unsupported PHYS version")
	}
	version := le.Uint16(header)
	p := &physicsData{}
	if kind := chunks["PHYT"]; len(kind) > 0 {
		if len(kind) != 4 {
			return nil, fmt.Errorf("invalid PHYT")
		}
		p.kind = le.Uint32(kind)
	}
	if p.kind == 2 || p.kind > 4 {
		return nil, fmt.Errorf("unsupported PHYS object type %d", p.kind)
	}
	records := func(name string, stride int) ([]byte, error) {
		d := chunks[name]
		if len(d)%stride != 0 || len(d)/stride > 4096 {
			return nil, fmt.Errorf("invalid PHYS %s records", name)
		}
		return d, nil
	}
	choose := func(names ...string) string {
		for _, name := range names {
			if len(chunks[name]) > 0 {
				return name
			}
		}
		return names[len(names)-1]
	}
	bodyName := choose("BDY4", "BDY3", "BDY2", "BODY")
	bodyStride := map[string]int{"BODY": 28, "BDY2": 32, "BDY3": 44, "BDY4": 48}[bodyName]
	bodies, err := records(bodyName, bodyStride)
	if err != nil || len(bodies) == 0 || len(bodies)/bodyStride > 256 {
		return nil, fmt.Errorf("invalid PHYS bodies")
	}
	shapeName := choose("SHP2", "SHAP")
	shapeStride := 20
	if shapeName == "SHP2" {
		shapeStride = 32
	}
	shapesRaw, err := records(shapeName, shapeStride)
	if err != nil {
		return nil, err
	}
	shapes := make([]physicsShape, len(shapesRaw)/shapeStride)
	for i := range shapes {
		d := shapesRaw[i*shapeStride:]
		shapes[i] = physicsShape{int(le.Uint16(d)), int(le.Uint16(d[2:])), f32(d, 16)}
		if !finite(shapes[i].density) || shapes[i].density < 0 {
			return nil, fmt.Errorf("invalid PHYS shape density")
		}
	}
	capsules, err := records("CAPS", 28)
	if err != nil {
		return nil, err
	}
	spheres, err := records("SPHS", 16)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(bodies); i += bodyStride {
		d := bodies[i:]
		if le.Uint16(d) > 1 {
			return nil, fmt.Errorf("unsupported PHYS body type %d", le.Uint16(d))
		}
		b := physicsBody{
			bone: int(le.Uint16(d[2:])), group: -1, dynamic: le.Uint16(d) == 1,
			position: vec(d, 4), gravity: 1, follow: 0.9,
		}
		shapeAt := 16
		if bodyStride <= 32 {
			b.bone = int(le.Uint32(d[16:]) & 0xffff)
			shapeAt = 20
		}
		first, count := uint64(le.Uint32(d[shapeAt:])), uint64(le.Uint32(d[shapeAt+4:]))
		if first+count > uint64(len(shapes)) || (b.bone != 0xffff && b.bone >= len(bones)) {
			return nil, fmt.Errorf("PHYS body reference out of bounds")
		}
		inertiaScale := float32(1)
		if bodyStride == 32 {
			b.gravity = f32(d, 28)
		} else if bodyStride >= 44 {
			b.gravity, inertiaScale = f32(d, 24), f32(d, 28)
			b.linear, b.angular = f32(d, 32), f32(d, 36)
			b.attachment = le.Uint16(d[40:])
			if bodyStride == 48 {
				b.follow, b.attachment = f32(d, 40), le.Uint16(d[44:])
			}
		}
		for _, v := range []float32{b.position[0], b.position[1], b.position[2], b.gravity, inertiaScale, b.linear, b.angular, b.follow} {
			if !finite(v) {
				return nil, fmt.Errorf("non-finite PHYS body")
			}
		}
		if b.linear < 0 || b.angular < 0 || inertiaScale < 0 || b.follow < 0 || b.follow > 1 {
			return nil, fmt.Errorf("invalid PHYS body tuning")
		}
		b.dynamic = b.dynamic && count > 0 && b.bone < len(bones) && bones[b.bone].Flags&0x400 != 0
		var mass float32
		var inertia physicsMatrix
		for _, sh := range shapes[first : first+count] {
			var center, axis Vec3
			var radius, length float32
			var capsule physicsCapsule
			switch sh.kind {
			case 1:
				if sh.index >= len(capsules)/28 {
					return nil, fmt.Errorf("PHYS capsule out of bounds")
				}
				s := capsules[sh.index*28:]
				a, z := vec(s, 0), vec(s, 12)
				capsule.a, capsule.b = a, z
				center, axis = scale(add(a, z), 0.5), sub(z, a)
				length, radius = physicsLength(axis), f32(s, 24)
				axis = norm(axis)
			case 2:
				if sh.index >= len(spheres)/16 {
					return nil, fmt.Errorf("PHYS sphere out of bounds")
				}
				s := spheres[sh.index*16:]
				center, radius = vec(s, 0), f32(s, 12)
				capsule.a, capsule.b = center, center
			default:
				return nil, fmt.Errorf("unsupported PHYS shape %d", sh.kind)
			}
			if !finite(radius) || !finite(length) || radius <= 0 {
				return nil, fmt.Errorf("invalid PHYS shape")
			}
			for _, c := range center {
				if !finite(c) {
					return nil, fmt.Errorf("non-finite PHYS shape")
				}
			}
			capsule.radius = radius
			b.shapes = append(b.shapes, capsule)
			if !b.dynamic {
				continue
			}
			density := sh.density
			cylinder := float32(math.Pi) * radius * radius * length
			sphere := float32(4*math.Pi/3) * radius * radius * radius
			if sh.kind == 1 && version <= 4 {
				density *= (float32(4.0/3)*radius*radius*radius + cylinder) / (sphere + cylinder)
			}
			mc, ms := cylinder*density, sphere*density
			weight := mc + ms
			parallel := (0.5*mc + 0.4*ms) * radius * radius
			perp := mc*(3*radius*radius+length*length)/12 +
				ms*(0.4*radius*radius+0.25*length*length+0.375*length*radius)
			tensor := physicsDiagonal(perp)
			tensor = tensor.add(physicsOuter(axis, axis).scaled(parallel - perp))
			inertia = inertia.add(tensor).add(physicsParallel(center).scaled(weight))
			b.center = add(b.center, scale(center, weight))
			mass += weight
		}
		if b.dynamic {
			if !(mass > 0) || !finite(mass) {
				return nil, fmt.Errorf("invalid PHYS mass")
			}
			b.center = scale(b.center, 1/mass)
			inertia = inertia.add(physicsParallel(b.center).scaled(-mass)).scaled(inertiaScale)
			b.invMass = 1 / mass
			if inertiaScale > 0 {
				var ok bool
				b.invInertia, ok = inertia.inverse()
				if !ok {
					return nil, fmt.Errorf("invalid PHYS inertia")
				}
			}
		}
		p.bodies = append(p.bodies, b)
	}
	parent := -1
	for i, b := range p.bodies {
		if !b.dynamic && b.attachment&0x8000 != 0 {
			parent = i
			break
		}
	}
	for i := range p.bodies {
		b := &p.bodies[i]
		if !b.dynamic {
			continue
		}
		group := int(b.attachment & 0x3fff)
		if parent < 0 {
			group = -1
			for j := 0; j < i; j++ {
				if !p.bodies[j].dynamic {
					group = j
					break
				}
			}
		}
		if group >= 0 && group < len(p.bodies) && !p.bodies[group].dynamic &&
			(parent < 0 || p.bodies[group].attachment&0x8000 != 0) {
			b.group = group
		}
	}
	join, err := records("JOIN", 16)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(join); i += 16 {
		d := join[i:]
		a, b := uint64(le.Uint32(d)), uint64(le.Uint32(d[4:]))
		if a >= uint64(len(p.bodies)) || b >= uint64(len(p.bodies)) || a == b {
			return nil, fmt.Errorf("PHYS joint body out of bounds")
		}
		j := physicsJoint{a: int(a), b: int(b), frameA: Identity(), frameB: Identity()}
		index := int(le.Uint16(d[14:]))
		var data []byte
		switch le.Uint16(d[12:]) {
		case 1:
			name := choose("SHJ2", "SHOJ")
			stride := 116
			if name == "SHJ2" {
				stride = 124
			}
			if data, err = records(name, stride); err != nil || index >= len(data)/stride {
				return nil, fmt.Errorf("PHYS shoulder joint out of bounds")
			}
			data = data[index*stride:]
			j.lower, j.upper = f32(data, 96)*math.Pi/180, f32(data, 100)*math.Pi/180
			j.cone = min(max(f32(data, 104), 10), 170) * math.Pi / 180
			if f32(data, 108) != 0 && le.Uint32(data[112:]) != 0 {
				return nil, fmt.Errorf("PHYS joint motors are not supported")
			}
		case 2:
			name := choose("WLJ3", "WLJ2", "WELJ")
			stride := map[string]int{"WELJ": 104, "WLJ2": 112, "WLJ3": 116}[name]
			if data, err = records(name, stride); err != nil || index >= len(data)/stride {
				return nil, fmt.Errorf("PHYS weld joint out of bounds")
			}
			data = data[index*stride:]
			if f32(data, 96) != 0 || (stride > 104 && f32(data, 104) != 0) {
				return nil, fmt.Errorf("PHYS soft welds are not supported")
			}
			j.weld = true
		default:
			return nil, fmt.Errorf("unsupported PHYS joint type %d", le.Uint16(d[12:]))
		}
		j.frameA, j.frameB = physicsFrame(data), physicsFrame(data[48:])
		for _, frame := range []Mat4{j.frameA, j.frameB} {
			for _, v := range frame {
				if !finite(v) {
					return nil, fmt.Errorf("non-finite PHYS joint frame")
				}
			}
			x, y, z := Vec3{frame[0], frame[1], frame[2]}, Vec3{frame[4], frame[5], frame[6]}, Vec3{frame[8], frame[9], frame[10]}
			if math.Abs(float64(physicsDot(x, x)-1)) > 0.01 ||
				math.Abs(float64(physicsDot(y, y)-1)) > 0.01 ||
				math.Abs(float64(physicsDot(z, z)-1)) > 0.01 ||
				math.Abs(float64(physicsDot(x, y))) > 0.01 ||
				math.Abs(float64(physicsDot(x, z))) > 0.01 ||
				math.Abs(float64(physicsDot(y, z))) > 0.01 ||
				physicsDot(physicsCross(x, y), z) < 0.99 {
				return nil, fmt.Errorf("invalid PHYS joint rotation")
			}
		}
		if !finite(j.lower) || !finite(j.upper) || !finite(j.cone) {
			return nil, fmt.Errorf("non-finite PHYS joint limits")
		}
		p.joints = append(p.joints, j)
	}
	if len(p.joints) == 0 {
		return nil, fmt.Errorf("PHYS has no supported joints")
	}
	return p, nil
}

func physicsFrame(data []byte) Mat4 {
	m := Identity()
	for c := range 3 {
		for r := range 3 {
			m[c*4+r] = f32(data, (c*3+r)*4)
		}
		m[12+c] = f32(data, 36+c*4)
	}
	return m
}
