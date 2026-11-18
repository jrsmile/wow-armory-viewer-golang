//go:build linux || (windows && amd64)

package viewer

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"
)

func mouseEvent(kind uint32, button byte, buttons uint32, dx, dy int32) [56]byte {
	var event [56]byte
	binary.LittleEndian.PutUint32(event[:4], kind)
	if kind == 0x400 {
		binary.LittleEndian.PutUint32(event[16:20], buttons)
		binary.LittleEndian.PutUint32(event[28:32], uint32(dx))
		binary.LittleEndian.PutUint32(event[32:36], uint32(dy))
	} else {
		event[16] = button
	}
	return event
}

func TestMouseDragRotation(t *testing.T) {
	var d mouseDrag
	var modes []int32
	setRelative := func(mode int32) int32 {
		modes = append(modes, mode)
		return 0
	}
	send := func(event [56]byte, hasScene bool) float32 {
		return d.handle(&event, hasScene, setRelative)
	}
	down := mouseEvent(0x401, 1, 0, 0, 0)
	move := mouseEvent(0x400, 0, 1, 40, 100)
	up := mouseEvent(0x402, 1, 0, 0, 0)
	if send(move, true) != 0 || send(down, false) != 0 || send(move, true) != 0 {
		t.Fatal("motion without a drag or scene rotated the model")
	}
	send(mouseEvent(0x401, 3, 0, 0, 0), true)
	if send(move, true) != 0 {
		t.Fatal("right mouse button started a drag")
	}
	send(down, true)
	send(down, true)
	if got := send(move, true); math.Abs(float64(got-0.4)) > 1e-6 {
		t.Fatalf("rightward drag = %v, want 0.4 radians", got)
	}
	if got := send(mouseEvent(0x400, 0, 1, -25, -100), true); math.Abs(float64(got+0.25)) > 1e-6 {
		t.Fatalf("leftward drag = %v, want -0.25 radians", got)
	}
	send(mouseEvent(0x402, 3, 0, 0, 0), true)
	if !d.active {
		t.Fatal("releasing another button stopped the drag")
	}
	send(up, true)
	if send(move, true) != 0 || d.active || !reflect.DeepEqual(modes, []int32{1, 0}) {
		t.Fatalf("drag did not stop on release: active=%v modes=%v", d.active, modes)
	}
}

func TestMouseDragCancellationAndFallback(t *testing.T) {
	var d mouseDrag
	var modes []int32
	setRelative := func(mode int32) int32 {
		modes = append(modes, mode)
		return 0
	}
	down := mouseEvent(0x401, 1, 0, 0, 0)
	d.handle(&down, true, setRelative)
	var focusLost [56]byte
	binary.LittleEndian.PutUint32(focusLost[:4], 0x200)
	focusLost[12] = 13
	d.handle(&focusLost, true, setRelative)
	if d.active || !reflect.DeepEqual(modes, []int32{1, 0}) {
		t.Fatalf("focus loss left mouse captured: active=%v modes=%v", d.active, modes)
	}
	d.handle(&down, true, setRelative)
	noButton := mouseEvent(0x400, 0, 0, 10, 0)
	if got := d.handle(&noButton, true, setRelative); got != 0 || d.active || !reflect.DeepEqual(modes, []int32{1, 0, 1, 0}) {
		t.Fatalf("lost release left mouse captured: delta=%v active=%v modes=%v", got, d.active, modes)
	}

	modes = nil
	setRelative = func(mode int32) int32 {
		modes = append(modes, mode)
		return -1
	}
	d.handle(&down, true, setRelative)
	move := mouseEvent(0x400, 0, 1, 10, 0)
	if got := d.handle(&move, true, setRelative); math.Abs(float64(got-0.1)) > 1e-6 {
		t.Fatalf("drag did not fall back when relative mode failed: %v", got)
	}
	up := mouseEvent(0x402, 1, 0, 0, 0)
	d.handle(&up, true, setRelative)
	if d.active || !reflect.DeepEqual(modes, []int32{1}) {
		t.Fatalf("fallback drag did not stop: active=%v modes=%v", d.active, modes)
	}
}
