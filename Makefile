BINARY := observer
PKG    := github.com/marmutapp/superbased-observer
CMD    := ./cmd/observer

GO         ?= go
GOFLAGS    ?=
BUILD_DIR  := bin
COVER_OUT  := coverage.txt

WEB_DIR        := web
WEB_DIST       := $(WEB_DIR)/dist
WEB_EMBED_DIST := internal/intelligence/dashboard/webapp/dist

.PHONY: all build test test-race lint fmt vet tidy clean run cover \
        web-install web-dev web-build web-clean

all: fmt vet lint test build

build: build-observer build-antigravity-bridge

build-observer:
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY) $(CMD)

# Cross-compile the Antigravity Windows-side gRPC bridge. Used by
# observer-on-WSL2 to reach Antigravity's local language_server,
# which binds to Windows-side 127.0.0.1 and isn't reachable from
# inside a WSL distro under default networking. The bridge is a
# tiny Go binary (~8 MB) that runs Windows-side under powershell.exe,
# does process discovery + the gRPC call, and returns Markdown via
# stdout. Skipped silently on non-Linux build hosts where it'd
# never be invoked.
build-antigravity-bridge:
	@mkdir -p $(BUILD_DIR)
	GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -o $(BUILD_DIR)/antigravity-bridge.exe ./cmd/antigravity-bridge

run: build
	$(BUILD_DIR)/$(BINARY)

test:
	$(GO) test $(GOFLAGS) ./...

test-race:
	$(GO) test $(GOFLAGS) -race ./...

cover:
	$(GO) test $(GOFLAGS) -race -coverprofile=$(COVER_OUT) -covermode=atomic ./...
	$(GO) tool cover -func=$(COVER_OUT) | tail -1

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed — see https://golangci-lint.run/"; exit 0; }
	golangci-lint run ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BUILD_DIR) $(COVER_OUT)

# ---------------------------------------------------------------
# Web (redesigned React/Vite dashboard, mounted at /v2/).
#
# `make build` stays pure-Go and does NOT require Node. The built
# artifacts at $(WEB_EMBED_DIST) are committed; regenerate them
# via `make web-build` whenever you touch web/ sources, before
# committing.
# ---------------------------------------------------------------
web-install:
	cd $(WEB_DIR) && npm ci

web-dev:
	cd $(WEB_DIR) && npm run dev

web-build:
	cd $(WEB_DIR) && npm ci --silent && npm run build
	@rm -rf $(WEB_EMBED_DIST)
	@mkdir -p $(WEB_EMBED_DIST)
	@cp -R $(WEB_DIST)/. $(WEB_EMBED_DIST)/
	@echo "web: rebuilt $(WEB_EMBED_DIST) from $(WEB_DIST)"

web-clean:
	rm -rf $(WEB_DIST) $(WEB_DIR)/node_modules
