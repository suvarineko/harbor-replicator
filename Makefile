.PHONY: build clean test lint fmt vet deps run docker-build docker-run help

# Build variables
BINARY_NAME=harbor-replicator
BUILD_DIR=bin
DOCKER_IMAGE=harbor-replicator:latest

# Go variables
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=gofmt
GOVET=$(GOCMD) vet

# Build flags
LDFLAGS=-ldflags "-w -s"
BUILD_FLAGS=-v $(LDFLAGS)

help: ## Display help message
	@echo "Available commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

deps: ## Download dependencies
	$(GOMOD) download
	$(GOMOD) tidy

build: deps ## Build the application
	mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/harbor-replicator

clean: ## Clean build artifacts
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)

test: ## Run tests
	$(GOTEST) -v ./...

test-coverage: ## Run tests with coverage
	$(GOTEST) -v -cover -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

lint: ## Run linter
	golangci-lint run

fmt: ## Format code
	$(GOFMT) -s -w .

vet: ## Run go vet
	$(GOVET) ./...

run: build ## Build and run the application
	./$(BUILD_DIR)/$(BINARY_NAME) -config configs/config.yaml

docker-build: ## Build Docker image
	docker build -t $(DOCKER_IMAGE) .

docker-run: docker-build ## Build and run Docker container
	docker run --rm -p 8080:8080 -v $(PWD)/configs:/app/configs $(DOCKER_IMAGE)

install-tools: ## Install development tools
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

all: clean deps fmt vet lint test build ## Run all checks and build