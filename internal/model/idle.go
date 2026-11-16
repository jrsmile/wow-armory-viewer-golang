package model

import "math/rand"

// Stand repeats for this long (randomized in between) before a fidget plays.
const (
	minStandMS = 6000
	maxStandMS = 12000
	minBlendMS = 150
	maxBlendMS = 500
)

// IdleCycle schedules the body's idle like the game client does: Stand loops
// for a while, then one fidget (a Stand variation such as a yawn) plays once,
// then back to Stand. Fidgets rotate in shuffled order so each one is shown
// before any repeats, and every clip change is cross-faded.
type IdleCycle struct {
	m          *Model
	rng        *rand.Rand
	order      []int
	clip, prev int
	start      uint32 // scene time the current clip began
	standUntil uint32 // earliest scene time a fidget may start
	blend      uint32 // cross-fade length into the current clip
	last       uint32
	started    bool
}

func NewIdleCycle(m *Model, seed int64) *IdleCycle {
	return &IdleCycle{m: m, rng: rand.New(rand.NewSource(seed))}
}

func (c *IdleCycle) standTime() uint32 {
	return uint32(minStandMS + c.rng.Intn(maxStandMS-minStandMS+1))
}

func (c *IdleCycle) reset(ms uint32) {
	c.clip, c.prev, c.start, c.blend, c.last, c.started = 0, 0, ms, 0, ms, true
	c.standUntil = ms + c.standTime()
}

// next picks the clip that follows the one ending at scene time end.
func (c *IdleCycle) next(end uint32) int {
	if c.clip != 0 {
		c.standUntil = end + c.standTime()
		return 0
	}
	if len(c.m.Idles) == 0 || end < c.standUntil {
		return 0
	}
	if len(c.order) == 0 {
		c.order = c.rng.Perm(len(c.m.Idles))
		for i := range c.order {
			c.order[i]++
		}
		if len(c.order) > 1 && c.order[0] == c.prev {
			c.order[0], c.order[len(c.order)-1] = c.order[len(c.order)-1], c.order[0]
		}
	}
	n := c.order[0]
	c.order = c.order[1:]
	return n
}

func (c *IdleCycle) blendTime(from, to int) uint32 {
	clip := func(i int) (uint16, uint16) {
		if i > 0 {
			return c.m.Idles[i-1].BlendIn, c.m.Idles[i-1].BlendOut
		}
		s := c.m.Sequences[c.m.Stand]
		return s.BlendIn, s.BlendOut
	}
	_, out := clip(from)
	in, _ := clip(to)
	return min(max(uint32(max(out, in)), minBlendMS), maxBlendMS)
}

// Clip reports the clip playing at the last evaluated time (0: Stand).
func (c *IdleCycle) Clip() int { return c.clip }

// At returns the blend to show at scene time ms. Time must not go backwards
// except to restart the cycle (a new scene or a reset clock).
func (c *IdleCycle) At(ms uint32) Blend {
	if c.m.Stand < 0 {
		return Blend{From: ClipTime{Time: ms}}
	}
	if !c.started || ms < c.last {
		c.reset(ms)
	}
	c.last = ms
	for {
		d := c.m.ClipDuration(c.clip)
		if d == 0 || ms-c.start < d {
			break
		}
		end := c.start + d
		n := c.next(end)
		if n == 0 && c.clip == 0 {
			c.start, c.blend = end, 0 // Stand loops seamlessly by itself
			continue
		}
		c.prev, c.blend, c.clip, c.start = c.clip, c.blendTime(c.clip, n), n, end
	}
	t := ms - c.start
	b := Blend{From: ClipTime{c.clip, t}}
	if t < c.blend {
		from := ClipTime{c.prev, t} // Stand keeps looping under the fade
		if c.prev != 0 {
			from.Time = c.m.ClipDuration(c.prev) - 1 // a finished fidget holds its last frame
		}
		x := float32(t) / float32(c.blend)
		b = Blend{From: from, To: b.From, Weight: x * x * (3 - 2*x)}
	}
	return b
}
