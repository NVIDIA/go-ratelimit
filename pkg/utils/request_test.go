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
package utils

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractClientID(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		value    string
		url      string
		remote   string
		expected string
	}{
		{
			name:     "client ID header takes precedence",
			header:   "X-Client-ID",
			value:    "header-client",
			url:      "/test?client_id=query-client",
			remote:   "192.0.2.1:1234",
			expected: "header-client",
		},
		{
			name:     "query parameter",
			url:      "/test?client_id=query-client",
			remote:   "192.0.2.1:1234",
			expected: "query-client",
		},
		{
			name:     "forwarded IP fallback",
			header:   "X-Forwarded-For",
			value:    "198.51.100.2, 192.0.2.1",
			url:      "/test",
			remote:   "192.0.2.1:1234",
			expected: "198.51.100.2",
		},
		{
			name:     "remote IP fallback",
			url:      "/test",
			remote:   "192.0.2.1:1234",
			expected: "192.0.2.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			req.RemoteAddr = tt.remote
			if tt.header != "" {
				req.Header.Set(tt.header, tt.value)
			}

			if got := ExtractClientID(req); got != tt.expected {
				t.Errorf("ExtractClientID() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestKeyByClientID(t *testing.T) {
	tests := []struct {
		name     string
		clientID string
		expected string
	}{
		{
			name:     "with client ID",
			clientID: "test-client-123",
			expected: "client:test-client-123",
		},
		{
			name:     "without client ID",
			clientID: "",
			expected: "ip:192.0.2.1", // IP from the test request
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = "192.0.2.1:1234" // Test IP

			result := KeyByClientID(req, tc.clientID)
			if result != tc.expected {
				t.Errorf("KeyByClientID() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestKeyByIP(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		xff      string
		expected string
	}{
		{
			name:     "direct connection",
			ip:       "192.0.2.1:1234",
			xff:      "",
			expected: "ip:192.0.2.1",
		},
		{
			name:     "with X-Forwarded-For",
			ip:       "10.0.0.1:1234",
			xff:      "203.0.113.1, 192.168.1.1",
			expected: "ip:203.0.113.1",
		},
		{
			name:     "invalid RemoteAddr format",
			ip:       "invalid-format",
			xff:      "",
			expected: "ip:invalid-format",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = tc.ip
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}

			result := KeyByIP(req)
			if result != tc.expected {
				t.Errorf("KeyByIP() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestKeyByClientIDAndRoute(t *testing.T) {
	tests := []struct {
		name     string
		clientID string
		path     string
		ip       string
		expected string
	}{
		{
			name:     "with client ID and path",
			clientID: "test-client-123",
			path:     "/v1/users",
			ip:       "192.0.2.1:1234",
			expected: "client:test-client-123|route:/v1/users",
		},
		{
			name:     "without client ID",
			clientID: "",
			path:     "/v1/products",
			ip:       "192.0.2.1:1234",
			expected: "ip:192.0.2.1|route:/v1/products",
		},
		{
			name:     "with nested path",
			clientID: "test-client-123",
			path:     "/v1/users/123/profile",
			ip:       "192.0.2.1:1234",
			expected: "client:test-client-123|route:/v1/users",
		},
		{
			name:     "with root path",
			clientID: "test-client-123",
			path:     "/",
			ip:       "192.0.2.1:1234",
			expected: "client:test-client-123|route:/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.RemoteAddr = tc.ip

			result := KeyByClientIDAndRoute(req, tc.clientID)
			if result != tc.expected {
				t.Errorf("KeyByClientIDAndRoute() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestRouteBucket(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "root path",
			path:     "/",
			expected: "/",
		},
		{
			name:     "single segment",
			path:     "/users",
			expected: "/users",
		},
		{
			name:     "two segments",
			path:     "/v1/users",
			expected: "/v1/users",
		},
		{
			name:     "multiple segments",
			path:     "/v1/users/123/profile",
			expected: "/v1/users",
		},
		{
			name:     "trailing slash",
			path:     "/v1/users/",
			expected: "/v1/users",
		},
		{
			name:     "empty path",
			path:     "",
			expected: "/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := RouteBucket(tc.path)
			if result != tc.expected {
				t.Errorf("RouteBucket() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		expected   string
	}{
		{
			name:       "standard format",
			remoteAddr: "192.0.2.1:1234",
			xff:        "",
			expected:   "192.0.2.1",
		},
		{
			name:       "with X-Forwarded-For",
			remoteAddr: "10.0.0.1:1234",
			xff:        "203.0.113.1, 192.168.1.1",
			expected:   "203.0.113.1",
		},
		{
			name:       "invalid format",
			remoteAddr: "invalid-format",
			xff:        "",
			expected:   "invalid-format",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}

			result := ClientIP(req)
			if result != tc.expected {
				t.Errorf("ClientIP() = %v, want %v", result, tc.expected)
			}
		})
	}
}
