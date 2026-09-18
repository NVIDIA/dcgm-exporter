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

// Package dcgmprobe defines the private JSON contract shared by the DCGM e2e
// probe and its callers.
package dcgmprobe

const (
	// CurrentCacheCountsVersion is the cache-count report schema version.
	CurrentCacheCountsVersion = 2
)

// CacheCounts reports the number of cached samples for each requested field on
// each monitored DCGM entity. Entity keys are "<entity-group-id>:<entity-id>";
// field keys are decimal DCGM field IDs.
type CacheCounts struct {
	Version  int                       `json:"version"`
	Entities map[string]map[string]int `json:"entities"`
}
