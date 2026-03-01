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

package server

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
)

func TestMetricsServer_ClearRegistry(t *testing.T) {
	t.Run("clears registry and returns old", func(t *testing.T) {
		server := &MetricsServer{}

		registry1 := registry.NewRegistry()
		server.registry.Store(registry1)

		old := server.ClearRegistry()

		assert.Equal(t, registry1, old)
		assert.NotNil(t, server.GetRegistry()) // GetRegistry returns empty registry if stored value is nil (fallback)
	})

	t.Run("clearing already nil registry returns nil", func(t *testing.T) {
		server := &MetricsServer{}
		server.registry.Store(nil)

		old := server.ClearRegistry()

		assert.Nil(t, old)
		assert.NotNil(t, server.GetRegistry()) // GetRegistry returns empty registry fallback
	})
}

func TestMetricsServer_SetRegistry(t *testing.T) {
	t.Run("sets new registry", func(t *testing.T) {
		server := &MetricsServer{}

		registry1 := registry.NewRegistry()
		server.SetRegistry(registry1)

		assert.Equal(t, registry1, server.GetRegistry())
	})

	t.Run("overwrites existing registry", func(t *testing.T) {
		server := &MetricsServer{}
		server.registry.Store(registry.NewRegistry())

		registry2 := registry.NewRegistry()
		server.SetRegistry(registry2)

		assert.Equal(t, registry2, server.GetRegistry())
	})

	t.Run("multiple sets work correctly", func(t *testing.T) {
		server := &MetricsServer{}

		for i := 1; i <= 5; i++ {
			newReg := registry.NewRegistry()
			server.SetRegistry(newReg)
			assert.Equal(t, newReg, server.GetRegistry())
		}
	})
}

func TestMetricsServer_GetRegistry(t *testing.T) {
	t.Run("returns stored registry", func(t *testing.T) {
		server := &MetricsServer{}
		reg := registry.NewRegistry()
		server.registry.Store(reg)

		got := server.GetRegistry()
		assert.Equal(t, reg, got)
	})

	t.Run("handles nil registry gracefully", func(t *testing.T) {
		server := &MetricsServer{}
		// Don't store anything, registry is nil

		got := server.GetRegistry()
		require.NotNil(t, got, "should return empty registry instead of nil")
	})
}

func TestMetricsServer_ReloadInProgress(t *testing.T) {
	t.Run("default is false", func(t *testing.T) {
		server := &MetricsServer{}
		assert.False(t, server.IsReloadInProgress())
	})

	t.Run("can be set and retrieved", func(t *testing.T) {
		server := &MetricsServer{}

		server.SetReloadInProgress(true)
		assert.True(t, server.IsReloadInProgress())

		server.SetReloadInProgress(false)
		assert.False(t, server.IsReloadInProgress())
	})
}

func TestMetricsServer_ConcurrentSwap(t *testing.T) {
	t.Run("concurrent reads during swap", func(t *testing.T) {
		server := &MetricsServer{}
		server.registry.Store(registry.NewRegistry())

		var wg sync.WaitGroup
		errors := make(chan error, 100)

		// Start 50 concurrent readers
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					reg := server.GetRegistry()
					if reg == nil {
						errors <- fmt.Errorf("got nil registry")
						return
					}
				}
			}()
		}

		// Perform sets while reading
		for i := 0; i < 10; i++ {
			time.Sleep(10 * time.Millisecond)
			server.SetRegistry(registry.NewRegistry())
		}

		wg.Wait()
		close(errors)

		for err := range errors {
			t.Fatal(err)
		}
	})

	t.Run("concurrent sets are safe", func(t *testing.T) {
		server := &MetricsServer{}
		server.registry.Store(registry.NewRegistry())

		var wg sync.WaitGroup
		setCount := 20

		for i := 0; i < setCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				server.SetRegistry(registry.NewRegistry())
			}()
		}

		wg.Wait()

		// Verify we have a registry and it's not nil
		assert.NotNil(t, server.GetRegistry())
	})

	t.Run("concurrent clear and set", func(t *testing.T) {
		server := &MetricsServer{}
		server.registry.Store(registry.NewRegistry())

		var wg sync.WaitGroup
		operations := 20

		for i := 0; i < operations; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				server.ClearRegistry()
			}()
			go func() {
				defer wg.Done()
				server.SetRegistry(registry.NewRegistry())
			}()
		}

		wg.Wait()

		// Either nil (cleared) or has a registry - both are valid
		// GetRegistry returns empty fallback if nil
		assert.NotNil(t, server.GetRegistry())
	})
}

func BenchmarkRegistrySet(b *testing.B) {
	server := &MetricsServer{}
	server.registry.Store(registry.NewRegistry())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		server.SetRegistry(registry.NewRegistry())
	}
}

func BenchmarkConcurrentRegistryRead(b *testing.B) {
	server := &MetricsServer{}
	server.registry.Store(registry.NewRegistry())

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = server.GetRegistry()
		}
	})
}
