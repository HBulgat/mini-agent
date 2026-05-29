# mini-agent Makefile
#
# Conventions:
#   - All targets are .PHONY unless they produce a real file.
#   - Tools that aren't part of go.mod (sqlc, staticcheck) are invoked via
#     `go run` against pinned versions, so contributors don't need to
#     `go install` anything by hand. The first invocation downloads them
#     into the module cache.
#
# See docs/dev-process/01-development-plan.md §11 for the DoD checklist
# every target ultimately serves.

# ----- Variables -----------------------------------------------------------

MODULE       := github.com/HBulgat/mini-agent
BIN_DIR      := bin
BIN_NAME     := mini-agent
BIN          := $(BIN_DIR)/$(BIN_NAME)
ENTRY_PKG    := ./cmd/mini-agent

# Build metadata stamped into the binary via -ldflags.
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
COMMIT       ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_TIME   ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS      := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)"

# Pinned tool versions (no permanent go install needed). We force
# CGO_ENABLED=0 on `sqlc` so it picks the pure-Go wasm backend
# (wasilibs/go-pgquery) instead of the cgo C parser, which fails to
# build on macOS due to a `strchrnul` redeclaration in pg_query.
SQLC_VERSION         ?= v1.27.0
STATICCHECK_VERSION  ?= 2025.1
SQLC                 := CGO_ENABLED=0 go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
STATICCHECK          := go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

GO_PKGS              := ./...
WEB_DIR              := web

# Default target prints help so a bare `make` is informative, not destructive.
.DEFAULT_GOAL := help

# ----- Help ----------------------------------------------------------------

.PHONY: help
help:  ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n\nTargets:\n"} \
	      /^[a-zA-Z0-9_.-]+:.*##/ {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ----- Build / Run ---------------------------------------------------------

.PHONY: build
build:  ## Compile the CLI binary into ./bin/mini-agent
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN) $(ENTRY_PKG)
	@echo "Built $(BIN) ($(VERSION) @ $(COMMIT))"

.PHONY: run
run:  ## Run the CLI directly via `go run`
	go run $(LDFLAGS) $(ENTRY_PKG) $(ARGS)

.PHONY: serve
serve:  ## Start the Web UI backend on port 7777 (T5.4 onward)
	go run $(LDFLAGS) $(ENTRY_PKG) serve --port 7777 $(ARGS)

.PHONY: install
install:  ## go install the CLI into $$GOBIN
	go install $(LDFLAGS) $(ENTRY_PKG)

# ----- Quality gates -------------------------------------------------------

.PHONY: test
test:  ## Run all unit tests
	go test $(GO_PKGS)

.PHONY: test-race
test-race:  ## Run tests with the race detector (concurrency hot paths)
	go test -race $(GO_PKGS)

.PHONY: cover
cover:  ## Run tests with coverage profile -> coverage.txt
	go test -coverprofile=coverage.txt -covermode=atomic $(GO_PKGS)
	go tool cover -func=coverage.txt | tail -n 1

.PHONY: vet
vet:  ## go vet
	go vet $(GO_PKGS)

.PHONY: staticcheck
staticcheck:  ## staticcheck (downloads on first run)
	$(STATICCHECK) $(GO_PKGS)

.PHONY: fmt
fmt:  ## gofmt -s -w on Go sources
	gofmt -s -w $$(find . -name '*.go' -not -path './$(WEB_DIR)/*')

.PHONY: tidy
tidy:  ## go mod tidy
	go mod tidy

.PHONY: lint
lint: vet staticcheck  ## All static analysis (vet + staticcheck)

.PHONY: check
check: fmt vet test  ## fmt + vet + test (the basic local-CI loop)

# ----- Database ------------------------------------------------------------

.PHONY: migrate
migrate:  ## Run pending DB migrations against ~/.mini-agent/data.db
	go run $(LDFLAGS) $(ENTRY_PKG) migrate $(ARGS)

.PHONY: sqlc
sqlc:  ## Regenerate type-safe queries from internal/session/migrations + queries
	$(SQLC) generate

# ----- Tool schema golden files (R7-1' D83) -------------------------------

.PHONY: update-tool-goldens
update-tool-goldens:  ## Regenerate testdata/<tool>.schema.golden.json for every tool
	# testify/suite emits sub-tests as Test<Suite>/TestSchemaGolden, so
	# we match on the trailing TestSchemaGolden via -run with /-prefix.
	UPDATE_TOOL_GOLDENS=1 go test ./internal/tool/... -run /TestSchemaGolden -count=1

# ----- Frontend (web/) -----------------------------------------------------

.PHONY: web-dev
web-dev:  ## Start the Vite dev server in web/
	cd $(WEB_DIR) && pnpm dev

.PHONY: web-build
web-build:  ## Build the production Web UI bundle
	cd $(WEB_DIR) && pnpm build

.PHONY: web-install
web-install:  ## Install frontend deps (run after pulling package.json changes)
	cd $(WEB_DIR) && pnpm install

.PHONY: web-test
web-test:  ## Run the Vitest suite once
	cd $(WEB_DIR) && pnpm test

.PHONY: web-typecheck
web-typecheck:  ## Run tsc -b --noEmit on web/
	cd $(WEB_DIR) && pnpm lint:types

# ----- Cleanup -------------------------------------------------------------

.PHONY: clean
clean:  ## Remove build artifacts (does NOT touch ~/.mini-agent or the module cache)
	rm -rf $(BIN_DIR) coverage.txt
	@echo "Cleaned local build artifacts."
