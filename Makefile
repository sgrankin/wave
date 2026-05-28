GO ?= go

.PHONY: all build test test-race vet lint fmt tidy clean

all: build test

## build: compile everything cgo-free (single-binary goal)
build:
	CGO_ENABLED=0 $(GO) build ./...

## test: run the test suite
test:
	$(GO) test ./...

## test-race: run tests under the race detector (needs cgo)
test-race:
	$(GO) test -race ./...

## vet: run go vet
vet:
	$(GO) vet ./...

## lint: run golangci-lint
lint:
	golangci-lint run

## fmt: format the tree
fmt:
	$(GO) fmt ./...

## tidy: tidy module requirements
tidy:
	$(GO) mod tidy

## clean: remove build artifacts
clean:
	$(GO) clean
	rm -rf dist/
