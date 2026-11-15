package character

import (
	"image"
	"sort"
)

// Rect is a texture section in canvas pixels (Y grows downwards).
type Rect struct{ X, Y, W, H int }

// Layer is one texture drawn onto a character canvas (CharMaterialRenderer).
type Layer struct {
	Target int // ChrModelTextureTargetID or slotLayer*100+section; draw order
	Image  *image.NRGBA
	Rect   Rect
	Blend  int // ChrModelTextureLayer.BlendMode
}

// Canvas composites layers for one texture type.
type Canvas struct {
	Width, Height int
	Layers        map[int]Layer
}

func NewCanvas(w, h int) *Canvas { return &Canvas{Width: w, Height: h, Layers: map[int]Layer{}} }

// Set places a layer, replacing any previous layer with the same target.
func (c *Canvas) Set(l Layer) { c.Layers[l.Target] = l }

// Render draws all layers in target order onto an opaque black canvas.
func (c *Canvas) Render() *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, c.Width, c.Height))
	for i := 3; i < len(dst.Pix); i += 4 {
		dst.Pix[i] = 255
	}
	targets := make([]int, 0, len(c.Layers))
	for t := range c.Layers {
		targets = append(targets, t)
	}
	sort.Ints(targets)
	for _, t := range targets {
		l := c.Layers[t]
		r := image.Rect(l.Rect.X, l.Rect.Y, l.Rect.X+l.Rect.W, l.Rect.Y+l.Rect.H).Intersect(dst.Rect)
		if r.Empty() || l.Image == nil {
			continue
		}
		src := scale(l.Image, l.Rect.W, l.Rect.H)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				s := src.Pix[src.PixOffset(x-l.Rect.X, y-l.Rect.Y):]
				d := dst.Pix[dst.PixOffset(x, y):]
				blendPixel(d[:4], s[:4], l.Blend)
			}
		}
	}
	return dst
}

// scale resizes with bilinear filtering on straight (non-premultiplied)
// channels and clamped edges, matching how WebGL samples the source texture
// (colour under transparent texels is kept rather than premultiplied away).
func scale(src *image.NRGBA, w, h int) *image.NRGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	if sw == w && sh == h {
		return src
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	axis := func(i, dst, n int) (int, int, float32) {
		f := (float32(i)+0.5)*float32(n)/float32(dst) - 0.5
		f = max(0, min(float32(n-1), f))
		a := int(f)
		return a, min(a+1, n-1), f - float32(a)
	}
	for y := range h {
		y0, y1, fy := axis(y, h, sh)
		for x := range w {
			x0, x1, fx := axis(x, w, sw)
			p00 := src.Pix[src.PixOffset(src.Rect.Min.X+x0, src.Rect.Min.Y+y0):]
			p10 := src.Pix[src.PixOffset(src.Rect.Min.X+x1, src.Rect.Min.Y+y0):]
			p01 := src.Pix[src.PixOffset(src.Rect.Min.X+x0, src.Rect.Min.Y+y1):]
			p11 := src.Pix[src.PixOffset(src.Rect.Min.X+x1, src.Rect.Min.Y+y1):]
			d := out.Pix[out.PixOffset(x, y):]
			for c := range 4 {
				top := float32(p00[c])*(1-fx) + float32(p10[c])*fx
				bottom := float32(p01[c])*(1-fx) + float32(p11[c])*fx
				d[c] = uint8(top*(1-fy) + bottom*fy + 0.5)
			}
		}
	}
	return out
}

// blendPixel mirrors wow.export's char.fragment.shader plus the GL blend state.
func blendPixel(d, s []byte, mode int) {
	f := func(b byte) float32 { return float32(b) / 255 }
	sr, sg, sb, sa := f(s[0]), f(s[1]), f(s[2]), f(s[3])
	dr, dg, db, da := f(d[0]), f(d[1]), f(d[2]), f(d[3])
	switch mode {
	case 0, 1:
		copy(d, s)
		return
	case 4:
		sr, sg, sb, sa = dr*sr, dg*sg, db*sb, da*sa
	case 6:
		ov := func(base, blend float32) float32 {
			if blend < 0.5 {
				return 2 * base * blend
			}
			return 1 - 2*(1-base)*(1-blend)
		}
		sr, sg, sb = ov(dr, sr), ov(dg, sg), ov(db, sb)
	case 7:
		sr, sg, sb = 1-(1-dr)*(1-sr), 1-(1-dg)*(1-sg), 1-(1-db)*(1-sb)
	}
	out := [4]float32{sr*sa + dr*(1-sa), sg*sa + dg*(1-sa), sb*sa + db*(1-sa), sa*sa + da*(1-sa)}
	if mode == 9 {
		out[3] = sa + da*(1-sa)
	}
	for i, v := range out {
		d[i] = byte(min(255, max(0, v*255+0.5)))
	}
}
