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

package scenario

// MIGLayout names the host MIG layout a scenario declares in Scenario.MIGLayout.
// The CLI applies it before the scenario's suite runs and restores MIG
// afterward.
type MIGLayout string

const (
	// MIGLayoutNone leaves MIG as the host has it.
	MIGLayoutNone MIGLayout = ""
	// MIGLayoutTwoInstancesPerGPU puts every GPU in MIG mode with two GPU
	// instances, each with a compute instance, of the largest profile that
	// fits twice on the GPU.
	MIGLayoutTwoInstancesPerGPU MIGLayout = "two-instances-per-gpu"
)

// MIGLayoutEnv is the environment variable through which a test learns the
// MIG layout the CLI applied for it.
const MIGLayoutEnv = "E2E_MIG_LAYOUT"
