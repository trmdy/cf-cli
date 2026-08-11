# Pinned spec revision: bump SPEC_REF deliberately (make spec gen, review
# docs/generated/products.md diff, commit). The spec-sync workflow opens a PR
# for this weekly.
SPEC_REF := 4e2f140437b8
SPEC_URL := https://raw.githubusercontent.com/cloudflare/api-schemas/$(SPEC_REF)/openapi.json
SPEC := specs/openapi.json
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/trmdy/cf-cli/internal/cli.Version=$(VERSION)

.PHONY: all build test vet fmt gen spec check clean

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/cf ./cmd/cf

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

check: vet test build

clean:
	rm -rf bin
