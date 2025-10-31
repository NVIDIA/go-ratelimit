# Testing Guide

This document explains how to run tests for the go-ratelimit project.

## Container Runtime Support

The test tooling **automatically detects** and works with both:
- **Docker** (`docker` and `docker-compose`)
- **Podman** (`podman` and `podman-compose`)

To check which runtime is being used:
```bash
make check-runtime
```

## Quick Start

### Option 1: Run tests without Redis (Recommended for quick checks)

```bash
# Most tests will be skipped if Redis is not available
make test
```

or

```bash
go test -short ./...
```

### Option 2: Run tests with Redis using docker-compose (Recommended)

```bash
# Automatically starts Redis, runs tests, and cleans up
make test-redis
```

### Option 3: Manual Redis setup

```bash
# Start Redis container for development/testing
make redis-up

# Run tests
make test

# Stop Redis when done
make redis-down
```

## Detailed Testing Options

### 1. Using Compose (Simplest - Works with Docker or Podman)

The project includes a `docker-compose.test.yml` file for easy Redis setup.
The Makefile automatically uses `docker-compose` or `podman-compose` depending on what's available:

```bash
# Start Redis in the background (auto-detects docker-compose or podman-compose)
docker-compose -f docker-compose.test.yml up -d
# or with Podman:
podman-compose -f docker-compose.test.yml up -d

# Run tests
go test -v ./...

# Stop Redis
docker-compose -f docker-compose.test.yml down
# or with Podman:
podman-compose -f docker-compose.test.yml down
```

Or use the Makefile shortcuts (automatically detects runtime):
```bash
make redis-up      # Start Redis (uses docker-compose or podman-compose)
make test          # Run tests  
make redis-down    # Stop Redis
```

### 2. Using Standalone Container (Works with Docker or Podman)

```bash
# Start Redis with Docker
docker run --name go-ratelimit-redis -d -p 6379:6379 redis:7-alpine

# Or with Podman
podman run --name go-ratelimit-redis -d -p 6379:6379 redis:7-alpine

# Run tests
go test -v ./...

# Stop and remove Redis
docker stop go-ratelimit-redis && docker rm go-ratelimit-redis
# or with Podman:
podman stop go-ratelimit-redis && podman rm go-ratelimit-redis
```

Or use the Makefile (automatically detects docker or podman):
```bash
make test-with-redis
```

### 3. Using Local Redis Installation

If you have Redis installed locally:

```bash
# Start Redis (varies by OS)
# macOS: brew services start redis
# Linux: sudo systemctl start redis
# Or: redis-server

# Run tests
go test -v ./...
```

## Test Behavior Without Redis

When Redis is not available:
- Tests automatically **skip** instead of failing
- Tests complete in **~1 second** (instead of hanging)
- You'll see messages like: `"Redis not available, skipping test"`

This is useful for:
- Quick syntax/logic checks
- CI/CD environments without Redis
- Development without local Redis

## Running Specific Tests

```bash
# Run tests for a specific package
go test -v ./pkg/limiter/

# Run a specific test
go test -v -run TestRedisLimiter ./pkg/limiter/

# Run tests with race detection
go test -race ./...

# Run tests with coverage
make test-cover
```

## Continuous Integration

The test suite is designed to work in CI environments:

```yaml
# Example GitHub Actions workflow
- name: Start Redis
  run: docker run -d -p 6379:6379 redis:7-alpine

- name: Run tests
  run: go test -v ./...
```

## Using Podman Instead of Docker

If you're using Podman (common on RHEL, Fedora, and other Linux distributions), the Makefile automatically detects and uses it:

```bash
# Check what's being used
make check-runtime

# All commands work the same
make test-redis
make redis-up
make redis-down
```

**Note**: Podman runs rootless by default, which is more secure. Make sure `podman-compose` is installed:
```bash
# Install podman-compose
pip3 install podman-compose
# or on Fedora/RHEL:
sudo dnf install podman-compose
```

## Troubleshooting

### Tests hang indefinitely
**Solution**: All Redis tests now have 1-second timeouts and will skip if Redis is unavailable. If you still experience hangs, ensure you're using the latest version of the tests.

### Redis connection errors
**Solution**: Check that Redis is running on `localhost:6379`:
```bash
# Test Redis connection
redis-cli ping
# Should return: PONG

# Or check with Docker
docker ps | grep redis
```

### Port 6379 already in use
**Solution**: Stop the existing Redis instance:
```bash
# With Docker
docker stop $(docker ps -q --filter "publish=6379")
# Or with Podman
podman stop $(podman ps -q --filter "publish=6379")
# Or using Makefile
make redis-down
```

### Podman permission issues
**Solution**: Podman runs rootless by default. If you encounter permission issues:
```bash
# Check podman is working
podman run --rm redis:7-alpine redis-cli --version

# If using SELinux, you may need to add :Z to volume mounts
# This is handled automatically in docker-compose.test.yml
```

## Test Configuration

Tests use Redis database 15 by default to avoid conflicts with development data:

```go
rdb := redis.NewClient(&redis.Options{
    Addr: "localhost:6379",
    DB:   15,  // Test database
})
```

All tests include automatic cleanup with `defer rdb.FlushDB(ctx)`.

