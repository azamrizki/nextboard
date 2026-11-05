VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS := -s -w -X github.com/azamrizki/nextboard/internal/version.Version=$(VERSION)
DATA    ?= $(CURDIR)/.devdata
LISTEN  ?= 0.0.0.0:12312
WEB_PORT ?= 15173
RELOAD  ?= 1

.PHONY: all web build test test test-web-ssh dev dev-hub dev-web clean snapshot

all: build

web:
	cd web && npm ci && npm run build
	touch internal/webui/dist/.gitkeep

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/nextboard ./cmd/nextboard

test:
	go vet ./...
	go test -race ./...

# Needs web/node_modules: `make web` or `cd web && npm ci` first.
test-web:
	cd web && npm test

# Includes the agentless SSH test against a throwaway local sshd.
test-ssh:
	NEXTBOARD_SSHD_TEST=1 go test -race -run SSH -v ./internal/machine/

# Hub + Vite in one terminal; Ctrl-C stops both. Go changes restart the hub through air
# (.air.toml); RELOAD=0 runs it without air.
dev:
	@DATA="$(DATA)" LISTEN="$(LISTEN)" WEB_PORT="$(WEB_PORT)" RELOAD="$(RELOAD)" ./scripts/dev.sh

# Hub on http://localhost:12312 with a separate data dir; pair with `make dev-web`.
dev-hub:
	go run ./cmd/nextboard serve --dev --dev-web-port $(WEB_PORT) --data-dir $(DATA) --listen $(LISTEN)

# Vite on http://localhost:15173, proxying /api to the dev hub.
dev-web:
	cd web && NEXTBOARD_HUB=http://localhost:$(lastword $(subst :, ,$(LISTEN))) npm run dev -- --port $(WEB_PORT)

snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin dist .devdata
