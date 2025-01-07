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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// GetTransformations return list of transformation applicable for metrics
func GetTransformations(c *appconfig.Config) []Transform {
	var transformations []Transform
	if c.Kubernetes {
		podMapper := NewPodMapper(c)
		transformations = append(transformations, podMapper)
	}

	if c.HPCJobMappingDir != "" {
		hpcMapper := newHPCMapper(c)
		transformations = append(transformations, hpcMapper)
	}

	return transformations
}
