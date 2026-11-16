package model

import (
	"math"
	"time"
)

// Instance is one model in a character scene.
type Instance struct {
	Name    string
	Asset   *Asset
	Geosets map[uint16]bool // visible submesh IDs; nil shows every geoset
	// BoneMap maps this model's bones onto the scene root skeleton (-1: own
	// animation). Used for skinned customization and collection models.
	BoneMap []int
	// Attachment parents the model to a root bone at a local offset.
	Attached   bool
	AttachBone int
	AttachPos  Vec3
}

// Scene is an assembled character; Instances[0] is the body (root skeleton).
type Scene struct {
	Title     string
	Instances []*Instance
	Warnings  []string
	idle      *IdleCycle
}

// SingleScene wraps one asset for diagnostic viewing.
func SingleScene(a *Asset, title string) *Scene {
	return &Scene{Title: title, Instances: []*Instance{{Name: "model", Asset: a}}, Warnings: a.Warnings}
}

// Visible reports whether a batch's geoset is shown.
func (in *Instance) Visible(geoset uint16) bool {
	return in.Geosets == nil || in.Geosets[geoset]
}

// Frame holds per-instance bone palettes and model matrices for one time.
type Frame struct {
	Poses  [][]Mat4
	Models []Mat4
}

func (s *Scene) Root() *Model {
	if len(s.Instances) == 0 {
		return nil
	}
	return s.Instances[0].Asset.Model
}

// Animate evaluates every instance at the given time. The body rotates
// through Stand and its idle fidgets; ms must grow monotonically (a smaller
// value restarts the idle cycle).
func (s *Scene) Animate(ms uint32) (Frame, error) {
	blend := Blend{From: ClipTime{Time: ms}}
	if root := s.Root(); root != nil && len(root.Idles) > 0 {
		if s.idle == nil {
			s.idle = NewIdleCycle(root, time.Now().UnixNano())
		}
		blend = s.idle.At(ms)
	}
	return s.animate(blend, ms)
}

// IdleClip reports the body clip last played by Animate (0: Stand,
// k: Root().Idles[k-1]).
func (s *Scene) IdleClip() int {
	if s.idle == nil {
		return 0
	}
	return s.idle.Clip()
}

func (s *Scene) animate(blend Blend, ms uint32) (Frame, error) {
	f := Frame{Poses: make([][]Mat4, len(s.Instances)), Models: make([]Mat4, len(s.Instances))}
	var root []Mat4
	for i, in := range s.Instances {
		m := in.Asset.Model
		var override func(int) (Mat4, bool)
		if i > 0 && root != nil && in.BoneMap != nil {
			override = func(b int) (Mat4, bool) {
				if b < len(in.BoneMap) && in.BoneMap[b] >= 0 && in.BoneMap[b] < len(root) {
					return root[in.BoneMap[b]], true
				}
				return Mat4{}, false
			}
		}
		var pose []Mat4
		var err error
		if i == 0 {
			pose, err = m.PoseBlend(blend, ms, override)
		} else {
			pose, err = m.PoseWith(ms, override)
		}
		if err != nil {
			return f, err
		}
		if i == 0 {
			root = pose
		}
		f.Poses[i] = pose
		f.Models[i] = Identity()
		if in.Attached && root != nil && in.AttachBone < len(root) {
			f.Models[i] = Multiply(root[in.AttachBone], Translate(in.AttachPos))
		}
	}
	return f, nil
}

// MapBones matches bones of m onto the root skeleton by name CRC and pivot.
func MapBones(m, root *Model) []int {
	out := make([]int, len(m.Bones))
	close := func(a, b Vec3) bool {
		return math.Abs(float64(a[0]-b[0])) < 1e-3 && math.Abs(float64(a[1]-b[1])) < 1e-3 && math.Abs(float64(a[2]-b[2])) < 1e-3
	}
	for i, b := range m.Bones {
		out[i] = -1
		for j, r := range root.Bones {
			if b.CRC != 0 && b.CRC == r.CRC && close(b.Pivot, r.Pivot) {
				out[i] = j
				break
			}
		}
		if out[i] >= 0 {
			continue
		}
		if b.CRC != 0 {
			for j, r := range root.Bones {
				if b.CRC == r.CRC {
					out[i] = j
					break
				}
			}
		}
		if out[i] < 0 && i < len(root.Bones) && root.Bones[i].CRC == b.CRC && close(b.Pivot, root.Bones[i].Pivot) {
			out[i] = i
		}
	}
	return out
}

// Bounds returns the bounding box of the visible body geometry at time ms,
// which keeps the camera framed on the character rather than on hidden or
// oversized attachments.
func (s *Scene) Bounds(ms uint32) (Vec3, Vec3) {
	lo := Vec3{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32}
	hi := Vec3{-math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}
	frame, err := s.animate(Blend{From: ClipTime{Time: ms}}, ms)
	for i, in := range s.Instances {
		if i > 0 && in.Attached {
			continue
		}
		m := in.Asset.Model
		used := make([]bool, len(m.Vertices))
		for _, b := range m.Batches {
			if !in.Visible(b.Geoset) {
				continue
			}
			for _, idx := range m.Indices[b.Start : b.Start+b.Count] {
				used[idx] = true
			}
		}
		for vi, v := range m.Vertices {
			if !used[vi] {
				continue
			}
			p := v.Position
			if err == nil && len(m.Bones) > 0 {
				p = skin(frame.Poses[i], v)
			}
			for k := range 3 {
				lo[k], hi[k] = min(lo[k], p[k]), max(hi[k], p[k])
			}
		}
	}
	if lo[0] > hi[0] {
		if root := s.Root(); root != nil {
			return root.Min, root.Max
		}
		return Vec3{-1, -1, -1}, Vec3{1, 1, 1}
	}
	return lo, hi
}

func skin(pose []Mat4, v Vertex) Vec3 {
	var out Vec3
	total := float32(0)
	for j, w := range v.Weights {
		if w == 0 || int(v.Bones[j]) >= len(pose) {
			continue
		}
		p := Transform(pose[v.Bones[j]], v.Position)
		f := float32(w) / 255
		for k := range 3 {
			out[k] += p[k] * f
		}
		total += f
	}
	if total == 0 {
		return v.Position
	}
	for k := range 3 {
		out[k] /= total
	}
	return out
}

// Transform applies m to point p.
func Transform(m Mat4, p Vec3) Vec3 {
	return Vec3{
		m[0]*p[0] + m[4]*p[1] + m[8]*p[2] + m[12],
		m[1]*p[0] + m[5]*p[1] + m[9]*p[2] + m[13],
		m[2]*p[0] + m[6]*p[1] + m[10]*p[2] + m[14],
	}
}
