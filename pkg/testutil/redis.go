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
package testutil

import (
	"time"

	"github.com/redis/go-redis/v9"
)

// NewTestRedisClient creates a Redis client configured for testing with appropriate timeouts.
// This ensures tests don't hang when Redis is unavailable and gives Redis time to start in containerized environments.
func NewTestRedisClient(addr string, db int) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         addr,
		DB:           db,
		DialTimeout:  3 * time.Second, // Give Redis time to start in containers
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
	})
}

// NewTestRedisClientDefault creates a Redis client with default test settings (localhost:6379, DB 0).
func NewTestRedisClientDefault() *redis.Client {
	return NewTestRedisClient("localhost:6379", 0)
}
