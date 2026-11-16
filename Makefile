.PHONY: build build-windows windows windows-runtime test check smoke

SDL2_VERSION := 2.32.10
SDL2_WINDOWS_ARCHIVE := build/SDL2-$(SDL2_VERSION)-win32-x64.zip
SDL2_WINDOWS_SHA256 := 6cf9706eefd0a4a06dc764007934d428afaf029fabdd408a9e646048c91e18fb
SDL2_WINDOWS_RUNTIME := internal/viewer/runtime/windows-amd64

build:
	mkdir -p build
	CGO_ENABLED=0 go build -o build/armory ./cmd/armory

windows: build-windows

build-windows: windows-runtime
	mkdir -p build
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o build/armory-windows-amd64.exe ./cmd/armory

$(SDL2_WINDOWS_ARCHIVE):
	mkdir -p build
	curl --fail --location --retry 3 --output "$@.tmp" "https://github.com/libsdl-org/SDL/releases/download/release-$(SDL2_VERSION)/SDL2-$(SDL2_VERSION)-win32-x64.zip"
	mv "$@.tmp" "$@"

windows-runtime: $(SDL2_WINDOWS_ARCHIVE)
	printf '%s  %s\n' "$(SDL2_WINDOWS_SHA256)" "$(SDL2_WINDOWS_ARCHIVE)" | sha256sum --check -
	mkdir -p "$(SDL2_WINDOWS_RUNTIME)"
	unzip -p "$(SDL2_WINDOWS_ARCHIVE)" SDL2.dll > "$(SDL2_WINDOWS_RUNTIME)/SDL2.dll"
	unzip -p "$(SDL2_WINDOWS_ARCHIVE)" README-SDL.txt > "$(SDL2_WINDOWS_RUNTIME)/README-SDL.txt"

test:
	go test -race ./...

check:
	go vet ./...
	test -z "$$(gofmt -l cmd internal)"

smoke: build
	./build/armory self-test
