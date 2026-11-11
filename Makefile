.PHONY: build test lint fuzz ui web sdk depguard clean proto docker

GO ?= go

build:
	$(GO) build -o bin/organond ./cmd/organond
	$(GO) build -o bin/organon ./cmd/organon

test:
	$(GO) test -race ./...

lint:
	golangci-lint run ./...

# Regenerate the layer rules in .golangci.yml (spec 15.2, ADR 0004).
depguard:
	./scripts/gen-depguard.sh

fuzz:
	$(GO) test ./internal/schema/domain -run '^$$' -fuzz FuzzCompile -fuzztime 10m

# Build the admin console / flow editor; output is embedded into organond.
ui:
	cd admin && pnpm install --frozen-lockfile && pnpm run build

# Build the end-user UI renderer (ADR 0016); output is embedded into organond.
web:
	cd web && pnpm install --frozen-lockfile && pnpm run build

clean:
	rm -rf bin

# Generate the tracker client and typecheck the SDK (spec 13).
sdk:
	$(GO) run ./cmd/organon codegen ts examples/tracker --out sdk/ts/gen
	$(GO) run ./cmd/organon codegen ts examples/tracker --out sdk/ts/smoke/gen --client-import ../../client/src/index.ts
	cd sdk/ts && pnpm install && pnpm run typecheck
	node sdk/ts/ui/test/expr.test.ts
	node sdk/ts/ui/test/texts.test.ts
	node sdk/ts/ui/test/speech.test.ts

# Regenerate the code-node protocol (spec 11.1); needs protoc,
# protoc-gen-go and protoc-gen-go-grpc on PATH.
proto:
	protoc --go_out=. --go_opt=module=organon --go-grpc_out=. --go-grpc_opt=module=organon api/proto/codenode/v1/codenode.proto

# Build the framework image (organon + organond); see docs/usage/10-docker.md.
IMAGE ?= organon
# The release (platform/buildinfo.Version) and source revision for `organon version`.
VERSION ?= $(shell sed -n 's/^const Version = "\(.*\)"/\1/p' platform/buildinfo/buildinfo.go)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null)

# docker builds $(IMAGE):latest and $(IMAGE):$(VERSION); push both to release.
docker:
	docker build --build-arg COMMIT=$(COMMIT) --build-arg VERSION=$(VERSION) -t $(IMAGE):latest -t $(IMAGE):$(VERSION) .
