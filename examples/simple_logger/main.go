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
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/NVIDIA/go-ratelimit/pkg/ratelimit"
	"github.com/NVIDIA/go-ratelimit/pkg/utils"
	"github.com/redis/go-redis/v9"
)

// SimpleLogger implements the middleware.Logger interface
type SimpleLogger struct{}

func (s SimpleLogger) Info(msg string, keysAndValues ...interface{}) {
	log.Printf("[INFO] %s %v\n", msg, keysAndValues)
}

func (s SimpleLogger) Error(msg string, keysAndValues ...interface{}) {
	log.Printf("[ERROR] %s %v\n", msg, keysAndValues)
}

func main() {
	// Create Redis client
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	// Verify Redis connection
	ctx := context.Background()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatal("Failed to connect to Redis:", err)
	}

	// Create rate limiting service with a custom logger
	options := ratelimit.Options{
		Redis:        redisClient,
		Prefix:       "example:ratelimit",
		RequestLimit: 10,
		ByteLimit:    1024 * 1024, // 1MB
		Window:       time.Minute,
		KeyFunc: func(r *http.Request) string {
			// Rate limit by tenant ID header, fallback to IP
			tenantID := r.Header.Get("X-Tenant-ID")
			if tenantID == "" {
				return utils.KeyByIP(r)
			}
			return fmt.Sprintf("tenant-%s", tenantID)
		},
		Logger: SimpleLogger{}, // Use our custom logger
	}

	service := ratelimit.New(options)

	// Create HTTP mux
	mux := http.NewServeMux()

	// Register config API (provides /admin/config/* endpoints)
	service.RegisterConfigAPI(mux)

	// Add a test endpoint with rate limiting
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-ID")
		if tenantID == "" {
			tenantID = "anonymous"
		}
		fmt.Fprintf(w, "Hello! Request from tenant: %s\n", tenantID)
	})

	// Apply rate limiting to the test endpoint
	mux.Handle("/api/test", service.Middleware()(testHandler))

	// Health check (no rate limiting)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Start server
	fmt.Println("Server starting on :8080")
	fmt.Println("\nTest commands:")
	fmt.Println("  # Test rate limiting (10 requests per minute)")
	fmt.Println("  for i in {1..15}; do curl -H 'X-Tenant-ID: test' http://localhost:8080/api/test; echo; done")
	fmt.Println("\n  # Get client config (requires admin key)")
	fmt.Println("  curl -H 'X-Admin-Key: admin-secret-key' http://localhost:8080/admin/config/test")
	fmt.Println("\n  # Update client limits")
	fmt.Println("  curl -X PUT -H 'X-Admin-Key: admin-secret-key' -H 'Content-Type: application/json' \\")
	fmt.Println("    -d '{\"request_limit\": 20, \"byte_limit\": 2097152, \"window_secs\": 60}' \\")
	fmt.Println("    http://localhost:8080/admin/config/test")

	log.Fatal(http.ListenAndServe(":8080", mux))
}
