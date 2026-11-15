package model

import (
	"context"
	"fmt"
	"image"
	"sync"
)

type Files interface {
	File(context.Context, uint32) ([]byte, error)
}

type Asset struct {
	Model    *Model
	Textures []*image.NRGBA
	Warnings []string
}

// Options controls how replaceable textures (M2 texture type != 0) are bound.
type Options struct {
	TypeImages map[uint32]*image.NRGBA // replaceable type -> pre-composited image
	TypeFiles  map[uint32]uint32       // replaceable type -> texture FileDataID
	Cache      *TextureCache
}

// TextureCache shares decoded BLP textures between models.
type TextureCache struct {
	mu     sync.Mutex
	images map[uint32]*image.NRGBA
}

func (c *TextureCache) Get(ctx context.Context, files Files, id uint32) (*image.NRGBA, error) {
	if c != nil {
		c.mu.Lock()
		img, ok := c.images[id]
		c.mu.Unlock()
		if ok {
			return img, nil
		}
	}
	raw, err := files.File(ctx, id)
	if err != nil {
		return nil, err
	}
	img, err := DecodeBLP(raw)
	if err != nil {
		return nil, err
	}
	if c != nil {
		c.mu.Lock()
		if c.images == nil {
			c.images = make(map[uint32]*image.NRGBA)
		}
		c.images[id] = img
		c.mu.Unlock()
	}
	return img, nil
}

func Load(ctx context.Context, files Files, id uint32, replacement uint32) (*Asset, error) {
	opts := Options{}
	if replacement != 0 {
		opts.TypeFiles = map[uint32]uint32{1: replacement}
	}
	return LoadWith(ctx, files, id, opts)
}

func LoadWith(ctx context.Context, files Files, id uint32, opts Options) (*Asset, error) {
	raw, err := files.File(ctx, id)
	if err != nil {
		return nil, err
	}
	m, err := ParseM2(raw)
	if err != nil {
		return nil, err
	}
	if len(m.SkinIDs) == 0 {
		return nil, fmt.Errorf("model has no SFID; filename-based legacy skins are not implemented")
	}
	raw, err = files.File(ctx, m.SkinIDs[0])
	if err != nil {
		return nil, err
	}
	if err = m.SetSkin(raw); err != nil {
		return nil, err
	}
	if m.SkeletonID != 0 {
		raw, err = files.File(ctx, m.SkeletonID)
		if err != nil {
			return nil, err
		}
		parent, err := m.SetSkeleton(raw)
		if err != nil {
			return nil, err
		}
		seen := map[uint32]bool{m.SkeletonID: true}
		for parent != 0 {
			if seen[parent] || len(seen) > 16 {
				return nil, fmt.Errorf("invalid parent skeleton chain")
			}
			seen[parent] = true
			raw, err = files.File(ctx, parent)
			if err != nil {
				return nil, err
			}
			parent, err = m.MergeParentAnimations(raw)
			if err != nil {
				return nil, err
			}
		}
	}
	a := &Asset{Model: m, Textures: make([]*image.NRGBA, len(m.Textures))}
	animID, err := m.StandFileID()
	if err == nil {
		var animation []byte
		if animID != 0 {
			animation, err = files.File(ctx, animID)
		}
		if err == nil {
			err = m.PrepareStand(animation)
		}
	}
	if err != nil {
		m.StandError = err.Error()
		a.Warnings = append(a.Warnings, fmt.Sprintf("model %d: bind pose only: %v", id, err))
		m.Stand = -1
		if err = m.PrepareStand(nil); err != nil {
			m.Bones = nil
		}
	}
	for i, t := range m.Textures {
		if img := opts.TypeImages[t.Type]; t.Type != 0 && img != nil {
			a.Textures[i] = img
			continue
		}
		textureID := t.FileID
		if t.Type != 0 {
			textureID = opts.TypeFiles[t.Type]
		}
		if textureID == 0 {
			if t.Type != 0 {
				a.Warnings = append(a.Warnings, fmt.Sprintf("model %d: texture %d (replaceable type %d) unresolved", id, i, t.Type))
			}
			continue
		}
		a.Textures[i], err = opts.Cache.Get(ctx, files, textureID)
		if err != nil {
			a.Warnings = append(a.Warnings, fmt.Sprintf("model %d: texture %d: %v", id, textureID, err))
		}
	}
	return a, nil
}
