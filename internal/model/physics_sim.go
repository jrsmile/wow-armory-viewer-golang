package model

import "math"

// This is a small constrained rigid-body solver, not Blizzard's Domino
// engine. It uses PHYS masses, joint frames/limits and damping, with WoW's
// z-up gravity of -10 yards/s^2. Capsule/sphere self-contacts are supported;
// this does not create a collision world from the character's rendered mesh.
type physicsSystem struct {
	model    *Model
	states   []physicsState
	previous []physicsTarget
	last     uint32
	started  bool
	scale    float32
}

type physicsState struct {
	position, velocity, angular Vec3
	rotation                    Quat
	center                      Vec3
	invMass                     float32
	invInertia                  physicsMatrix
	before                      Vec3
	beforeRotation              Quat
}

type physicsTarget struct {
	origin   Vec3
	rotation Quat
}

func (s *physicsState) origin() Vec3 {
	return sub(s.position, physicsRotate(s.rotation, s.center))
}

func (s *physicsState) inertia(v Vec3) Vec3 {
	return physicsRotate(s.rotation, s.invInertia.vector(physicsRotate(physicsConjugate(s.rotation), v)))
}

func (p *physicsSystem) targets(world Mat4, pose []Mat4, initial bool) []physicsTarget {
	out := make([]physicsTarget, len(p.model.physics.bodies))
	for i, b := range p.model.physics.bodies {
		frame, pivot := world, b.position
		if b.bone < len(p.model.Bones) && (initial || p.model.physics.kind >= 3) {
			frame, pivot = Multiply(world, pose[b.bone]), p.model.Bones[b.bone].Pivot
		}
		out[i] = physicsTarget{transformPoint(frame, pivot), physicsOrientation(frame)}
	}
	return out
}

func (p *physicsSystem) update(ms uint32, world Mat4, pose []Mat4) {
	inverse, ok := physicsInverseFrame(world)
	if !ok {
		p.started = false
		return
	}
	size := physicsLength(Vec3{world[0], world[1], world[2]})
	if !(size > 1e-5) {
		return
	}
	targets := p.targets(world, pose, false)
	if !p.started || ms < p.last || ms-p.last > 250 || math.Abs(float64(size-p.scale)) > float64(size*0.001) {
		p.scale, p.started = size, true
		p.states = make([]physicsState, len(targets))
		initial := p.targets(world, pose, true)
		for i, b := range p.model.physics.bodies {
			s := &p.states[i]
			s.center = scale(b.center, size)
			s.position = add(initial[i].origin, physicsRotate(initial[i].rotation, s.center))
			s.rotation = initial[i].rotation
			s.invMass = b.invMass / (size * size * size)
			s.invInertia = b.invInertia.scaled(1 / (size * size * size * size * size))
		}
		// Settle on the first frame; the model should not appear in its
		// exported, gravity-free pose and visibly drop into place.
		for range 180 {
			p.step(1.0/120, targets)
		}
	} else if ms > p.last {
		dt := float32(ms-p.last) / 1000
		steps := int(math.Ceil(float64(dt * 120)))
		for i := 1; i <= steps; i++ {
			t := float32(i) / float32(steps)
			between := make([]physicsTarget, len(targets))
			for j, target := range targets {
				old := p.previous[j]
				between[j] = physicsTarget{
					add(old.origin, scale(sub(target.origin, old.origin), t)),
					interpolateQuat(old.rotation, target.rotation, t),
				}
			}
			p.step(dt/float32(steps), between)
		}
	}
	p.previous, p.last = targets, ms
	p.writePose(inverse, pose)
}

func (p *physicsSystem) step(dt float32, targets []physicsTarget) {
	def := p.model.physics
	for i, b := range def.bodies {
		if b.dynamic {
			continue
		}
		s, target := &p.states[i], targets[i]
		origin := s.origin()
		rate := min(1/dt, 45)
		share := func(speed, low, high float32) float32 {
			return b.follow + (1-b.follow)*min(max((speed-low)/(high-low), 0), 1)
		}
		turn := physicsRotationVector(physicsProduct(target.rotation, physicsConjugate(s.rotation)))
		follow := max(b.follow,
			share(physicsLength(sub(target.origin, origin))/dt, 0.25*rate, 0.5*rate),
			share(physicsLength(turn)/dt, math.Pi/8*rate, math.Pi/4*rate))
		middle := add(origin, scale(sub(target.origin, origin), follow))
		rotation := interpolateQuat(s.rotation, target.rotation, follow)
		carry := physicsProduct(rotation, physicsConjugate(s.rotation))
		for j, child := range def.bodies {
			if child.dynamic && child.group == i {
				c := &p.states[j]
				c.position = add(middle, physicsRotate(carry, sub(c.position, origin)))
				c.rotation = normalize(physicsProduct(carry, c.rotation))
			}
		}
		s.position = add(target.origin, physicsRotate(target.rotation, s.center))
		s.rotation = target.rotation
	}
	for i, b := range def.bodies {
		if !b.dynamic {
			continue
		}
		s := &p.states[i]
		s.before, s.beforeRotation = s.position, s.rotation
		s.velocity[2] -= 10 * b.gravity * dt
		s.velocity = scale(s.velocity, max(0, 1-b.linear*dt))
		s.angular = scale(s.angular, max(0, 1-b.angular*dt))
		s.position = add(s.position, scale(s.velocity, dt))
		s.rotation = physicsTurn(s.rotation, scale(s.angular, dt))
	}
	for range 12 {
		for _, j := range def.joints {
			p.solveAnchor(j)
			p.solveAngle(j)
		}
		p.solveContacts()
	}
	for i, b := range def.bodies {
		if b.dynamic {
			s := &p.states[i]
			s.velocity = scale(sub(s.position, s.before), 1/dt)
			s.angular = scale(physicsRotationVector(physicsProduct(s.rotation, physicsConjugate(s.beforeRotation))), 1/dt)
		}
	}
}

func (p *physicsSystem) solveContacts() {
	def := p.model.physics
	for i, a := range def.bodies {
		for j := i + 1; j < len(def.bodies); j++ {
			b := def.bodies[j]
			if !a.dynamic && !b.dynamic {
				continue
			}
			connected := false
			for _, joint := range def.joints {
				connected = connected || (joint.a == i && joint.b == j) || (joint.a == j && joint.b == i)
			}
			if connected {
				continue
			}
			sa, sb := &p.states[i], &p.states[j]
			for _, ca := range a.shapes {
				for _, cb := range b.shapes {
					point := func(s *physicsState, v Vec3) Vec3 {
						return add(s.position, physicsRotate(s.rotation, sub(scale(v, p.scale), s.center)))
					}
					pa, pb := physicsClosestSegments(point(sa, ca.a), point(sa, ca.b), point(sb, cb.a), point(sb, cb.b))
					delta := sub(pa, pb)
					distance := physicsLength(delta)
					depth := (ca.radius+cb.radius)*p.scale - distance - 0.005
					if depth <= 0 {
						continue
					}
					normal := Vec3{1, 0, 0}
					if distance > 1e-6 {
						normal = scale(delta, 1/distance)
					} else if offset := sub(sa.position, sb.position); physicsLength(offset) > 1e-6 {
						normal = norm(offset)
					}
					ra, rb := sub(pa, sa.position), sub(pb, sb.position)
					aa, ab := sa.inertia(physicsCross(ra, normal)), sb.inertia(physicsCross(rb, normal))
					effective := sa.invMass + sb.invMass + physicsDot(normal, add(physicsCross(aa, ra), physicsCross(ab, rb)))
					if effective <= 1e-12 {
						continue
					}
					impulse := min(depth, 0.1*p.scale) / effective
					sa.position = add(sa.position, scale(normal, impulse*sa.invMass))
					sb.position = sub(sb.position, scale(normal, impulse*sb.invMass))
					sa.rotation = physicsTurn(sa.rotation, scale(aa, impulse))
					sb.rotation = physicsTurn(sb.rotation, scale(ab, -impulse))
				}
			}
		}
	}
}

func physicsClosestSegments(a, b, c, d Vec3) (Vec3, Vec3) {
	u, v, r := sub(b, a), sub(d, c), sub(a, c)
	aa, bb, ab := physicsDot(u, u), physicsDot(v, v), physicsDot(u, v)
	ar, br := physicsDot(u, r), physicsDot(v, r)
	var s, t float32
	switch {
	case aa < 1e-12 && bb < 1e-12:
		return a, c
	case aa < 1e-12:
		t = min(max(br/bb, 0), 1)
	case bb < 1e-12:
		s = min(max(-ar/aa, 0), 1)
	default:
		if denom := aa*bb - ab*ab; denom > 1e-12 {
			s = min(max((ab*br-ar*bb)/denom, 0), 1)
		}
		t = (ab*s + br) / bb
		if t < 0 {
			t, s = 0, min(max(-ar/aa, 0), 1)
		} else if t > 1 {
			t, s = 1, min(max((ab-ar)/aa, 0), 1)
		}
	}
	return add(a, scale(u, s)), add(c, scale(v, t))
}

func (p *physicsSystem) solveAnchor(j physicsJoint) {
	a, b := &p.states[j.a], &p.states[j.b]
	ra := physicsRotate(a.rotation, sub(scale(Vec3{j.frameA[12], j.frameA[13], j.frameA[14]}, p.scale), a.center))
	rb := physicsRotate(b.rotation, sub(scale(Vec3{j.frameB[12], j.frameB[13], j.frameB[14]}, p.scale), b.center))
	error := sub(add(a.position, ra), add(b.position, rb))
	var effective physicsMatrix
	for i := range 3 {
		var axis Vec3
		axis[i] = 1
		column := add(scale(axis, a.invMass+b.invMass),
			add(physicsCross(a.inertia(physicsCross(ra, axis)), ra), physicsCross(b.inertia(physicsCross(rb, axis)), rb)))
		copy(effective[i*3:], column[:])
	}
	inverse, ok := effective.inverse()
	if !ok {
		return
	}
	impulse := inverse.vector(scale(error, -1))
	a.position = add(a.position, scale(impulse, a.invMass))
	b.position = sub(b.position, scale(impulse, b.invMass))
	a.rotation = physicsTurn(a.rotation, a.inertia(physicsCross(ra, impulse)))
	b.rotation = physicsTurn(b.rotation, scale(b.inertia(physicsCross(rb, impulse)), -1))
}

func (p *physicsSystem) solveAngle(j physicsJoint) {
	a, b := &p.states[j.a], &p.states[j.b]
	qa := physicsProduct(a.rotation, physicsOrientation(j.frameA))
	qb := physicsProduct(b.rotation, physicsOrientation(j.frameB))
	relative := normalize(physicsProduct(physicsConjugate(qa), qb))
	if relative[3] < 0 {
		for i := range relative {
			relative[i] = -relative[i]
		}
	}
	limited := Quat{0, 0, 0, 1}
	if !j.weld {
		// The shoulder's twist axis is its frame's Z axis.
		twist := normalize(Quat{0, 0, relative[2], relative[3]})
		swing := physicsRotationVector(physicsProduct(relative, physicsConjugate(twist)))
		if angle := physicsLength(swing); angle > j.cone {
			swing = scale(swing, j.cone/angle)
		}
		if j.upper > j.lower {
			angle := float32(2 * math.Atan2(float64(twist[2]), float64(twist[3])))
			angle = min(max(angle, j.lower), j.upper)
			twist = Quat{0, 0, sin(angle / 2), cos(angle / 2)}
		}
		limited = physicsProduct(physicsTurn(Quat{0, 0, 0, 1}, swing), twist)
	}
	error := physicsRotationVector(physicsProduct(physicsProduct(qa, limited), physicsConjugate(qb)))
	if physicsLength(error) < 1e-6 {
		return
	}
	var effective physicsMatrix
	for i := range 3 {
		var axis Vec3
		axis[i] = 1
		column := add(a.inertia(axis), b.inertia(axis))
		copy(effective[i*3:], column[:])
	}
	inverse, ok := effective.inverse()
	if !ok {
		return
	}
	impulse := inverse.vector(error)
	a.rotation = physicsTurn(a.rotation, scale(a.inertia(impulse), -1))
	b.rotation = physicsTurn(b.rotation, b.inertia(impulse))
}

func (p *physicsSystem) writePose(inverseWorld Mat4, pose []Mat4) {
	original := append([]Mat4(nil), pose...)
	controlled := make([]bool, len(pose))
	for i, b := range p.model.physics.bodies {
		if !b.dynamic || b.group < 0 {
			continue
		}
		s := &p.states[i]
		frame := Multiply(Translate(s.origin()), Multiply(Rotation(s.rotation), Scale(Vec3{p.scale, p.scale, p.scale})))
		pivot := p.model.Bones[b.bone].Pivot
		pose[b.bone] = Multiply(inverseWorld, Multiply(frame, Translate(scale(pivot, -1))))
		controlled[b.bone] = true
	}
	done := make([]bool, len(pose))
	var visit func(int)
	visit = func(i int) {
		if done[i] {
			return
		}
		done[i] = true
		if parent := int(p.model.Bones[i].Parent); parent >= 0 && !controlled[i] {
			visit(parent)
			if inverse, ok := physicsInverseFrame(original[parent]); ok {
				pose[i] = Multiply(pose[parent], Multiply(inverse, original[i]))
			}
		}
	}
	for i := range p.model.Bones {
		visit(i)
	}
}

type physicsMatrix [9]float32

func physicsDiagonal(v float32) physicsMatrix { return physicsMatrix{v, 0, 0, 0, v, 0, 0, 0, v} }
func physicsOuter(a, b Vec3) (m physicsMatrix) {
	for c := range 3 {
		for r := range 3 {
			m[c*3+r] = a[r] * b[c]
		}
	}
	return
}
func physicsParallel(v Vec3) physicsMatrix {
	return physicsDiagonal(physicsDot(v, v)).add(physicsOuter(v, v).scaled(-1))
}
func (m physicsMatrix) add(b physicsMatrix) physicsMatrix {
	for i := range m {
		m[i] += b[i]
	}
	return m
}
func (m physicsMatrix) scaled(v float32) physicsMatrix {
	for i := range m {
		m[i] *= v
	}
	return m
}
func (m physicsMatrix) vector(v Vec3) Vec3 {
	return Vec3{m[0]*v[0] + m[3]*v[1] + m[6]*v[2], m[1]*v[0] + m[4]*v[1] + m[7]*v[2], m[2]*v[0] + m[5]*v[1] + m[8]*v[2]}
}
func (m physicsMatrix) inverse() (physicsMatrix, bool) {
	a, b, c := Vec3{m[0], m[1], m[2]}, Vec3{m[3], m[4], m[5]}, Vec3{m[6], m[7], m[8]}
	x, y, z := physicsCross(b, c), physicsCross(c, a), physicsCross(a, b)
	det := physicsDot(a, x)
	if !finite(det) || math.Abs(float64(det)) < 1e-20 {
		return physicsMatrix{}, false
	}
	return physicsMatrix{x[0], y[0], z[0], x[1], y[1], z[1], x[2], y[2], z[2]}.scaled(1 / det), true
}
func physicsCross(a, b Vec3) Vec3 {
	return Vec3{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func physicsDot(a, b Vec3) float32 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func physicsLength(v Vec3) float32 { return float32(math.Sqrt(float64(physicsDot(v, v)))) }
func physicsProduct(a, b Quat) Quat {
	return Quat{
		a[3]*b[0] + a[0]*b[3] + a[1]*b[2] - a[2]*b[1],
		a[3]*b[1] - a[0]*b[2] + a[1]*b[3] + a[2]*b[0],
		a[3]*b[2] + a[0]*b[1] - a[1]*b[0] + a[2]*b[3],
		a[3]*b[3] - a[0]*b[0] - a[1]*b[1] - a[2]*b[2],
	}
}
func physicsConjugate(q Quat) Quat { return Quat{-q[0], -q[1], -q[2], q[3]} }
func physicsRotate(q Quat, v Vec3) Vec3 {
	u := Vec3{q[0], q[1], q[2]}
	return add(v, scale(physicsCross(u, add(physicsCross(u, v), scale(v, q[3]))), 2))
}
func physicsTurn(q Quat, v Vec3) Quat {
	angle := physicsLength(v)
	if angle < 1e-8 {
		return q
	}
	k := sin(angle/2) / angle
	return normalize(physicsProduct(Quat{v[0] * k, v[1] * k, v[2] * k, cos(angle / 2)}, q))
}
func physicsRotationVector(q Quat) Vec3 {
	q = normalize(q)
	if q[3] < 0 {
		for i := range q {
			q[i] = -q[i]
		}
	}
	v := Vec3{q[0], q[1], q[2]}
	length := physicsLength(v)
	if length < 1e-8 {
		return Vec3{}
	}
	return scale(v, float32(2*math.Atan2(float64(length), float64(q[3])))/length)
}
func physicsOrientation(m Mat4) Quat {
	for c := range 3 {
		v := norm(Vec3{m[c*4], m[c*4+1], m[c*4+2]})
		copy(m[c*4:], v[:])
	}
	var q Quat
	if trace := m[0] + m[5] + m[10]; trace > 0 {
		s := float32(math.Sqrt(float64(trace+1))) * 2
		q = Quat{(m[6] - m[9]) / s, (m[8] - m[2]) / s, (m[1] - m[4]) / s, s / 4}
	} else {
		i := 0
		if m[5] > m[0] {
			i = 1
		}
		if m[10] > m[i*5] {
			i = 2
		}
		j, k := (i+1)%3, (i+2)%3
		s := float32(math.Sqrt(math.Max(0, float64(1+m[i*5]-m[j*5]-m[k*5])))) * 2
		if s < 1e-8 {
			return Quat{0, 0, 0, 1}
		}
		q[i], q[j], q[k], q[3] = s/4, (m[j*4+i]+m[i*4+j])/s, (m[k*4+i]+m[i*4+k])/s, (m[j*4+k]-m[k*4+j])/s
	}
	return normalize(q)
}
func physicsInverseFrame(m Mat4) (Mat4, bool) {
	basis := physicsMatrix{m[0], m[1], m[2], m[4], m[5], m[6], m[8], m[9], m[10]}
	inverse, ok := basis.inverse()
	if !ok {
		return Mat4{}, false
	}
	out := Identity()
	for c := range 3 {
		copy(out[c*4:], inverse[c*3:c*3+3])
	}
	position := inverse.vector(Vec3{-m[12], -m[13], -m[14]})
	copy(out[12:], position[:])
	return out, true
}
