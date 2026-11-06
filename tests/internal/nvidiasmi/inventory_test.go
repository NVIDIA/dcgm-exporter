/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package nvidiasmi_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// countingRunner returns a runner that counts calls and answers with the next
// result. It is safe for concurrent use.
func countingRunner(calls *atomic.Int64, results ...func() ([]byte, error)) nvidiasmi.Runner {
	return func(context.Context, ...string) ([]byte, error) {
		call := calls.Add(1) - 1
		return results[min(int(call), len(results)-1)]()
	}
}

// ok returns a result function that succeeds with output.
func ok(output string) func() ([]byte, error) {
	return func() ([]byte, error) { return []byte(output), nil }
}

// TestInventoryIdentityIsReadOnce runs nvidia-smi for the first call only.
func TestInventoryIdentityIsReadOnce(t *testing.T) {
	var calls atomic.Int64
	inventory := nvidiasmi.NewInventory(countingRunner(&calls, ok("0, GPU-aaa, B200, 0:1:0.0, 1, Disabled\n")))

	for range 3 {
		gpus, err := inventory.Identity(context.Background())
		if err != nil || len(gpus) != 1 || gpus[0].UUID != "GPU-aaa" {
			t.Fatalf("Identity() = %+v, %v", gpus, err)
		}
	}

	if calls.Load() != 1 {
		t.Fatalf("nvidia-smi ran %d times, want 1", calls.Load())
	}
}

// TestInventoryIdentityRetriesAfterAFailedRead does not save a failed read.
func TestInventoryIdentityRetriesAfterAFailedRead(t *testing.T) {
	var calls atomic.Int64
	failing := func() ([]byte, error) { return nil, errors.New("driver not loaded") }
	inventory := nvidiasmi.NewInventory(countingRunner(&calls, failing, ok("0, GPU-aaa, B200, 0:1:0.0, 1, Disabled\n")))

	if _, err := inventory.Identity(context.Background()); err == nil {
		t.Fatal("Identity() should fail when nvidia-smi fails")
	}
	gpus, err := inventory.Identity(context.Background())

	if err != nil || len(gpus) != 1 {
		t.Fatalf("Identity() after a failure = %+v, %v", gpus, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("nvidia-smi ran %d times, want 2", calls.Load())
	}
}

// TestInventoryIdentityIsSafeForConcurrentUse reads once for many callers.
func TestInventoryIdentityIsSafeForConcurrentUse(t *testing.T) {
	var calls atomic.Int64
	inventory := nvidiasmi.NewInventory(countingRunner(&calls, ok("0, GPU-aaa, B200, 0:1:0.0, 1, Disabled\n")))

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := inventory.Identity(context.Background()); err != nil {
				t.Errorf("Identity() error = %v", err)
			}
		}()
	}
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("nvidia-smi ran %d times, want 1", calls.Load())
	}
}

// TestInventoryReadingsAreNeverSaved runs nvidia-smi for every call.
func TestInventoryReadingsAreNeverSaved(t *testing.T) {
	var calls atomic.Int64
	inventory := nvidiasmi.NewInventory(countingRunner(&calls, ok("GPU-aaa, 31\n")))

	for range 3 {
		if _, err := inventory.Readings(context.Background(), []string{"temperature.gpu"}); err != nil {
			t.Fatalf("Readings() error = %v", err)
		}
	}

	if calls.Load() != 3 {
		t.Fatalf("nvidia-smi ran %d times, want 3", calls.Load())
	}
}
