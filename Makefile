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
	rm -rf $(BUILD_DIR) $(COVERAGE_FILE) $(COVERAGE_HTML)

.PHONY: help
help: ## Show this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
