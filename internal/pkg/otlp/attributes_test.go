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

package otlp

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
)

// TestBuildResourceAttributes checks required service identity, optional fields, and detached results.
func TestBuildResourceAttributes(t *testing.T) {
	tests := []struct {
		name     string
		identity ResourceIdentity
		want     []attribute.KeyValue
		err      string
	}{
		{"resolved defaults", ResourceIdentity{ServiceName: "dcgm-exporter", ServiceVersion: "version", ServiceInstanceID: "process-id", Hostname: "host", KubernetesNodeName: "node"}, []attribute.KeyValue{attribute.String("service.name", "dcgm-exporter"), attribute.String("service.version", "version"), attribute.String("service.instance.id", "process-id"), attribute.String("host.name", "host"), attribute.String("k8s.node.name", "node")}, ""},
		{"override and hostname opt out", ResourceIdentity{ServiceName: "override", ServiceInstanceID: "same-id"}, []attribute.KeyValue{attribute.String("service.name", "override"), attribute.String("service.instance.id", "same-id")}, ""},
		{"missing name", ResourceIdentity{ServiceInstanceID: "id"}, nil, "service.name"},
		{"missing instance", ResourceIdentity{ServiceName: "exporter"}, nil, "service.instance.id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildResourceAttributes(tt.identity)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.ElementsMatch(t, tt.want, got)
			got[0] = attribute.String("mutated", "result")
			rebuilt, err := BuildResourceAttributes(tt.identity)
			require.NoError(t, err)
			require.ElementsMatch(t, tt.want, rebuilt)
		})
	}
}

// gpuFixture supplies reported GPU identity shared by data-point mapping tests.
func gpuFixture() collector.Metric {
	return collector.Metric{GPU: "0", GPUUUID: "GPU-reported", GPUDevice: "nvidia0", GPUPCIBusID: "00000000:01:00.0", GPUModelName: "NVIDIA-GPU", Source: &collector.SourceIdentity{EntityGroup: dcgm.FE_GPU, GPUModelName: "NVIDIA GPU"}}
}

// TestBuildDataPointAttributes verifies hardware and workload mappings, validation, and input ownership.
func TestBuildDataPointAttributes(t *testing.T) {
	tests := []struct {
		name      string
		group     dcgm.Field_Entity_Group
		metric    collector.Metric
		container *collector.ContainerIdentity
		want      []attribute.KeyValue
		omitted   []string
		err       string
	}{
		{name: "physical GPU", metric: gpuFixture(), want: []attribute.KeyValue{attribute.String("hw.id", "gpu:GPU-reported"), attribute.String("hw.type", "gpu"), attribute.String("hw.vendor", "NVIDIA"), attribute.String("hw.name", "nvidia0"), attribute.String("hw.model", "NVIDIA GPU"), attribute.String("com.nvidia.gpu.uuid", "GPU-reported"), attribute.Int64("com.nvidia.gpu.index", 0), attribute.String("com.nvidia.gpu.pci.bus_id", "00000000:01:00.0")}, omitted: []string{"com.nvidia.gpu.pcie.link.generation.maximum", "com.nvidia.gpu.pcie.link.width.maximum", "com.nvidia.gpu.pcie.link.generation.current", "com.nvidia.gpu.pcie.link.width.current", "com.nvidia.gpu.peer.uuid", "com.nvidia.gpu.pcie.peer.path.type"}},
		{name: "GI zero without profile", metric: func() collector.Metric {
			m := gpuFixture()
			m.Source.EntityGroup = dcgm.FE_GPU_I
			m.GPUInstanceID = "0"
			return m
		}(), want: []attribute.KeyValue{attribute.String("hw.id", "mig-gi:GPU-reported:0"), attribute.String("hw.parent", "gpu:GPU-reported"), attribute.Int64("com.nvidia.mig.gpu_instance.id", 0)}, omitted: []string{"com.nvidia.mig.gpu_instance.profile"}},
		{name: "CI excluded", metric: func() collector.Metric { m := gpuFixture(); m.Source.EntityGroup = dcgm.FE_GPU_CI; return m }(), err: "compute instance"},
		{name: "synthetic UUID", metric: func() collector.Metric { m := gpuFixture(); m.Source.SyntheticGPUUUID = true; return m }(), err: "GPU UUID"},
		{name: "absent UUID", metric: func() collector.Metric { m := gpuFixture(); m.GPUUUID = ""; return m }(), err: "GPU UUID"},
		{name: "missing hardware source", group: dcgm.FE_GPU, metric: collector.Metric{GPUUUID: "GPU-real"}, err: "source identity"},
		{name: "exporter only", group: dcgm.FE_NONE, metric: collector.Metric{}, want: []attribute.KeyValue{}},
		{name: "optional PCIe and serial", metric: func() collector.Metric {
			m := gpuFixture()
			m.Labels = map[string]string{"DCGM_FI_DRIVER_VERSION": "prometheus-driver"}
			m.Source.PCIeMaxGeneration = new(int64(4))
			m.Source.PCIeMaxWidth = new(int64(16))
			m.Source.PCIeGeneration = new(int64)
			m.Source.PCIeWidth = new(int64(8))
			m.Source.GPUSerial = "serial"
			m.Source.GPUVBIOS = "firmware"
			m.Source.GPUDriverVersion = "driver"
			return m
		}(), want: []attribute.KeyValue{attribute.Int64("com.nvidia.gpu.pcie.link.generation.maximum", 4), attribute.Int64("com.nvidia.gpu.pcie.link.width.maximum", 16), attribute.Int64("com.nvidia.gpu.pcie.link.generation.current", 0), attribute.Int64("com.nvidia.gpu.pcie.link.width.current", 8), attribute.String("hw.serial_number", "serial"), attribute.String("hw.firmware_version", "firmware"), attribute.String("hw.driver_version", "driver")}, omitted: []string{"DCGM_FI_DRIVER_VERSION", "service.name"}},
		{name: "negative index", metric: func() collector.Metric { m := gpuFixture(); m.GPU = "-1"; return m }(), err: "com.nvidia.gpu.index"},
		{name: "malformed GI", metric: func() collector.Metric {
			m := gpuFixture()
			m.Source.EntityGroup = dcgm.FE_GPU_I
			m.GPUInstanceID = "invalid"
			return m
		}(), err: "com.nvidia.mig.gpu_instance.id"},
		{name: "overflow index", metric: func() collector.Metric { m := gpuFixture(); m.GPU = "9223372036854775808"; return m }(), err: "com.nvidia.gpu.index"},
		{name: "CPU", metric: collector.Metric{CPU: "2", CPUSerial: "cpu-serial", Source: &collector.SourceIdentity{EntityGroup: dcgm.FE_CPU}}, want: []attribute.KeyValue{attribute.String("hw.id", "cpu:2"), attribute.String("hw.type", "cpu"), attribute.Int64("com.nvidia.cpu.index", 2), attribute.String("hw.serial_number", "cpu-serial")}},
		{name: "CPU core", metric: collector.Metric{CPU: "2", CPUCore: "0", CPUSerial: "parent serial", Source: &collector.SourceIdentity{EntityGroup: dcgm.FE_CPU_CORE}}, want: []attribute.KeyValue{attribute.String("hw.id", "cpu-core:2:0"), attribute.String("hw.parent", "cpu:2"), attribute.String("hw.type", "cpu_core"), attribute.Int64("com.nvidia.cpu.core.index", 0)}, omitted: []string{"hw.serial_number"}},
		{name: "switch zero", metric: collector.Metric{Source: &collector.SourceIdentity{EntityGroup: dcgm.FE_SWITCH, NVSwitchIndex: new(int64)}}, want: []attribute.KeyValue{attribute.String("hw.id", "nvswitch:0"), attribute.String("hw.type", "nvswitch"), attribute.Int64("com.nvidia.nvswitch.index", 0)}},
		{name: "missing switch index", metric: collector.Metric{Source: &collector.SourceIdentity{EntityGroup: dcgm.FE_SWITCH}}, err: "com.nvidia.nvswitch.index"},
		{name: "negative switch index", metric: collector.Metric{Source: &collector.SourceIdentity{EntityGroup: dcgm.FE_SWITCH, NVSwitchIndex: new(int64(-1))}}, err: "com.nvidia.nvswitch.index"},
		{name: "GPU link zero", metric: func() collector.Metric {
			m := gpuFixture()
			m.Source.EntityGroup = dcgm.FE_LINK
			m.Source.NVLinkIndex = new(int64)
			m.ParentType = dcgm.FE_GPU
			return m
		}(), want: []attribute.KeyValue{attribute.String("hw.id", "nvlink:gpu:GPU-reported:0"), attribute.String("hw.parent", "gpu:GPU-reported"), attribute.String("hw.type", "nvlink"), attribute.Int64("com.nvidia.nvlink.index", 0)}},
		{name: "switch link", metric: collector.Metric{ParentType: dcgm.FE_SWITCH, Source: &collector.SourceIdentity{EntityGroup: dcgm.FE_LINK, NVLinkIndex: new(int64), NVSwitchIndex: new(int64)}}, want: []attribute.KeyValue{attribute.String("hw.id", "nvlink:nvswitch:0:0"), attribute.String("hw.parent", "nvswitch:0")}},
		{name: "missing switch link parent index", metric: collector.Metric{ParentType: dcgm.FE_SWITCH, Source: &collector.SourceIdentity{EntityGroup: dcgm.FE_LINK, NVLinkIndex: new(int64)}}, err: "com.nvidia.nvswitch.index"},
		{name: "missing GPU link index", metric: func() collector.Metric {
			m := gpuFixture()
			m.Source.EntityGroup = dcgm.FE_LINK
			m.ParentType = dcgm.FE_GPU
			return m
		}(), err: "com.nvidia.nvlink.index"},
		{name: "missing link parent", metric: collector.Metric{Source: &collector.SourceIdentity{EntityGroup: dcgm.FE_LINK, NVLinkIndex: new(int64)}}, err: "NVLink parent"},
		{name: "peer PCIe path", metric: func() collector.Metric {
			m := gpuFixture()
			m.Source.PeerGPUUUID = "GPU-peer"
			m.Source.PeerPCIPathType = "PIX"
			return m
		}(), want: []attribute.KeyValue{attribute.String("com.nvidia.gpu.peer.uuid", "GPU-peer"), attribute.String("com.nvidia.gpu.pcie.peer.path.type", "PIX")}},
		{name: "peer without known PCIe path", metric: func() collector.Metric {
			m := gpuFixture()
			m.Source.PeerGPUUUID = "GPU-peer"
			return m
		}(), want: []attribute.KeyValue{attribute.String("com.nvidia.gpu.peer.uuid", "GPU-peer")}, omitted: []string{"com.nvidia.gpu.pcie.peer.path.type"}},
		{name: "PCIe path without peer", metric: func() collector.Metric {
			m := gpuFixture()
			m.Source.PeerPCIPathType = "PIX"
			return m
		}(), omitted: []string{"com.nvidia.gpu.peer.uuid", "com.nvidia.gpu.pcie.peer.path.type"}},
		{name: "raw pod keys and aliases", metric: func() collector.Metric {
			m := gpuFixture()
			m.Attributes = map[string]string{"pod_name": "pod", "pod_namespace": "ns", "container_name": "ctr", "pod_uid": "uid", "vgpu": "slot", "unexpected": "ignored"}
			m.Source.PodLabels = map[string]string{"a/b.c-d": "value", "hw.id": "hostile", "service.name": "protected", "empty": ""}
			m.Source.DevicePluginResourceName = "nvidia.com/gpu"
			return m
		}(), want: []attribute.KeyValue{attribute.String("k8s.pod.name", "pod"), attribute.String("k8s.namespace.name", "ns"), attribute.String("k8s.container.name", "ctr"), attribute.String("k8s.pod.uid", "uid"), attribute.String("k8s.pod.label.a/b.c-d", "value"), attribute.String("k8s.pod.label.hw.id", "hostile"), attribute.String("k8s.pod.label.service.name", "protected"), attribute.String("com.nvidia.gpu.sharing.id", "slot"), attribute.String("com.nvidia.k8s.device_plugin.resource.name", "nvidia.com/gpu")}, omitted: []string{"unexpected", "k8s.pod.label.empty", "container.name", "container.id", "service.name"}},
		{name: "DRA and HPC", metric: func() collector.Metric {
			m := gpuFixture()
			m.Attributes = map[string]string{"dra_claim_name": "claim", "dra_claim_namespace": "ns", "dra_driver_name": "driver", "dra_pool_name": "pool", "dra_device_name": "device", "dra_mig_profile": "1g", "dra_mig_device_uuid": "MIG-id", "hpc_job": "job"}
			return m
		}(), want: []attribute.KeyValue{attribute.String("com.nvidia.k8s.dra.claim.name", "claim"), attribute.String("com.nvidia.k8s.dra.claim.namespace.name", "ns"), attribute.String("com.nvidia.k8s.dra.driver.name", "driver"), attribute.String("com.nvidia.k8s.dra.pool.name", "pool"), attribute.String("com.nvidia.k8s.dra.device.name", "device"), attribute.String("com.nvidia.k8s.dra.mig.profile", "1g"), attribute.String("com.nvidia.k8s.dra.mig.device.uuid", "MIG-id"), attribute.String("com.nvidia.hpc.job.id", "job")}},
		{name: "standalone raw container", metric: func() collector.Metric {
			m := gpuFixture()
			m.Attributes = map[string]string{"container": "sanitized"}
			m.Source.Containers = []collector.ContainerIdentity{{ID: "full-id", Name: "raw\nname"}}
			return m
		}(), container: &collector.ContainerIdentity{ID: "full-id", Name: "raw\nname"}, want: []attribute.KeyValue{attribute.String("container.name", "raw\nname"), attribute.String("container.id", "full-id"), attribute.String("com.nvidia.dcgm.exporter.container.label", "sanitized")}, omitted: []string{"k8s.container.name"}},
		{name: "standalone absent name", metric: gpuFixture(), container: &collector.ContainerIdentity{ID: "full-id"}, want: []attribute.KeyValue{attribute.String("container.id", "full-id")}, omitted: []string{"container.name"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputSnapshot := func() string {
				data, err := json.Marshal(struct {
					Labels, Attributes map[string]string
					Source             *collector.SourceIdentity
					Container          *collector.ContainerIdentity
				}{tt.metric.Labels, tt.metric.Attributes, tt.metric.Source, tt.container})
				require.NoError(t, err)
				return string(data)
			}
			before := inputSnapshot()
			got, err := BuildDataPointAttributes(tt.group, tt.metric, tt.container)
			require.Equal(t, before, inputSnapshot(), "builder mutated its input")
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			set := attribute.NewSet(got...)
			require.Equal(t, len(got), set.Len(), "duplicate keys")
			for _, want := range tt.want {
				v, ok := set.Value(want.Key)
				require.True(t, ok, string(want.Key))
				require.Equal(t, want.Value, v)
			}
			for _, key := range tt.omitted {
				require.False(t, set.HasValue(attribute.Key(key)), key)
			}
			if len(got) > 0 {
				got[0] = attribute.String("changed", "result")
			}
			rebuilt, err := BuildDataPointAttributes(tt.group, tt.metric, tt.container)
			require.NoError(t, err)
			require.Equal(t, set, attribute.NewSet(rebuilt...))
			require.Equal(t, before, inputSnapshot(), "builder mutated its input")
		})
	}
	t.Run("concurrent immutable input", func(t *testing.T) {
		m := gpuFixture()
		m.Source.PodLabels = map[string]string{"raw/key": "value"}
		m.Source.PCIeWidth = new(int64(8))
		want, err := BuildDataPointAttributes(dcgm.FE_GPU, m, nil)
		require.NoError(t, err)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				got, err := BuildDataPointAttributes(dcgm.FE_GPU, m, nil)
				assert.NoError(t, err)
				assert.Equal(t, want, got)
			})
		}
		wg.Wait()
	})
}

// TestKubernetesContainerIdentity keeps raw variants distinct and prefers
// transformed Kubernetes names while retaining a raw-name fallback.
func TestKubernetesContainerIdentity(t *testing.T) {
	metric := gpuFixture()
	metric.Attributes = map[string]string{"pod": "pod", "namespace": "ns"}
	first, err := BuildDataPointAttributes(dcgm.FE_GPU, metric, &collector.ContainerIdentity{ID: "first-id", Name: "raw-name"})
	require.NoError(t, err)
	second, err := BuildDataPointAttributes(dcgm.FE_GPU, metric, &collector.ContainerIdentity{ID: "second-id", Name: "raw-name"})
	require.NoError(t, err)
	assert.NotEqual(t, attribute.NewSet(first...), attribute.NewSet(second...))
	assert.Contains(t, first, attribute.String("container.id", "first-id"))
	assert.Contains(t, first, attribute.String("k8s.container.name", "raw-name"))

	metric.Attributes["container_name"] = "transformed-name"
	transformed, err := BuildDataPointAttributes(dcgm.FE_GPU, metric, &collector.ContainerIdentity{ID: "first-id", Name: "raw-name"})
	require.NoError(t, err)
	assert.Contains(t, transformed, attribute.String("k8s.container.name", "transformed-name"))
	assert.Contains(t, transformed, attribute.String("container.id", "first-id"))
	assert.NotContains(t, transformed, attribute.String("container.name", "raw-name"))
}
