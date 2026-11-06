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

package transformation

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

// deviceMappingKeys returns every metric identifier that can name one kubelet
// device ID. Dedicated and shared allocation modes use the same aliases, but
// store their pod information differently. It never returns an empty key,
// which would match every metric without a GPU identity.
func (p *PodMapper) deviceMappingKeys(deviceID string, deviceInfo deviceinfo.Provider) []string {
	return slices.DeleteFunc(p.deviceIDAliases(deviceID, deviceInfo), func(key string) bool { return key == "" })
}

// deviceIDAliases returns the metric identifiers for one kubelet device ID,
// before empty ones are dropped.
func (p *PodMapper) deviceIDAliases(deviceID string, deviceInfo deviceinfo.Provider) []string {
	if strings.HasPrefix(deviceID, appconfig.MIG_UUID_PREFIX) {
		return append(p.migDeviceMappingKeys(deviceID, deviceInfo), deviceID)
	}
	if matches := gkeMigDeviceIDRegex.FindStringSubmatch(deviceID); matches != nil {
		return []string{fmt.Sprintf("%s-%s", matches[1], matches[2]), deviceID}
	}
	if gpuID, _, found := strings.Cut(deviceID, gkeVirtualGPUDeviceIDSeparator); found {
		return []string{gpuID, deviceID}
	}
	if gpuInstanceID, _, found := strings.Cut(deviceID, "::"); found {
		return []string{gpuInstanceID, deviceID}
	}
	return []string{deviceID}
}

// migDeviceMappingKeys resolves the GPU and GPU-instance identifiers for a
// MIG UUID. A failed NVML lookup still returns the UUID without its MIG- prefix,
// matching the existing best-effort attribution behavior. The GPU-instance
// key is left out when the parent GPU is not in deviceInfo, as when a non-GPU
// entity group is enriched: an empty key would match every metric without a
// GPU UUID, such as CPU and NVSwitch metrics.
func (p *PodMapper) migDeviceMappingKeys(deviceID string, deviceInfo deviceinfo.Provider) []string {
	migUUID := stripVGPUSuffix(deviceID)
	keys := []string{migUUID[len(appconfig.MIG_UUID_PREFIX):]}

	migDevice, err := p.migLookups.lookup(migUUID)
	if err != nil {
		slog.Debug("Failed to get MIG device info", "device_id", deviceID, "error", err)
		return keys
	}
	if migDevice.GPUInstanceID < 0 {
		return keys
	}
	instanceID := deviceinfo.GetGPUInstanceIdentifier(
		deviceInfo,
		migDevice.ParentUUID,
		uint(migDevice.GPUInstanceID), //nolint:gosec // Negative values return above.
	)
	if instanceID == "" {
		return keys
	}

	return []string{instanceID, keys[0]}
}
