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
	"fmt"
	"strconv"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

const (
	retentionGlobalFieldID      = 1603
	retentionOverrideFieldID    = 150
	retentionGlobalMaxSamples   = 4
	retentionOverrideMaxSamples = 2
	retentionGlobalCacheMax     = retentionGlobalMaxSamples + 1
	retentionOverrideCacheMax   = retentionOverrideMaxSamples + 1
)

// retentionCacheCountsError checks both fields against their stable sample-count bounds.
func retentionCacheCountsError(report dcgmprobe.CacheCounts) error {
	if report.Version != dcgmprobe.CurrentCacheCountsVersion {
		return fmt.Errorf(
			"cache-count report version = %d, want %d",
			report.Version,
			dcgmprobe.CurrentCacheCountsVersion,
		)
	}
	if len(report.Entities) == 0 {
		return fmt.Errorf("cache-count report contains no monitored entities")
	}
	overrideField := strconv.Itoa(retentionOverrideFieldID)
	globalField := strconv.Itoa(retentionGlobalFieldID)
	for entity, fields := range report.Entities {
		overrideCount, ok := fields[overrideField]
		if !ok {
			return fmt.Errorf("entity %s is missing field %s", entity, overrideField)
		}
		globalCount, ok := fields[globalField]
		if !ok {
			return fmt.Errorf("entity %s is missing field %s", entity, globalField)
		}
		if overrideCount < retentionOverrideMaxSamples || overrideCount > retentionOverrideCacheMax {
			return fmt.Errorf(
				"entity %s field %s cached %d samples, want %d-%d for maxSamples=%d",
				entity,
				overrideField,
				overrideCount,
				retentionOverrideMaxSamples,
				retentionOverrideCacheMax,
				retentionOverrideMaxSamples,
			)
		}
		if globalCount < retentionGlobalMaxSamples || globalCount > retentionGlobalCacheMax {
			return fmt.Errorf(
				"entity %s field %s cached %d samples, want %d-%d for maxSamples=%d",
				entity,
				globalField,
				globalCount,
				retentionGlobalMaxSamples,
				retentionGlobalCacheMax,
				retentionGlobalMaxSamples,
			)
		}
		if globalCount <= overrideCount {
			return fmt.Errorf(
				"entity %s global field %s cached %d samples, want more than overridden field %s count %d",
				entity,
				globalField,
				globalCount,
				overrideField,
				overrideCount,
			)
		}
	}
	return nil
}
