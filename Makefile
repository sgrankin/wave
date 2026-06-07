GO ?= go
NPM ?= npm

.PHONY: all build test test-race vet lint fmt tidy clean release \
	web-build web-test check check-all

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

## web-build: build the browser bundle into web/dist
web-build:
	cd web && $(NPM) run build

## web-test: type-check + unit (node) + component (headless Chromium) tests
web-test:
	cd web && $(NPM) run typecheck && $(NPM) test && $(NPM) run test:web

## check: the fast pre-commit suite — Go build+test + web type/unit/component
check: build test web-test

## check-all: everything, including the heavy end-to-end browser convergence
## test (builds the bundle, spawns waved, launches Chromium). May need the host
## sandbox disabled for spawn/loopback/browser.
check-all: check
	cd web && $(NPM) run test:browser

## release: build the self-contained single binary — the web client embedded
## (-tags embed) so it ships as one file with no -webroot. Output: ./waved.
release: web-build
	CGO_ENABLED=0 $(GO) build -tags embed -o waved ./cmd/waved

## clean: remove build artifacts
clean:
	$(GO) clean
	rm -rf dist/ web/dist/ waved
