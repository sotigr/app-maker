.PHONY: build test lint fuzz ui sdk depguard clean

GO ?= go

build:
	$(GO) build -o bin/fwd ./cmd/fwd
	$(GO) build -o bin/fw ./cmd/fw

test:
	$(GO) test -race ./...

lint:
	golangci-lint run ./...

# Regenerate the layer rules in .golangci.yml (spec 15.2, ADR 0004).
depguard:
	./scripts/gen-depguard.sh

fuzz:
	$(GO) test ./internal/schema/domain -run '^$$' -fuzz FuzzCompile -fuzztime 10m

# Build the admin console / flow editor; output is embedded into fwd.
ui:
	cd admin && pnpm install --frozen-lockfile && pnpm run build

clean:
	rm -rf bin

# Generate the tracker client and typecheck the SDK (spec 13).
sdk:
	$(GO) run ./cmd/fw codegen ts examples/tracker --out sdk/ts/gen
	$(GO) run ./cmd/fw codegen ts examples/tracker --out sdk/ts/smoke/gen --client-import ../../client/src/index.ts
	cd sdk/ts && pnpm install && pnpm run typecheck
