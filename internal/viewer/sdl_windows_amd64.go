package viewer

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Generated from the checksum-verified official release by make build-windows.
//
//go:embed runtime/windows-amd64/SDL2.dll runtime/windows-amd64/README-SDL.txt
var sdlRuntime embed.FS

var sdlDirectory string

func openSDL() (uintptr, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return 0, err
	}
	base := filepath.Join(cache, "armory", "runtime")
	if err := os.MkdirAll(base, 0700); err != nil {
		return 0, err
	}
	dir, err := os.MkdirTemp(base, "sdl2-")
	if err != nil {
		return 0, err
	}
	loaded := false
	defer func() {
		if !loaded {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, name := range []string{"SDL2.dll", "README-SDL.txt"} {
		data, err := sdlRuntime.ReadFile("runtime/windows-amd64/" + name)
		if err != nil {
			return 0, err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return 0, err
		}
	}
	// Use a fully qualified path, never a DLL from the working directory.
	lib, err := syscall.LoadLibrary(filepath.Join(dir, "SDL2.dll"))
	if err != nil {
		return 0, fmt.Errorf("load embedded SDL2: %w", err)
	}
	loaded, sdlDirectory = true, dir
	return uintptr(lib), nil
}

func closeSDL(lib uintptr) {
	if err := syscall.FreeLibrary(syscall.Handle(lib)); err == nil {
		_ = os.RemoveAll(sdlDirectory)
		sdlDirectory = ""
	}
}
