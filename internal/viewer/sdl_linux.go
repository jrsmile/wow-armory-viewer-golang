package viewer

import "github.com/ebitengine/purego"

func openSDL() (uintptr, error) {
	return purego.Dlopen("libSDL2-2.0.so.0", purego.RTLD_NOW|purego.RTLD_LOCAL)
}

func closeSDL(lib uintptr) {
	_ = purego.Dlclose(lib)
}
