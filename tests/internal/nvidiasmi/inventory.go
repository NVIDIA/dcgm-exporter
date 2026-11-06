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
package nvidiasmi

import (
	"context"
	"slices"
	"sync"
)

// Inventory reads GPU data through one Runner. It is safe for concurrent use.
type Inventory struct {
	run Runner

	mu       sync.Mutex
	identity []GPU
}

// NewInventory returns an Inventory that runs nvidia-smi through run. Create
// one per place nvidia-smi runs and share it for the whole test process.
func NewInventory(run Runner) *Inventory {
	return &Inventory{run: run}
}

// Identity returns the GPU identities. The first successful read runs
// nvidia-smi and every later call returns the saved result. A failed read is
// not saved, so the next call tries again.
func (i *Inventory) Identity(ctx context.Context) ([]GPU, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.identity == nil {
		gpus, err := QueryGPUIdentity(ctx, i.run)
		if err != nil {
			return nil, err
		}
		i.identity = gpus
	}

	return slices.Clone(i.identity), nil
}

// Readings runs nvidia-smi once for the named query fields. It never saves the
// result, because readings go stale.
func (i *Inventory) Readings(ctx context.Context, fields []string) (Readings, error) {
	return QueryGPUReadings(ctx, i.run, fields)
}
