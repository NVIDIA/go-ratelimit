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
package middleware

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-ratelimit/pkg/config"
	"github.com/NVIDIA/go-ratelimit/pkg/limiter"
	"github.com/NVIDIA/go-ratelimit/pkg/testutil"
	"github.com/redis/go-redis/v9"
)

// =============================================================================
// Test Utilities
// =============================================================================

func setupTestRedis(t *testing.T) *redis.Client {
	client := testutil.NewTestRedisClientDefault()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping integration tests")
	}

	client.FlushDB(ctx)
	return client
}

// =============================================================================
// ByteCounter Tests
// =============================================================================

func TestByteCounterWriter(t *testing.T) {
	tests := []struct {
		name          string
		writeData     []byte
		expectedBytes int
	}{
		{
			name:          "empty write",
			writeData:     []byte{},
			expectedBytes: 0,
		},
		{
			name:          "small write",
			writeData:     []byte("hello"),
			expectedBytes: 5,
		},
		{
			name:          "large write",
			writeData:     bytes.Repeat([]byte("a"), 1024),
			expectedBytes: 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			counter := NewByteCounterWriter(recorder)

			n, err := counter.Write(tt.writeData)
			if err != nil {
				t.Fatalf("Write failed: %v", err)
			}

			if n != tt.expectedBytes {
				t.Errorf("Expected %d bytes written, got %d", tt.expectedBytes, n)
			}

			if counter.BytesWritten != int64(tt.expectedBytes) {
				t.Errorf("Expected BytesWritten to be %d, got %d",
					tt.expectedBytes, counter.BytesWritten)
			}
		})
	}
}

func TestByteCounterWriterWithHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	counter := NewByteCounterWriter(recorder)

	// Write header
	counter.Header().Set("Content-Type", "text/plain")
	counter.WriteHeader(http.StatusCreated)

	// Write data
	data := []byte("test content")
	n, err := counter.Write(data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if n != len(data) {
		t.Errorf("Expected %d bytes written, got %d", len(data), n)
	}

	if recorder.Code != http.StatusCreated {
		t.Errorf("Expected status %d, got %d", http.StatusCreated, recorder.Code)
	}
}

func TestByteCounterMultipleWrites(t *testing.T) {
	recorder := httptest.NewRecorder()
	counter := NewByteCounterWriter(recorder)

	writes := [][]byte{
		[]byte("first "),
		[]byte("second "),
		[]byte("third"),
	}

	var expectedTotal int64
	for _, data := range writes {
		n, err := counter.Write(data)
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		expectedTotal += int64(n)
	}

	if counter.BytesWritten != expectedTotal {
		t.Errorf("Expected total bytes %d, got %d", expectedTotal, counter.BytesWritten)
	}

	result := recorder.Body.String()
	expected := "first second third"
	if result != expected {
		t.Errorf("Expected body %q, got %q", expected, result)
	}
}

// =============================================================================
// Selective Rate Limiting Tests
// =============================================================================

func TestSelectiveRateLimitMiddleware(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	keyFunc := func(r *http.Request) string {
		return "test:selective"
	}

	dl := limiter.NewDynamicLimiter(client, "test", 3, 1000, time.Minute, keyFunc)

	tests := []struct {
		name        string
		matcher     PathMatcher
		path        string
		shouldLimit bool
	}{
		{
			name:        "exact match - should limit",
			matcher:     MatchPathExact("/api/v1/query"),
			path:        "/api/v1/query",
			shouldLimit: true,
		},
		{
			name:        "exact match - no limit",
			matcher:     MatchPathExact("/api/v1/query"),
			path:        "/api/v1/other",
			shouldLimit: false,
		},
		{
			name:        "prefix match - should limit",
			matcher:     MatchPathPrefix("/api/"),
			path:        "/api/v1/query",
			shouldLimit: true,
		},
		{
			name:        "prefix match - no limit",
			matcher:     MatchPathPrefix("/api/"),
			path:        "/health",
			shouldLimit: false,
		},
		{
			name:        "regex match - should limit",
			matcher:     MatchPathRegex(`^/api/v\d+/.*`),
			path:        "/api/v2/query",
			shouldLimit: true,
		},
		{
			name:        "regex match - no limit",
			matcher:     MatchPathRegex(`^/api/v\d+/.*`),
			path:        "/api/query",
			shouldLimit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset counter for clean test
			client.FlushDB(context.Background())

			middleware := SelectiveRateLimitMiddleware(dl, tt.matcher)

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("OK"))
			})

			wrapped := middleware(handler)

			// Make requests until we should be rate limited
			var gotLimited bool
			for i := 0; i < 5; i++ {
				req := httptest.NewRequest("GET", tt.path, nil)
				rr := httptest.NewRecorder()

				wrapped.ServeHTTP(rr, req)

				if rr.Code == http.StatusTooManyRequests {
					gotLimited = true
					break
				}
			}

			if gotLimited != tt.shouldLimit {
				t.Errorf("Expected limited=%v, got limited=%v", tt.shouldLimit, gotLimited)
			}
		})
	}
}

func TestMatcherCombinations(t *testing.T) {
	tests := []struct {
		name    string
		matcher PathMatcher
		path    string
		matches bool
	}{
		{
			name:    "MatchAny - first matches",
			matcher: MatchAny(MatchPathPrefix("/api/"), MatchPathPrefix("/admin/")),
			path:    "/api/test",
			matches: true,
		},
		{
			name:    "MatchAny - second matches",
			matcher: MatchAny(MatchPathPrefix("/api/"), MatchPathPrefix("/admin/")),
			path:    "/admin/test",
			matches: true,
		},
		{
			name:    "MatchAny - none match",
			matcher: MatchAny(MatchPathPrefix("/api/"), MatchPathPrefix("/admin/")),
			path:    "/public/test",
			matches: false,
		},
		{
			name:    "MatchNone - excludes first",
			matcher: MatchNone(MatchPathPrefix("/health"), MatchPathPrefix("/metrics")),
			path:    "/health/check",
			matches: false,
		},
		{
			name:    "MatchNone - allows others",
			matcher: MatchNone(MatchPathPrefix("/health"), MatchPathPrefix("/metrics")),
			path:    "/api/test",
			matches: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.matcher(tt.path)
			if result != tt.matches {
				t.Errorf("Expected %v, got %v for path %s", tt.matches, result, tt.path)
			}
		})
	}
}

// =============================================================================
// Rate Limiting Middleware Tests
// =============================================================================

func TestRateLimitMiddleware(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	keyFunc := func(r *http.Request) string {
		return "test:" + r.URL.Path
	}

	tests := []struct {
		name         string
		limit        int
		requests     int
		expectDenied int
	}{
		{
			name:         "all requests allowed",
			limit:        10,
			requests:     5,
			expectDenied: 0,
		},
		{
			name:         "some requests denied",
			limit:        3,
			requests:     5,
			expectDenied: 2,
		},
		{
			name:         "single request limit",
			limit:        1,
			requests:     3,
			expectDenied: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client.FlushDB(context.Background())

			rl := limiter.NewRedisLimiter(client, tt.name, tt.limit, time.Minute, keyFunc)
			middleware := RateLimitMiddleware(rl)

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("OK"))
			})

			wrapped := middleware(handler)

			deniedCount := 0
			for i := 0; i < tt.requests; i++ {
				req := httptest.NewRequest("GET", "/test", nil)
				rr := httptest.NewRecorder()

				wrapped.ServeHTTP(rr, req)

				if rr.Code == http.StatusTooManyRequests {
					deniedCount++
				}

				// Check headers are set
				if rr.Header().Get("RateLimit-Limit") == "" {
					t.Errorf("Request %d: Missing RateLimit-Limit header", i)
				}
			}

			if deniedCount != tt.expectDenied {
				t.Errorf("Expected %d denied requests, got %d", tt.expectDenied, deniedCount)
			}
		})
	}
}

// =============================================================================
// Dual Rate Limiting Middleware Tests
// =============================================================================

func TestDualRateLimitMiddleware(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	keyFunc := func(r *http.Request) string {
		return "test:dual"
	}

	tests := []struct {
		name         string
		requestLimit int
		byteLimit    int64
		responseSize int
		requests     int
		expectDenied int
	}{
		{
			name:         "hit request limit",
			requestLimit: 3,
			byteLimit:    10000,
			responseSize: 100,
			requests:     5,
			expectDenied: 2,
		},
		{
			name:         "hit byte limit",
			requestLimit: 10,
			byteLimit:    300,
			responseSize: 200,
			requests:     3,
			expectDenied: 0, // Byte limiting is post-facto, can't deny based on predicted usage
		},
		{
			name:         "both limits respected",
			requestLimit: 5,
			byteLimit:    1000,
			responseSize: 100,
			requests:     4,
			expectDenied: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client.FlushDB(context.Background())

			dl := limiter.NewDualLimiter(client, tt.name, tt.requestLimit, tt.byteLimit,
				time.Minute, keyFunc)
			middleware := DualRateLimitMiddleware(dl)

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write(bytes.Repeat([]byte("a"), tt.responseSize))
			})

			wrapped := middleware(handler)

			deniedCount := 0
			for i := 0; i < tt.requests; i++ {
				req := httptest.NewRequest("GET", "/test", nil)
				rr := httptest.NewRecorder()

				wrapped.ServeHTTP(rr, req)

				if rr.Code == http.StatusTooManyRequests {
					deniedCount++
				}
			}

			if deniedCount != tt.expectDenied {
				t.Errorf("Expected %d denied requests, got %d", tt.expectDenied, deniedCount)
			}
		})
	}
}

// =============================================================================
// Dynamic Rate Limiting Middleware Tests
// =============================================================================

func TestDynamicRateLimitMiddleware(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	keyFunc := func(r *http.Request) string {
		if clientID := r.Header.Get("X-Client-ID"); clientID != "" {
			return "client:" + clientID
		}
		return "default"
	}

	dl := limiter.NewDynamicLimiter(client, "test", 5, 1000, time.Minute, keyFunc)

	// Set custom limits for specific clients
	ctx := context.Background()
	dl.SetClientConfig(ctx, "client:premium", config.ClientConfig{
		RequestLimit: 20,
		ByteLimit:    10000,
		WindowSecs:   60,
	})
	dl.SetClientConfig(ctx, "client:basic", config.ClientConfig{
		RequestLimit: 3,
		ByteLimit:    500,
		WindowSecs:   60,
	})

	middleware := DynamicRateLimitMiddleware(dl)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Response"))
	})

	wrapped := middleware(handler)

	tests := []struct {
		name         string
		clientID     string
		requests     int
		expectDenied int
	}{
		{
			name:         "premium client high limit",
			clientID:     "premium",
			requests:     15,
			expectDenied: 0,
		},
		{
			name:         "basic client low limit",
			clientID:     "basic",
			requests:     5,
			expectDenied: 2,
		},
		{
			name:         "unknown client default limit",
			clientID:     "unknown",
			requests:     7,
			expectDenied: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear count for this specific client
			if tt.clientID != "" {
				client.Del(ctx, "test:client:"+tt.clientID+":requests")
			} else {
				client.Del(ctx, "test:default:requests")
			}

			deniedCount := 0
			for i := 0; i < tt.requests; i++ {
				req := httptest.NewRequest("GET", "/test", nil)
				if tt.clientID != "" {
					req.Header.Set("X-Client-ID", tt.clientID)
				}
				rr := httptest.NewRecorder()

				wrapped.ServeHTTP(rr, req)

				if rr.Code == http.StatusTooManyRequests {
					deniedCount++
				}
			}

			if deniedCount != tt.expectDenied {
				t.Errorf("Expected %d denied requests, got %d", tt.expectDenied, deniedCount)
			}
		})
	}
}

// =============================================================================
// Concurrent Operations Tests
// =============================================================================

func TestMiddlewareConcurrency(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	keyFunc := func(r *http.Request) string {
		return r.Header.Get("X-Client-ID")
	}

	dl := limiter.NewDynamicLimiter(client, "test", 100, 10000, time.Minute, keyFunc)
	middleware := DynamicRateLimitMiddleware(dl)

	var requestCount int64
	var deniedCount int64

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrapped := middleware(handler)

	var wg sync.WaitGroup
	clientCount := 10
	requestsPerClient := 20

	for i := 0; i < clientCount; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			for j := 0; j < requestsPerClient; j++ {
				req := httptest.NewRequest("GET", "/test", nil)
				req.Header.Set("X-Client-ID", fmt.Sprintf("client-%d", clientID))
				rr := httptest.NewRecorder()

				wrapped.ServeHTTP(rr, req)

				if rr.Code == http.StatusTooManyRequests {
					atomic.AddInt64(&deniedCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	totalRequests := int64(clientCount * requestsPerClient)
	successfulRequests := atomic.LoadInt64(&requestCount)
	deniedRequests := atomic.LoadInt64(&deniedCount)

	if successfulRequests+deniedRequests != totalRequests {
		t.Errorf("Request count mismatch: successful=%d, denied=%d, total=%d",
			successfulRequests, deniedRequests, totalRequests)
	}

	// With 100 request limit per client and 20 requests, none should be denied
	if deniedRequests > 0 {
		t.Errorf("Unexpected denied requests: %d", deniedRequests)
	}
}

// =============================================================================
// Error Handling Tests
// =============================================================================

func TestMiddlewareErrorScenarios(t *testing.T) {
	t.Run("redis connection error", func(t *testing.T) {
		// Create client with wrong address
		badClient := redis.NewClient(&redis.Options{
			Addr:         "localhost:9999",
			DialTimeout:  100 * time.Millisecond,
			ReadTimeout:  100 * time.Millisecond,
			WriteTimeout: 100 * time.Millisecond,
		})

		keyFunc := func(r *http.Request) string { return "test" }
		rl := limiter.NewRedisLimiter(badClient, "test", 10, time.Minute, keyFunc)
		middleware := RateLimitMiddleware(rl)

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		wrapped := middleware(handler)

		req := httptest.NewRequest("GET", "/test", nil)
		rr := httptest.NewRecorder()

		wrapped.ServeHTTP(rr, req)

		// For connection errors, middleware returns 500 Internal Server Error
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("Expected 500 for connection error, got %d", rr.Code)
		}
	})
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkByteCounterWriter(b *testing.B) {
	data := bytes.Repeat([]byte("a"), 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder := httptest.NewRecorder()
		counter := NewByteCounterWriter(recorder)
		counter.Write(data)
	}
}

func BenchmarkSelectiveRateLimitMiddleware(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer client.Close()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		b.Skip("Redis not available")
	}

	keyFunc := func(r *http.Request) string { return "benchmark" }
	dl := limiter.NewDynamicLimiter(client, "bench", 1000000, 10000000, time.Hour, keyFunc)

	matcher := MatchPathPrefix("/api/")
	middleware := SelectiveRateLimitMiddleware(dl, matcher)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrapped := middleware(handler)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("GET", "/api/test", nil)
			rr := httptest.NewRecorder()
			wrapped.ServeHTTP(rr, req)
		}
	})
}

func BenchmarkRateLimitMiddleware(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer client.Close()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		b.Skip("Redis not available")
	}

	keyFunc := func(r *http.Request) string { return "benchmark" }

	benchmarks := []struct {
		name       string
		middleware func(http.Handler) http.Handler
	}{
		{
			name: "RedisLimiter",
			middleware: RateLimitMiddleware(
				limiter.NewRedisLimiter(client, "bench", 1000000, time.Hour, keyFunc),
			),
		},
		{
			name: "DualLimiter",
			middleware: DualRateLimitMiddleware(
				limiter.NewDualLimiter(client, "bench", 1000000, 10000000, time.Hour, keyFunc),
			),
		},
		{
			name: "DynamicLimiter",
			middleware: DynamicRateLimitMiddleware(
				limiter.NewDynamicLimiter(client, "bench", 1000000, 10000000, time.Hour, keyFunc),
			),
		},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			wrapped := bm.middleware(handler)

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					req := httptest.NewRequest("GET", "/test", nil)
					rr := httptest.NewRecorder()
					wrapped.ServeHTTP(rr, req)
				}
			})
		})
	}
}

// =============================================================================
// Additional Helper Tests
// =============================================================================

func TestMatchPathSuffix(t *testing.T) {
	matcher := MatchPathSuffix(".json")

	tests := []struct {
		path    string
		matches bool
	}{
		{"/api/data.json", true},
		{"/api/data.xml", false},
		{"/data.json", true},
		{"/data", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if matcher(tt.path) != tt.matches {
				t.Errorf("Expected %v for path %s", tt.matches, tt.path)
			}
		})
	}
}

// Helper function for custom matchers
func MatchPathSuffix(suffix string) PathMatcher {
	return func(path string) bool {
		return strings.HasSuffix(path, suffix)
	}
}

// =============================================================================
// Additional Coverage Tests
// =============================================================================

// Test ByteCounterWriter Flush method
func TestByteCounterWriterFlush(t *testing.T) {
	// Create a mock flusher
	mockFlusher := &mockFlusher{
		ResponseWriter: httptest.NewRecorder(),
		flushed:        false,
	}

	bcw := NewByteCounterWriter(mockFlusher)

	// ByteCounterWriter always implements Flush method
	bcw.Flush()
	if !mockFlusher.flushed {
		t.Error("Expected Flush to be called on underlying writer when it implements Flusher")
	}

	// Test when underlying writer doesn't implement Flusher
	recorder := httptest.NewRecorder()
	bcw2 := NewByteCounterWriter(recorder)
	// This should not panic, just do nothing
	bcw2.Flush()
}

// Test ByteCounterWriter Hijack method
func TestByteCounterWriterHijack(t *testing.T) {
	// Create a mock hijacker
	mockHijacker := &mockHijacker{
		ResponseWriter: httptest.NewRecorder(),
	}

	bcw := NewByteCounterWriter(mockHijacker)

	// Test that Hijack works when underlying writer implements it
	conn, buf, err := bcw.Hijack()
	if err == nil || err.Error() != "mock hijack" {
		t.Errorf("Expected mock hijack error, got %v", err)
	}
	if conn != nil || buf != nil {
		t.Error("Expected nil connection and buffer from mock")
	}

	// Test when underlying writer doesn't implement Hijacker
	recorder := httptest.NewRecorder()
	bcw2 := NewByteCounterWriter(recorder)
	_, _, err = bcw2.Hijack()
	if err != http.ErrNotSupported {
		t.Errorf("Expected ErrNotSupported, got %v", err)
	}
}

// Mock types for testing
type mockFlusher struct {
	http.ResponseWriter
	flushed bool
}

func (m *mockFlusher) Flush() {
	m.flushed = true
}

type mockHijacker struct {
	http.ResponseWriter
}

func (m *mockHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, fmt.Errorf("mock hijack")
}

// Test NoOpLogger
func TestNoOpLogger(t *testing.T) {
	logger := NoOpLogger{}

	// These should not panic
	logger.Info("test info message")
	logger.Error("test error message")

	// Just verify they don't panic - NoOpLogger doesn't do anything
	t.Log("NoOpLogger methods executed without panic")
}

// Test extractClientID with various scenarios
func TestExtractClientID(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*http.Request)
		expected string
	}{
		{
			name: "from X-Client-ID header",
			setup: func(r *http.Request) {
				r.Header.Set("X-Client-ID", "client123")
			},
			expected: "client123",
		},
		{
			name: "from API key header",
			setup: func(r *http.Request) {
				r.Header.Set("X-API-Key", "key456")
			},
			expected: "key456",
		},
		{
			name: "from query parameter",
			setup: func(r *http.Request) {
				r.URL.RawQuery = "client_id=query789"
			},
			expected: "query789",
		},
		{
			name: "from remote address",
			setup: func(r *http.Request) {
				r.RemoteAddr = "192.168.1.1:8080"
			},
			expected: "192.168.1.1",
		},
		{
			name: "from X-Forwarded-For",
			setup: func(r *http.Request) {
				r.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
				r.RemoteAddr = "192.168.1.1:8080"
			},
			expected: "10.0.0.1",
		},
		{
			name: "priority: X-Client-ID over others",
			setup: func(r *http.Request) {
				r.Header.Set("X-Client-ID", "priority1")
				r.Header.Set("X-API-Key", "ignored")
				r.URL.RawQuery = "client_id=ignored"
			},
			expected: "priority1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			tt.setup(req)

			clientID := extractClientID(req)
			if clientID != tt.expected {
				t.Errorf("Expected client ID %q, got %q", tt.expected, clientID)
			}
		})
	}
}

// Test DynamicRateLimitMiddlewareWithLogger with custom logger
func TestDynamicRateLimitMiddlewareWithCustomLogger(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	// Create a mock logger to verify it's being used
	mockLogger := &mockLogger{
		infoMessages:  []string{},
		errorMessages: []string{},
	}

	keyFunc := func(r *http.Request) string {
		return "test:dynamic:" + extractClientID(r)
	}

	dl := limiter.NewDynamicLimiter(client, "test", 10, int64(time.Minute.Seconds()), 1024, keyFunc)

	// Set up custom config for a client
	ctx := context.Background()
	dl.SetClientConfig(ctx, "premium", config.ClientConfig{
		RequestLimit: 100,
		ByteLimit:    10240,
		WindowSecs:   60,
	})

	middleware := DynamicRateLimitMiddlewareWithLogger(dl, mockLogger)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	wrapped := middleware(handler)

	// Make a request
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Client-ID", "premium")
	rr := httptest.NewRecorder()

	wrapped.ServeHTTP(rr, req)

	// Check that logger was called
	if len(mockLogger.infoMessages) == 0 {
		t.Error("Expected logger.Info to be called")
	}

	// Check response
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}
}

// Mock logger for testing
type mockLogger struct {
	infoMessages  []string
	errorMessages []string
}

func (m *mockLogger) Info(msg string, keysAndValues ...interface{}) {
	m.infoMessages = append(m.infoMessages, msg)
}

func (m *mockLogger) Error(msg string, keysAndValues ...interface{}) {
	m.errorMessages = append(m.errorMessages, msg)
}
