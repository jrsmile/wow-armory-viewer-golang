package model

import (
	"fmt"
	"math"
)

// Particle emitters follow the client's CParticleEmitter2 as reimplemented by
// WebWowViewerCpp (wow.tools' renderer): per-sequence emitter tracks drive
// spawning, particles age through fixed-point colour/alpha/scale curves and
// are drawn as camera-facing (or velocity/model-plane aligned) quads.

const emitterStride = 492

// PartTrack is a keyframed curve over a particle's normalized age.
type PartTrack struct {
	Times  []float32
	Values []Quat
}

func (p PartTrack) at(t float32, fallback Quat) Quat {
	n := min(len(p.Times), len(p.Values))
	if n == 0 {
		return fallback
	}
	if t <= p.Times[0] {
		return p.Values[0]
	}
	for i := 1; i < n; i++ {
		if t <= p.Times[i] {
			span := p.Times[i] - p.Times[i-1]
			f := float32(0)
			if span > 0 {
				f = (t - p.Times[i-1]) / span
			}
			var out Quat
			for k := range out {
				out[k] = p.Values[i-1][k] + (p.Values[i][k]-p.Values[i-1][k])*f
			}
			return out
		}
	}
	return p.Values[n-1]
}

// Emitter is one M2Particle record.
type Emitter struct {
	Flags      uint32
	Position   Vec3
	Bone       int
	Texture    int // index into the model's textures
	Blend      uint8
	Type       uint8 // 1 plane, 2 sphere, 3 spline, 4 bone
	Rows, Cols int

	Speed, SpeedVary, VerticalRange, HorizontalRange Track
	Gravity, Lifespan, Rate, AreaLength, AreaWidth   Track
	ZSource, Enabled                                 Track
	LifespanVary, RateVary                           float32

	Color, Alpha, Scale, HeadCell PartTrack
	ScaleVary                     [2]float32

	TwinkleSpeed, TwinklePercent, TwinkleMin, TwinkleMax float32
	Drag, BaseSpin, BaseSpinVary, Spin, SpinVary        float32
	Wind                                                Vec3
	WindTime                                            float32
}

const (
	particleLocal       = 0x10       // particles stay in emitter space
	particleVelocity    = 0x4        // quads stretch along the velocity
	particleUp          = 0x100      // sphere particles move straight up
	particleFlipSpin    = 0x200      // odd seeds spin backwards
	particleModelPlane  = 0x1000     // quads lie in the model's XY plane
	particleFlatZ       = 0x2000     // spawn on the emitter's z = 0 plane
	particleRandomCell  = 0x10000    // random atlas cell
	particleScaleXY     = 0x80000    // independent x/y scale variation
	particleGravityComp = 0x800000   // compressed gravity vectors
	particleMultiTex    = 0x10000000 // texture holds 3 packed 5-bit indices
)

func readPartTrack(data []byte, offset, kind int) (PartTrack, error) {
	var p PartTrack
	times, err := nested(data, data[offset:], 2)
	if err != nil {
		return p, err
	}
	size := kind & trackKindStride
	values, err := nested(data, data[offset+8:], size)
	if err != nil {
		return p, err
	}
	n := min(len(times)/2, len(values)/size)
	for i := range n {
		var v Quat
		raw := values[i*size:]
		switch kind {
		case trackVec3:
			v = Quat{f32(raw, 0), f32(raw, 4), f32(raw, 8)}
		case 8: // vec2
			v = Quat{f32(raw, 0), f32(raw, 4)}
		case trackFixed16:
			v[0] = float32(int16(le.Uint16(raw))) / 32767
		case 0x102: // uint16 cell index
			v[0] = float32(le.Uint16(raw))
		}
		for _, c := range v {
			if !finite(c) {
				return PartTrack{}, fmt.Errorf("non-finite particle curve")
			}
		}
		p.Times = append(p.Times, float32(le.Uint16(times[i*2:]))/32767)
		p.Values = append(p.Values, v)
	}
	return p, nil
}

// readEmitters parses the particle emitter array (M2 header 0x128).
func (m *Model) readEmitters() []Emitter {
	data := m.data
	raw, err := array(data, 0x128, emitterStride)
	if err != nil {
		return nil
	}
	base := int(le.Uint32(data[0x12c:]))
	out := make([]Emitter, 0, len(raw)/emitterStride)
	for i := range len(raw) / emitterStride {
		o := base + i*emitterStride
		d := data[o:]
		e := Emitter{
			Flags: le.Uint32(d[4:]), Position: vec(d, 8), Bone: int(le.Uint16(d[0x14:])),
			Texture: int(le.Uint16(d[0x16:])), Blend: d[0x28], Type: d[0x29],
			Rows: int(le.Uint16(d[0x30:])), Cols: int(le.Uint16(d[0x32:])),
			LifespanVary: f32(d, 0xAC), RateVary: f32(d, 0xC4),
			ScaleVary:    [2]float32{f32(d, 0x134), f32(d, 0x138)},
			TwinkleSpeed: f32(d, 0x160), TwinklePercent: f32(d, 0x164),
			TwinkleMin: f32(d, 0x168), TwinkleMax: f32(d, 0x16C),
			Drag: f32(d, 0x174), BaseSpin: f32(d, 0x178), BaseSpinVary: f32(d, 0x17C),
			Spin: f32(d, 0x180), SpinVary: f32(d, 0x184),
			Wind: vec(d, 0x1A0), WindTime: f32(d, 0x1AC),
		}
		if e.Flags&particleMultiTex != 0 {
			e.Texture &= 0x1f
		}
		e.Rows, e.Cols = max(e.Rows, 1), max(e.Cols, 1)
		track := func(off int) Track { return m.effectTrack(o+off, trackFloat) }
		e.Speed, e.SpeedVary, e.VerticalRange, e.HorizontalRange = track(0x34), track(0x48), track(0x5C), track(0x70)
		if e.Flags&particleGravityComp != 0 {
			e.Gravity = m.effectTrack(o+0x84, trackGravity)
		} else {
			e.Gravity = track(0x84)
		}
		e.Lifespan, e.Rate, e.AreaLength, e.AreaWidth, e.ZSource = track(0x98), track(0xB0), track(0xC8), track(0xDC), track(0xF0)
		e.Enabled = m.effectTrack(o+0x1C8, trackUint8)
		var errs [4]error
		e.Color, errs[0] = readPartTrack(data, o+0x104, trackVec3)
		e.Alpha, errs[1] = readPartTrack(data, o+0x114, trackFixed16)
		e.Scale, errs[2] = readPartTrack(data, o+0x124, 8)
		e.HeadCell, errs[3] = readPartTrack(data, o+0x13C, 0x102)
		bad := false
		for _, err := range errs {
			bad = bad || err != nil
		}
		for _, v := range []float32{e.LifespanVary, e.RateVary, e.ScaleVary[0], e.ScaleVary[1], e.TwinkleSpeed, e.TwinklePercent,
			e.TwinkleMin, e.TwinkleMax, e.Drag, e.BaseSpin, e.BaseSpinVary, e.Spin, e.SpinVary, e.WindTime} {
			bad = bad || !finite(v)
		}
		if bad || e.Texture >= len(m.Textures) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// emitterParams are an emitter's track values at one time.
type emitterParams struct {
	speed, speedVary, vRange, hRange, life, rate, areaX, areaY, zSource float32
	gravity                                                             Vec3
	enabled                                                             bool
}

func (m *Model) emitterParams(e *Emitter, ms uint32) emitterParams {
	f := func(t Track) float32 { return m.sampleEffect(t, ms, Quat{}, false)[0] }
	p := emitterParams{
		speed: f(e.Speed), speedVary: f(e.SpeedVary), vRange: f(e.VerticalRange), hRange: f(e.HorizontalRange),
		life: f(e.Lifespan), rate: f(e.Rate), areaX: f(e.AreaLength), areaY: f(e.AreaWidth), zSource: f(e.ZSource),
		enabled: m.sampleEffect(e.Enabled, ms, Quat{1}, false)[0] != 0,
	}
	g := m.sampleEffect(e.Gravity, ms, Quat{}, false)
	if e.Flags&particleGravityComp != 0 {
		p.gravity = Vec3{g[0], g[1], g[2]}
	} else {
		p.gravity = Vec3{0, 0, -g[0]}
	}
	return p
}

type particle struct {
	pos, vel  Vec3
	age, life float32
	seed      uint32
}

type emitterState struct {
	particles []particle
	emission  float32
}

// ParticleSystem simulates a model's emitters over time.
type ParticleSystem struct {
	model   *Model
	states  []emitterState
	last    uint32
	started bool
	rng     uint32
}

// MaxParticles caps the live particles of one emitter.
const MaxParticles = 2000

func NewParticleSystem(m *Model) *ParticleSystem {
	return &ParticleSystem{model: m, states: make([]emitterState, len(m.Emitters)), rng: 0x9E3779B9}
}

var particleRandTable = func() (t [128]float32) {
	r := uint32(0x2545F491)
	for i := range t {
		r ^= r << 13
		r ^= r >> 17
		r ^= r << 5
		t[i] = float32(r>>8) / (1 << 24)
	}
	return
}()

func (p *ParticleSystem) next() uint32 {
	p.rng ^= p.rng << 13
	p.rng ^= p.rng >> 17
	p.rng ^= p.rng << 5
	return p.rng
}
func (p *ParticleSystem) unit() float32    { return float32(p.next()>>8) / (1 << 24) }
func (p *ParticleSystem) uniform() float32 { return p.unit()*2 - 1 }

// seedUniform is a stable per-particle random value in [-1, 1].
func seedUniform(seed uint32, salt uint32) float32 {
	r := seed*0x9E3779B1 + salt*0x85EBCA77
	r ^= r >> 15
	r *= 0x2C1B3C6D
	r ^= r >> 12
	return float32(r>>8)/(1<<23) - 1
}

// Update advances the simulation to ms. emitters holds each emitter's
// model-space transform (see Frame.Emitter). The first call pre-warms the
// system so effects are already running when the character appears.
func (p *ParticleSystem) Update(ms uint32, emitters []Mat4) {
	if len(emitters) < len(p.states) {
		return
	}
	if !p.started {
		p.started, p.last = true, ms
		for t := float32(0); t < 3; t += 0.1 {
			p.step(ms, 0.1, emitters)
		}
		return
	}
	dt := float32(ms-p.last) / 1000
	if ms < p.last || dt > 1 {
		dt = 0.1
	}
	p.last = ms
	for dt > 0 {
		s := min(dt, 0.1)
		p.step(ms, s, emitters)
		dt -= s
	}
}

func (p *ParticleSystem) step(ms uint32, dt float32, emitters []Mat4) {
	for i := range p.states {
		e, st := &p.model.Emitters[i], &p.states[i]
		par := p.model.emitterParams(e, ms)
		live := st.particles[:0]
		for _, q := range st.particles {
			q.age += dt
			if q.age >= q.life {
				continue
			}
			if q.age < e.WindTime {
				q.vel = add(q.vel, scale(e.Wind, dt))
			}
			q.pos = add(q.pos, add(scale(q.vel, dt), scale(par.gravity, dt*dt/2)))
			q.vel = add(q.vel, scale(par.gravity, dt))
			q.vel = scale(q.vel, 1-min(max(e.Drag*dt, 0), 1))
			live = append(live, q)
		}
		st.particles = live
		if !par.enabled || (e.Type != 1 && e.Type != 2 && e.Type != 3) {
			st.emission = 0
			continue
		}
		rate := par.rate + p.uniform()*e.RateVary
		if !(rate > 0) || !finite(rate) {
			continue
		}
		st.emission += dt * rate
		for st.emission > 1 {
			st.emission--
			if len(st.particles) >= MaxParticles {
				continue
			}
			if q, ok := p.spawn(e, par, emitters[i], dt); ok {
				st.particles = append(st.particles, q)
			}
		}
		st.emission = min(st.emission, MaxParticles)
	}
}

func (p *ParticleSystem) spawn(e *Emitter, par emitterParams, mat Mat4, dt float32) (particle, bool) {
	q := particle{seed: p.next()}
	q.life = par.life + p.uniform()*e.LifespanVary
	if !(q.life > 0.001) || !finite(q.life) {
		return q, false
	}
	q.age = dt * p.unit()
	speed := par.speed * (1 + par.speedVary*p.uniform())
	switch e.Type {
	case 2:
		radius := par.areaX + (par.areaY-par.areaX)*p.unit()
		polar, azimuth := par.vRange*p.uniform(), par.hRange*p.uniform()
		dir := Vec3{cos(polar) * cos(azimuth), cos(polar) * sin(azimuth), sin(polar)}
		q.pos = scale(dir, radius)
		switch {
		case e.Flags&particleUp != 0:
			dir = Vec3{0, 0, 1}
		case par.zSource > 0.001:
			dir = norm(sub(q.pos, Vec3{0, 0, par.zSource}))
		}
		q.vel = scale(dir, speed)
	default: // plane; spline emitters are approximated by their plane area
		q.pos = Vec3{p.uniform() * par.areaX * 0.5, p.uniform() * par.areaY * 0.5, 0}
		if par.zSource < 0.001 {
			polar, azimuth := par.vRange*p.uniform(), par.hRange*p.uniform()
			q.vel = scale(Vec3{cos(azimuth) * sin(polar), sin(azimuth) * sin(polar), cos(polar)}, speed)
		} else {
			q.vel = scale(norm(sub(q.pos, Vec3{0, 0, par.zSource})), speed)
		}
	}
	if e.Flags&particleFlatZ != 0 {
		q.pos[2] = 0
	}
	if e.Flags&particleLocal == 0 {
		q.pos, q.vel = transformPoint(mat, q.pos), transformDir(mat, q.vel)
	}
	return q, finite(q.pos[0]) && finite(q.pos[1]) && finite(q.pos[2]) && finite(q.vel[0]) && finite(q.vel[1]) && finite(q.vel[2])
}

// ParticleBatch is one emitter's quads in view space: 6 vertices of
// x, y, z, u, v, r, g, b, a per particle.
type ParticleBatch struct {
	Texture  int
	Blend    uint8
	Vertices []float32
}

// Batches builds the live particles' quads. view maps model space to view
// space; emitters are the transforms passed to Update.
func (p *ParticleSystem) Batches(view Mat4, emitters []Mat4) []ParticleBatch {
	var out []ParticleBatch
	for i := range p.states {
		e, st := &p.model.Emitters[i], &p.states[i]
		if len(st.particles) == 0 || i >= len(emitters) {
			continue
		}
		toView := view
		if e.Flags&particleLocal != 0 {
			toView = Multiply(view, emitters[i])
		}
		b := ParticleBatch{Texture: e.Texture, Blend: e.Blend, Vertices: make([]float32, 0, len(st.particles)*54)}
		for _, q := range st.particles {
			b.Vertices = p.quad(b.Vertices, e, q, toView)
		}
		if len(b.Vertices) > 0 {
			out = append(out, b)
		}
	}
	return out
}

func (p *ParticleSystem) quad(out []float32, e *Emitter, q particle, toView Mat4) []float32 {
	maxLife := q.life
	t := min(max(q.age/maxLife, 0), 1)
	col := e.Color.at(t, Quat{255, 255, 255})
	alpha := e.Alpha.at(t, Quat{1})[0]
	sc := e.Scale.at(t, Quat{1, 1})
	mx := max(1+seedUniform(q.seed, 1)*e.ScaleVary[0], 1e-4)
	my := mx
	if e.Flags&particleScaleXY != 0 {
		my = max(1+seedUniform(q.seed, 2)*e.ScaleVary[1], 1e-4)
	}
	sx, sy := sc[0]*mx, sc[1]*my
	if e.TwinklePercent > 0 && (e.TwinklePercent < 1 || e.TwinkleMin != 0 || e.TwinkleMax != 0) {
		r := particleRandTable[0x7f&(int(q.age*e.TwinkleSpeed)+int(q.seed&0x7f))]
		if e.TwinklePercent < r {
			return out
		}
		if e.TwinkleMin != 0 || e.TwinkleMax != 0 {
			k := e.TwinkleMin + (e.TwinkleMax-e.TwinkleMin)*r
			sx, sy = sx*k, sy*k
		}
	}
	if !(sx > 0 || sy > 0) || alpha <= 0 {
		return out
	}
	cells := e.Rows * e.Cols
	cell := int(e.HeadCell.at(t, Quat{})[0])
	if len(e.HeadCell.Times) == 0 && e.Flags&particleRandomCell != 0 {
		cell = int(q.seed>>16) % cells
	}
	cell = ((cell % cells) + cells) % cells
	u0, v0 := float32(cell%e.Cols)/float32(e.Cols), float32(cell/e.Cols)/float32(e.Rows)
	du, dv := 1/float32(e.Cols), 1/float32(e.Rows)

	center := transformPoint(toView, q.pos)
	m0, m1 := Vec3{sx, 0, 0}, Vec3{0, sy, 0}
	switch {
	case e.Flags&particleVelocity != 0:
		v := transformDir(toView, q.vel)
		l := float32(math.Hypot(float64(v[0]), float64(v[1])))
		if l < 1e-5 {
			break
		}
		n := Vec3{v[0] / l, v[1] / l, 0}
		m0, m1 = scale(n, sx), Vec3{n[1] * sy, -n[0] * sy, 0}
	case e.Flags&particleModelPlane != 0:
		m0, m1 = transformDir(toView, Vec3{sx, 0, 0}), transformDir(toView, Vec3{0, sy, 0})
	default:
		theta := e.BaseSpin + seedUniform(q.seed, 3)*e.BaseSpinVary + (e.Spin+seedUniform(q.seed, 4)*e.SpinVary)*q.age
		if e.Flags&particleFlipSpin != 0 && q.seed&1 != 0 {
			theta = -theta
		}
		if theta != 0 {
			c, s := cos(theta), sin(theta)
			m0, m1 = Vec3{c * sx, s * sx, 0}, Vec3{-s * sy, c * sy, 0}
		}
	}
	r, g, b := col[0]/255, col[1]/255, col[2]/255
	corner := func(vx, vy, tx, ty float32) {
		pos := add(center, add(scale(m0, vx), scale(m1, vy)))
		out = append(out, pos[0], pos[1], pos[2], u0+tx*du, v0+ty*dv, r, g, b, alpha)
	}
	corner(-1, 1, 0, 0)
	corner(-1, -1, 0, 1)
	corner(1, 1, 1, 0)
	corner(1, 1, 1, 0)
	corner(-1, -1, 0, 1)
	corner(1, -1, 1, 1)
	return out
}

// Count reports the live particles (diagnostics and tests).
func (p *ParticleSystem) Count() int {
	n := 0
	for _, s := range p.states {
		n += len(s.particles)
	}
	return n
}

// Emitter returns emitter e's model-space transform for instance i.
func (f Frame) Emitter(i int, e Emitter) Mat4 {
	m := f.Models[i]
	if pose := f.Poses[i]; e.Bone < len(pose) {
		m = Multiply(m, pose[e.Bone])
	}
	return Multiply(m, Translate(e.Position))
}

func cos(v float32) float32 { return float32(math.Cos(float64(v))) }
func sin(v float32) float32 { return float32(math.Sin(float64(v))) }
func add(a, b Vec3) Vec3    { return Vec3{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func sub(a, b Vec3) Vec3    { return Vec3{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func scale(a Vec3, s float32) Vec3 {
	return Vec3{a[0] * s, a[1] * s, a[2] * s}
}
func norm(a Vec3) Vec3 {
	l := float32(math.Sqrt(float64(a[0]*a[0] + a[1]*a[1] + a[2]*a[2])))
	if l < 1e-6 {
		return Vec3{0, 0, 1}
	}
	return scale(a, 1/l)
}
func transformPoint(m Mat4, v Vec3) Vec3 {
	return Vec3{m[0]*v[0] + m[4]*v[1] + m[8]*v[2] + m[12], m[1]*v[0] + m[5]*v[1] + m[9]*v[2] + m[13], m[2]*v[0] + m[6]*v[1] + m[10]*v[2] + m[14]}
}
func transformDir(m Mat4, v Vec3) Vec3 {
	return Vec3{m[0]*v[0] + m[4]*v[1] + m[8]*v[2], m[1]*v[0] + m[5]*v[1] + m[9]*v[2], m[2]*v[0] + m[6]*v[1] + m[10]*v[2]}
}
