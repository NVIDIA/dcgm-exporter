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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHPCJobsSourceAppliesLimits checks the file limits replace the defaults
// and record the key that set them, while a limit left out keeps its default.
func TestHPCJobsSourceAppliesLimits(t *testing.T) {
	y, err := ParseYAMLConfig([]byte("version: 2\nsources:\n  hpc_jobs: {mapping_dir: /jobs, max_file_bytes: 100, max_jobs_per_file: 3}\n" + v2Outputs))
	require.NoError(t, err)

	config := DefaultConfig()
	require.NoError(t, y.ApplyTo(config))

	assert.Equal(t, "/jobs", config.HPCJobMappingDir)
	assert.EqualValues(t, 100, config.HPCJobMappingMaxFileBytes)
	assert.Equal(t, DefaultHPCJobMappingMaxJobIDBytes, config.HPCJobMappingMaxJobIDBytes)
	assert.EqualValues(t, 3, config.HPCJobMappingMaxJobsPerFile)

	origins := y.SettingOrigins()
	assert.Equal(t, "sources.hpc_jobs.max_file_bytes", origins["HPCJobMappingMaxFileBytes"])
	assert.Equal(t, "sources.hpc_jobs.max_jobs_per_file", origins["HPCJobMappingMaxJobsPerFile"])
	assert.NotContains(t, origins, "HPCJobMappingMaxJobIDBytes")
}

// TestHPCJobsSourceRejectsUnusableLimits checks every limit must be positive
// and leave room for the extra byte a reader takes, even with mapping off.
func TestHPCJobsSourceRejectsUnusableLimits(t *testing.T) {
	for _, key := range []string{"max_file_bytes", "max_job_id_bytes", "max_jobs_per_file"} {
		for _, value := range []string{"0", "-1", "9223372036854775807"} {
			t.Run(key+"="+value, func(t *testing.T) {
				y, err := ParseYAMLConfig([]byte("version: 2\nsources:\n  hpc_jobs: {" + key + ": " + value + "}\n" + v2Outputs))
				if err == nil {
					err = y.ApplyTo(DefaultConfig())
				}

				require.ErrorContains(t, err, "sources.hpc_jobs."+key)
				require.ErrorContains(t, err, "must be greater than 0")
			})
		}
	}
}

// TestValidateHPCJobMappingLimits checks the reader's guard accepts the
// defaults and refuses a config that skipped validation.
func TestValidateHPCJobMappingLimits(t *testing.T) {
	require.NoError(t, ValidateHPCJobMappingLimits(DefaultConfig()))
	require.ErrorContains(t, ValidateHPCJobMappingLimits(&Config{}), "hpc-job-mapping-max-file-bytes")
}
