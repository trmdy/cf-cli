# Pinned spec revision: bump SPEC_REF deliberately (make spec gen, review
# docs/generated/products.md diff, commit). The spec-sync workflow opens a PR
# for this weekly.
SPEC_REF := 0762c8781bab
SPEC_URL := https://raw.githubusercontent.com/cloudflare/api-schemas/$(SPEC_REF)/openapi.json
SPEC := specs/openapi.json
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/trmdy/cf-cli/internal/cli.Version=$(VERSION)

BINDIR ?= $(shell [ -d /opt/homebrew/bin ] && echo /opt/homebrew/bin || echo /usr/local/bin)

.PHONY: all build install test vet fmt gen spec lint check clean

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/cf ./cmd/cf

install: build
	install bin/cf $(BINDIR)/cf
	@echo "installed $(BINDIR)/cf"

spec:
	mkdir -p specs
	curl -sL -o $(SPEC) $(SPEC_URL)

gen:
	go run ./tools/gen -spec $(SPEC) -mapping tools/gen/mapping.yaml \
		-out internal/registry/data/registry.json.gz -products docs/generated/products.md

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

lint:
	go run ./tools/lint

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:" && gofmt -l . && exit 1)

check: fmt-check vet lint test build

clean:
	rm -rf bin
