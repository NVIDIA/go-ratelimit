/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package limiter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NVIDIA/go-ratelimit/pkg/config"
	"github.com/NVIDIA/go-ratelimit/pkg/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

// testRedisClient wraps redis.Client to allow mocking errors
type testRedisClient struct {
	*redis.Client
	pingErr error
	runErr  error
}

// Override Ping to simulate Redis availability
func (c *testRedisClient) Ping(ctx context.Context) *redis.StatusCmd {
	if c.pingErr != nil {
		cmd := redis.NewStatusCmd(ctx)
		cmd.SetErr(c.pingErr)
		return cmd
	}
	return c.Client.Ping(ctx)
}

// Override Do method to simulate Redis errors during script execution
func (c *testRedisClient) Do(ctx context.Context, args ...interface{}) *redis.Cmd {
	if c.runErr != nil {
		cmd := redis.NewCmd(ctx)
		cmd.SetErr(c.runErr)
		return cmd
	}
	return c.Client.Do(ctx, args...)
}

func TestResilientLimiter_RedisAvailable(t *testing.T) {
	// Create a real Redis client for testing
	rdb := testutil.NewTestRedisClientDefault()

	// Wrap it in our test client
	testRdb := &testRedisClient{
		Client: rdb,
	}

	// Create a resilient limiter
	limiter := NewResilientLimiter(
		testRdb,
		"test",
		10,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test" },
	)

	// Create a test request
	req := httptest.NewRequest("GET", "/?client_id=test123", nil)
	ctx := context.Background()

	// Test AllowRequest
	allowed, remaining, reset, err := limiter.AllowRequest(ctx, req)
	assert.NoError(t, err)
	assert.True(t, allowed)
	// Don't assert exact remaining value as it depends on Redis state
	assert.GreaterOrEqual(t, remaining, int64(0))
	assert.Greater(t, reset, int64(0))

	// Test RecordBytes
	allowed, remaining, reset, err = limiter.RecordBytes(ctx, req, 1024)
	assert.NoError(t, err)
	assert.True(t, allowed)
	// Don't assert exact remaining value as it depends on Redis state
	assert.GreaterOrEqual(t, remaining, int64(0))
	assert.Greater(t, reset, int64(0))

	// Test GetClientConfig
	cfg, err := limiter.GetClientConfig(ctx, "test123")
	assert.NoError(t, err)
	assert.Equal(t, 10, cfg.RequestLimit)
	assert.Equal(t, int64(1048576), cfg.ByteLimit)
	assert.Equal(t, 60, cfg.WindowSecs)
}

func TestResilientLimiter_RedisUnavailable(t *testing.T) {
	// Create a real Redis client for testing
	rdb := testutil.NewTestRedisClientDefault()

	// Wrap it in our test client with errors
	testRdb := &testRedisClient{
		Client:  rdb,
		pingErr: errors.New("connection refused"),
		runErr:  errors.New("connection refused"),
	}

	// Create a resilient limiter
	limiter := NewResilientLimiter(
		testRdb,
		"test",
		10,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test" },
	)

	// Force Redis to be marked as unavailable
	limiter.mu.Lock()
	limiter.redisAvailable = false
	limiter.mu.Unlock()

	// Create a test request
	req := httptest.NewRequest("GET", "/?client_id=test123", nil)
	ctx := context.Background()

	// Test AllowRequest with Redis unavailable
	allowed, remaining, reset, err := limiter.AllowRequest(ctx, req)
	assert.NoError(t, err)
	assert.True(t, allowed)
	// Don't assert exact remaining value as it depends on memory state
	assert.GreaterOrEqual(t, remaining, int64(0))
	assert.Greater(t, reset, int64(0))

	// Test RecordBytes with Redis unavailable
	allowed, remaining, reset, err = limiter.RecordBytes(ctx, req, 1024)
	assert.NoError(t, err)
	assert.True(t, allowed)
	// Don't assert exact remaining value as it depends on memory state
	assert.GreaterOrEqual(t, remaining, int64(0))
	assert.Greater(t, reset, int64(0))

	// Test GetClientConfig with Redis unavailable
	cfg, err := limiter.GetClientConfig(ctx, "test123")
	assert.NoError(t, err)
	assert.Equal(t, 10, cfg.RequestLimit)
	assert.Equal(t, int64(1048576), cfg.ByteLimit)
	assert.Equal(t, 60, cfg.WindowSecs)
}

func TestResilientLimiter_RedisBecomesUnavailable(t *testing.T) {
	// Create a real Redis client for testing
	rdb := testutil.NewTestRedisClientDefault()

	// Wrap it in our test client that initially works
	testRdb := &testRedisClient{
		Client: rdb,
	}

	// Create a resilient limiter
	limiter := NewResilientLimiter(
		testRdb,
		"test",
		10,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test" },
	)

	// Create a test request
	req := httptest.NewRequest("GET", "/?client_id=test123", nil)
	ctx := context.Background()

	// Test with Redis available
	allowed, _, _, err := limiter.AllowRequest(ctx, req)
	assert.NoError(t, err)
	assert.True(t, allowed)

	// Make Redis unavailable
	testRdb.pingErr = errors.New("connection refused")
	testRdb.runErr = errors.New("connection refused")

	// Force Redis check
	limiter.mu.Lock()
	limiter.lastRedisCheck = time.Now().Add(-10 * time.Second)
	limiter.mu.Unlock()

	// Test with Redis unavailable - should fall back to memory
	allowed, _, _, err = limiter.AllowRequest(ctx, req)
	assert.NoError(t, err)
	assert.True(t, allowed)
}

func TestResilientLimiter_ConfigSync(t *testing.T) {
	// Create a real Redis client for testing
	rdb := testutil.NewTestRedisClientDefault()

	// Wrap it in our test client
	testRdb := &testRedisClient{
		Client: rdb,
	}

	// Create a resilient limiter
	limiter := NewResilientLimiter(
		testRdb,
		"test",
		10,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test" },
	)

	// Set a custom config
	customCfg := config.ClientConfig{
		RequestLimit: 20,
		ByteLimit:    2097152,
		WindowSecs:   120,
	}

	ctx := context.Background()
	err := limiter.SetClientConfig(ctx, "test123", customCfg)
	assert.NoError(t, err)

	// Verify the config is set in both Redis and memory
	cfg, err := limiter.GetClientConfig(ctx, "test123")
	assert.NoError(t, err)
	assert.Equal(t, 20, cfg.RequestLimit)
	assert.Equal(t, int64(2097152), cfg.ByteLimit)
	assert.Equal(t, 120, cfg.WindowSecs)

	// Make Redis unavailable
	testRdb.pingErr = errors.New("connection refused")
	limiter.mu.Lock()
	limiter.redisAvailable = false
	limiter.mu.Unlock()

	// Verify we can still get the config from memory
	cfg, err = limiter.GetClientConfig(ctx, "test123")
	assert.NoError(t, err)
	assert.Equal(t, 20, cfg.RequestLimit)
	assert.Equal(t, int64(2097152), cfg.ByteLimit)
	assert.Equal(t, 120, cfg.WindowSecs)
}

func TestResilientLimiter_ResetConfig(t *testing.T) {
	// Create a real Redis client for testing
	rdb := testutil.NewTestRedisClientDefault()

	// Wrap it in our test client
	testRdb := &testRedisClient{
		Client: rdb,
	}

	// Create a resilient limiter
	limiter := NewResilientLimiter(
		testRdb,
		"test",
		10,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test" },
	)

	// Set a custom config
	customCfg := config.ClientConfig{
		RequestLimit: 20,
		ByteLimit:    2097152,
		WindowSecs:   120,
	}

	ctx := context.Background()
	err := limiter.SetClientConfig(ctx, "test123", customCfg)
	assert.NoError(t, err)

	// Reset the config
	err = limiter.ResetClientConfig(ctx, "test123")
	assert.NoError(t, err)

	// Verify the config is reset to defaults
	cfg, err := limiter.GetClientConfig(ctx, "test123")
	assert.NoError(t, err)
	assert.Equal(t, 10, cfg.RequestLimit)
	assert.Equal(t, int64(1048576), cfg.ByteLimit)
	assert.Equal(t, 60, cfg.WindowSecs)
}
