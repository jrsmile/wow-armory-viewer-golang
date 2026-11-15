.PHONY: build test check smoke

build:
	mkdir -p build
	CGO_ENABLED=0 go build -o build/armory ./cmd/armory

test:
	go test -race ./...

check:
	go vet ./...
	test -z "$$(gofmt -l cmd internal)"

smoke: build
	./build/armory self-test
