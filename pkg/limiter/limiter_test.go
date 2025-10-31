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
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-ratelimit/pkg/config"
	"github.com/NVIDIA/go-ratelimit/pkg/testutil"
	"github.com/stretchr/testify/assert"
)

// TestHelpers contains helper functions for testing limiters
type TestHelpers struct {
	t *testing.T
}

// NewTestHelpers creates a new test helper instance
func NewTestHelpers(t *testing.T) *TestHelpers {
	return &TestHelpers{t: t}
}

// CreateTestRequest creates a test HTTP request with optional headers
func (th *TestHelpers) CreateTestRequest(method, path string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req
}

// TestConcurrentRateLimiting tests concurrent access to rate limiters
func TestConcurrentRateLimiting(t *testing.T) {
	// Skip if not in integration test mode
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	// Connect to Redis for testing
	rdb := testutil.NewTestRedisClient("localhost:6379", 15)

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping concurrent test")
	}

	defer rdb.FlushDB(ctx)

	t.Run("RedisLimiterConcurrent", func(t *testing.T) {
		keyBuilder := func(r *http.Request) string {
			return "concurrent-test"
		}

		limiter := NewRedisLimiter(rdb, "test", 100, time.Second, keyBuilder)
		testConcurrentAccess(t, limiter, 100, 50)
	})

	t.Run("DualLimiterConcurrent", func(t *testing.T) {
		keyBuilder := func(r *http.Request) string {
			return "concurrent-dual-test"
		}

		limiter := NewDualLimiter(rdb, "test", 100, 10*1048576, time.Second, keyBuilder)
		testConcurrentDualAccess(t, limiter, 100, 50)
	})

	t.Run("DynamicLimiterConcurrent", func(t *testing.T) {
		keyBuilder := func(r *http.Request) string {
			return "concurrent-dynamic-test"
		}

		limiter := NewDynamicLimiter(rdb, "test", 100, 10*1048576, time.Second, keyBuilder)
		testConcurrentDynamicAccess(t, limiter, 100, 50)
	})
}

// testConcurrentAccess tests concurrent requests against RedisLimiter
func testConcurrentAccess(t *testing.T, limiter *RedisLimiter, limit int, numGoroutines int) {
	ctx := context.Background()
	var allowed int64
	var denied int64
	var errors int64
	var wg sync.WaitGroup

	// Create a channel to control request timing
	start := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Wait for signal to start
			<-start

			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			// Make multiple requests per goroutine
			for j := 0; j < 5; j++ {
				isAllowed, _, _, err := limiter.Allow(ctx, req)

				if err != nil {
					atomic.AddInt64(&errors, 1)
				} else if isAllowed {
					atomic.AddInt64(&allowed, 1)
				} else {
					atomic.AddInt64(&denied, 1)
				}

				// Small delay between requests
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Start all goroutines simultaneously
	close(start)

	// Wait for all goroutines to complete
	wg.Wait()

	// Verify results
	total := allowed + denied
	t.Logf("RedisLimiter Concurrent Test: Allowed=%d, Denied=%d, Errors=%d, Total=%d",
		allowed, denied, errors, total)

	assert.Equal(t, int64(0), errors, "Should have no errors")
	assert.LessOrEqual(t, allowed, int64(limit), "Should not exceed limit")
	assert.Equal(t, int64(numGoroutines*5), total, "Should process all requests")
}

// testConcurrentDualAccess tests concurrent requests against DualLimiter
func testConcurrentDualAccess(t *testing.T, limiter *DualLimiter, limit int, numGoroutines int) {
	ctx := context.Background()
	var allowedRequests int64
	var deniedRequests int64
	var allowedBytes int64
	var deniedBytes int64
	var errors int64
	var wg sync.WaitGroup

	// Create a channel to control request timing
	start := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Wait for signal to start
			<-start

			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			// Test both AllowRequest and RecordBytes
			for j := 0; j < 5; j++ {
				if j%2 == 0 {
					// Test AllowRequest
					isAllowed, _, _, err := limiter.AllowRequest(ctx, req)

					if err != nil {
						atomic.AddInt64(&errors, 1)
					} else if isAllowed {
						atomic.AddInt64(&allowedRequests, 1)
					} else {
						atomic.AddInt64(&deniedRequests, 1)
					}
				} else {
					// Test RecordBytes
					bytes := int64(1024 * (id%10 + 1))
					isAllowed, _, _, err := limiter.RecordBytes(ctx, req, bytes)

					if err != nil {
						atomic.AddInt64(&errors, 1)
					} else if isAllowed {
						atomic.AddInt64(&allowedBytes, 1)
					} else {
						atomic.AddInt64(&deniedBytes, 1)
					}
				}

				// Small delay between requests
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Start all goroutines simultaneously
	close(start)

	// Wait for all goroutines to complete
	wg.Wait()

	// Verify results
	totalRequests := allowedRequests + deniedRequests
	totalBytes := allowedBytes + deniedBytes

	t.Logf("DualLimiter Concurrent Test:")
	t.Logf("  Requests: Allowed=%d, Denied=%d, Total=%d",
		allowedRequests, deniedRequests, totalRequests)
	t.Logf("  Bytes: Allowed=%d, Denied=%d, Total=%d",
		allowedBytes, deniedBytes, totalBytes)
	t.Logf("  Errors=%d", errors)

	assert.Equal(t, int64(0), errors, "Should have no errors")
	assert.LessOrEqual(t, allowedRequests, int64(limit), "Should not exceed request limit")
}

// testConcurrentDynamicAccess tests concurrent requests against DynamicLimiter
func testConcurrentDynamicAccess(t *testing.T, limiter *DynamicLimiter, limit int, numGoroutines int) {
	ctx := context.Background()
	var allowed int64
	var denied int64
	var errors int64
	var configOps int64
	var wg sync.WaitGroup

	// Create a channel to control request timing
	start := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Wait for signal to start
			<-start

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			clientID := fmt.Sprintf("client-%d", id%5)
			req.Header.Set("X-Client-ID", clientID)

			// Make multiple requests per goroutine
			for j := 0; j < 5; j++ {
				// Every 3rd operation, update config
				if j%3 == 0 {
					cfg := config.ClientConfig{
						RequestLimit: 50 + id,
						ByteLimit:    int64((1 + id) * 1048576),
						WindowSecs:   1,
					}
					if err := limiter.SetClientConfig(ctx, clientID, cfg); err == nil {
						atomic.AddInt64(&configOps, 1)
					}
				}

				isAllowed, _, _, err := limiter.AllowRequest(ctx, req)

				if err != nil {
					atomic.AddInt64(&errors, 1)
				} else if isAllowed {
					atomic.AddInt64(&allowed, 1)
				} else {
					atomic.AddInt64(&denied, 1)
				}

				// Small delay between requests
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Start all goroutines simultaneously
	close(start)

	// Wait for all goroutines to complete
	wg.Wait()

	// Verify results
	total := allowed + denied
	t.Logf("DynamicLimiter Concurrent Test:")
	t.Logf("  Allowed=%d, Denied=%d, Errors=%d, Total=%d",
		allowed, denied, errors, total)
	t.Logf("  Config Operations=%d", configOps)

	assert.Equal(t, int64(0), errors, "Should have no errors")
	assert.Equal(t, int64(numGoroutines*5), total, "Should process all requests")
}

// TestRaceConditions tests for race conditions using Go's race detector
func TestRaceConditions(t *testing.T) {
	// This test is specifically for race detection
	// Run with: go test -race ./pkg/limiter

	mockRedis := NewMockRedisClient()
	rdb := testutil.NewTestRedisClientDefault()

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping race condition test")
	}
	defer rdb.Close()

	keyBuilder := func(r *http.Request) string {
		return "race-test"
	}

	t.Run("RedisLimiterRace", func(t *testing.T) {
		limiter := NewRedisLimiter(rdb, "test", 100, time.Minute, keyBuilder)
		testRaceCondition(t, func(ctx context.Context, req *http.Request) {
			_, _, _, err := limiter.Allow(ctx, req)
			assert.NoError(t, err)
		})
	})

	t.Run("DualLimiterRace", func(t *testing.T) {
		limiter := NewDualLimiter(rdb, "test", 100, 1048576, time.Minute, keyBuilder)
		testRaceCondition(t, func(ctx context.Context, req *http.Request) {
			_, _, _, err := limiter.AllowRequest(ctx, req)
			assert.NoError(t, err)
			_, _, _, err = limiter.RecordBytes(ctx, req, 1024)
			assert.NoError(t, err)
		})
	})

	t.Run("DynamicLimiterRace", func(t *testing.T) {
		limiter := NewDynamicLimiter(rdb, "test", 100, 1048576, time.Minute, keyBuilder)
		testRaceCondition(t, func(ctx context.Context, req *http.Request) {
			_, _, _, err := limiter.AllowRequest(ctx, req)
			assert.NoError(t, err)
			cfg := config.ClientConfig{RequestLimit: 50, ByteLimit: 524288, WindowSecs: 60}
			err = limiter.SetClientConfig(ctx, "test-client", cfg)
			assert.NoError(t, err)
			_, err = limiter.GetClientConfig(ctx, "test-client")
			assert.NoError(t, err)
		})
	})

	_ = mockRedis // Use to avoid unused variable warning
}

// testRaceCondition runs a function concurrently to detect race conditions
func testRaceCondition(t *testing.T, fn func(context.Context, *http.Request)) {
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				fn(ctx, req)
			}
		}()
	}
	wg.Wait()
}

// TestMemoryLeaks checks for potential memory leaks
func TestMemoryLeaks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory leak test in short mode")
	}

	rdb := testutil.NewTestRedisClientDefault()

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping memory leak test")
	}
	defer rdb.Close()

	keyBuilder := func(r *http.Request) string {
		return "memory-test"
	}

	// Create limiters
	redisLimiter := NewRedisLimiter(rdb, "test", 1000, time.Minute, keyBuilder)
	dualLimiter := NewDualLimiter(rdb, "test", 1000, 10*1048576, time.Minute, keyBuilder)
	dynamicLimiter := NewDynamicLimiter(rdb, "test", 1000, 10*1048576, time.Minute, keyBuilder)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	// Simulate heavy usage
	iterations := 10000

	t.Run("RedisLimiterMemory", func(t *testing.T) {
		for i := 0; i < iterations; i++ {
			_, _, _, err := redisLimiter.Allow(ctx, req)
			assert.NoError(t, err)
			if i%1000 == 0 {
				t.Logf("RedisLimiter iteration %d", i)
			}
		}
	})

	t.Run("DualLimiterMemory", func(t *testing.T) {
		for i := 0; i < iterations; i++ {
			_, _, _, err := dualLimiter.AllowRequest(ctx, req)
			assert.NoError(t, err)
			if i%2 == 0 {
				_, _, _, err = dualLimiter.RecordBytes(ctx, req, int64(i))
				assert.NoError(t, err)
			}
			if i%1000 == 0 {
				t.Logf("DualLimiter iteration %d", i)
			}
		}
	})

	t.Run("DynamicLimiterMemory", func(t *testing.T) {
		for i := 0; i < iterations; i++ {
			_, _, _, err := dynamicLimiter.AllowRequest(ctx, req)
			assert.NoError(t, err)
			if i%10 == 0 {
				cfg := config.ClientConfig{
					RequestLimit: 100 + i%50,
					ByteLimit:    int64((1 + i%10) * 1048576),
					WindowSecs:   60,
				}
				err = dynamicLimiter.SetClientConfig(ctx, fmt.Sprintf("client-%d", i%100), cfg)
				assert.NoError(t, err)
			}
			if i%1000 == 0 {
				t.Logf("DynamicLimiter iteration %d", i)
			}
		}
	})
}

// BenchmarkComparison compares performance of different limiters
func BenchmarkComparison(b *testing.B) {
	rdb := testutil.NewTestRedisClientDefault()

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		b.Skip("Redis not available, skipping benchmark")
	}
	defer rdb.Close()

	keyBuilder := func(r *http.Request) string {
		return "bench-compare"
	}

	redisLimiter := NewRedisLimiter(rdb, "bench", 10000, time.Minute, keyBuilder)
	dualLimiter := NewDualLimiter(rdb, "bench", 10000, 100*1048576, time.Minute, keyBuilder)
	dynamicLimiter := NewDynamicLimiter(rdb, "bench", 10000, 100*1048576, time.Minute, keyBuilder)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	b.Run("RedisLimiter", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _, _, err := redisLimiter.Allow(ctx, req)
			assert.NoError(b, err)
		}
	})

	b.Run("DualLimiter", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _, _, err := dualLimiter.AllowRequest(ctx, req)
			assert.NoError(b, err)
		}
	})

	b.Run("DynamicLimiter", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _, _, err := dynamicLimiter.AllowRequest(ctx, req)
			assert.NoError(b, err)
		}
	})
}

// TestEdgeCases tests edge cases and boundary conditions
func TestEdgeCases(t *testing.T) {
	rdb := testutil.NewTestRedisClientDefault()

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping edge case test")
	}
	defer rdb.Close()

	t.Run("ZeroLimit", func(t *testing.T) {
		keyBuilder := func(r *http.Request) string { return "zero-test" }
		limiter := NewRedisLimiter(rdb, "test", 0, time.Minute, keyBuilder)
		assert.Equal(t, 0, limiter.GetLimit())
	})

	t.Run("NegativeByteLimit", func(t *testing.T) {
		keyBuilder := func(r *http.Request) string { return "negative-test" }
		// Negative limits might be used to disable limiting
		limiter := NewDualLimiter(rdb, "test", -1, -1, time.Minute, keyBuilder)
		assert.Equal(t, -1, limiter.GetRequestLimit())
		assert.Equal(t, int64(-1), limiter.GetByteLimit())
	})

	t.Run("VeryShortWindow", func(t *testing.T) {
		keyBuilder := func(r *http.Request) string { return "short-window-test" }
		limiter := NewRedisLimiter(rdb, "test", 10, 1*time.Millisecond, keyBuilder)
		assert.Equal(t, 1*time.Millisecond, limiter.window)
	})

	t.Run("VeryLongWindow", func(t *testing.T) {
		keyBuilder := func(r *http.Request) string { return "long-window-test" }
		limiter := NewRedisLimiter(rdb, "test", 10, 24*time.Hour, keyBuilder)
		assert.Equal(t, 24*time.Hour, limiter.window)
	})

	t.Run("EmptyKey", func(t *testing.T) {
		keyBuilder := func(r *http.Request) string { return "" }
		limiter := NewRedisLimiter(rdb, "test", 10, time.Minute, keyBuilder)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		key := limiter.keyBuilder(req)
		assert.Equal(t, "", key)
		assert.Equal(t, "test:", limiter.key(key))
	})

	t.Run("VeryLongKey", func(t *testing.T) {
		longKey := ""
		for i := 0; i < 1024; i++ {
			longKey += "a"
		}
		keyBuilder := func(r *http.Request) string { return longKey }
		limiter := NewRedisLimiter(rdb, "test", 10, time.Minute, keyBuilder)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		key := limiter.keyBuilder(req)
		assert.Equal(t, longKey, key)
	})
}
