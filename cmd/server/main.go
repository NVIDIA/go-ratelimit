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
	"net/http"
	"time"

	"github.com/NVIDIA/go-ratelimit/pkg/api"
	"github.com/NVIDIA/go-ratelimit/pkg/limiter"
	logger "github.com/NVIDIA/go-ratelimit/pkg/logger"
	"github.com/NVIDIA/go-ratelimit/pkg/middleware"
	"github.com/NVIDIA/go-ratelimit/pkg/ratelimit"
	"github.com/NVIDIA/go-ratelimit/pkg/utils"
	"github.com/redis/go-redis/v9"
)

func main() {
	// Use a Redis Cluster or a single node; tune PoolSize for high QPS.
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:6379",
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
		PoolSize:     64,
	})

	// Health check Redis up-front
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Log.Error(fmt.Sprintf("Redis connection failed: %v. Make sure Redis is running at 127.0.0.1:6379", err))
	}
	logger.Log.Info("Successfully connected to Redis")

	// Create a custom key function that uses client ID from query parameter
	keyByClientID := func(r *http.Request) string {
		clientID := r.URL.Query().Get("client_id")
		if clientID == "" {
			// Fallback to IP if no client ID provided
			return "ip:" + utils.ClientIP(r)
		}
		return "client:" + clientID
	}

	// Create a dynamic rate limiter with per-client configurations
	// Default policy: 10 requests and 1MB per minute
	dynamicLimiter := limiter.NewDynamicLimiter(
		rdb,
		"rl",
		10,      // Default: 10 requests per minute
		1048576, // Default: 1MB (1048576 bytes) per minute
		time.Minute,
		keyByClientID,
	)

	// Create API routes
	mux := http.NewServeMux()

	// Regular API endpoints
	mux.HandleFunc("/v1/thing", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/v1/heavy", func(w http.ResponseWriter, r *http.Request) {
		// Generate a larger response (about 10KB)
		largeResponse := make([]byte, 10240)
		for i := range largeResponse {
			largeResponse[i] = byte('A' + (i % 26))
		}
		_, _ = w.Write(largeResponse)
	})
	mux.HandleFunc("/v1/veryheavy", func(w http.ResponseWriter, r *http.Request) {
		// Generate a very large response (about 500KB)
		largeResponse := make([]byte, 512000)
		for i := range largeResponse {
			largeResponse[i] = byte('A' + (i % 26))
		}
		_, _ = w.Write(largeResponse)
	})

	// Add client configuration endpoint
	configHandler := api.NewConfigHandler(dynamicLimiter)
	configHandler.RegisterRoutes(mux)

	// Add example endpoint to show current client limits
	mux.HandleFunc("/v1/my-limits", func(w http.ResponseWriter, r *http.Request) {
		clientID := r.URL.Query().Get("client_id")
		if clientID == "" {
			http.Error(w, "client_id parameter required", http.StatusBadRequest)
			return
		}

		cfg, err := dynamicLimiter.GetClientConfig(r.Context(), clientID)
		if err != nil {
			http.Error(w, "Error retrieving limits: "+err.Error(), http.StatusInternalServerError)
			return
		}

		fmt.Fprintf(w, "Your rate limits:\n")
		fmt.Fprintf(w, "- Client ID: %s\n", clientID)
		fmt.Fprintf(w, "- Request limit: %d requests per %d seconds\n", cfg.RequestLimit, cfg.WindowSecs)
		fmt.Fprintf(w, "- Byte limit: %d bytes per %d seconds\n", cfg.ByteLimit, cfg.WindowSecs)
	})

	// Create a service to use the new API
	service := ratelimit.NewFromLimiter(dynamicLimiter)

	// Apply selective rate limiting middleware - only rate limit heavy endpoints
	heavyPathMatcher := middleware.MatchAny(
		middleware.MatchPathExact("/v1/heavy"),
		middleware.MatchPathExact("/v1/veryheavy"),
	)

	// Apply rate limiting only to specific paths
	rateLimitHandler := service.SelectiveMiddleware(heavyPathMatcher)(mux)

	logger.Log.Info("Server running on :8082")
	logger.Log.Info("API endpoints:")
	logger.Log.Info("- GET /v1/thing?client_id=user123 - Simple endpoint")
	logger.Log.Info("- GET /v1/heavy?client_id=user123 - Returns ~10KB response")
	logger.Log.Info("- GET /v1/veryheavy?client_id=user123 - Returns ~500KB response")
	logger.Log.Info("- GET /v1/my-limits?client_id=user123 - Shows your current rate limits")
	logger.Log.Info("Admin endpoints:")
	logger.Log.Info("- GET /admin/config/{clientID} - Get client's rate limit config")
	logger.Log.Info("- PUT /admin/config/{clientID} - Update client's rate limit config")
	logger.Log.Info("- DELETE /admin/config/{clientID} - Reset client to default config")

	if err := http.ListenAndServe(":8082", rateLimitHandler); err != nil {
		logger.Log.Error(fmt.Sprintf("Failed to start server: %v", err))
	}
}
