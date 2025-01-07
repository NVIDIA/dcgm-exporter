/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestGetTransformations(t *testing.T) {
	tests := []struct {
		name   string
		config *appconfig.Config
		assert func(*testing.T, []Transform)
	}{
		{
			name: "The environment is not kubernetes",
			config: &appconfig.Config{
				Kubernetes: false,
			},
			assert: func(t *testing.T, transforms []Transform) {
				assert.Len(t, transforms, 0)
			},
		},
		{
			name: "The environment is kubernetes",
			config: &appconfig.Config{
				Kubernetes: true,
			},
			assert: func(t *testing.T, transforms []Transform) {
				assert.Len(t, transforms, 1)
			},
		},
		{
			name: "The environment is HPC cluster",
			config: &appconfig.Config{
				HPCJobMappingDir: "/var/run/nvidia/slurm",
			},
			assert: func(t *testing.T, transforms []Transform) {
				assert.Len(t, transforms, 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transformations := GetTransformations(tt.config)
			tt.assert(t, transformations)
		})
	}
}
