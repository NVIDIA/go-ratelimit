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
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/NVIDIA/go-ratelimit/pkg/config"
	"github.com/NVIDIA/go-ratelimit/pkg/limiter"
	logger "github.com/NVIDIA/go-ratelimit/pkg/logger"
	"github.com/NVIDIA/go-ratelimit/pkg/middleware"
	"github.com/NVIDIA/go-ratelimit/pkg/utils"
	"github.com/redis/go-redis/v9"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	// Initialize logger with default settings
	initDefaultLogger()

	// Use a Redis Cluster or a single node; tune PoolSize for high QPS.
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:6379",
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
		PoolSize:     64,
	})

	// Initialize redisAvailable flag
	redisAvailable := false

	// Try to connect to Redis (logger is now initialized)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Safely check Redis connection with error handling
	pingCmd := rdb.Ping(ctx)
	if pingCmd != nil {
		if err := pingCmd.Err(); err != nil {
			logger.Log.Error(fmt.Sprintf("Redis connection failed: %v. Will use in-memory fallback.", err))
		} else {
			redisAvailable = true
			logger.Log.Info("Successfully connected to Redis")
		}
	} else {
		logger.Log.Error("Redis ping command returned nil. Will use in-memory fallback.")
	}

	// Create a custom key function that uses client ID from query parameter
	keyByClientID := func(r *http.Request) string {
		clientID := r.URL.Query().Get("client_id")
		if clientID == "" {
			// Fallback to IP if no client ID provided
			return "ip:" + utils.ClientIP(r)
		}
		return "client:" + clientID
	}

	// Create a resilient rate limiter with per-client configurations
	// Default policy: 10 requests and 1MB per minute
	resilientLimiter := limiter.NewResilientLimiter(
		rdb,
		"rl",
		10,    // Default: 10 requests per minute
		10485, // Default: 1MB (10485 bytes) per minute
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
		w.Write(largeResponse)
	})

	// Add example endpoint to show current client limits
	mux.HandleFunc("/v1/my-limits", func(w http.ResponseWriter, r *http.Request) {
		clientID := r.URL.Query().Get("client_id")
		if clientID == "" {
			http.Error(w, "client_id parameter required", http.StatusBadRequest)
			return
		}

		cfg, err := resilientLimiter.GetClientConfig(r.Context(), clientID)
		if err != nil {
			http.Error(w, "Error retrieving limits: "+err.Error(), http.StatusInternalServerError)
			return
		}

		fmt.Fprintf(w, "Your rate limits:\n")
		fmt.Fprintf(w, "- Client ID: %s\n", clientID)
		fmt.Fprintf(w, "- Request limit: %d requests per %d seconds\n", cfg.RequestLimit, cfg.WindowSecs)
		fmt.Fprintf(w, "- Byte limit: %d bytes per %d seconds\n", cfg.ByteLimit, cfg.WindowSecs)
		if redisAvailable {
			fmt.Fprintf(w, "- Using: Redis with memory fallback\n")
		} else {
			fmt.Fprintf(w, "- Using: In-memory limiter (Redis unavailable)\n")
		}
	})

	// Add endpoint to toggle Redis availability (for testing)
	mux.HandleFunc("/admin/toggle-redis", func(w http.ResponseWriter, r *http.Request) {
		// This is just for demonstration purposes
		redisAvailable = !redisAvailable
		if redisAvailable {
			fmt.Fprintf(w, "Redis marked as available (this is just a simulation)\n")
		} else {
			fmt.Fprintf(w, "Redis marked as unavailable (this is just a simulation)\n")
		}
	})

	// Add endpoint to view default limits
	mux.HandleFunc("/v1/default-limits", func(w http.ResponseWriter, r *http.Request) {
		defaultCfg := resilientLimiter.GetDefaultConfig()

		fmt.Fprintf(w, "Default rate limits:\n")
		fmt.Fprintf(w, "- Request limit: %d requests per %d seconds\n", defaultCfg.RequestLimit, defaultCfg.WindowSecs)
		fmt.Fprintf(w, "- Byte limit: %d bytes per %d seconds\n", defaultCfg.ByteLimit, defaultCfg.WindowSecs)
	})

	// Add endpoint to update default limits
	mux.HandleFunc("/admin/update-defaults", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse request body
		var newConfig config.ClientConfig
		if err := json.NewDecoder(r.Body).Decode(&newConfig); err != nil {
			http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Validate config
		if newConfig.RequestLimit <= 0 {
			http.Error(w, "request_limit must be positive", http.StatusBadRequest)
			return
		}
		if newConfig.ByteLimit <= 0 {
			http.Error(w, "byte_limit must be positive", http.StatusBadRequest)
			return
		}
		if newConfig.WindowSecs <= 0 {
			http.Error(w, "window_secs must be positive", http.StatusBadRequest)
			return
		}

		// Update default config
		if err := resilientLimiter.UpdateDefaultConfig(r.Context(), newConfig.RequestLimit, newConfig.ByteLimit, newConfig.WindowSecs); err != nil {
			http.Error(w, "Failed to update default limits: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Return success
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Successfully updated default limits:\n")
		fmt.Fprintf(w, "- Request limit: %d requests per %d seconds\n", newConfig.RequestLimit, newConfig.WindowSecs)
		fmt.Fprintf(w, "- Byte limit: %d bytes per %d seconds\n", newConfig.ByteLimit, newConfig.WindowSecs)
	})

	// Apply dynamic rate limiting middleware
	rateLimitHandler := middleware.DynamicRateLimitMiddleware(resilientLimiter)(mux)

	// Now it's safe to use the logger
	if redisAvailable {
		logger.Log.Info("Using Redis for rate limiting with memory fallback")
	} else {
		logger.Log.Warn("Redis is unavailable, using in-memory rate limiting only")
	}

	logger.Log.Info("Server running on :8083")
	logger.Log.Info("API endpoints:")
	logger.Log.Info("- GET /v1/thing?client_id=user123 - Simple endpoint")
	logger.Log.Info("- GET /v1/heavy?client_id=user123 - Returns ~10KB response")
	logger.Log.Info("- GET /v1/my-limits?client_id=user123 - Shows your current rate limits")
	logger.Log.Info("- GET /v1/default-limits - Shows default rate limits")
	logger.Log.Info("Admin endpoints:")
	logger.Log.Info("- GET /admin/toggle-redis - Toggle Redis availability (simulation)")
	logger.Log.Info("- POST /admin/update-defaults - Update default limits for all clients")
	logger.Log.Info("")
	logger.Log.Info("Example to update default limits:")
	logger.Log.Info("curl -X POST -H \"Content-Type: application/json\" -d '{\"request_limit\":20,\"byte_limit\":20480,\"window_secs\":60}' http://localhost:8083/admin/update-defaults")

	if err := http.ListenAndServe(":8083", rateLimitHandler); err != nil {
		logger.Log.Error(fmt.Sprintf("Failed to start server: %v", err))
	}
}

// initDefaultLogger initializes the logger with default settings
func initDefaultLogger() {
	// Create a simple console encoder
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoder := zapcore.NewConsoleEncoder(encoderConfig)

	// Write to console
	core := zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), zapcore.InfoLevel)

	// Create the logger
	zapLogger := zap.New(core)

	// Create an otelzap logger and set it as the global logger
	logger.Log = otelzap.New(zapLogger)
}
