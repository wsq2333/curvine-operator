.PHONY: build test check generate test-envtest

CONTROLLER_GEN_VERSION := v0.21.0

build:
	go build -trimpath -o bin/curvine-operator ./cmd/curvine-operator
	go build -trimpath -o bin/manager ./cmd/manager

test:
	go test -race ./...

check: test
	go vet ./...
	test -z "$$(gofmt -l api internal cmd tests)"

generate:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) object crd paths=./api/... output:crd:artifacts:config=config/crd/bases

test-envtest:
	go test -race -tags=integration -count=1 -timeout=5m ./tests/integration
