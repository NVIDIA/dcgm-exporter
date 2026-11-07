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

package appconfig

import (
	"fmt"
	"math"
	"strings"
)

// hpcJobsKeyBase is the key prefix of the HPC job source settings.
const hpcJobsKeyBase = "sources.hpc_jobs."

// hpcJobMappingLimit is one HPC mapping file limit with the names that set it.
type hpcJobMappingLimit struct {
	field string
	flag  string
	key   string
	value *int64
}

// hpcJobMappingLimits lists the HPC mapping file limits of config.
func hpcJobMappingLimits(config *Config) []hpcJobMappingLimit {
	return []hpcJobMappingLimit{
		{"HPCJobMappingMaxFileBytes", FlagHPCJobMappingMaxFileBytes, hpcJobsKeyBase + "max_file_bytes", &config.HPCJobMappingMaxFileBytes},
		{"HPCJobMappingMaxJobIDBytes", FlagHPCJobMappingMaxJobIDBytes, hpcJobsKeyBase + "max_job_id_bytes", &config.HPCJobMappingMaxJobIDBytes},
		{"HPCJobMappingMaxJobsPerFile", FlagHPCJobMappingMaxJobsPerFile, hpcJobsKeyBase + "max_jobs_per_file", &config.HPCJobMappingMaxJobsPerFile},
	}
}

// validHPCJobMappingLimit says a limit is positive and leaves room for the one
// extra byte a reader takes to detect a file that is too large.
func validHPCJobMappingLimit(value int64) bool {
	return value > 0 && value < math.MaxInt64
}

// hpcJobMappingLimitRule is the rule every HPC mapping limit must meet.
var hpcJobMappingLimitRule = fmt.Sprintf("must be greater than 0 and less than %d", int64(math.MaxInt64))

// apply applies the HPC job source settings. Limits are checked even when
// mapping is off, so a saved config always loads back.
func (h *YAMLHPCJobsSource) apply(config *Config, rec originRecorder) error {
	if h == nil {
		return nil
	}

	if dir := strings.TrimSpace(h.MappingDir); dir != "" {
		config.HPCJobMappingDir = dir
		rec.record("HPCJobMappingDir", hpcJobsKeyBase+"mapping_dir")
	}

	// Values are matched to limits by key, so the order of either list does not matter.
	values := map[string]*int64{
		hpcJobsKeyBase + "max_file_bytes":    h.MaxFileBytes,
		hpcJobsKeyBase + "max_job_id_bytes":  h.MaxJobIDBytes,
		hpcJobsKeyBase + "max_jobs_per_file": h.MaxJobsPerFile,
	}

	for _, limit := range hpcJobMappingLimits(config) {
		value := values[limit.key]
		if value == nil {
			continue
		}

		if !validHPCJobMappingLimit(*value) {
			return keyErrorf(limit.key, "%s", hpcJobMappingLimitRule)
		}

		*limit.value = *value
		rec.record(limit.field, limit.key)
	}

	return nil
}

// validateHPCJobMappingLimits checks the HPC mapping file limits whether or
// not mapping is on. Zero does not mean unlimited.
func validateHPCJobMappingLimits(config *Config) []error {
	var errs []error

	for _, limit := range hpcJobMappingLimits(config) {
		if !validHPCJobMappingLimit(*limit.value) {
			errs = append(errs, &SettingError{
				Field: limit.field,
				Err:   fmt.Errorf("invalid %s parameter value: %d, %s", limit.flag, *limit.value, hpcJobMappingLimitRule),
			})
		}
	}

	return errs
}

// ValidateHPCJobMappingLimits reports the first HPC mapping file limit that
// is not usable, so a reader can refuse a config that skipped validation.
func ValidateHPCJobMappingLimits(config *Config) error {
	if errs := validateHPCJobMappingLimits(config); len(errs) > 0 {
		return errs[0]
	}

	return nil
}
