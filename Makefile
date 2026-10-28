.PHONY: build test check

build:
	go build -trimpath -o bin/curvine-operator ./cmd/curvine-operator

test:
	go test -race ./...

check: test
	go vet ./...
	test -z "$$(gofmt -l api internal cmd)"
