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
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	redisClient := testutil.NewTestRedisClientDefault()

	ctx := context.Background()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping integration tests")
	}

	return redisClient
}

func cleanupRedis(client *redis.Client) {
	ctx := context.Background()
	client.FlushDB(ctx)
}

// =============================================================================
// Unit Tests - Admin Handler Core Functionality
// =============================================================================

func TestAdminHandlerStatusEndpoint(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer cleanupRedis(redisClient)

	dynamicLimiter := limiter.NewDynamicLimiter(
		redisClient,
		"test",
		100,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test-client" },
	)

	adminConfig := AdminConfig{
		EnableAuth:          true,
		AdminKey:            "test-key",
		RedisAddress:        "localhost:6379",
		DefaultRequestLimit: 100,
		DefaultByteLimit:    1048576,
		DefaultWindow:       time.Minute,
		RateLimitByTenant:   true,
		FallbackToIP:        true,
	}

	handler := NewAdminHandler(dynamicLimiter, adminConfig)

	tests := []struct {
		name           string
		headers        map[string]string
		expectedStatus int
		checkResponse  func(t *testing.T, body map[string]interface{})
	}{
		{
			name: "status with valid auth",
			headers: map[string]string{
				"X-Admin-Key": "test-key",
			},
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, body map[string]interface{}) {
				if body["rate_limiting_enabled"] != true {
					t.Error("Expected rate_limiting_enabled to be true")
				}
				if body["redis_address"] != "localhost:6379" {
					t.Error("Expected correct Redis address")
				}
			},
		},
		{
			name:           "status without auth",
			headers:        map[string]string{},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "status with invalid auth",
			headers: map[string]string{
				"X-Admin-Key": "wrong-key",
			},
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/admin/ratelimit/status", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rr := httptest.NewRecorder()
			handler.handleStatus(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rr.Code)
			}

			if tt.checkResponse != nil && rr.Code == http.StatusOK {
				var body map[string]interface{}
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				tt.checkResponse(t, body)
			}
		})
	}
}

func TestAdminHandlerTenantConfiguration(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer cleanupRedis(redisClient)

	ctx := context.Background()
	dynamicLimiter := limiter.NewDynamicLimiter(
		redisClient,
		"test",
		100,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test-client" },
	)

	adminConfig := AdminConfig{
		EnableAuth:          true,
		AdminKey:            "test-key",
		DefaultRequestLimit: 100,
		DefaultByteLimit:    1048576,
		DefaultWindow:       time.Minute,
	}

	handler := NewAdminHandler(dynamicLimiter, adminConfig)

	// Test PUT - Set tenant limits
	t.Run("set tenant limits", func(t *testing.T) {
		reqBody := `{"request_limit": 50, "byte_limit": 524288, "window_secs": 60}`
		req := httptest.NewRequest("PUT", "/admin/ratelimit/tenant/test-tenant",
			strings.NewReader(reqBody))
		req.Header.Set("X-Admin-Key", "test-key")
		req.Header.Set("Content-Type", "application/json")

		rr := httptest.NewRecorder()
		handler.handleTenant(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", rr.Code)
		}

		// Verify the configuration was set (tenant ID is prefixed with "tenant-")
		cfg, err := dynamicLimiter.GetClientConfig(ctx, "tenant-test-tenant")
		if err != nil {
			t.Fatalf("Failed to get client config: %v", err)
		}
		if cfg.RequestLimit != 50 {
			t.Errorf("Expected request limit 50, got %d", cfg.RequestLimit)
		}
	})

	// Test GET - Get tenant configuration
	t.Run("get tenant configuration", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/admin/ratelimit/tenant/test-tenant", nil)
		req.Header.Set("X-Admin-Key", "test-key")

		rr := httptest.NewRecorder()
		handler.handleTenant(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", rr.Code)
		}

		var resp map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if resp["request_limit"].(float64) != 50 {
			t.Errorf("Expected request limit 50, got %v", resp["request_limit"])
		}
	})

	// Test DELETE - Reset tenant to defaults
	t.Run("reset tenant to defaults", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", "/admin/ratelimit/tenant/test-tenant", nil)
		req.Header.Set("X-Admin-Key", "test-key")

		rr := httptest.NewRecorder()
		handler.handleTenant(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", rr.Code)
		}

		// Verify configuration was reset
		cfg, err := dynamicLimiter.GetClientConfig(ctx, "test-tenant")
		if err != nil {
			t.Fatalf("Failed to get client config: %v", err)
		}
		if cfg.RequestLimit != adminConfig.DefaultRequestLimit {
			t.Errorf("Expected default request limit %d, got %d",
				adminConfig.DefaultRequestLimit, cfg.RequestLimit)
		}
	})
}

// =============================================================================
// Integration Tests - Batch Operations
// =============================================================================

func TestAdminHandlerBatchOperations(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer cleanupRedis(redisClient)

	ctx := context.Background()
	dynamicLimiter := limiter.NewDynamicLimiter(
		redisClient,
		"test-batch",
		100,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test-client" },
	)

	handler := NewAdminHandler(dynamicLimiter, AdminConfig{
		EnableAuth:          true,
		AdminKey:            "test-key",
		DefaultRequestLimit: 100,
		DefaultByteLimit:    1048576,
		DefaultWindow:       time.Minute,
	})

	t.Run("batch set operations", func(t *testing.T) {
		batchReq := BatchOperation{
			Operation: "set",
			Clients: []BatchClientConfig{
				{ClientID: "tenant1", RequestLimit: 75, ByteLimit: 750000, WindowSecs: 60},
				{ClientID: "tenant2", RequestLimit: 150, ByteLimit: 1500000, WindowSecs: 120},
				{ClientID: "tenant3", RequestLimit: 200, ByteLimit: 2000000, WindowSecs: 180},
			},
		}

		body, _ := json.Marshal(batchReq)
		req := httptest.NewRequest("POST", "/admin/ratelimit/batch", bytes.NewReader(body))
		req.Header.Set("X-Admin-Key", "test-key")
		req.Header.Set("Content-Type", "application/json")

		rr := httptest.NewRecorder()
		handler.handleBatch(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", rr.Code)
		}

		var resp map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if status, ok := resp["status"].(string); !ok || status != "completed" {
			t.Errorf("Expected status 'completed', got %v", resp["status"])
		}
		if results, ok := resp["results"].([]interface{}); !ok || len(results) != 3 {
			t.Errorf("Expected 3 results, got %v", len(results))
		}

		// Verify each tenant configuration
		for i, client := range batchReq.Clients {
			cfg, err := dynamicLimiter.GetClientConfig(ctx, client.ClientID)
			if err != nil {
				t.Errorf("Failed to get config for tenant %s: %v", client.ClientID, err)
				continue
			}
			if cfg.RequestLimit != client.RequestLimit {
				t.Errorf("Tenant %d: expected request limit %d, got %d",
					i, client.RequestLimit, cfg.RequestLimit)
			}
		}
	})

	t.Run("batch reset operations", func(t *testing.T) {
		batchReq := BatchOperation{
			Operation: "reset",
			ClientIDs: []string{"tenant1", "tenant2", "tenant3"},
		}

		body, _ := json.Marshal(batchReq)
		req := httptest.NewRequest("POST", "/admin/ratelimit/batch", bytes.NewReader(body))
		req.Header.Set("X-Admin-Key", "test-key")
		req.Header.Set("Content-Type", "application/json")

		rr := httptest.NewRecorder()
		handler.handleBatch(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", rr.Code)
		}

		// Verify all configs were deleted (should return defaults)
		for _, clientID := range batchReq.ClientIDs {
			cfg, err := dynamicLimiter.GetClientConfig(ctx, clientID)
			if err != nil {
				t.Errorf("Failed to get config for client %s: %v", clientID, err)
				continue
			}
			if cfg.RequestLimit != 100 { // Default value
				t.Errorf("Client %s: expected default request limit 100, got %d",
					clientID, cfg.RequestLimit)
			}
		}
	})
}

// =============================================================================
// Edge Case Tests
// =============================================================================

func TestAdminHandlerEdgeCases(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer cleanupRedis(redisClient)

	dynamicLimiter := limiter.NewDynamicLimiter(
		redisClient,
		"test",
		100,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test-client" },
	)

	handler := NewAdminHandler(dynamicLimiter, AdminConfig{
		EnableAuth:          true,
		AdminKey:            "test-key",
		DefaultRequestLimit: 100,
		DefaultByteLimit:    1048576,
		DefaultWindow:       time.Minute,
	})

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "invalid JSON in PUT request",
			method:         "PUT",
			path:           "/admin/ratelimit/tenant/test",
			body:           `{"invalid json`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid JSON",
		},
		{
			name:           "negative request limit",
			method:         "PUT",
			path:           "/admin/ratelimit/tenant/test",
			body:           `{"request_limit": -10, "byte_limit": 1000}`,
			expectedStatus: http.StatusInternalServerError, // Validation errors return 500
			expectedError:  "request limit must be positive",
		},
		{
			name:           "zero byte limit",
			method:         "PUT",
			path:           "/admin/ratelimit/tenant/test",
			body:           `{"request_limit": 10, "byte_limit": 0}`,
			expectedStatus: http.StatusInternalServerError, // Validation errors return 500
			expectedError:  "byte limit must be positive",
		},
		{
			name:           "missing tenant ID in path",
			method:         "GET",
			path:           "/admin/ratelimit/tenant/",
			body:           "",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Missing tenant ID",
		},
		{
			name:           "special characters in tenant ID",
			method:         "PUT",
			path:           "/admin/ratelimit/tenant/test-tenant_123.com",
			body:           `{"request_limit": 50, "byte_limit": 5000}`,
			expectedStatus: http.StatusOK,
			expectedError:  "",
		},
		{
			name:           "unsupported batch operation",
			method:         "POST",
			path:           "/admin/ratelimit/batch",
			body:           `{"operation": "update", "clients": []}`,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid operation",
		},
		{
			name:           "empty batch request",
			method:         "POST",
			path:           "/admin/ratelimit/batch",
			body:           `{"operation": "set", "clients": []}`,
			expectedStatus: http.StatusOK, // Empty batch is allowed, just returns empty results
			expectedError:  "",
		},
		{
			name:           "batch with invalid tenant ID",
			method:         "POST",
			path:           "/admin/ratelimit/batch",
			body:           `{"operation": "set", "clients": [{"tenant_id": "", "request_limit": 10}]}`,
			expectedStatus: http.StatusOK, // Still processes but marks as failed
			expectedError:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("X-Admin-Key", "test-key")
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}

			rr := httptest.NewRecorder()

			// Route to appropriate handler
			if strings.Contains(tt.path, "/batch") {
				handler.handleBatch(rr, req)
			} else if strings.Contains(tt.path, "/tenant/") {
				handler.handleTenant(rr, req)
			}

			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rr.Code)
			}

			if tt.expectedError != "" {
				body := rr.Body.String()
				if !strings.Contains(body, tt.expectedError) {
					t.Errorf("Expected error containing %q, got %q", tt.expectedError, body)
				}
			}
		})
	}
}

// =============================================================================
// Concurrent Operations Tests
// =============================================================================

func TestAdminHandlerConcurrentOperations(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer cleanupRedis(redisClient)

	ctx := context.Background()
	dynamicLimiter := limiter.NewDynamicLimiter(
		redisClient,
		"test",
		100,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test-client" },
	)

	_ = NewAdminHandler(dynamicLimiter, AdminConfig{
		EnableAuth: true,
		AdminKey:   "test-key",
	})

	// Test concurrent updates
	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(tenantNum int) {
			defer wg.Done()
			cfg := config.ClientConfig{
				RequestLimit: (tenantNum + 1) * 10,
				ByteLimit:    int64((tenantNum + 1) * 1000),
				WindowSecs:   60,
			}
			err := dynamicLimiter.SetClientConfig(ctx,
				fmt.Sprintf("tenant-%d", tenantNum), cfg)
			if err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	var errorCount int
	for err := range errors {
		t.Errorf("Concurrent operation error: %v", err)
		errorCount++
	}

	if errorCount > 0 {
		t.Fatalf("%d concurrent operations failed", errorCount)
	}

	// Verify all configurations were set correctly
	for i := 0; i < 10; i++ {
		tenantID := fmt.Sprintf("tenant-%d", i)
		cfg, err := dynamicLimiter.GetClientConfig(ctx, tenantID)
		if err != nil {
			t.Errorf("Failed to get config for %s: %v", tenantID, err)
			continue
		}
		expectedLimit := (i + 1) * 10
		if cfg.RequestLimit != expectedLimit {
			t.Errorf("Tenant %s: expected request limit %d, got %d",
				tenantID, expectedLimit, cfg.RequestLimit)
		}
	}
}

// =============================================================================
// Large Batch Operations Tests
// =============================================================================

func TestAdminHandlerLargeBatchOperations(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer cleanupRedis(redisClient)

	dynamicLimiter := limiter.NewDynamicLimiter(
		redisClient,
		"test",
		100,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test-client" },
	)

	handler := NewAdminHandler(dynamicLimiter, AdminConfig{
		EnableAuth:          true,
		AdminKey:            "test-key",
		DefaultRequestLimit: 100,
		DefaultByteLimit:    1048576,
		DefaultWindow:       time.Minute,
	})

	t.Run("batch with 100 clients", func(t *testing.T) {
		var clients []BatchClientConfig
		for i := 0; i < 100; i++ {
			clients = append(clients, BatchClientConfig{
				TenantID:     fmt.Sprintf("tenant-%03d", i),
				RequestLimit: 100 + i,
				ByteLimit:    int64(10000 * (i + 1)),
				WindowSecs:   60,
			})
		}

		batchReq := BatchOperation{
			Operation: "set",
			Clients:   clients,
		}

		body, _ := json.Marshal(batchReq)
		req := httptest.NewRequest("POST", "/admin/ratelimit/batch", bytes.NewReader(body))
		req.Header.Set("X-Admin-Key", "test-key")
		req.Header.Set("Content-Type", "application/json")

		rr := httptest.NewRecorder()
		handler.handleBatch(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", rr.Code)
		}

		var resp map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if status, ok := resp["status"].(string); !ok || status != "completed" {
			t.Errorf("Expected status 'completed', got %v", resp["status"])
		}
		if results, ok := resp["results"].([]interface{}); !ok || len(results) != 100 {
			t.Errorf("Expected 100 results, got %d", len(results))
			if errors, ok := resp["errors"].([]interface{}); ok && len(errors) > 0 {
				t.Errorf("Errors occurred: %v", errors)
			}
		}
	})
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkAdminHandler(b *testing.B) {
	redisClient := testutil.NewTestRedisClientDefault()
	defer redisClient.Close()

	ctx := context.Background()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		b.Skip("Redis not available")
	}

	dynamicLimiter := limiter.NewDynamicLimiter(
		redisClient,
		"bench",
		100,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test-client" },
	)

	handler := NewAdminHandler(dynamicLimiter, AdminConfig{
		EnableAuth:          false, // Disable auth for benchmark
		DefaultRequestLimit: 100,
		DefaultByteLimit:    1048576,
		DefaultWindow:       time.Minute,
	})

	b.Run("Status", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest("GET", "/admin/ratelimit/status", nil)
			rr := httptest.NewRecorder()
			handler.handleStatus(rr, req)
		}
	})

	b.Run("GetTenant", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest("GET", "/admin/ratelimit/tenant/bench-tenant", nil)
			rr := httptest.NewRecorder()
			handler.handleTenant(rr, req)
		}
	})

	b.Run("SetTenant", func(b *testing.B) {
		body := []byte(`{"request_limit": 50, "byte_limit": 524288, "window_secs": 60}`)
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest("PUT", "/admin/ratelimit/tenant/bench-tenant",
				bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			handler.handleTenant(rr, req)
		}
	})

	b.Run("BatchSet", func(b *testing.B) {
		batchReq := BatchOperation{
			Operation: "set",
			Clients: []BatchClientConfig{
				{TenantID: "t1", RequestLimit: 75, ByteLimit: 750000, WindowSecs: 60},
				{TenantID: "t2", RequestLimit: 150, ByteLimit: 1500000, WindowSecs: 60},
				{TenantID: "t3", RequestLimit: 200, ByteLimit: 2000000, WindowSecs: 60},
			},
		}
		body, _ := json.Marshal(batchReq)

		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest("POST", "/admin/ratelimit/batch",
				bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			handler.handleBatch(rr, req)
		}
	})
}

// =============================================================================
// Config Handler Tests (Legacy)
// =============================================================================

func TestConfigHandler(t *testing.T) {
	redisClient := setupTestRedis(t)
	defer cleanupRedis(redisClient)

	dynamicLimiter := limiter.NewDynamicLimiter(
		redisClient,
		"test",
		100,
		1048576,
		time.Minute,
		func(r *http.Request) string { return "test-client" },
	)

	handler := NewConfigHandler(dynamicLimiter)
	handler.SetAdminKey("admin-key") // Set the admin key to match test expectations

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		headers        map[string]string
		expectedStatus int
	}{
		{
			name:   "GET config with auth",
			method: "GET",
			path:   "/admin/config/client1",
			headers: map[string]string{
				"X-Admin-Key": "admin-key",
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:   "PUT config with auth",
			method: "PUT",
			path:   "/admin/config/client1",
			body:   `{"request_limit": 50, "byte_limit": 500000, "window_secs": 60}`,
			headers: map[string]string{
				"X-Admin-Key":  "admin-key",
				"Content-Type": "application/json",
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:   "DELETE config with auth",
			method: "DELETE",
			path:   "/admin/config/client1",
			headers: map[string]string{
				"X-Admin-Key": "admin-key",
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Request without auth",
			method:         "GET",
			path:           "/admin/config/client1",
			headers:        map[string]string{},
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}

			req := httptest.NewRequest(tt.method, tt.path, body)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rr := httptest.NewRecorder()
			handler.handleConfigRequest(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d. Body: %s",
					tt.expectedStatus, rr.Code, rr.Body.String())
			}
		})
	}
}
