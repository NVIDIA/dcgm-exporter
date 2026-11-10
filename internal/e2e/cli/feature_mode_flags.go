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

package cli

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// featureModeFlags holds the winning flag or environment text of each mode
// flag until applyFeatureModeFlags parses it.
type featureModeFlags struct {
	k3dCgroupV1        string
	sharedGPUConfigure string
	draConfigure       string
	gpuOperator        string
}

// applyFeatureModeFlags parses each mode flag once into its typed field. An
// empty value, such as a mode variable set to nothing, keeps the mode already in
// opts, which is still checked, so an invalid environment value is reported when
// the flag is blank. An error names the flag, whether the value came from the
// flag or its environment variable.
func applyFeatureModeFlags(opts *config.TestOptions, modes featureModeFlags) error {
	for _, flag := range []struct {
		name  string
		value string
		mode  *config.FeatureMode
	}{
		{name: "--k3d-cgroup-v1", value: modes.k3dCgroupV1, mode: &opts.K3dCgroupV1},
		{name: "--shared-gpu-configure", value: modes.sharedGPUConfigure, mode: &opts.SharedGPUConfigure},
		{name: "--dra-configure", value: modes.draConfigure, mode: &opts.DRAConfigure},
	} {
		value := flag.value
		if strings.TrimSpace(value) == "" {
			value = string(*flag.mode)
		}
		if strings.TrimSpace(value) == "" {
			continue
		}

		mode, err := config.ParseFeatureMode(value)
		if err != nil {
			return fmt.Errorf("%s %w", flag.name, err)
		}
		*flag.mode = mode
	}

	value := modes.gpuOperator
	if strings.TrimSpace(value) == "" {
		value = string(opts.GPUOperator)
	}
	if strings.TrimSpace(value) == "" {
		return nil
	}

	gpuOperator, err := config.ParseGPUOperatorMode(value)
	if err != nil {
		return fmt.Errorf("--gpu-operator %w", err)
	}
	opts.GPUOperator = gpuOperator

	return nil
}
