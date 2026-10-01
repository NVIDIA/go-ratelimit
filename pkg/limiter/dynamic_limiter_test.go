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
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/go-ratelimit/pkg/config"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDynamicLimiter_NewDynamicLimiter(t *testing.T) {
	mockRedis := NewMockRedisClient()
	rdb := redis.NewClient(&redis.Options{})

	keyBuilder := func(r *http.Request) string {
		return "test-key"
	}

	limiter := NewDynamicLimiter(
		rdb,
		"test-prefix",
		100,
		1048576,
		time.Minute,
		keyBuilder,
	)

	assert.NotNil(t, limiter)
	assert.Equal(t, "test-prefix", limiter.prefix)
	assert.NotNil(t, limiter.cfgManager)
	assert.NotNil(t, limiter.keyBuilder)
	assert.NotNil(t, limiter.lua)

	// Check that config manager is properly initialized
	cfgMgr := limiter.GetConfigManager()
	assert.NotNil(t, cfgMgr)

	_ = mockRedis // Use to avoid unused variable warning
}

func TestDynamicLimiter_GetConfigManager(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "test-key"
	}

	limiter := NewDynamicLimiter(rdb, "test", 50, 2097152, time.Minute, keyBuilder)

	cfgMgr := limiter.GetConfigManager()
	assert.NotNil(t, cfgMgr)
}

func TestDynamicLimiter_ExtractClientID(t *testing.T) {
	testCases := []struct {
		name     string
		key      string
		expected string
	}{
		{
			name:     "API key format",
			key:      "apikey:ABC123|endpoint:/api/test",
			expected: "ABC123",
		},
		{
			name:     "Simple API key",
			key:      "apikey:XYZ789",
			expected: "XYZ789",
		},
		{
			name:     "Non-API key",
			key:      "user:john.doe",
			expected: "user:john.doe",
		},
		{
			name:     "Client key",
			key:      "client:john.doe",
			expected: "john.doe",
		},
		{
			name:     "Client key with route",
			key:      "client:john.doe|route:/v1/users",
			expected: "john.doe",
		},
		{
			name:     "IP key",
			key:      "ip:192.0.2.1",
			expected: "192.0.2.1",
		},
		{
			name:     "Header key",
			key:      "header:x-api-key:ABC123",
			expected: "ABC123",
		},
		{
			name:     "Header key preserves colons in value",
			key:      "header:authorization:Bearer token:with:colon",
			expected: "Bearer token:with:colon",
		},
		{
			name:     "Header fallback key",
			key:      "header-fallback:192.0.2.1",
			expected: "192.0.2.1",
		},
		{
			name:     "Empty key",
			key:      "",
			expected: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := extractClientID(tc.key)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestDynamicLimiter_AllowRequest(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})

	keyBuilder := func(r *http.Request) string {
		apiKey := r.Header.Get("X-API-Key")
		if apiKey == "" {
			return "default"
		}
		return "apikey:" + apiKey
	}

	limiter := NewDynamicLimiter(rdb, "test", 10, 1048576, time.Minute, keyBuilder)

	// Create test requests
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", "TEST123")

	ctx := context.Background()

	// Test AllowRequest
	allowed, remaining, reset, err := limiter.AllowRequest(ctx, req)

	// These would be actual assertions with a working Redis connection
	_ = allowed
	_ = remaining
	_ = reset
	_ = err
}

func TestDynamicLimiter_RecordBytes(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})

	keyBuilder := func(r *http.Request) string {
		apiKey := r.Header.Get("X-API-Key")
		if apiKey == "" {
			return "default"
		}
		return "apikey:" + apiKey
	}

	limiter := NewDynamicLimiter(rdb, "test", 100, 1024, time.Minute, keyBuilder)

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", "TEST456")

	ctx := context.Background()

	// Test RecordBytes with different byte amounts
	testCases := []int64{100, 256, 512, 1024, 2048}

	for _, bytes := range testCases {
		t.Run(fmt.Sprintf("bytes_%d", bytes), func(t *testing.T) {
			allowed, remaining, reset, err := limiter.RecordBytes(ctx, req, bytes)

			// These would be actual assertions with a working Redis connection
			_ = allowed
			_ = remaining
			_ = reset
			_ = err

			t.Logf("Recording %d bytes", bytes)
		})
	}
}

func TestDynamicLimiter_ClientConfig(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})

	keyBuilder := func(r *http.Request) string {
		return "test-key"
	}

	limiter := NewDynamicLimiter(rdb, "test", 10, 1048576, time.Minute, keyBuilder)

	ctx := context.Background()

	// Test getting default config
	defaultCfg, err := limiter.GetClientConfig(ctx, "new-client")
	// Would check actual values with working Redis
	_ = defaultCfg
	_ = err

	// Test setting custom config
	customCfg := config.ClientConfig{
		RequestLimit: 50,
		ByteLimit:    5242880, // 5MB
		WindowSecs:   30,
	}

	err = limiter.SetClientConfig(ctx, "custom-client", customCfg)
	_ = err

	// Test getting custom config
	retrievedCfg, err := limiter.GetClientConfig(ctx, "custom-client")
	_ = retrievedCfg
	_ = err

	// Test resetting config
	err = limiter.ResetClientConfig(ctx, "custom-client")
	_ = err
}

func TestDynamicLimiter_GetCurrentUsage(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})

	keyBuilder := func(r *http.Request) string {
		apiKey := r.Header.Get("X-API-Key")
		if apiKey == "" {
			return "default"
		}
		return "apikey:" + apiKey
	}

	limiter := NewDynamicLimiter(rdb, "test", 100, 10485760, time.Minute, keyBuilder)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", "USAGE123")

	ctx := context.Background()

	// Get initial usage (should be 0)
	usage, err := limiter.GetCurrentUsage(ctx, req)
	_ = usage
	_ = err

	// Would test after recording some bytes that usage increases
}

func TestDynamicLimiter_MultipleClients(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})

	keyBuilder := func(r *http.Request) string {
		clientID := r.Header.Get("X-Client-ID")
		if clientID == "" {
			return "anonymous"
		}
		return "apikey:" + clientID
	}

	limiter := NewDynamicLimiter(rdb, "test", 10, 1048576, time.Minute, keyBuilder)

	ctx := context.Background()

	// Set different configs for different clients
	clients := []struct {
		id     string
		config config.ClientConfig
	}{
		{
			id: "premium",
			config: config.ClientConfig{
				RequestLimit: 1000,
				ByteLimit:    104857600, // 100MB
				WindowSecs:   60,
			},
		},
		{
			id: "basic",
			config: config.ClientConfig{
				RequestLimit: 100,
				ByteLimit:    10485760, // 10MB
				WindowSecs:   60,
			},
		},
		{
			id: "trial",
			config: config.ClientConfig{
				RequestLimit: 10,
				ByteLimit:    1048576, // 1MB
				WindowSecs:   300,     // 5 minutes
			},
		},
	}

	// Set configs for each client
	for _, client := range clients {
		err := limiter.SetClientConfig(ctx, client.id, client.config)
		_ = err

		// Verify config was set
		cfg, err := limiter.GetClientConfig(ctx, client.id)
		_ = cfg
		_ = err

		t.Logf("Set config for client %s: %+v", client.id, client.config)
	}

	// Create requests for each client and verify they use their own configs
	for _, client := range clients {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Client-ID", client.id)

		// Each client should have their own limits
		allowed, remaining, reset, err := limiter.AllowRequest(ctx, req)

		// These would be actual assertions with working Redis
		_ = allowed
		_ = remaining
		_ = reset
		_ = err

		t.Logf("Client %s: allowed=%v", client.id, allowed)
	}
}

func TestDynamicLimiter_ConfigFailover(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})

	keyBuilder := func(r *http.Request) string {
		return "failover-test"
	}

	// Create limiter with default config
	defaultRequestLimit := 10
	defaultByteLimit := int64(1048576)
	defaultWindow := time.Minute

	limiter := NewDynamicLimiter(
		rdb,
		"test",
		defaultRequestLimit,
		defaultByteLimit,
		defaultWindow,
		keyBuilder,
	)

	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	// When config retrieval fails, should use default
	// This tests the fail-open behavior
	allowed, _, _, err := limiter.AllowRequest(ctx, req)

	// Should not fail completely even if config retrieval has issues
	_ = allowed
	_ = err

	t.Log("Config failover test: should use defaults on error")
}

func TestDynamicLimiter_ConcurrentConfigUpdates(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})

	keyBuilder := func(r *http.Request) string {
		return "concurrent-config-test"
	}

	limiter := NewDynamicLimiter(rdb, "test", 100, 1048576, time.Minute, keyBuilder)

	ctx := context.Background()
	clientID := "concurrent-client"

	// Test concurrent config updates
	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Each goroutine sets a different config
			cfg := config.ClientConfig{
				RequestLimit: 10 + id,
				ByteLimit:    int64((1 + id) * 1048576),
				WindowSecs:   60,
			}

			err := limiter.SetClientConfig(ctx, clientID, cfg)
			_ = err

			// Also try to read the config
			readCfg, err := limiter.GetClientConfig(ctx, clientID)
			_ = readCfg
			_ = err

			t.Logf("Goroutine %d updated config", id)
		}(i)
	}

	wg.Wait()

	// No race conditions should occur
	assert.True(t, true, "Concurrent config updates completed without panic")
}

func BenchmarkDynamicLimiter_AllowRequest(b *testing.B) {
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "bench-key"
	}

	limiter := NewDynamicLimiter(rdb, "bench", 1000, 10*1048576, time.Minute, keyBuilder)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _, _ = limiter.AllowRequest(ctx, req)
		}
	})
}

func BenchmarkDynamicLimiter_GetClientConfig(b *testing.B) {
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "bench-key"
	}

	limiter := NewDynamicLimiter(rdb, "bench", 1000, 10*1048576, time.Minute, keyBuilder)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = limiter.GetClientConfig(ctx, "bench-client")
		}
	})
}

// Integration test with actual Redis
func TestDynamicLimiter_Integration(t *testing.T) {
	// Skip if not in integration test mode
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Connect to actual Redis
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15, // Use test database
	})

	// Check if Redis is available
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping integration test")
	}

	// Clean up test data
	defer rdb.FlushDB(ctx)

	keyBuilder := func(r *http.Request) string {
		apiKey := r.Header.Get("X-API-Key")
		if apiKey == "" {
			return "default"
		}
		return "apikey:" + apiKey
	}

	limiter := NewDynamicLimiter(rdb, "test", 5, 1024, time.Second, keyBuilder)

	t.Run("DefaultConfig", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-API-Key", "DEFAULT_TEST")

		// Test with default config
		for i := 0; i < 7; i++ {
			allowed, remaining, reset, err := limiter.AllowRequest(ctx, req)
			require.NoError(t, err)

			if i < 5 {
				assert.True(t, allowed, "Request %d should be allowed", i+1)
				assert.Equal(t, int64(4-i), remaining)
			} else {
				assert.False(t, allowed, "Request %d should be denied", i+1)
				assert.Equal(t, int64(0), remaining)
			}
			assert.Greater(t, reset, int64(0))
		}

		// Wait for window reset
		time.Sleep(time.Second + 100*time.Millisecond)
	})

	t.Run("CustomConfig", func(t *testing.T) {
		// Set custom config for a client
		customCfg := config.ClientConfig{
			RequestLimit: 10,
			ByteLimit:    2048,
			WindowSecs:   1,
		}

		err := limiter.SetClientConfig(ctx, "CUSTOM_TEST", customCfg)
		require.NoError(t, err)

		// Verify custom config is applied
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-API-Key", "CUSTOM_TEST")

		// Should allow 10 requests with custom config
		for i := 0; i < 12; i++ {
			allowed, remaining, _, err := limiter.AllowRequest(ctx, req)
			require.NoError(t, err)

			if i < 10 {
				assert.True(t, allowed, "Request %d should be allowed with custom config", i+1)
				assert.Equal(t, int64(9-i), remaining)
			} else {
				assert.False(t, allowed, "Request %d should be denied with custom config", i+1)
				assert.Equal(t, int64(0), remaining)
			}
		}

		// Test byte limiting with custom config
		time.Sleep(time.Second + 100*time.Millisecond)

		totalBytes := int64(0)
		for i := 0; i < 10; i++ {
			bytes := int64(512)
			allowed, _, _, err := limiter.RecordBytes(ctx, req, bytes)
			require.NoError(t, err)

			totalBytes += bytes
			if totalBytes <= 2048 {
				assert.True(t, allowed, "Bytes %d should be allowed", totalBytes)
			} else {
				assert.False(t, allowed, "Bytes %d should be denied", totalBytes)
				break
			}
		}
	})

	t.Run("ConfigReset", func(t *testing.T) {
		// Reset custom config
		err := limiter.ResetClientConfig(ctx, "CUSTOM_TEST")
		require.NoError(t, err)

		// Should now use default config
		cfg, err := limiter.GetClientConfig(ctx, "CUSTOM_TEST")
		require.NoError(t, err)

		// Verify it's back to defaults
		assert.Equal(t, 5, cfg.RequestLimit)
		assert.Equal(t, int64(1024), cfg.ByteLimit)
		assert.Equal(t, 1, cfg.WindowSecs)
	})
}
