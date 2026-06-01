BINARY_NAME   := tynet-unifi-clients
GO            := go
GOTEST        := go test
GOBUILD       := go build
GOFLAGS       := -trimpath
LDFLAGS       := -s -w

BUILD_DIR     := bin
OUTPUT_BINARY := $(BUILD_DIR)/$(BINARY_NAME)

COVERAGE_FILE := coverage.out
COVERAGE_HTML := coverage.html

# Convert hyphens to tildes so git-describe output (e.g. v0.1.0-3-gabc-dirty)
# is a valid Debian version. Fall back to 0.0.0~dev when no tag exists yet.
GIT_VERSION := $(shell git describe --tags --dirty 2>/dev/null | sed -e 's/^v//' -e 's/-/~/g')
VERSION     ?= $(if $(GIT_VERSION),$(GIT_VERSION),0.0.0~dev)

.PHONY: all
all: test build

.PHONY: build
build: ## Build the binary (production-ready, stripped)
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(OUTPUT_BINARY) .
	@echo "Binary created: $(OUTPUT_BINARY)"

.PHONY: build-debug
build-debug: ## Build with full symbols
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(OUTPUT_BINARY)-debug .
	@echo "Debug binary created: $(OUTPUT_BINARY)-debug"

.PHONY: build-linux
build-linux: ## Cross-compile for Pi (linux/arm64) into the repo root
	GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) .
	@echo "Linux arm64 binary created: $(BINARY_NAME)"

.PHONY: deb
deb: build-linux ## Build linux/arm64 .deb into dist/ (requires nfpm)
	@command -v nfpm >/dev/null || { echo "install nfpm: https://nfpm.goreleaser.com/install/"; exit 1; }
	mkdir -p dist
	# Minimal Debian changelog, gzipped to satisfy lintian's no-changelog
	# requirement for native packages. Version pinned to whatever nfpm
	# is about to package.
	{ \
	  echo "$(BINARY_NAME) ($(VERSION)) stable; urgency=low"; \
	  echo ""; \
	  echo "  * See https://github.com/tya/tynet-unifi-clients/releases/tag/v$(VERSION) for details."; \
	  echo ""; \
	  echo " -- Ty Alexander <ty.alexander@gmail.com>  $$(LC_ALL=C date -u '+%a, %d %b %Y %H:%M:%S +0000')"; \
	} | gzip -9 -n > dist/changelog.gz
	VERSION=$(VERSION) nfpm package -f packaging/nfpm.yaml -p deb -t dist/

.PHONY: test
test: ## Run all tests with race detector
	$(GOTEST) -race ./...

.PHONY: test-cover
test-cover: ## Run tests + generate coverage report
	$(GOTEST) -race -coverprofile=$(COVERAGE_FILE) ./...
	$(GO) tool cover -html=$(COVERAGE_FILE) -o $(COVERAGE_HTML)

.PHONY: fmt
fmt: ## Format all Go source files
	$(GO) fmt ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: vet fmt ## Run formatting + vet

.PHONY: tidy
tidy: ## Clean up go.mod / go.sum
	$(GO) mod tidy

.PHONY: clean
clean: ## Remove build artifacts and coverage files
	rm -rf $(BUILD_DIR) dist $(BINARY_NAME) $(COVERAGE_FILE) $(COVERAGE_HTML)

.PHONY: help
help: ## Show this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
