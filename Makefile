.PHONY: build generate generate-check format test test-integration test-e2e vet validate validate-all clean

GOCACHE ?= /tmp/posthouse-go-cache

build:
	mkdir -p bin
	CGO_ENABLED=0 GOCACHE=$(GOCACHE) go build -trimpath -o bin/posthouse ./cmd/posthouse

generate:
	GOCACHE=$(GOCACHE) go run github.com/grindlemire/go-tui/cmd/tui generate internal/tui/app.gsx

generate-check: generate
	git diff --exit-code -- internal/tui/app_gsx.go

format:
	gofmt -w cmd internal

test:
	GOCACHE=$(GOCACHE) go test -race ./...

test-integration:
	./scripts/test-protocols.sh integration

test-e2e: build
	POSTHOUSE_TEST_BINARY="$(CURDIR)/bin/posthouse" ./scripts/test-protocols.sh e2e

vet:
	GOCACHE=$(GOCACHE) go vet ./...

validate: vet test
	test -z "$$(gofmt -l cmd internal)"
	CGO_ENABLED=0 GOCACHE=$(GOCACHE) go build -o /tmp/posthouse-validate ./cmd/posthouse

validate-all: validate generate-check test-integration test-e2e

clean:
	rm -rf bin
