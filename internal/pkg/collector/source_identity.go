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

package collector

import (
	"math"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
)

// sourceIdentity preserves entity scope and raw metadata for OTLP before Prometheus formatting.
// Callers pass the device snapshot that supplied the metric's GPU identity.
func sourceIdentity(mi devicemonitoring.Info, values []dcgm.FieldValue_v1, devices deviceinfo.Provider) *SourceIdentity {
	s := &SourceIdentity{EntityGroup: mi.Entity.EntityGroupId}
	if s.EntityGroup == dcgm.FE_LINK {
		s.NVLinkIndex = nativeIdentityIndex(mi.Entity.EntityId)
	}

	if s.EntityGroup == dcgm.FE_SWITCH || mi.ParentType == dcgm.FE_SWITCH {
		index := mi.Entity.EntityId
		if s.EntityGroup == dcgm.FE_LINK {
			index = mi.ParentId
		}
		s.NVSwitchIndex = nativeIdentityIndex(index)
		return s
	}

	switch s.EntityGroup {
	case dcgm.FE_GPU, dcgm.FE_GPU_I, dcgm.FE_GPU_CI:
	case dcgm.FE_LINK:
		if mi.ParentType != dcgm.FE_GPU {
			return s
		}
	default:
		return s
	}

	if devices != nil {
		s.SyntheticGPUUUID = devices.GPU(mi.DeviceInfo.GPU).SyntheticGPUUUID
	}
	s.GPUModelName = sourceString(mi.DeviceInfo.Identifiers.Model)
	s.GPUSerial = sourceString(mi.DeviceInfo.Identifiers.Serial)
	s.GPUVBIOS = sourceString(mi.DeviceInfo.Identifiers.Vbios)
	s.GPUDriverVersion = sourceString(mi.DeviceInfo.Identifiers.DriverVersion)

	// PCIe identity comes from physical-GPU reads, including the parent of an instance.
	if s.EntityGroup != dcgm.FE_GPU {
		return s
	}

	s.preserveGPUValues(values)
	return s
}

// preserveParentPCIe copies physical link attributes from the parent read into instance identity.
func (s *SourceIdentity) preserveParentPCIe(parent *SourceIdentity) {
	if parent == nil {
		return
	}

	s.PCIeMaxGeneration = parent.PCIeMaxGeneration
	s.PCIeMaxWidth = parent.PCIeMaxWidth
	s.PCIeGeneration = parent.PCIeGeneration
	s.PCIeWidth = parent.PCIeWidth
}

// preserveGPUValues enriches identity with available driver and PCIe fields from a whole-GPU read.
func (s *SourceIdentity) preserveGPUValues(values []dcgm.FieldValue_v1) {
	for _, value := range values {
		if value.Status != dcgm.DCGM_ST_OK {
			continue
		}
		if value.FieldID == dcgm.DCGM_FI_DRIVER_VERSION && value.FieldType == dcgm.DCGM_FT_STRING {
			if driver := sourceString(value.String()); driver != "" {
				s.GPUDriverVersion = driver
			}
		}
		if value.FieldType != dcgm.DCGM_FT_INT64 || isInt64Blank(value.Int64()) || value.Int64() < 0 {
			continue
		}
		n := value.Int64()
		switch value.FieldID {
		case dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_GEN:
			s.PCIeMaxGeneration = &n
		case dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH:
			s.PCIeMaxWidth = &n
		case dcgm.DCGM_FI_DEV_PCIE_LINK_GEN:
			s.PCIeGeneration = &n
		case dcgm.DCGM_FI_DEV_PCIE_LINK_WIDTH:
			s.PCIeWidth = &n
		}
	}
}

// nativeIdentityIndex preserves zero and omits native indexes that cannot fit an OTLP INT64 attribute.
func nativeIdentityIndex(value uint) *int64 {
	if value > math.MaxInt64 {
		return nil
	}
	index := int64(value)
	return &index
}

// sourceString omits DCGM string sentinels while retaining unsanitized identity values.
func sourceString(value string) string {
	if isStringBlank(value) {
		return ""
	}
	return value
}

// preservePeerIdentity uses the discovery snapshot, never another native read.
func preservePeerIdentity(source *SourceIdentity, device dcgm.Device, peer uint, devices []deviceinfo.GPUInfo) {
	if source == nil {
		return
	}
	for _, gpu := range devices {
		if gpu.DeviceInfo.GPU == peer && !gpu.SyntheticGPUUUID {
			source.PeerGPUUUID = gpu.DeviceInfo.UUID
			break
		}
	}
	if source.PeerGPUUUID == "" {
		return
	}
	for _, link := range device.Topology {
		if link.GPU == peer && link.Link > dcgm.P2PLinkUnknown && link.Link < dcgm.SingleNVLINKLink {
			source.PeerPCIPathType = link.Link.PCIPaths()
			break
		}
	}
}
