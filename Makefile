.PHONY: build format test vet validate clean

GOCACHE ?= /tmp/posthouse-go-cache

build:
	mkdir -p bin
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/posthouse ./cmd/posthouse

format:
	gofmt -w cmd internal

test:
	GOCACHE=$(GOCACHE) go test -race ./...

vet:
	GOCACHE=$(GOCACHE) go vet ./...

validate: vet test
	test -z "$$(gofmt -l cmd internal)"
	GOCACHE=$(GOCACHE) go build -o /tmp/posthouse-validate ./cmd/posthouse

clean:
	rm -rf bin
