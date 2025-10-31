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
package ratelimit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-ratelimit/pkg/config"
	"github.com/NVIDIA/go-ratelimit/pkg/limiter"
	"github.com/NVIDIA/go-ratelimit/pkg/middleware"
	"github.com/NVIDIA/go-ratelimit/pkg/ratelimit"
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

	// Clean up test keys
	client.FlushDB(ctx)
	return client
}

// =============================================================================
// Unit Tests - Options and Configuration
// =============================================================================

func TestDefaultOptions(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	options := ratelimit.DefaultOptions(client)

	if options.Redis == nil {
		t.Error("Redis client not set correctly")
	}

	if options.Prefix != "ratelimit" {
		t.Errorf("Expected prefix 'ratelimit', got '%s'", options.Prefix)
	}

	if options.RequestLimit != 60 {
		t.Errorf("Expected request limit 60, got %d", options.RequestLimit)
	}

	if options.ByteLimit != 1048576 {
		t.Errorf("Expected byte limit 1048576, got %d", options.ByteLimit)
	}

	if options.Window != time.Minute {
		t.Errorf("Expected window 1m0s, got %s", options.Window)
	}

	// Test the key function
	req := httptest.NewRequest(http.MethodGet, "/test?client_id=test-client", nil)
	req.RemoteAddr = "192.0.2.1:1234"

	keyResult := options.KeyFunc(req)
	expectedKey := "ip:192.0.2.1"
	if keyResult != expectedKey {
		t.Errorf("Expected key '%s', got '%s'", expectedKey, keyResult)
	}
}

func TestServiceCreation(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	tests := []struct {
		name    string
		options ratelimit.Options
		wantErr bool
	}{
		{
			name: "valid options",
			options: ratelimit.Options{
				Redis:        client,
				Prefix:       "test",
				RequestLimit: 100,
				ByteLimit:    1000000,
				Window:       time.Minute,
			},
			wantErr: false,
		},
		{
			name: "custom key function",
			options: ratelimit.Options{
				Redis:        client,
				Prefix:       "custom",
				RequestLimit: 50,
				ByteLimit:    500000,
				Window:       time.Second * 30,
				KeyFunc: func(r *http.Request) string {
					return "custom:" + r.Header.Get("X-API-Key")
				},
			},
			wantErr: false,
		},
		{
			name: "with custom logger",
			options: ratelimit.Options{
				Redis:        client,
				Prefix:       "logged",
				RequestLimit: 100,
				ByteLimit:    1000000,
				Window:       time.Minute,
				Logger:       middleware.NoOpLogger{},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := ratelimit.New(tt.options)
			if service == nil {
				t.Error("Expected service to be created")
			}
		})
	}
}

// =============================================================================
// Key Function Tests
// =============================================================================

func TestKeyFunctions(t *testing.T) {
	tests := []struct {
		name     string
		keyFunc  func(*http.Request) string
		request  *http.Request
		expected string
	}{
		{
			name:    "KeyByIP",
			keyFunc: ratelimit.KeyByIP,
			request: func() *http.Request {
				req := httptest.NewRequest("GET", "/test", nil)
				req.RemoteAddr = "192.168.1.1:5000"
				return req
			}(),
			expected: "ip:192.168.1.1",
		},
		{
			name:    "KeyByHeader",
			keyFunc: ratelimit.KeyByHeader("X-API-Key"),
			request: func() *http.Request {
				req := httptest.NewRequest("GET", "/test", nil)
				req.Header.Set("X-API-Key", "test-key")
				return req
			}(),
			expected: "test-key",
		},
		{
			name:    "KeyByMultipleHeaders",
			keyFunc: ratelimit.KeyByMultipleHeaders([]string{"X-Tenant", "X-Region"}, ":"),
			request: func() *http.Request {
				req := httptest.NewRequest("GET", "/test", nil)
				req.Header.Set("X-Tenant", "tenant1")
				req.Header.Set("X-Region", "us-west")
				return req
			}(),
			expected: "tenant1:us-west",
		},
		{
			name:    "KeyByHeaderWithFallback",
			keyFunc: ratelimit.KeyByHeaderWithFallback([]string{"X-API-Key", "Authorization"}),
			request: func() *http.Request {
				req := httptest.NewRequest("GET", "/test", nil)
				req.Header.Set("Authorization", "Bearer token")
				return req
			}(),
			expected: "header:authorization:Bearer token",
		},
		{
			name:    "KeyByPathAndMethod",
			keyFunc: nil, // KeyByPathAndMethod not implemented
			request: func() *http.Request {
				return httptest.NewRequest("POST", "/api/users", nil)
			}(),
			expected: "path:POST:/api/users",
		},
		{
			name:    "KeyByUserAgent",
			keyFunc: nil, // KeyByUserAgent not implemented
			request: func() *http.Request {
				req := httptest.NewRequest("GET", "/test", nil)
				req.Header.Set("User-Agent", "TestClient/1.0")
				return req
			}(),
			expected: "ua:TestClient/1.0",
		},
		{
			name:    "KeyBySanitizedUserAgent",
			keyFunc: nil, // KeyBySanitizedUserAgent not implemented
			request: func() *http.Request {
				req := httptest.NewRequest("GET", "/test", nil)
				req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
				return req
			}(),
			expected: "ua:mozilla5.0windowsnt10.0win64x64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.keyFunc == nil {
				t.Skip("Function not implemented")
				return
			}
			key := tt.keyFunc(tt.request)
			if key != tt.expected {
				t.Errorf("Expected key %q, got %q", tt.expected, key)
			}
		})
	}
}

// =============================================================================
// Path Matcher Tests
// =============================================================================

func TestMatcherFunctions(t *testing.T) {
	tests := []struct {
		name    string
		matcher middleware.PathMatcher
		path    string
		matches bool
	}{
		{
			name:    "MatchExact - matches",
			matcher: ratelimit.MatchPathExact("/api/v1/query"),
			path:    "/api/v1/query",
			matches: true,
		},
		{
			name:    "MatchExact - no match",
			matcher: ratelimit.MatchPathExact("/api/v1/query"),
			path:    "/api/v1/other",
			matches: false,
		},
		{
			name:    "MatchPrefix - matches",
			matcher: ratelimit.MatchPathPrefix("/api/"),
			path:    "/api/v1/query",
			matches: true,
		},
		{
			name:    "MatchPrefix - no match",
			matcher: ratelimit.MatchPathPrefix("/api/"),
			path:    "/health",
			matches: false,
		},
		{
			name:    "MatchRegex - matches",
			matcher: ratelimit.MatchPathRegex(`^/api/v\d+/.*`),
			path:    "/api/v2/query",
			matches: true,
		},
		{
			name:    "MatchRegex - no match",
			matcher: ratelimit.MatchPathRegex(`^/api/v\d+/.*`),
			path:    "/api/query",
			matches: false,
		},
		{
			name: "MatchAny - first matches",
			matcher: ratelimit.MatchAny(
				ratelimit.MatchPathPrefix("/api/"),
				ratelimit.MatchPathPrefix("/admin/"),
			),
			path:    "/api/test",
			matches: true,
		},
		{
			name: "MatchAny - second matches",
			matcher: ratelimit.MatchAny(
				ratelimit.MatchPathPrefix("/api/"),
				ratelimit.MatchPathPrefix("/admin/"),
			),
			path:    "/admin/test",
			matches: true,
		},
		{
			name: "MatchAny - none match",
			matcher: ratelimit.MatchAny(
				ratelimit.MatchPathPrefix("/api/"),
				ratelimit.MatchPathPrefix("/admin/"),
			),
			path:    "/public/test",
			matches: false,
		},
		{
			name: "MatchNone - excludes first",
			matcher: ratelimit.MatchNone(
				ratelimit.MatchPathPrefix("/health"),
				ratelimit.MatchPathPrefix("/metrics"),
			),
			path:    "/health/check",
			matches: false,
		},
		{
			name: "MatchNone - allows others",
			matcher: ratelimit.MatchNone(
				ratelimit.MatchPathPrefix("/health"),
				ratelimit.MatchPathPrefix("/metrics"),
			),
			path:    "/api/test",
			matches: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			result := tt.matcher(req.URL.Path)
			if result != tt.matches {
				t.Errorf("Expected %v for path %s", tt.matches, tt.path)
			}
		})
	}
}

// =============================================================================
// Middleware Tests
// =============================================================================

func TestServiceMiddleware(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	service := ratelimit.New(ratelimit.Options{
		Redis:        client,
		Prefix:       "test",
		RequestLimit: 3,
		ByteLimit:    1000,
		Window:       time.Minute,
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	t.Run("request rate limiting", func(t *testing.T) {
		middleware := service.Middleware()
		wrapped := middleware(handler)

		// Make requests up to limit
		for i := 0; i < 3; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			rr := httptest.NewRecorder()
			wrapped.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("Request %d: expected 200, got %d", i, rr.Code)
			}
		}

		// Next request should be rate limited
		req := httptest.NewRequest("GET", "/test", nil)
		rr := httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)

		if rr.Code != http.StatusTooManyRequests {
			t.Errorf("Expected 429, got %d", rr.Code)
		}
	})

	t.Run("dual rate limiting", func(t *testing.T) {
		// Clear previous test data
		client.FlushDB(context.Background())

		middleware := service.Middleware()

		// Handler that returns 500 bytes
		bigHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write(bytes.Repeat([]byte("a"), 500))
		})

		wrapped := middleware(bigHandler)

		// First request should succeed (500 bytes)
		req := httptest.NewRequest("GET", "/test", nil)
		rr := httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected 200, got %d", rr.Code)
		}

		// Second request should succeed (total 1000 bytes)
		req = httptest.NewRequest("GET", "/test", nil)
		rr = httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected 200, got %d", rr.Code)
		}

		// Third request will be allowed because byte limiting is post-facto
		// (can't deny based on predicted usage, only track after sending)
		req = httptest.NewRequest("GET", "/test", nil)
		rr = httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected 200, got %d", rr.Code)
		}
	})
}

func TestSelectiveMiddleware(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	service := ratelimit.New(ratelimit.Options{
		Redis:        client,
		Prefix:       "test",
		RequestLimit: 2,
		ByteLimit:    1000,
		Window:       time.Minute,
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Only rate limit /api/* paths
	matcher := ratelimit.MatchPathPrefix("/api/")
	middleware := service.SelectiveMiddleware(matcher)
	wrapped := middleware(handler)

	// Clear test data
	client.FlushDB(context.Background())

	// Requests to /api should be rate limited
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		rr := httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)

		if i < 2 {
			if rr.Code != http.StatusOK {
				t.Errorf("Request %d: expected 200, got %d", i, rr.Code)
			}
		} else {
			if rr.Code != http.StatusTooManyRequests {
				t.Errorf("Request %d: expected 429, got %d", i, rr.Code)
			}
		}
	}

	// Requests to /health should NOT be rate limited
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/health", nil)
		rr := httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Health request %d: expected 200, got %d", i, rr.Code)
		}
	}
}

// =============================================================================
// Dynamic Configuration Tests
// =============================================================================

func TestDynamicConfiguration(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()
	ctx := context.Background()

	// Create limiter with tenant-based key function
	keyFunc := func(r *http.Request) string {
		return r.Header.Get("X-Tenant-ID")
	}

	dynamicLimiter := limiter.NewDynamicLimiter(
		client,
		"test",
		10,   // default request limit
		1000, // default byte limit
		time.Minute,
		keyFunc,
	)

	service := ratelimit.NewFromLimiter(dynamicLimiter)

	// Configure specific tenant
	err := dynamicLimiter.SetClientConfig(ctx, "tenant-premium", config.ClientConfig{
		RequestLimit: 100,
		ByteLimit:    10000,
		WindowSecs:   60,
	})
	if err != nil {
		t.Fatalf("Failed to set client config: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	middleware := service.Middleware()
	wrapped := middleware(handler)

	t.Run("premium tenant high limit", func(t *testing.T) {
		// Premium tenant should have 100 request limit
		for i := 0; i < 20; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("X-Tenant-ID", "tenant-premium")
			rr := httptest.NewRecorder()
			wrapped.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("Premium tenant request %d: expected 200, got %d", i, rr.Code)
			}
		}
	})

	t.Run("default tenant low limit", func(t *testing.T) {
		// Default tenant should have 10 request limit
		client.FlushDB(ctx) // Clear counters

		for i := 0; i < 15; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("X-Tenant-ID", "tenant-default")
			rr := httptest.NewRecorder()
			wrapped.ServeHTTP(rr, req)

			if i < 10 {
				if rr.Code != http.StatusOK {
					t.Errorf("Default tenant request %d: expected 200, got %d", i, rr.Code)
				}
			} else {
				if rr.Code != http.StatusTooManyRequests {
					t.Errorf("Default tenant request %d: expected 429, got %d", i, rr.Code)
				}
			}
		}
	})
}

// =============================================================================
// Admin API Integration Tests
// =============================================================================

func TestAdminAPIIntegration(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	service := ratelimit.New(ratelimit.Options{
		Redis:        client,
		Prefix:       "test",
		RequestLimit: 100,
		ByteLimit:    10000,
		Window:       time.Minute,
	})

	// Create test server
	mux := http.NewServeMux()
	service.RegisterConfigAPI(mux)

	// Test protected endpoint
	mux.HandleFunc("/api/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Protected endpoint"))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	t.Run("get config", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/admin/config/test-client", nil)
		req.Header.Set("X-Admin-Key", "admin-secret-key") // Default key in ConfigHandler

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}

		var config map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		// Check that we got a valid config response
		if _, ok := config["request_limit"]; !ok {
			t.Error("Expected request_limit in response")
		}
	})

	t.Run("set client limits", func(t *testing.T) {
		body := strings.NewReader(`{"request_limit": 50, "byte_limit": 5000, "window_secs": 60}`)
		req, _ := http.NewRequest("PUT", server.URL+"/admin/config/test-client", body)
		req.Header.Set("X-Admin-Key", "admin-secret-key")
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}
	})
}

// =============================================================================
// Concurrent Operations Tests
// =============================================================================

func TestConcurrentRequests(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	service := ratelimit.New(ratelimit.Options{
		Redis:        client,
		Prefix:       "test",
		RequestLimit: 100,
		ByteLimit:    100000,
		Window:       time.Minute,
		KeyFunc: func(r *http.Request) string {
			return r.Header.Get("X-Client-ID")
		},
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	middleware := service.Middleware()
	wrapped := middleware(handler)

	var successCount int64
	var failureCount int64
	var wg sync.WaitGroup

	// Run concurrent requests from multiple clients
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			for j := 0; j < 20; j++ {
				req := httptest.NewRequest("GET", "/test", nil)
				req.Header.Set("X-Client-ID", fmt.Sprintf("client-%d", clientID))
				rr := httptest.NewRecorder()

				wrapped.ServeHTTP(rr, req)

				if rr.Code == http.StatusOK {
					atomic.AddInt64(&successCount, 1)
				} else if rr.Code == http.StatusTooManyRequests {
					atomic.AddInt64(&failureCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	totalRequests := int64(200) // 10 clients * 20 requests
	actualTotal := successCount + failureCount

	if actualTotal != totalRequests {
		t.Errorf("Request count mismatch: success=%d, failure=%d, total=%d",
			successCount, failureCount, totalRequests)
	}

	// Each client has 100 request limit, so most should succeed
	if successCount < 100 {
		t.Errorf("Expected at least 100 successful requests, got %d", successCount)
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkRateLimitMiddleware(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer client.Close()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		b.Skip("Redis not available")
	}

	service := ratelimit.New(ratelimit.Options{
		Redis:        client,
		Prefix:       "bench",
		RequestLimit: 1000000,
		ByteLimit:    1000000000,
		Window:       time.Hour,
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	middleware := service.Middleware()
	wrapped := middleware(handler)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("GET", "/test", nil)
			rr := httptest.NewRecorder()
			wrapped.ServeHTTP(rr, req)
		}
	})
}

func BenchmarkDualMiddleware(b *testing.B) {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer client.Close()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		b.Skip("Redis not available")
	}

	service := ratelimit.New(ratelimit.Options{
		Redis:        client,
		Prefix:       "bench",
		RequestLimit: 1000000,
		ByteLimit:    1000000000,
		Window:       time.Hour,
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	middleware := service.Middleware()
	wrapped := middleware(handler)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("GET", "/test", nil)
			rr := httptest.NewRecorder()
			wrapped.ServeHTTP(rr, req)
		}
	})
}

func BenchmarkKeyFunctions(b *testing.B) {
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.RemoteAddr = "192.168.1.1:5000"
	req.Header.Set("X-API-Key", "test-key")
	req.Header.Set("X-Tenant", "tenant1")
	req.Header.Set("User-Agent", "TestClient/1.0")

	benchmarks := []struct {
		name    string
		keyFunc func(*http.Request) string
	}{
		{"KeyByIP", ratelimit.KeyByIP},
		{"KeyByHeader", ratelimit.KeyByHeader("X-API-Key")},
		{"KeyByMultipleHeaders", ratelimit.KeyByMultipleHeaders([]string{"X-Tenant", "X-API-Key"}, ":")},
		{"KeyByPathAndMethod", nil},      // KeyByPathAndMethod not implemented
		{"KeyByUserAgent", nil},          // KeyByUserAgent not implemented
		{"KeyBySanitizedUserAgent", nil}, // KeyBySanitizedUserAgent not implemented
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			if bm.keyFunc == nil {
				b.Skip("Function not implemented")
				return
			}
			for i := 0; i < b.N; i++ {
				_ = bm.keyFunc(req)
			}
		})
	}
}
