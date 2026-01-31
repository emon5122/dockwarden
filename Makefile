.PHONY: build test clean docker docker-push release help

# Variables
BINARY_NAME := dockwarden
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-w -s -X github.com/emon5122/dockwarden/internal/meta.Version=$(VERSION) -X github.com/emon5122/dockwarden/internal/meta.Commit=$(COMMIT) -X github.com/emon5122/dockwarden/internal/meta.BuildDate=$(BUILD_DATE)"

# Docker
DOCKER_REPO := emon5122/dockwarden
PLATFORMS := linux/amd64,linux/arm64,linux/arm/v7

## Build the binary
build:
	@echo "Building $(BINARY_NAME) $(VERSION)..."
	go build $(LDFLAGS) -o $(BINARY_NAME) ./cmd/dockwarden

## Build for all platforms
build-all:
	@echo "Building for all platforms..."
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-amd64 ./cmd/dockwarden
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-arm64 ./cmd/dockwarden
	GOOS=linux GOARCH=arm GOARM=7 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-armv7 ./cmd/dockwarden
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-darwin-amd64 ./cmd/dockwarden
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-darwin-arm64 ./cmd/dockwarden

## Run tests
test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...

## Run tests with coverage report
test-coverage: test
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## Run linter
lint:
	@echo "Running linter..."
	golangci-lint run ./...

## Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...

## Tidy dependencies
tidy:
	@echo "Tidying dependencies..."
	go mod tidy

## Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -f $(BINARY_NAME)
	rm -rf dist/
	rm -f coverage.out coverage.html

## Build Docker image
docker:
	@echo "Building Docker image..."
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(DOCKER_REPO):$(VERSION) \
		-t $(DOCKER_REPO):latest \
		-f build/Dockerfile .

## Build multi-arch Docker image
docker-multiarch:
	@echo "Building multi-arch Docker image..."
	docker buildx build \
		--platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(DOCKER_REPO):$(VERSION) \
		-t $(DOCKER_REPO):latest \
		-f build/Dockerfile \
		--push .

## Push Docker image
docker-push:
	@echo "Pushing Docker image..."
	docker push $(DOCKER_REPO):$(VERSION)
	docker push $(DOCKER_REPO):latest

## Run locally
run:
	@echo "Running $(BINARY_NAME)..."
	go run ./cmd/dockwarden --log-level debug --interval 30s

## Run with Docker Compose
compose-up:
	docker-compose -f deployments/docker-compose.yml up -d

## Stop Docker Compose
compose-down:
	docker-compose -f deployments/docker-compose.yml down

## Install development dependencies
dev-deps:
	@echo "Installing development dependencies..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install github.com/air-verse/air@latest

## Show version
version:
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Build Date: $(BUILD_DATE)"

## Show help
help:
	@echo "DockWarden Makefile"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' Makefile | sed 's/## /  /'
	@echo ""

# Default target
.DEFAULT_GOAL := help
