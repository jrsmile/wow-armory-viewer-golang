package character

import (
	"image"
	"image/color"
	"reflect"
	"testing"
)

func TestParseArmory(t *testing.T) {
	doc := `{"character":{"name":"Naowh"},"playable_race":{"id":10},"playable_class":{"id":5},
	"gender":{"type":"FEMALE"},"customizations":[{"option":{"id":1},"choice":{"id":2}}],
	"items":[{"id":100,"internal_slot_id":0,"item_appearance_modifier_id":0},
	{"id":300,"internal_slot_id":2,"item_appearance_modifier_id":1,"secondary_id":301,"secondary_item_appearance_modifier_id":4},
	{"id":500,"internal_slot_id":4,"enchant":213}]}`
	a, err := ParseArmory([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{{SlotHead, 100, 0, 0}, {SlotShoulder, 300, 1, 0}, {SlotRightShoulder, 301, 4, 0}, {SlotChest, 500, 0, 213}}
	if a.Name != "Naowh" || a.Race != 10 || a.Class != 5 || !a.Female || a.Choices[1] != 2 || !reflect.DeepEqual(a.Items, want) {
		t.Fatalf("got %+v", a)
	}

	// Without a split transmog both shoulders use the same appearance.
	a, err = ParseArmory([]byte(`{"playable_race":{"id":2},"gender":{"type":"MALE"},
	"items":[{"id":300,"internal_slot_id":2,"item_appearance_modifier_id":3}]}`))
	if err != nil || a.Female || !reflect.DeepEqual(a.Items, []Item{{SlotShoulder, 300, 3, 0}, {SlotRightShoulder, 300, 3, 0}}) {
		t.Fatalf("got %+v %v", a, err)
	}
	if _, err := ParseArmory([]byte(`{"gender":{"type":"MALE"}}`)); err == nil {
		t.Error("missing race accepted")
	}
}

func solid(c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, c)
	return img
}

func TestCanvasOrderAndBlend(t *testing.T) {
	cv := NewCanvas(4, 2)
	// Added out of order: targets, not insertion, decide drawing order.
	cv.Set(Layer{Target: 300, Image: solid(color.NRGBA{0, 0, 255, 128}), Rect: Rect{0, 1, 4, 1}, Blend: 15})
	cv.Set(Layer{Target: 200, Image: solid(color.NRGBA{128, 128, 128, 255}), Rect: Rect{2, 0, 2, 2}, Blend: 4})
	cv.Set(Layer{Target: 100, Image: solid(color.NRGBA{200, 0, 0, 255}), Rect: Rect{0, 0, 4, 2}, Blend: 1})
	cv.Set(Layer{Target: 100, Image: solid(color.NRGBA{255, 0, 0, 255}), Rect: Rect{0, 0, 4, 2}, Blend: 1})
	out := cv.Render()
	near := func(got color.NRGBA, want color.NRGBA) bool {
		d := func(a, b uint8) bool { return int(a)+2 >= int(b) && int(b)+2 >= int(a) }
		return d(got.R, want.R) && d(got.G, want.G) && d(got.B, want.B) && d(got.A, want.A)
	}
	for _, c := range []struct {
		x, y int
		want color.NRGBA
	}{
		{0, 0, color.NRGBA{255, 0, 0, 255}},   // replaced base layer
		{3, 0, color.NRGBA{128, 0, 0, 255}},   // multiplied by 50% gray
		{0, 1, color.NRGBA{127, 0, 128, 191}}, // 50% blue over red; GL SRC_ALPHA also scales alpha
		{3, 1, color.NRGBA{64, 0, 128, 191}},  // over the multiplied half
	} {
		if got := out.NRGBAAt(c.x, c.y); !near(got, c.want) {
			t.Errorf("pixel %d,%d = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestHiddenAppearanceLeavesSlotEmpty(t *testing.T) {
	b := &build{app: Appearance{Items: []Item{{Slot: SlotBack, ItemID: 134111}, {Slot: SlotShoulder, ItemID: 134112}}}}
	if got := b.resolveItems(); len(got) != 0 {
		t.Fatalf("hidden items resolved: %v", got)
	}
}
