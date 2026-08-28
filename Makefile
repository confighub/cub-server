# cub-server — the ConfigHub server installer plugin for cub
#
# Common targets (run `make help` for the full list):
#   make build              host binary into bin/
#   make check              fmt-check + vet + test (the CI gate)
#   make plugin             install into cub from this checkout, via cub itself
#   make dist GOOS=.. GOARCH=..   cross-compile one release asset + checksum

BINARY   := cub-server
BIN_DIR  := bin
DIST_DIR := dist
GO       ?= go
CUB      ?= cub

# Version stamps main.version. Derived from git (leading "v" stripped so the
# release asset and `cub server version` read e.g. 0.1.5); override on the
# release build with `make dist VERSION=0.1.5`.
GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
VERSION ?= $(if $(GIT_VERSION),$(GIT_VERSION),dev)

# Dev build keeps symbols; release build strips them and trims paths (pure Go,
# so CGO is disabled — the binary is static and cross-compiles from one host).
LDFLAGS         := -X main.version=$(VERSION)
RELEASE_LDFLAGS := -s -w -X main.version=$(VERSION)

# Cross-compile target (defaults to the host); the CI release matrix sets these.
GOOS   ?= $(shell $(GO) env GOOS)
GOARCH ?= $(shell $(GO) env GOARCH)
ASSET  := cub-server-$(GOOS)-$(GOARCH)

# SBOM for the release. One platform-neutral file (the Go module graph is the
# same across the pure-Go cross-builds), named without os/arch tokens so
# `cub plugin install`'s asset matcher never picks it over a binary.
SBOM   := $(DIST_DIR)/cub-server.spdx.json

.PHONY: all build dist sbom vet fmt fmt-check test test-race tidy check run plugin plugin-uninstall clean help

all: build

build: ## Build the cub-server binary into bin/
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) ./cmd/server

dist: ## Cross-compile one release asset (set GOOS/GOARCH) + checksum into dist/
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(RELEASE_LDFLAGS)' -o $(DIST_DIR)/$(ASSET) ./cmd/server
	@cd $(DIST_DIR) && (sha256sum $(ASSET) 2>/dev/null || shasum -a 256 $(ASSET)) > $(ASSET).sha256
	@echo "built $(DIST_DIR)/$(ASSET) (version $(VERSION))"

sbom: ## Generate an SPDX SBOM from the release binary (needs syft on PATH)
	syft scan file:$(DIST_DIR)/$(ASSET) -o spdx-json=$(SBOM)
	@echo "wrote $(SBOM)"

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format the sources in place
	gofmt -w .

fmt-check: ## Fail if any source is not gofmt-clean
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

test: ## Run the tests
	$(GO) test ./...

test-race: ## Run the tests under the race detector
	$(GO) test -race ./...

tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

check: fmt-check vet test ## The CI gate: fmt-check + vet + test

run: ## Run cub-server directly (pass args via ARGS="...")
	$(GO) run ./cmd/server $(ARGS)

# Installs through cub's own local-path install rather than by dropping a binary
# into the plugin directory, so local development exercises the same code a
# release does — including the install hook that writes cub-plugin.yaml.
plugin: build ## Install this build into cub as the "server" plugin
	@if $(CUB) plugin list 2>/dev/null | grep -q '^server[[:space:]]'; then \
		$(CUB) plugin upgrade server; \
	else \
		$(CUB) plugin install ./$(BIN_DIR)/$(BINARY); \
	fi
	@echo "run 'cub server version'"

plugin-uninstall: ## Remove the plugin from cub
	$(CUB) plugin uninstall server

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) $(DIST_DIR)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
