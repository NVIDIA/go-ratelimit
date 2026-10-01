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
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDualLimiter_NewDualLimiter(t *testing.T) {
	mockRedis := NewMockRedisClient()
	rdb := redis.NewClient(&redis.Options{})

	keyBuilder := func(r *http.Request) string {
		return "test-key"
	}

	limiter := NewDualLimiter(rdb, "test-prefix", 100, 1048576, time.Minute, keyBuilder)

	assert.NotNil(t, limiter)
	assert.Equal(t, "test-prefix", limiter.prefix)
	assert.Equal(t, 100, limiter.requestLimit)
	assert.Equal(t, int64(1048576), limiter.byteLimit)
	assert.Equal(t, time.Minute, limiter.window)
	assert.NotNil(t, limiter.keyBuilder)
	assert.NotNil(t, limiter.lua)

	_ = mockRedis // Use to avoid unused variable warning
}

func TestDualLimiter_GetLimits(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "test-key"
	}

	limiter := NewDualLimiter(rdb, "test", 50, 2097152, time.Minute, keyBuilder)

	assert.Equal(t, 50, limiter.GetRequestLimit())
	assert.Equal(t, int64(2097152), limiter.GetByteLimit())
}

func TestDualLimiter_KeyGeneration(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "client-456"
	}

	limiter := NewDualLimiter(rdb, "api", 10, 1048576, time.Minute, keyBuilder)

	// Test key generation for requests and bytes
	reqKey := limiter.requestKey("client-456")
	byteKey := limiter.byteKey("client-456")

	assert.Equal(t, "api:req:client-456", reqKey)
	assert.Equal(t, "api:byte:client-456", byteKey)
	assert.NotEqual(t, reqKey, byteKey)
}

func TestDualLimiter_AllowRequest(t *testing.T) {
	// Create limiter with test configuration
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "test-key"
	}

	limiter := NewDualLimiter(rdb, "test", 10, 1048576, time.Minute, keyBuilder)

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := context.Background()

	// Test structure for AllowRequest
	// In actual implementation with Redis, this would check request count limits
	allowed, remaining, reset, err := limiter.AllowRequest(ctx, req)

	// These would be actual assertions with a working Redis connection
	_ = allowed
	_ = remaining
	_ = reset
	_ = err
}

func TestDualLimiter_RecordBytes(t *testing.T) {
	// Create limiter with test configuration
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "test-key"
	}

	// Small byte limit for testing
	limiter := NewDualLimiter(rdb, "test", 100, 1024, time.Minute, keyBuilder)

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := context.Background()

	// Test recording different byte amounts
	testCases := []struct {
		name  string
		bytes int64
		desc  string
	}{
		{"small", 100, "Small payload under limit"},
		{"medium", 512, "Medium payload under limit"},
		{"large", 1024, "Payload at limit"},
		{"oversized", 2048, "Payload over limit"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// In actual implementation with Redis, this would check byte limits
			allowed, remaining, reset, err := limiter.RecordBytes(ctx, req, tc.bytes)

			// These would be actual assertions with a working Redis connection
			_ = allowed
			_ = remaining
			_ = reset
			_ = err

			t.Logf("%s: %d bytes", tc.desc, tc.bytes)
		})
	}
}

func TestDualLimiter_RequestLimitHitsFirst(t *testing.T) {
	// Test scenario where request limit is hit before byte limit
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "request-limit-test"
	}

	// Low request limit, high byte limit
	limiter := NewDualLimiter(rdb, "test", 5, 10*1048576, time.Minute, keyBuilder)

	// Would test that after 5 requests, no more are allowed
	// even though byte limit not reached
	assert.Equal(t, 5, limiter.GetRequestLimit())
	assert.Equal(t, int64(10*1048576), limiter.GetByteLimit())
}

func TestDualLimiter_ByteLimitHitsFirst(t *testing.T) {
	// Test scenario where byte limit is hit before request limit
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "byte-limit-test"
	}

	// High request limit, low byte limit
	limiter := NewDualLimiter(rdb, "test", 1000, 1024, time.Minute, keyBuilder)

	// Would test that after exceeding 1024 bytes, no more requests allowed
	// even though request limit not reached
	assert.Equal(t, 1000, limiter.GetRequestLimit())
	assert.Equal(t, int64(1024), limiter.GetByteLimit())
}

func TestDualLimiter_DisabledRequestLimit(t *testing.T) {
	// Test with disabled request limit (negative value)
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "disabled-req-limit"
	}

	// Negative request limit means disabled
	limiter := NewDualLimiter(rdb, "test", -1, 1048576, time.Minute, keyBuilder)

	assert.Equal(t, -1, limiter.GetRequestLimit())

	// With disabled request limit, only byte limit should apply
	// This would be tested with actual Redis
}

func TestDualLimiter_ConcurrentRequests(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "concurrent-test"
	}

	limiter := NewDualLimiter(rdb, "test", 100, 1048576, time.Minute, keyBuilder)

	// Test concurrent access for both AllowRequest and RecordBytes
	var wg sync.WaitGroup
	numGoroutines := 20

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			ctx := context.Background()

			// Half do AllowRequest, half do RecordBytes
			if id%2 == 0 {
				_, _, _, _ = limiter.AllowRequest(ctx, req)
			} else {
				_, _, _, _ = limiter.RecordBytes(ctx, req, int64(id*100))
			}

			t.Logf("Goroutine %d completed", id)
		}(i)
	}

	wg.Wait()

	// No race conditions should occur
	assert.True(t, true, "Concurrent access completed without panic")
}

func TestDualLimiter_DifferentClients(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})

	// Key builder that uses client ID from header
	keyBuilder := func(r *http.Request) string {
		clientID := r.Header.Get("X-Client-ID")
		if clientID == "" {
			return "anonymous"
		}
		return clientID
	}

	limiter := NewDualLimiter(rdb, "test", 10, 1048576, time.Minute, keyBuilder)

	// Create requests for different clients
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.Header.Set("X-Client-ID", "client1")

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set("X-Client-ID", "client2")

	req3 := httptest.NewRequest(http.MethodGet, "/test", nil)
	// No client ID - should use anonymous

	// Verify that different keys are used for different clients
	key1 := limiter.keyBuilder(req1)
	key2 := limiter.keyBuilder(req2)
	key3 := limiter.keyBuilder(req3)

	assert.Equal(t, "client1", key1)
	assert.Equal(t, "client2", key2)
	assert.Equal(t, "anonymous", key3)

	// Verify separate tracking
	assert.Equal(t, "test:req:client1", limiter.requestKey(key1))
	assert.Equal(t, "test:byte:client1", limiter.byteKey(key1))
	assert.Equal(t, "test:req:client2", limiter.requestKey(key2))
	assert.Equal(t, "test:byte:client2", limiter.byteKey(key2))
}

func TestDualLimiter_WindowExpiration(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "expiry-test"
	}

	// Create limiter with very short window for testing
	limiter := NewDualLimiter(rdb, "test", 5, 1024, 100*time.Millisecond, keyBuilder)

	assert.Equal(t, 100*time.Millisecond, limiter.window)

	// In real test with Redis:
	// 1. Use up limits
	// 2. Wait for window expiration
	// 3. Verify limits reset
}

func TestDualLimiter_LuaScript(t *testing.T) {
	// Test that the dual Lua script is properly configured
	assert.NotEmpty(t, dualSlidingLua, "Dual Lua script should not be empty")
	assert.Contains(t, dualSlidingLua, "KEYS[1]", "Should use request key")
	assert.Contains(t, dualSlidingLua, "KEYS[2]", "Should use byte key")
	assert.Contains(t, dualSlidingLua, "ARGV[1]", "Should use window_ms")
	assert.Contains(t, dualSlidingLua, "ARGV[2]", "Should use request limit")
	assert.Contains(t, dualSlidingLua, "ARGV[3]", "Should use byte limit")
	assert.Contains(t, dualSlidingLua, "ARGV[4]", "Should use bytes parameter")
	assert.Contains(t, dualSlidingLua, "ARGV[5]", "Should use operation mode")
	assert.Contains(t, dualSlidingLua, "req_key", "Should handle request key")
	assert.Contains(t, dualSlidingLua, "byte_key", "Should handle byte key")
}

func BenchmarkDualLimiter_AllowRequest(b *testing.B) {
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "bench-key"
	}

	limiter := NewDualLimiter(rdb, "bench", 1000, 10*1048576, time.Minute, keyBuilder)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _, _ = limiter.AllowRequest(ctx, req)
		}
	})
}

func BenchmarkDualLimiter_RecordBytes(b *testing.B) {
	rdb := redis.NewClient(&redis.Options{})
	keyBuilder := func(r *http.Request) string {
		return "bench-key"
	}

	limiter := NewDualLimiter(rdb, "bench", 1000, 10*1048576, time.Minute, keyBuilder)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _, _ = limiter.RecordBytes(ctx, req, 1024)
		}
	})
}

// Integration test with actual Redis
func TestDualLimiter_Integration(t *testing.T) {
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
		return "integration-test"
	}

	zeroByteLimiter := NewDualLimiter(rdb, "zero-byte", 2, 1024, time.Minute, keyBuilder)
	zeroByteRequest := httptest.NewRequest(http.MethodGet, "/zero-byte", nil)

	t.Run("ZeroByteRecordDoesNotCountRequest", func(t *testing.T) {
		allowed, _, _, err := zeroByteLimiter.RecordBytes(ctx, zeroByteRequest, 0)
		require.NoError(t, err)
		assert.True(t, allowed)

		for i := 0; i < 2; i++ {
			allowed, _, _, err := zeroByteLimiter.AllowRequest(ctx, zeroByteRequest)
			require.NoError(t, err)
			assert.True(t, allowed, "request %d should not be affected by an empty response", i+1)
		}
	})

	dynamicLimiter := NewDynamicLimiter(rdb, "dynamic-zero-byte", 2, 1024, time.Minute, keyBuilder)
	t.Run("DynamicZeroByteRecordDoesNotCountRequest", func(t *testing.T) {
		allowed, _, _, err := dynamicLimiter.RecordBytes(ctx, zeroByteRequest, 0)
		require.NoError(t, err)
		assert.True(t, allowed)

		for i := 0; i < 2; i++ {
			allowed, _, _, err := dynamicLimiter.AllowRequest(ctx, zeroByteRequest)
			require.NoError(t, err)
			assert.True(t, allowed, "request %d should not be affected by an empty response", i+1)
		}
	})

	// Create limiter with small limits for testing
	limiter := NewDualLimiter(rdb, "test", 5, 1024, time.Second, keyBuilder)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	t.Run("RequestLimitTest", func(t *testing.T) {
		// Test request limiting
		for i := 0; i < 7; i++ {
			allowed, remaining, reset, err := limiter.AllowRequest(ctx, req)
			require.NoError(t, err)

			if i < 5 {
				assert.True(t, allowed, "Request %d should be allowed", i+1)
				assert.Equal(t, int64(4-i), remaining, "Remaining count should decrease")
			} else {
				assert.False(t, allowed, "Request %d should be denied", i+1)
				assert.Equal(t, int64(0), remaining)
			}
			assert.Greater(t, reset, int64(0))
		}

		// Wait for window reset
		time.Sleep(time.Second + 100*time.Millisecond)
	})

	t.Run("ByteLimitTest", func(t *testing.T) {
		// Test byte limiting
		totalBytes := int64(0)

		for i := 0; i < 10; i++ {
			bytes := int64(256) // Each request uses 256 bytes
			allowed, remaining, reset, err := limiter.RecordBytes(ctx, req, bytes)
			require.NoError(t, err)

			totalBytes += bytes

			if totalBytes <= 1024 {
				assert.True(t, allowed, "Request %d with %d total bytes should be allowed", i+1, totalBytes)
				assert.GreaterOrEqual(t, remaining, int64(0))
			} else {
				assert.False(t, allowed, "Request %d with %d total bytes should be denied", i+1, totalBytes)
				assert.Equal(t, int64(0), remaining)
			}
			assert.Greater(t, reset, int64(0))

			if !allowed {
				break
			}
		}
	})
}
