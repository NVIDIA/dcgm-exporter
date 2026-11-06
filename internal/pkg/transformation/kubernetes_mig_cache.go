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

package transformation

import (
	"sync"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
)

// migLookupMaxAge is how long a successful MIG lookup is reused before NVML is
// asked again. A MIG UUID names one compute instance for its whole life, but
// kubelet can keep reporting a UUID after its instance was destroyed, and a new
// instance may take the same GPU instance ID. Re-checking bounds how long such
// a stale answer can attach a pod to the new instance's metrics.
const migLookupMaxAge = 5 * time.Minute

// migLookupCache remembers which parent GPU and instances each MIG UUID names,
// so a scrape does not repeat the NVML lookups for devices kubelet reported
// before. A successful answer is reused for maxAge; a failed lookup is retried
// once retryAfter has passed.
//
// Entries for UUIDs that kubelet no longer reports are dropped by
// retainUsed, so the cache never holds more than the current allocations.
type migLookupCache struct {
	mu         sync.Mutex
	entries    map[string]*migLookupEntry
	maxAge     time.Duration
	retryAfter time.Duration
	lookupFunc func(string) (*nvmlprovider.MIGDeviceInfo, error)
	now        func() time.Time
}

// migLookupEntry is one NVML answer, when it was asked, and whether the latest
// kubelet snapshot used it.
type migLookupEntry struct {
	info      *nvmlprovider.MIGDeviceInfo
	err       error
	checkedAt time.Time
	used      bool
}

// newMIGLookupCache returns a cache backed by the process NVML client.
func newMIGLookupCache(retryAfter time.Duration) *migLookupCache {
	return &migLookupCache{
		entries:    make(map[string]*migLookupEntry),
		maxAge:     migLookupMaxAge,
		retryAfter: retryAfter,
		lookupFunc: func(uuid string) (*nvmlprovider.MIGDeviceInfo, error) {
			return nvmlprovider.Client().GetMIGDeviceInfoByID(uuid)
		},
		now: time.Now,
	}
}

// lookup returns the MIG device for uuid, asking NVML only when there is no
// answer yet, a successful answer is older than maxAge, or a failed answer is
// older than retryAfter. A nil cache asks NVML every time.
func (c *migLookupCache) lookup(uuid string) (*nvmlprovider.MIGDeviceInfo, error) {
	if c == nil {
		return nvmlprovider.Client().GetMIGDeviceInfoByID(uuid)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if entry, found := c.entries[uuid]; found && now.Sub(entry.checkedAt) < c.reuseFor(entry) {
		entry.used = true
		return entry.info, entry.err
	}

	info, err := c.lookupFunc(uuid)
	c.entries[uuid] = &migLookupEntry{info: info, err: err, checkedAt: now, used: true}

	return info, err
}

// reuseFor returns how long entry's answer may be reused.
func (c *migLookupCache) reuseFor(entry *migLookupEntry) time.Duration {
	if entry.err != nil {
		return c.retryAfter
	}
	return c.maxAge
}

// retainUsed drops entries that the latest kubelet snapshot did not look up
// and starts tracking use for the next snapshot.
func (c *migLookupCache) retainUsed() {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for uuid, entry := range c.entries {
		if !entry.used {
			delete(c.entries, uuid)
			continue
		}
		entry.used = false
	}
}
