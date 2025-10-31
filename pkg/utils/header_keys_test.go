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

func TestKeyByHeader(t *testing.T) {
	tests := []struct {
		name       string
		headerName string
		headers    map[string]string
		expected   string
	}{
		{
			name:       "Extract API key from header",
			headerName: "X-API-Key",
			headers:    map[string]string{"X-API-Key": "test-key-123"},
			expected:   "test-key-123",
		},
		{
			name:       "Missing header falls back to IP",
			headerName: "X-API-Key",
			headers:    map[string]string{},
			expected:   "header-x-api-key-fallback:192.0.2.1",
		},
		{
			name:       "Empty header falls back to IP",
			headerName: "X-API-Key",
			headers:    map[string]string{"X-API-Key": ""},
			expected:   "header-x-api-key-fallback:192.0.2.1",
		},
		{
			name:       "Case insensitive by default",
			headerName: "X-Tenant-ID",
			headers:    map[string]string{"X-Tenant-ID": "TENANT123"},
			expected:   "tenant123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "192.0.2.1:1234"

			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			keyFunc := KeyByHeader(tt.headerName)
			result := keyFunc(req)

			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestKeyByHeaderWithOptions(t *testing.T) {
	tests := []struct {
		name     string
		opts     HeaderKeyOptions
		headers  map[string]string
		expected string
	}{
		{
			name: "With prefix",
			opts: HeaderKeyOptions{
				HeaderName: "X-Tenant-ID",
				Prefix:     "tenant",
			},
			headers:  map[string]string{"X-Tenant-ID": "abc123"},
			expected: "tenant:abc123",
		},
		{
			name: "With default value",
			opts: HeaderKeyOptions{
				HeaderName:   "X-Tenant-ID",
				DefaultValue: "default-tenant",
			},
			headers:  map[string]string{},
			expected: "default-tenant",
		},
		{
			name: "Case sensitive",
			opts: HeaderKeyOptions{
				HeaderName:    "X-API-Key",
				CaseSensitive: true,
			},
			headers:  map[string]string{"X-API-Key": "AbC123"},
			expected: "AbC123",
		},
		{
			name: "Allow empty",
			opts: HeaderKeyOptions{
				HeaderName: "X-API-Key",
				AllowEmpty: true,
			},
			headers:  map[string]string{"X-API-Key": ""},
			expected: "",
		},
		{
			name: "With sanitizer",
			opts: HeaderKeyOptions{
				HeaderName: "X-API-Key",
				Sanitizer:  AlphanumericSanitizer,
			},
			headers:  map[string]string{"X-API-Key": "key-123-test!@#"},
			expected: "key123test",
		},
		{
			name: "With prefix and sanitizer",
			opts: HeaderKeyOptions{
				HeaderName: "X-Tenant-ID",
				Prefix:     "tenant",
				Sanitizer:  MaxLengthSanitizer(5),
			},
			headers:  map[string]string{"X-Tenant-ID": "verylongtenant"},
			expected: "tenant:veryl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "192.0.2.1:1234"

			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			keyFunc := KeyByHeaderWithOptions(tt.opts)
			result := keyFunc(req)

			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestKeyByMultipleHeaders(t *testing.T) {
	tests := []struct {
		name        string
		headers     map[string]string
		headerNames []string
		separator   string
		expected    string
	}{
		{
			name:        "Both headers present",
			headerNames: []string{"X-Tenant-ID", "X-API-Version"},
			headers:     map[string]string{"X-Tenant-ID": "tenant1", "X-API-Version": "v2"},
			separator:   ":",
			expected:    "tenant1:v2",
		},
		{
			name:        "One header missing",
			headerNames: []string{"X-Tenant-ID", "X-API-Version"},
			headers:     map[string]string{"X-Tenant-ID": "tenant1"},
			separator:   ":",
			expected:    "tenant1:none",
		},
		{
			name:        "All headers missing",
			headerNames: []string{"X-Tenant-ID", "X-API-Version"},
			headers:     map[string]string{},
			separator:   ":",
			expected:    "multi-header-fallback:192.0.2.1",
		},
		{
			name:        "Custom separator",
			headerNames: []string{"X-Tenant-ID", "X-Region"},
			headers:     map[string]string{"X-Tenant-ID": "t1", "X-Region": "us-west"},
			separator:   "-",
			expected:    "t1-us-west",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "192.0.2.1:1234"

			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			keyFunc := KeyByMultipleHeaders(tt.headerNames, tt.separator)
			result := keyFunc(req)

			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestKeyByHeaderWithFallback(t *testing.T) {
	tests := []struct {
		name        string
		headerNames []string
		headers     map[string]string
		expected    string
	}{
		{
			name:        "First header found",
			headerNames: []string{"X-API-Key", "Authorization"},
			headers:     map[string]string{"X-API-Key": "key123", "Authorization": "Bearer token"},
			expected:    "header:x-api-key:key123",
		},
		{
			name:        "First header missing, second found",
			headerNames: []string{"X-API-Key", "Authorization"},
			headers:     map[string]string{"Authorization": "Bearer token"},
			expected:    "header:authorization:Bearer token",
		},
		{
			name:        "All headers missing",
			headerNames: []string{"X-API-Key", "Authorization"},
			headers:     map[string]string{},
			expected:    "header-fallback:192.0.2.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "192.0.2.1:1234"

			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			keyFunc := KeyByHeaderWithFallback(tt.headerNames)
			result := keyFunc(req)

			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestSanitizers(t *testing.T) {
	t.Run("AlphanumericSanitizer", func(t *testing.T) {
		tests := []struct {
			input    string
			expected string
		}{
			{"abc123", "abc123"},
			{"abc-123-xyz", "abc123xyz"},
			{"ABC!@#123", "ABC123"},
			{"", ""},
		}

		for _, tt := range tests {
			result := AlphanumericSanitizer(tt.input)
			if result != tt.expected {
				t.Errorf("AlphanumericSanitizer(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		}
	})

	t.Run("UUIDSanitizer", func(t *testing.T) {
		tests := []struct {
			input    string
			expected string
		}{
			{"550e8400-e29b-41d4-a716-446655440000", "550e8400e29b41d4a716446655440000"},
			{"550E8400-E29B-41D4-A716-446655440000", "550e8400e29b41d4a716446655440000"},
			{"550e8400e29b41d4a716446655440000", "550e8400e29b41d4a716446655440000"},
			{"invalid-uuid", ""},
			{"", ""},
		}

		for _, tt := range tests {
			result := UUIDSanitizer(tt.input)
			if result != tt.expected {
				t.Errorf("UUIDSanitizer(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		}
	})

	t.Run("MaxLengthSanitizer", func(t *testing.T) {
		sanitizer := MaxLengthSanitizer(5)
		tests := []struct {
			input    string
			expected string
		}{
			{"short", "short"},
			{"verylongstring", "veryl"},
			{"12345", "12345"},
			{"", ""},
		}

		for _, tt := range tests {
			result := sanitizer(tt.input)
			if result != tt.expected {
				t.Errorf("MaxLengthSanitizer(5)(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		}
	})
}

func TestPanicCases(t *testing.T) {
	t.Run("Empty header name", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("Expected panic for empty header name")
			}
		}()
		KeyByHeaderWithOptions(HeaderKeyOptions{HeaderName: ""})
	})

	t.Run("Empty header names array", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("Expected panic for empty header names array")
			}
		}()
		KeyByMultipleHeaders([]string{}, ":")
	})

	t.Run("Empty fallback array", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("Expected panic for empty fallback array")
			}
		}()
		KeyByHeaderWithFallback([]string{})
	})
}
