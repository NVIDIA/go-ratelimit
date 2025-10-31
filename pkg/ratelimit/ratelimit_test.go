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
package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestDefaultOptions(t *testing.T) {
	// Use a nil Redis client for testing - in real code this would panic
	// but for the test we just want to check the default values
	mockRedis := &redis.Client{}
	options := DefaultOptions(mockRedis)

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

func TestMatcherFunctions(t *testing.T) {
	// Test that the matcher functions are properly exposed
	tests := []struct {
		name     string
		path     string
		matcher  func(string) bool
		expected bool
	}{
		{
			name:     "MatchPathExact - match",
			path:     "/api/users",
			matcher:  MatchPathExact("/api/users"),
			expected: true,
		},
		{
			name:     "MatchPathExact - no match",
			path:     "/api/products",
			matcher:  MatchPathExact("/api/users"),
			expected: false,
		},
		{
			name:     "MatchPathPrefix - match",
			path:     "/api/users/123",
			matcher:  MatchPathPrefix("/api/users"),
			expected: true,
		},
		{
			name:     "MatchPathPrefix - no match",
			path:     "/api/products",
			matcher:  MatchPathPrefix("/api/users"),
			expected: false,
		},
		{
			name:     "MatchPathRegex - match",
			path:     "/api/users/123",
			matcher:  MatchPathRegex("^/api/users/\\d+$"),
			expected: true,
		},
		{
			name:     "MatchPathRegex - no match",
			path:     "/api/users/abc",
			matcher:  MatchPathRegex("^/api/users/\\d+$"),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.matcher(tc.path)
			if result != tc.expected {
				t.Errorf("Expected %v, got %v", tc.expected, result)
			}
		})
	}

	// Test MatchAny
	anyMatcher := MatchAny(
		MatchPathExact("/api/users"),
		MatchPathExact("/api/products"),
	)
	if !anyMatcher("/api/users") {
		t.Error("MatchAny should match /api/users")
	}
	if !anyMatcher("/api/products") {
		t.Error("MatchAny should match /api/products")
	}
	if anyMatcher("/api/orders") {
		t.Error("MatchAny should not match /api/orders")
	}

	// Test MatchNone
	noneMatcher := MatchNone(
		MatchPathExact("/api/users"),
		MatchPathExact("/api/products"),
	)
	if noneMatcher("/api/users") {
		t.Error("MatchNone should not match /api/users")
	}
	if noneMatcher("/api/products") {
		t.Error("MatchNone should not match /api/products")
	}
	if !noneMatcher("/api/orders") {
		t.Error("MatchNone should match /api/orders")
	}
}

func TestKeyFunctions(t *testing.T) {
	// Test that the key functions are properly exposed
	req := httptest.NewRequest(http.MethodGet, "/api/users/123", nil)
	req.RemoteAddr = "192.0.2.1:1234"

	// Test KeyByIP
	ipResult := KeyByIP(req)
	expectedIP := "ip:192.0.2.1"
	if ipResult != expectedIP {
		t.Errorf("Expected '%s', got '%s'", expectedIP, ipResult)
	}

	// Test KeyByClientID
	clientID := "test-client"
	clientIDFunc := KeyByClientID(clientID)
	clientIDResult := clientIDFunc(req)
	expectedClientID := "client:test-client"
	if clientIDResult != expectedClientID {
		t.Errorf("Expected '%s', got '%s'", expectedClientID, clientIDResult)
	}

	// Test KeyByClientIDAndRoute
	clientRouteFunc := KeyByClientIDAndRoute(clientID)
	clientRouteResult := clientRouteFunc(req)
	expectedClientRoute := "client:test-client|route:/api/users"
	if clientRouteResult != expectedClientRoute {
		t.Errorf("Expected '%s', got '%s'", expectedClientRoute, clientRouteResult)
	}
}

func TestWithKeyFunc(t *testing.T) {
	customKeyFunc := func(r *http.Request) string {
		return "custom:" + r.URL.Path
	}

	wrappedFunc := WithKeyFunc(customKeyFunc)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	result := wrappedFunc(req)
	expected := "custom:/api/test"

	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

// Mock service for testing
type mockService struct {
	middlewareCalled bool
	selectiveCalled  bool
	matcher          func(string) bool
}

func (s *mockService) Middleware() func(http.Handler) http.Handler {
	s.middlewareCalled = true
	return func(next http.Handler) http.Handler {
		return next
	}
}

func (s *mockService) SelectiveMiddleware(matcher func(string) bool) func(http.Handler) http.Handler {
	s.selectiveCalled = true
	s.matcher = matcher
	return func(next http.Handler) http.Handler {
		return next
	}
}

// TestMockService tests the mock service implementation
func TestMockService(t *testing.T) {
	t.Run("Middleware", func(t *testing.T) {
		mock := &mockService{}

		// Call Middleware and verify it was called
		middleware := mock.Middleware()
		if !mock.middlewareCalled {
			t.Error("Expected middlewareCalled to be true")
		}

		// Verify the middleware passes through the handler
		handlerCalled := false
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerCalled = true
		})

		wrapped := middleware(handler)
		req := httptest.NewRequest("GET", "/test", nil)
		rr := httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)

		if !handlerCalled {
			t.Error("Expected handler to be called")
		}
	})

	t.Run("SelectiveMiddleware", func(t *testing.T) {
		mock := &mockService{}

		// Create a matcher function
		matcher := func(path string) bool {
			return path == "/api/test"
		}

		// Call SelectiveMiddleware
		middleware := mock.SelectiveMiddleware(matcher)
		if !mock.selectiveCalled {
			t.Error("Expected selectiveCalled to be true")
		}

		// Verify matcher was stored
		if mock.matcher == nil {
			t.Error("Expected matcher to be stored")
		}
		if !mock.matcher("/api/test") {
			t.Error("Expected matcher to match /api/test")
		}
		if mock.matcher("/other") {
			t.Error("Expected matcher not to match /other")
		}

		// Verify the middleware passes through the handler
		handlerCalled := false
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerCalled = true
		})

		wrapped := middleware(handler)
		req := httptest.NewRequest("GET", "/test", nil)
		rr := httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)

		if !handlerCalled {
			t.Error("Expected handler to be called")
		}
	})
}
