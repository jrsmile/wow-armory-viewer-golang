package main

import (
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
