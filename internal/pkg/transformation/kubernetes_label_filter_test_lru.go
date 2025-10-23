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

package transformation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestLabelFilterCache_LRUEviction tests that the cache properly evicts least recently used entries
func TestLabelFilterCache_LRUEviction(t *testing.T) {
	patterns := []string{"^label-.*"} // Match labels starting with "label-"
	cacheSize := 5

	podMapper := &PodMapper{
		Config: &appconfig.Config{
			KubernetesPodLabelAllowlistRegex: patterns,
		},
		labelFilterCache: newLabelFilterCache(patterns, cacheSize),
	}

	cache := podMapper.labelFilterCache

	// Fill cache to capacity
	for i := 1; i <= cacheSize; i++ {
		labelKey := fmt.Sprintf("label-%d", i)
		result := podMapper.shouldIncludeLabel(labelKey)
		assert.True(t, result, "Label should match pattern")
	}

	// Verify cache is at capacity
	cache.mu.Lock()
	assert.Equal(t, cacheSize, len(cache.cache), "Cache should be at capacity")
	assert.Equal(t, cacheSize, cache.lruList.Len(), "LRU list should match cache size")
	cache.mu.Unlock()

	// Access label-3 to make it more recently used
	podMapper.shouldIncludeLabel("label-3")

	// Add a new label, should evict label-1 (oldest)
	result := podMapper.shouldIncludeLabel("label-6")
	assert.True(t, result, "New label should match pattern")

	// Verify cache is still at capacity
	cache.mu.Lock()
	assert.Equal(t, cacheSize, len(cache.cache), "Cache should still be at capacity")
	assert.Equal(t, cacheSize, cache.lruList.Len(), "LRU list should still match cache size")

	// Verify label-1 was evicted (oldest)
	_, exists := cache.cache["label-1"]
	assert.False(t, exists, "Oldest entry (label-1) should have been evicted")

	// Verify label-3 is still in cache (was accessed recently)
	_, exists = cache.cache["label-3"]
	assert.True(t, exists, "Recently accessed entry (label-3) should still be in cache")

	// Verify label-6 is in cache (just added)
	_, exists = cache.cache["label-6"]
	assert.True(t, exists, "Newly added entry (label-6) should be in cache")
	cache.mu.Unlock()
}

// TestLabelFilterCache_LRUOrdering tests that cache maintains proper LRU ordering
func TestLabelFilterCache_LRUOrdering(t *testing.T) {
	patterns := []string{"^app$", "^tier$", "^env$"}
	cacheSize := 3

	podMapper := &PodMapper{
		Config: &appconfig.Config{
			KubernetesPodLabelAllowlistRegex: patterns,
		},
		labelFilterCache: newLabelFilterCache(patterns, cacheSize),
	}

	cache := podMapper.labelFilterCache

	// Add entries in order: app, tier, env
	podMapper.shouldIncludeLabel("app")
	podMapper.shouldIncludeLabel("tier")
	podMapper.shouldIncludeLabel("env")

	// Cache should be at capacity with order (front to back): env, tier, app
	cache.mu.Lock()
	assert.Equal(t, cacheSize, len(cache.cache), "Cache should be at capacity")

	// Verify front is most recent (env)
	front := cache.lruList.Front()
	assert.Equal(t, "env", front.Value.(*labelCacheEntry).key, "Front should be most recent (env)")

	// Verify back is oldest (app)
	back := cache.lruList.Back()
	assert.Equal(t, "app", back.Value.(*labelCacheEntry).key, "Back should be oldest (app)")
	cache.mu.Unlock()

	// Access "app" to move it to front
	podMapper.shouldIncludeLabel("app")

	// Now order should be (front to back): app, env, tier
	cache.mu.Lock()
	front = cache.lruList.Front()
	assert.Equal(t, "app", front.Value.(*labelCacheEntry).key, "Front should now be app")

	back = cache.lruList.Back()
	assert.Equal(t, "tier", back.Value.(*labelCacheEntry).key, "Back should now be tier")
	cache.mu.Unlock()

	// Add new entry "version" - should evict "tier" (now oldest)
	podMapper.shouldIncludeLabel("version")

	cache.mu.Lock()
	_, tierExists := cache.cache["tier"]
	assert.False(t, tierExists, "tier should have been evicted")

	_, versionExists := cache.cache["version"]
	assert.True(t, versionExists, "version should be in cache")

	_, appExists := cache.cache["app"]
	assert.True(t, appExists, "app should still be in cache")

	_, envExists := cache.cache["env"]
	assert.True(t, envExists, "env should still be in cache")
	cache.mu.Unlock()
}

// TestLabelFilterCache_ConcurrentAccess tests that the cache is safe for concurrent use
func TestLabelFilterCache_ConcurrentAccess(t *testing.T) {
	patterns := []string{"^label-.*"}
	cacheSize := 100

	podMapper := &PodMapper{
		Config: &appconfig.Config{
			KubernetesPodLabelAllowlistRegex: patterns,
		},
		labelFilterCache: newLabelFilterCache(patterns, cacheSize),
	}

	// Launch multiple goroutines to access cache concurrently
	const numGoroutines = 10
	const numOperationsPerGoroutine = 100

	done := make(chan bool)

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			for i := 0; i < numOperationsPerGoroutine; i++ {
				labelKey := fmt.Sprintf("label-%d-%d", goroutineID, i)
				podMapper.shouldIncludeLabel(labelKey)
			}
			done <- true
		}(g)
	}

	// Wait for all goroutines to complete
	for g := 0; g < numGoroutines; g++ {
		<-done
	}

	// Verify cache is within size limits
	cache := podMapper.labelFilterCache
	cache.mu.Lock()
	assert.LessOrEqual(t, len(cache.cache), cacheSize, "Cache should not exceed max size")
	assert.LessOrEqual(t, cache.lruList.Len(), cacheSize, "LRU list should not exceed max size")
	assert.Equal(t, len(cache.cache), cache.lruList.Len(), "Cache map and list should be in sync")
	cache.mu.Unlock()
}
