# Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


.PHONY: build test test-redis test-with-redis clean lint examples run-basic run-dynamic run-server run-resilient run-dynamic-resilient run-dynamic-only redis-up redis-down check-runtime

# Variables
GO=go
GOFLAGS=-trimpath
BINARY_NAME=ratelimit
BUILD_DIR=build
EXAMPLES_DIR=cmd/examples
SERVER_DIR=cmd/server
PKG_DIR=pkg
RESILIENT_DIR=examples/resilient_limiter
DYNAMIC_RESILIENT_DIR=examples/dynamic_resilient
DYNAMIC_ONLY_DIR=examples/dynamic_only

# Detect docker-compose or podman-compose
COMPOSE := $(shell command -v docker-compose 2> /dev/null)
ifndef COMPOSE
	COMPOSE := $(shell command -v podman-compose 2> /dev/null)
endif
ifndef COMPOSE
	COMPOSE = docker-compose
endif

# Detect docker or podman
DOCKER := $(shell command -v docker 2> /dev/null)
ifndef DOCKER
	DOCKER := $(shell command -v podman 2> /dev/null)
endif
ifndef DOCKER
	DOCKER = docker
endif

# Build the main binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) .

# Run tests
test:
	$(GO) test -v ./...

# Run tests with Redis using docker-compose or podman-compose (recommended)
test-redis:
	@echo "Starting Redis with $(COMPOSE)..."
	@$(COMPOSE) -f docker-compose.test.yml up -d
	@echo "Waiting for Redis to be ready..."
	@sleep 2
	@echo "Running tests..."
	@$(GO) test -v ./...
	@echo "Stopping Redis..."
	@$(COMPOSE) -f docker-compose.test.yml down

# Alternative: Run tests with standalone Redis container
test-with-redis:
	@echo "Starting Redis container with $(DOCKER)..."
	@$(DOCKER) run --name go-ratelimit-redis -d -p 6379:6379 redis:7-alpine 2>/dev/null || echo "Redis container already running"
	@sleep 1
	@echo "Running tests..."
	@$(GO) test -v ./...
	@echo "Stopping Redis container..."
	@$(DOCKER) stop go-ratelimit-redis 2>/dev/null || true
	@$(DOCKER) rm go-ratelimit-redis 2>/dev/null || true

# Start Redis for manual testing
redis-up:
	@echo "Starting Redis with $(COMPOSE)..."
	@$(COMPOSE) -f docker-compose.test.yml up -d
	@echo "Redis is running on localhost:6379"

# Stop Redis
redis-down:
	@echo "Stopping Redis with $(COMPOSE)..."
	@$(COMPOSE) -f docker-compose.test.yml down

# Check which container runtime is being used
check-runtime:
	@echo "Container runtime: $(DOCKER)"
	@echo "Compose tool: $(COMPOSE)"

# Run tests with coverage
test-cover:
	$(GO) test -v -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out

# Run linting
lint:
	@echo "Linting..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed. Installing..."; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin; \
		golangci-lint run ./...; \
	fi

# Format code
fmt:
	$(GO) fmt ./...

# Verify dependencies
verify:
	$(GO) mod verify

# Tidy dependencies
tidy:
	$(GO) mod tidy

# Build examples
examples: build-basic build-server build-resilient build-dynamic-resilient build-dynamic-only

# Build basic example
build-basic:
	@echo "Building basic example..."
	@mkdir -p $(BUILD_DIR)/examples
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/examples/basic ./$(EXAMPLES_DIR)/basic

# Build server
build-server:
	@echo "Building server..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/server ./$(SERVER_DIR)

# Build resilient example
build-resilient:
	@echo "Building resilient example..."
	@mkdir -p $(BUILD_DIR)/examples
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/examples/resilient ./$(RESILIENT_DIR)

# Build dynamic-resilient example
build-dynamic-resilient:
	@echo "Building dynamic-resilient example..."
	@mkdir -p $(BUILD_DIR)/examples
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/examples/dynamic-resilient ./$(DYNAMIC_RESILIENT_DIR)

# Build dynamic-only example
build-dynamic-only:
	@echo "Building dynamic-only example..."
	@mkdir -p $(BUILD_DIR)/examples
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/examples/dynamic-only ./$(DYNAMIC_ONLY_DIR)

# Run basic example
run-basic:
	@echo "Running basic example..."
	$(GO) run $(EXAMPLES_DIR)/basic/main.go

# Run dynamic example
run-dynamic:
	@echo "Running dynamic example..."
	$(GO) run $(EXAMPLES_DIR)/dynamic/main.go

# Run server
run-server:
	@echo "Running server..."
	$(GO) run $(SERVER_DIR)/main.go

# Run resilient example
run-resilient:
	@echo "Running resilient example..."
	$(GO) run $(RESILIENT_DIR)/main.go

# Run dynamic-resilient example
run-dynamic-resilient:
	@echo "Running dynamic-resilient example..."
	$(GO) run $(DYNAMIC_RESILIENT_DIR)/main.go

# Run dynamic-only example
run-dynamic-only:
	@echo "Running dynamic-only example..."
	$(GO) run $(DYNAMIC_ONLY_DIR)/main.go

# Default target
all: clean build test lint examples

# Help
help:
	@echo "Available targets:"
	@echo "  build                - Build the main binary"
	@echo "  test                 - Run tests (skips Redis tests if Redis unavailable)"
	@echo "  test-redis           - Run tests with Redis (compose)"
	@echo "  test-with-redis      - Run tests with Redis (standalone container)"
	@echo "  test-cover           - Run tests with coverage"
	@echo "  redis-up             - Start Redis container for manual testing"
	@echo "  redis-down           - Stop Redis container"
	@echo "  check-runtime        - Show which container runtime is being used"
	@echo "  clean                - Clean build artifacts"
	@echo "  lint                 - Run linter"
	@echo "  fmt                  - Format code"
	@echo "  verify               - Verify dependencies"
	@echo "  tidy                 - Tidy dependencies"
	@echo "  examples             - Build all examples"
	@echo "  build-basic          - Build basic example"
	@echo "  build-server         - Build server"
	@echo "  build-resilient      - Build resilient example"
	@echo "  build-dynamic-resilient - Build dynamic-resilient example"
	@echo "  build-dynamic-only   - Build dynamic-only example"
	@echo "  run-basic            - Run basic example"
	@echo "  run-dynamic          - Run dynamic example"
	@echo "  run-server           - Run server"
	@echo "  run-resilient        - Run resilient example"
	@echo "  run-dynamic-resilient - Run dynamic-resilient example"
	@echo "  run-dynamic-only     - Run dynamic-only example"
	@echo "  all                  - Clean, build, test, lint, and build examples"
	@echo "  help                 - Show this help message"
	@echo ""
	@echo "Container runtime support: Docker and Podman"
	@echo "Compose support: docker-compose and podman-compose"
