package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteNewPreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	if err := writeNew(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(path, []byte("second")); err == nil {
		t.Fatal("overwrote existing file")
	}
	out, err := os.ReadFile(path)
	if err != nil || string(out) != "first" {
		t.Fatal(string(out), err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary files leaked", entries, err)
	}
}

func TestLoadPNG(t *testing.T) {
	dir := t.TempDir()
	src := image.NewPaletted(image.Rect(0, 0, 3, 2), color.Palette{color.NRGBA{0, 0, 0, 0}, color.NRGBA{10, 20, 30, 128}})
	src.SetColorIndex(2, 1, 1)
	path := filepath.Join(dir, "bg.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	img, err := loadPNG(path)
	if err != nil || img.Rect != image.Rect(0, 0, 3, 2) || img.NRGBAAt(2, 1) != (color.NRGBA{10, 20, 30, 128}) {
		t.Fatal(img, err)
	}
	if err := os.WriteFile(path, []byte("not a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPNG(path); err == nil {
		t.Fatal("accepted invalid PNG")
	}
}
