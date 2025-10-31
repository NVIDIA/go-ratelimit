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

package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/NVIDIA/go-ratelimit/pkg/ratelimit"
	"github.com/NVIDIA/go-ratelimit/pkg/utils"
	"github.com/redis/go-redis/v9"
)

// This example demonstrates various ways to use header-based rate limiting
func main() {
	// Create Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	// Example 1: Simple header-based rate limiting
	example1SimpleHeader(rdb)

	// Example 2: Header with options (prefix, sanitizer, etc.)
	example2HeaderWithOptions(rdb)

	// Example 3: Multiple headers composite key
	example3MultipleHeaders(rdb)

	// Example 4: Header with fallback
	example4HeaderFallback(rdb)

	// Example 5: Real-world scenario - Multi-tenant API
	example5MultiTenantAPI(rdb)
}

// Example 1: Simple header-based rate limiting
func example1SimpleHeader(rdb *redis.Client) {
	fmt.Println("\n=== Example 1: Simple Header-Based Rate Limiting ===")

	// Create rate limiter that uses X-API-Key header
	options := ratelimit.Options{
		Redis:        rdb,
		Prefix:       "example1",
		RequestLimit: 10,
		ByteLimit:    1024 * 1024, // 1MB
		Window:       time.Minute,
		KeyFunc:      ratelimit.KeyByHeader("X-API-Key"),
	}

	service := ratelimit.New(options)

	// Create HTTP handler
	mux := http.NewServeMux()
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("X-API-Key")
		fmt.Fprintf(w, "Hello API Key: %s\n", apiKey)
	})

	// Apply rate limiting
	handler := service.Middleware()(mux)

	fmt.Println("Server running on :8081")
	fmt.Println("Try: curl -H 'X-API-Key: key123' http://localhost:8081/api/data")

	// Start the server
	if err := http.ListenAndServe(":8081", handler); err != nil {
		log.Fatal("Server failed to start:", err)
	}
}

// Example 2: Header with advanced options
func example2HeaderWithOptions(rdb *redis.Client) {
	fmt.Println("\n=== Example 2: Header with Options ===")

	// Create rate limiter with advanced header options
	options := ratelimit.Options{
		Redis:        rdb,
		Prefix:       "example2",
		RequestLimit: 100,
		ByteLimit:    10 * 1024 * 1024, // 10MB
		Window:       time.Hour,
		KeyFunc: ratelimit.KeyByHeaderWithOptions(utils.HeaderKeyOptions{
			HeaderName:    "X-Tenant-ID",
			Prefix:        "tenant",
			DefaultValue:  "default-tenant",
			CaseSensitive: false,
			Sanitizer:     utils.AlphanumericSanitizer,
		}),
	}

	service := ratelimit.New(options)

	// Create HTTP handler
	mux := http.NewServeMux()
	mux.HandleFunc("/api/upload", func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-ID")
		fmt.Fprintf(w, "Processing upload for tenant: %s\n", tenantID)
	})

	// Apply rate limiting
	handler := service.Middleware()(mux)

	fmt.Println("Server with tenant-based limiting")
	fmt.Println("Tenant IDs are sanitized (alphanumeric only) and prefixed with 'tenant:'")
	_ = handler // Use the handler in your server
}

// Example 3: Multiple headers for composite keys
func example3MultipleHeaders(rdb *redis.Client) {
	fmt.Println("\n=== Example 3: Multiple Headers Composite Key ===")

	// Rate limit by combination of tenant and API version
	options := ratelimit.Options{
		Redis:        rdb,
		Prefix:       "example3",
		RequestLimit: 50,
		ByteLimit:    5 * 1024 * 1024, // 5MB
		Window:       time.Minute,
		KeyFunc:      ratelimit.KeyByMultipleHeaders([]string{"X-Tenant-ID", "X-API-Version"}, ":"),
	}

	service := ratelimit.New(options)

	// Create HTTP handler
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		tenant := r.Header.Get("X-Tenant-ID")
		version := r.Header.Get("X-API-Version")
		fmt.Fprintf(w, "Query from tenant %s using API %s\n", tenant, version)
	})

	// Apply rate limiting
	handler := service.Middleware()(mux)

	fmt.Println("Rate limiting by tenant AND API version combination")
	_ = handler
}

// Example 4: Header with fallback chain
func example4HeaderFallback(rdb *redis.Client) {
	fmt.Println("\n=== Example 4: Header Fallback Chain ===")

	// Try multiple headers in order
	options := ratelimit.Options{
		Redis:        rdb,
		Prefix:       "example4",
		RequestLimit: 20,
		ByteLimit:    2 * 1024 * 1024, // 2MB
		Window:       time.Minute,
		KeyFunc:      ratelimit.KeyByHeaderWithFallback([]string{"X-API-Key", "Authorization", "X-Client-ID"}),
	}

	service := ratelimit.New(options)

	// Create HTTP handler
	mux := http.NewServeMux()
	mux.HandleFunc("/api/flexible", func(w http.ResponseWriter, r *http.Request) {
		// The rate limiter will use the first available header
		fmt.Fprintln(w, "Request processed with flexible authentication")
	})

	// Apply rate limiting
	handler := service.Middleware()(mux)

	fmt.Println("Rate limiter tries headers in order: X-API-Key, Authorization, X-Client-ID")
	_ = handler
}

// Example 5: Real-world multi-tenant API scenario
func example5MultiTenantAPI(rdb *redis.Client) {
	fmt.Println("\n=== Example 5: Real-World Multi-Tenant API ===")

	// Different endpoints with different rate limits

	// Public endpoints - rate limit by IP
	publicOptions := ratelimit.Options{
		Redis:        rdb,
		Prefix:       "public",
		RequestLimit: 10,
		ByteLimit:    100 * 1024, // 100KB
		Window:       time.Minute,
		KeyFunc:      ratelimit.KeyByIP,
	}
	publicLimiter := ratelimit.New(publicOptions)

	// Authenticated endpoints - rate limit by tenant
	tenantOptions := ratelimit.Options{
		Redis:        rdb,
		Prefix:       "tenant",
		RequestLimit: 1000,
		ByteLimit:    100 * 1024 * 1024, // 100MB
		Window:       time.Hour,
		KeyFunc: ratelimit.KeyByHeaderWithOptions(utils.HeaderKeyOptions{
			HeaderName: "X-Tenant-ID",
			Prefix:     "tenant",
			Sanitizer:  utils.UUIDSanitizer,
		}),
	}
	tenantLimiter := ratelimit.New(tenantOptions)

	// Premium endpoints - higher limits
	premiumOptions := ratelimit.Options{
		Redis:        rdb,
		Prefix:       "premium",
		RequestLimit: 10000,
		ByteLimit:    1024 * 1024 * 1024, // 1GB
		Window:       time.Hour,
		KeyFunc: ratelimit.KeyByHeaderWithOptions(utils.HeaderKeyOptions{
			HeaderName: "X-Tenant-ID",
			Prefix:     "premium",
			Sanitizer:  utils.UUIDSanitizer,
		}),
	}
	premiumLimiter := ratelimit.New(premiumOptions)

	// Create main mux
	mux := http.NewServeMux()

	// Public endpoints
	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/api/public/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "System status: OK")
	})

	// Tenant endpoints
	tenantMux := http.NewServeMux()
	tenantMux.HandleFunc("/api/tenant/data", func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-ID")
		fmt.Fprintf(w, "Data for tenant: %s\n", tenantID)
	})

	// Premium endpoints
	premiumMux := http.NewServeMux()
	premiumMux.HandleFunc("/api/premium/analytics", func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-ID")
		fmt.Fprintf(w, "Premium analytics for tenant: %s\n", tenantID)
	})

	// Apply different rate limiters to different paths
	mux.Handle("/api/public/", publicLimiter.Middleware()(publicMux))
	mux.Handle("/api/tenant/", tenantLimiter.Middleware()(tenantMux))
	mux.Handle("/api/premium/", premiumLimiter.Middleware()(premiumMux))

	// Start server
	server := &http.Server{
		Addr:    ":8085",
		Handler: mux,
	}

	fmt.Println("\nMulti-tenant API server running on :8085")
	fmt.Println("Endpoints:")
	fmt.Println("  - /api/public/* - Rate limited by IP (10 req/min)")
	fmt.Println("  - /api/tenant/* - Rate limited by X-Tenant-ID header (1000 req/hour)")
	fmt.Println("  - /api/premium/* - Higher limits for premium tenants (10000 req/hour)")

	log.Fatal(server.ListenAndServe())
}
