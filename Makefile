.PHONY: help build build-tui build-agent-openai run test clean docker-build docker-push proto lint fmt tidy deps

# Variables
APP_NAME := kraclaw
TUI_NAME := kraclaw-tui
REGISTRY := ghcr.io/johanssonvincent
IMAGE := $(REGISTRY)/$(APP_NAME)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the server binary
	@echo "Building $(APP_NAME)..."
	@go build -ldflags="-w -s -X main.version=$(VERSION)" -o $(APP_NAME) ./cmd/kraclaw

build-tui: ## Build the TUI client binary
	@echo "Building $(TUI_NAME)..."
	@go build -ldflags="-w -s" -o $(TUI_NAME) ./cmd/kraclaw-tui

build-agent-openai: ## Build the OpenAI agent binary
	@echo "Building kraclaw-agent-openai..."
	@go build -ldflags="-w -s" -o kraclaw-agent-openai ./cmd/kraclaw-agent-openai

run: ## Run the server locally
	@echo "Running $(APP_NAME)..."
	@go run ./cmd/kraclaw

test: ## Run tests
	@echo "Running tests..."
	@go test -v -race ./...

test-short: ## Run unit tests only (skip integration)
	@echo "Running unit tests..."
	@go test -v -race -short ./...

test-integration: ## Run integration tests (requires Docker)
	@echo "Running integration tests..."
	@go test -v -race -run Integration ./...

clean: ## Clean build artifacts
	@echo "Cleaning..."
	@rm -f $(APP_NAME) $(TUI_NAME) kraclaw-agent-openai

docker-build: ## Build Docker image
	@echo "Building Docker image: $(IMAGE):$(VERSION)..."
	@docker build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-push: ## Push Docker image to registry
	@echo "Pushing Docker image: $(IMAGE):$(VERSION)..."
	@docker push $(IMAGE):$(VERSION)
	@docker push $(IMAGE):latest

proto: ## Generate protobuf Go code
	@echo "Generating protobuf code..."
	@buf generate

lint: ## Run golangci-lint
	@echo "Running linters..."
	@golangci-lint run

fmt: ## Format Go code
	@echo "Formatting code..."
	@go fmt ./...

tidy: ## Tidy Go modules
	@echo "Tidying Go modules..."
	@go mod tidy

deps: ## Download dependencies
	@echo "Downloading dependencies..."
	@go mod download
