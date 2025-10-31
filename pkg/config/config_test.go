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
package config

import (
	"context"
	"fmt"
	"testing"

	"github.com/NVIDIA/go-ratelimit/pkg/testutil"
	"github.com/redis/go-redis/v9"
)

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

func TestClientConfig(t *testing.T) {
	// Test ClientConfig struct
	cfg := ClientConfig{
		RequestLimit: 100,
		ByteLimit:    1024 * 1024,
		WindowSecs:   60,
	}

	if cfg.RequestLimit != 100 {
		t.Errorf("Expected RequestLimit 100, got %d", cfg.RequestLimit)
	}

	if cfg.ByteLimit != 1024*1024 {
		t.Errorf("Expected ByteLimit 1048576, got %d", cfg.ByteLimit)
	}

	if cfg.WindowSecs != 60 {
		t.Errorf("Expected WindowSecs 60, got %d", cfg.WindowSecs)
	}
}

func TestConfigManager(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	ctx := context.Background()
	defaultCfg := ClientConfig{
		RequestLimit: 100,
		ByteLimit:    10000,
		WindowSecs:   60,
	}
	manager := NewConfigManager(client, "test:config", defaultCfg)

	// Test setting and getting config
	cfg := ClientConfig{
		RequestLimit: 50,
		ByteLimit:    5000,
		WindowSecs:   30,
	}

	err := manager.SetConfig(ctx, "client1", cfg)
	if err != nil {
		t.Fatalf("Failed to set config: %v", err)
	}

	// Get the config back
	retrieved, err := manager.GetConfig(ctx, "client1")
	if err != nil {
		t.Fatalf("Failed to get config: %v", err)
	}

	if retrieved.RequestLimit != cfg.RequestLimit {
		t.Errorf("Expected RequestLimit %d, got %d", cfg.RequestLimit, retrieved.RequestLimit)
	}
	if retrieved.ByteLimit != cfg.ByteLimit {
		t.Errorf("Expected ByteLimit %d, got %d", cfg.ByteLimit, retrieved.ByteLimit)
	}
	if retrieved.WindowSecs != cfg.WindowSecs {
		t.Errorf("Expected WindowSecs %d, got %d", cfg.WindowSecs, retrieved.WindowSecs)
	}

	// Test deleting config
	err = manager.DeleteConfig(ctx, "client1")
	if err != nil {
		t.Fatalf("Failed to delete config: %v", err)
	}

	// Getting deleted config should return defaults
	deleted, err := manager.GetConfig(ctx, "client1")
	if err != nil {
		t.Fatalf("Failed to get config: %v", err)
	}
	// Should return defaults
	if deleted.RequestLimit != defaultCfg.RequestLimit {
		t.Errorf("Expected default RequestLimit %d, got %d", defaultCfg.RequestLimit, deleted.RequestLimit)
	}
}

func TestConfigManagerDefaults(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	ctx := context.Background()
	defaultCfg := ClientConfig{
		RequestLimit: 200,
		ByteLimit:    20000,
		WindowSecs:   120,
	}
	manager := NewConfigManager(client, "test:config", defaultCfg)

	// Test getting non-existent config returns defaults
	cfg, err := manager.GetConfig(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Failed to get config: %v", err)
	}

	if cfg.RequestLimit != defaultCfg.RequestLimit {
		t.Errorf("Expected default RequestLimit %d, got %d", defaultCfg.RequestLimit, cfg.RequestLimit)
	}
}

func TestConfigManagerConcurrency(t *testing.T) {
	client := setupTestRedis(t)
	defer client.Close()

	ctx := context.Background()
	defaultCfg := ClientConfig{
		RequestLimit: 100,
		ByteLimit:    10000,
		WindowSecs:   60,
	}
	manager := NewConfigManager(client, "test:config", defaultCfg)

	// Test concurrent operations
	done := make(chan bool, 10)
	errors := make(chan error, 10)

	// Concurrent sets
	for i := 0; i < 10; i++ {
		go func(id int) {
			cfg := ClientConfig{
				RequestLimit: (id + 1) * 10, // Ensure non-zero
				ByteLimit:    int64((id + 1) * 1000),
				WindowSecs:   60,
			}
			clientID := fmt.Sprintf("concurrent-%d", id)
			err := manager.SetConfig(ctx, clientID, cfg)
			if err != nil {
				errors <- err
				done <- false
				return
			}

			// Verify the set worked
			retrieved, err := manager.GetConfig(ctx, clientID)
			if err != nil {
				errors <- err
				done <- false
				return
			}

			if retrieved.RequestLimit != cfg.RequestLimit {
				errors <- fmt.Errorf("config mismatch for client %s", clientID)
				done <- false
				return
			}

			done <- true
		}(i)
	}

	// Wait for all operations
	success := true
	for i := 0; i < 10; i++ {
		if !<-done {
			success = false
		}
	}

	close(errors)
	for err := range errors {
		t.Errorf("Concurrent error: %v", err)
	}

	if !success {
		t.Fatal("Concurrent operations failed")
	}
}
