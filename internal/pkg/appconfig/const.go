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

package appconfig

import "time"

const (
	GPUUID     KubernetesGPUIDType = "uid"
	DeviceName KubernetesGPUIDType = "device-name"

	NvidiaResourceName      = "nvidia.com/gpu"
	NvidiaMigResourcePrefix = "nvidia.com/mig-"
	MIG_UUID_PREFIX         = "MIG-"
)

// --------------------------------------------------------------------------
// Feature 001-multi-user-gpu-util: runtime-derived constants (task T006).
// These are intentionally NOT exposed through config.yaml — the surface of
// the YAML file is intentionally limited to `labels:` and `server:` (see
// specs/001-multi-user-gpu-util/contracts/config.yaml.schema.md).
// --------------------------------------------------------------------------
const (
	// UserNameCacheTTL is the TTL for cached UID->username resolutions.
	UserNameCacheTTL = 300 * time.Second

	// EnvValueMaxLen is the maximum byte length of a sanitized env label value
	// before truncation (FR-004).
	EnvValueMaxLen = 64

	// MaxEnvCardinalityPerCycle is the hard cap on the number of distinct
	// values a single env label may take within one collection cycle
	// (Clarification Q5). Exceeding groups are merged into the fallback value
	// FallbackOther.
	MaxEnvCardinalityPerCycle = 128

	// Fallback label values, used whenever the respective source is missing
	// or invalid.
	FallbackNone    = "none"    // EnvLabel missing / unreadable (FR-004).
	FallbackUnknown = "unknown" // StaticLabel with empty Value and no matching env var (FR-003).
	FallbackOther   = "other"   // Env value evicted by cardinality cap (Q5).

	// DefaultConfigPath is the path consulted when --config is not passed.
	// A missing file here causes startup to fail hard (FR-011).
	DefaultConfigPath = "/etc/dcgm-exporter/config.yaml"

	// DefaultServerPort, DefaultServerTimeout, DefaultServerReadTimeout,
	// DefaultServerWriteTimeout are applied by ApplyDefaults() when the YAML
	// omits them.
	DefaultServerPort         = ":9400"
	DefaultServerTimeout      = 10 * time.Second
	DefaultServerReadTimeout  = 5 * time.Second
	DefaultServerWriteTimeout = 10 * time.Second
)

// SystemReservedLabelNames lists Prometheus label names already owned by
// dcgm-exporter or injected by upstream transformers. Users MUST NOT redeclare
// any of these as custom labels in config.yaml (FR-010 b / R8).
//
// Exposed as a function, not a var, to avoid accidental mutation at runtime.
func SystemReservedLabelNames() map[string]struct{} {
	return map[string]struct{}{
		"USER":               {}, // injected by the bare-metal user mapper
		"gpu":                {},
		"UUID":               {},
		"device":             {},
		"modelName":          {},
		"Hostname":           {},
		"container":          {},
		"namespace":          {},
		"pod":                {},
		"exported_container": {},
		"exported_namespace": {},
		"exported_pod":       {},
		"exported_job":       {},
	}
}
