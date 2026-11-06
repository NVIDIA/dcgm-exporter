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

// Package otlp converts collected DCGM metrics to the OpenTelemetry Protocol (OTLP).
// The signal catalog defines metric names, units, and fixed attributes.
package otlp

import (
	"cmp"
	"errors"
	"fmt"
	"strconv"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
)

var errIncompleteHardwareIdentity = errors.New("incomplete hardware identity")

// ResourceIdentity is resolved once per output generation by its runtime.
// Service defaults/overrides and process instance identity belong to that caller.
type ResourceIdentity struct {
	ServiceName, ServiceVersion, ServiceInstanceID string
	Hostname, KubernetesNodeName                   string
}

// BuildResourceAttributes creates resource identity without discovery or defaults.
func BuildResourceAttributes(identity ResourceIdentity) ([]attribute.KeyValue, error) {
	if identity.ServiceName == "" {
		return nil, errors.New("service.name is required")
	}
	if identity.ServiceInstanceID == "" {
		return nil, errors.New("service.instance.id is required")
	}
	a := attributes{values: make([]attribute.KeyValue, 0, 5)}
	a.addString("service.name", identity.ServiceName)
	a.addString("service.version", identity.ServiceVersion)
	a.addString("service.instance.id", identity.ServiceInstanceID)
	a.addString("host.name", identity.Hostname)
	a.addString("k8s.node.name", identity.KubernetesNodeName)
	return a.result()
}

// BuildDataPointAttributes identifies the hardware and workload for one point.
// Source.EntityGroup determines the hardware type when Source is present.
// Without Source, only exporter samples in FE_NONE are supported.
// The caller supplies one container identity for each workload point.
func BuildDataPointAttributes(group dcgm.Field_Entity_Group, metric collector.Metric, container *collector.ContainerIdentity) ([]attribute.KeyValue, error) {
	set, err := buildDataPointAttributeSet(group, metric, container)
	if err != nil {
		return nil, err
	}
	return set.ToSlice(), nil
}

// buildDataPointAttributeSet gives the converter the attributes without a slice copy.
func buildDataPointAttributeSet(group dcgm.Field_Entity_Group, metric collector.Metric, container *collector.ContainerIdentity) (attribute.Set, error) {
	s := metric.Source
	capacity := 16
	if s != nil {
		capacity += len(s.PodLabels)
	}
	a := attributes{values: make([]attribute.KeyValue, 0, capacity)}

	if s == nil && group != dcgm.FE_NONE {
		return attribute.Set{}, fmt.Errorf("%w: hardware source identity is required", errIncompleteHardwareIdentity)
	}

	if s != nil {
		if err := a.addHardware(group, metric); err != nil {
			return attribute.Set{}, err
		}

		for key, value := range s.PodLabels {
			a.addString("k8s.pod.label."+key, value)
		}
		a.addString("com.nvidia.k8s.device_plugin.resource.name", s.DevicePluginResourceName)
	}

	a.addWorkload(metric, container)
	return a.resultSet()
}

func (a *attributes) addWorkload(metric collector.Metric, container *collector.ContainerIdentity) {
	pod := cmp.Or(metric.Attributes["pod"], metric.Attributes["pod_name"])
	namespace := cmp.Or(metric.Attributes["namespace"], metric.Attributes["pod_namespace"])
	if pod != "" || namespace != "" {
		containerName := cmp.Or(metric.Attributes["container"], metric.Attributes["container_name"])
		if containerName == "" && container != nil {
			containerName = container.Name
		}
		a.addString("k8s.pod.name", pod)
		a.addString("k8s.namespace.name", namespace)
		a.addString("k8s.container.name", containerName)
		if container != nil {
			a.addString("container.id", container.ID)
		}
		a.addString("k8s.pod.uid", metric.Attributes["pod_uid"])
	} else {
		if container != nil {
			a.addString("container.name", container.Name)
			a.addString("container.id", container.ID)
		}
		a.addString("com.nvidia.dcgm.exporter.container.label", metric.Attributes["container"])
	}

	a.addString("com.nvidia.gpu.sharing.id", metric.Attributes["vgpu"])
	a.addString("com.nvidia.hpc.job.id", metric.Attributes["hpc_job"])

	for _, mapping := range [][2]string{
		{"dra_claim_name", "com.nvidia.k8s.dra.claim.name"},
		{"dra_claim_namespace", "com.nvidia.k8s.dra.claim.namespace.name"},
		{"dra_driver_name", "com.nvidia.k8s.dra.driver.name"},
		{"dra_pool_name", "com.nvidia.k8s.dra.pool.name"},
		{"dra_device_name", "com.nvidia.k8s.dra.device.name"},
		{"dra_mig_profile", "com.nvidia.k8s.dra.mig.profile"},
		{"dra_mig_device_uuid", "com.nvidia.k8s.dra.mig.device.uuid"},
	} {
		a.addString(mapping[1], metric.Attributes[mapping[0]])
	}
}

// attributes accumulates one fixed mapping and its first numeric identity error.
// It never logs source contents or forwards arbitrary metric labels/attributes.
type attributes struct {
	values []attribute.KeyValue
	err    error
}

// addHardware uses preserved entity scope because a collector group can contain different hardware types.
func (a *attributes) addHardware(group dcgm.Field_Entity_Group, metric collector.Metric) error {
	s := metric.Source
	var id, parent, kind string
	switch s.EntityGroup {
	case dcgm.FE_GPU, dcgm.FE_GPU_I:
		gpuID, err := a.addGPU(metric)
		if err != nil {
			return err
		}
		id, kind = gpuID, "gpu"
		if s.EntityGroup == dcgm.FE_GPU_I {
			index := a.addIndex("com.nvidia.mig.gpu_instance.id", metric.GPUInstanceID)
			id, parent = "mig-gi:"+metric.GPUUUID+":"+index, gpuID
			a.addString("com.nvidia.mig.gpu_instance.profile", metric.MigProfile)
		}
	case dcgm.FE_GPU_CI:
		return errors.New("GPU compute instance identity is outside the OTLP MVP")
	case dcgm.FE_CPU, dcgm.FE_CPU_CORE:
		index := a.addIndex("com.nvidia.cpu.index", metric.CPU)
		id, kind = "cpu:"+index, "cpu"
		if s.EntityGroup == dcgm.FE_CPU_CORE {
			core := a.addIndex("com.nvidia.cpu.core.index", metric.CPUCore)
			id, parent, kind = "cpu-core:"+index+":"+core, id, "cpu_core"
		} else {
			a.addString("hw.serial_number", metric.CPUSerial)
		}
	case dcgm.FE_SWITCH:
		id = "nvswitch:" + a.addNumber("com.nvidia.nvswitch.index", s.NVSwitchIndex, true)
		kind = "nvswitch"
	case dcgm.FE_LINK:
		var err error
		switch metric.ParentType {
		case dcgm.FE_GPU:
			parent, err = a.addGPU(metric)
		case dcgm.FE_SWITCH:
			parent = "nvswitch:" + a.addNumber("com.nvidia.nvswitch.index", s.NVSwitchIndex, true)
		default:
			return errors.New("NVLink parent identity is required")
		}

		if err != nil {
			return err
		}

		index := a.addNumber("com.nvidia.nvlink.index", s.NVLinkIndex, true)
		id, kind = "nvlink:"+parent+":"+index, "nvlink"
	case dcgm.FE_NONE:
		if group != dcgm.FE_NONE {
			return fmt.Errorf("%w: hardware source identity is required", errIncompleteHardwareIdentity)
		}
	default:
		return errors.New("unsupported hardware entity scope")
	}
	a.addString("hw.id", id)
	a.addString("hw.parent", parent)
	a.addString("hw.type", kind)
	return nil
}

// addString treats empty source strings as missing metadata in OTLP.
func (a *attributes) addString(key, value string) {
	if value != "" {
		a.values = append(a.values, attribute.String(key, value))
	}
}

// addIndex normalizes indexes so "00" and "0" produce the same hardware ID.
func (a *attributes) addIndex(key, value string) string {
	if value == "" {
		if a.err == nil {
			a.err = fmt.Errorf("%w: %s is required", errIncompleteHardwareIdentity, key)
		}
		return ""
	}

	n, err := strconv.ParseUint(value, 10, 63)
	if err != nil {
		if a.err == nil {
			a.err = fmt.Errorf("%s requires a nonnegative INT64 identity", key)
		}
		return ""
	}
	a.values = append(a.values, attribute.Int64(key, int64(n)))
	return strconv.FormatUint(n, 10)
}

// addNumber keeps unknown (nil) metadata distinct from a valid zero.
// Validation errors are collected for result to reject the entire point.
func (a *attributes) addNumber(key string, value *int64, required bool) string {
	if value == nil {
		if required && a.err == nil {
			a.err = fmt.Errorf("%s is required", key)
		}
		return ""
	}
	if *value < 0 {
		if a.err == nil {
			a.err = fmt.Errorf("%s must be nonnegative", key)
		}
		return ""
	}
	a.values = append(a.values, attribute.Int64(key, *value))
	return strconv.FormatInt(*value, 10)
}

// addGPU prevents discovery fallback UUIDs from becoming OTLP hardware identities.
func (a *attributes) addGPU(m collector.Metric) (string, error) {
	s := m.Source
	if s.SyntheticGPUUUID || m.GPUUUID == "" {
		return "", fmt.Errorf("%w: reported GPU UUID is required", errIncompleteHardwareIdentity)
	}
	a.addIndex("com.nvidia.gpu.index", m.GPU)
	a.addString("com.nvidia.gpu.uuid", m.GPUUUID)
	a.addString("com.nvidia.gpu.pci.bus_id", m.GPUPCIBusID)
	a.addString("hw.name", m.GPUDevice)
	a.addString("hw.model", s.GPUModelName)
	a.addString("hw.vendor", "NVIDIA")
	a.addString("hw.serial_number", s.GPUSerial)
	a.addString("hw.firmware_version", s.GPUVBIOS)
	a.addString("hw.driver_version", s.GPUDriverVersion)
	a.addNumber("com.nvidia.gpu.pcie.link.generation.maximum", s.PCIeMaxGeneration, false)
	a.addNumber("com.nvidia.gpu.pcie.link.width.maximum", s.PCIeMaxWidth, false)
	a.addNumber("com.nvidia.gpu.pcie.link.generation.current", s.PCIeGeneration, false)
	a.addNumber("com.nvidia.gpu.pcie.link.width.current", s.PCIeWidth, false)
	// Topology is populated only for existing directed peer samples.
	a.addString("com.nvidia.gpu.peer.uuid", s.PeerGPUUUID)
	if s.PeerGPUUUID != "" {
		a.addString("com.nvidia.gpu.pcie.peer.path.type", s.PeerPCIPathType)
	}
	return "gpu:" + m.GPUUUID, nil
}

// result rejects all collected attributes on a numeric identity error so incomplete points cannot be exported.
func (a *attributes) result() ([]attribute.KeyValue, error) {
	set, err := a.resultSet()
	if err != nil {
		return nil, err
	}
	return set.ToSlice(), nil
}

func (a *attributes) resultSet() (attribute.Set, error) {
	if a.err != nil {
		return attribute.Set{}, a.err
	}
	return attribute.NewSet(a.values...), nil
}
