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

package k8s

import (
	"strconv"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

// TestRetentionCacheCountsError accepts bounded counts and rejects malformed or indistinct reports.
func TestRetentionCacheCountsError(t *testing.T) {
	valid := func() dcgmprobe.CacheCounts {
		return dcgmprobe.CacheCounts{
			Version: dcgmprobe.CurrentCacheCountsVersion,
			Entities: map[string]map[string]int{
				"1:0": {
					strconv.Itoa(retentionOverrideFieldID): 2,
					strconv.Itoa(retentionGlobalFieldID):   4,
				},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*dcgmprobe.CacheCounts)
		valid  bool
	}{
		{name: "valid", valid: true},
		{name: "valid with DCGM scheduling slack", mutate: func(report *dcgmprobe.CacheCounts) {
			report.Entities["1:0"][strconv.Itoa(retentionOverrideFieldID)] = retentionOverrideCacheMax
			report.Entities["1:0"][strconv.Itoa(retentionGlobalFieldID)] = retentionGlobalCacheMax
		}, valid: true},
		{name: "wrong version", mutate: func(report *dcgmprobe.CacheCounts) { report.Version++ }},
		{name: "no entities", mutate: func(report *dcgmprobe.CacheCounts) { report.Entities = nil }},
		{name: "override empty", mutate: func(report *dcgmprobe.CacheCounts) {
			report.Entities["1:0"][strconv.Itoa(retentionOverrideFieldID)] = 0
		}},
		{name: "override over bound", mutate: func(report *dcgmprobe.CacheCounts) {
			report.Entities["1:0"][strconv.Itoa(retentionOverrideFieldID)] = retentionOverrideCacheMax + 1
		}},
		{name: "global not ready", mutate: func(report *dcgmprobe.CacheCounts) {
			report.Entities["1:0"][strconv.Itoa(retentionGlobalFieldID)] = retentionGlobalMaxSamples - 1
		}},
		{name: "global over bound", mutate: func(report *dcgmprobe.CacheCounts) {
			report.Entities["1:0"][strconv.Itoa(retentionGlobalFieldID)] = retentionGlobalCacheMax + 1
		}},
		{name: "bounds not distinguished", mutate: func(report *dcgmprobe.CacheCounts) {
			report.Entities["1:0"][strconv.Itoa(retentionOverrideFieldID)] = retentionOverrideCacheMax
			report.Entities["1:0"][strconv.Itoa(retentionGlobalFieldID)] = retentionOverrideCacheMax
		}},
		{name: "missing field", mutate: func(report *dcgmprobe.CacheCounts) {
			delete(report.Entities["1:0"], strconv.Itoa(retentionGlobalFieldID))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := valid()
			if test.mutate != nil {
				test.mutate(&report)
			}
			err := retentionCacheCountsError(report)
			if test.valid && err != nil {
				t.Fatalf("retentionCacheCountsError() error = %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("retentionCacheCountsError() error = nil")
			}
		})
	}
}
