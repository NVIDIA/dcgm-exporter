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
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// draPod returns a pod that holds one DRA claim on a full GPU.
func draPod(name, claim string) PodInfo {
	return PodInfo{
		Name: name, Namespace: "team", Container: "main",
		DynamicResources: &DynamicResourceInfo{
			ClaimName: claim, ClaimNamespace: "team", DriverName: DRAGPUDriverName,
			PoolName: "node-a", DeviceName: "gpu-0",
		},
	}
}

// draSample returns one GPU sample with no pod attributes.
func draSample(uuid string) collector.Metric {
	return collector.Metric{
		GPUUUID:    uuid,
		Value:      "1",
		Labels:     map[string]string{"Hostname": "node-a"},
		Attributes: map[string]string{},
	}
}

// fullDRAAttributes are the attributes a pod with a claim on a full GPU adds.
func fullDRAAttributes(pod, claim string) map[string]string {
	return map[string]string{
		podAttribute: pod, namespaceAttribute: "team", containerAttribute: "main",
		draClaimName: claim, draClaimNamespace: "team", draDriverName: DRAGPUDriverName,
		draPoolName: "node-a", draDeviceName: "gpu-0",
	}
}

// TestEnrichDRAMetrics checks that each DRA claim on a device gets its own copy
// of the sample with the claim attributes, and that samples with no claim, or
// a mapper with DRA labels off, keep the original sample.
func TestEnrichDRAMetrics(t *testing.T) {
	counter := counters.Counter{FieldName: "DCGM_FI_DEV_GPU_UTIL"}
	gpuGroup := func() dcgm.Field_Entity_Group { return dcgm.FE_GPU }

	tests := []struct {
		name         string
		disabled     bool
		samples      []collector.Metric
		deviceToPods map[string][]PodInfo
		want         []map[string]string // attributes of each output sample, in order
		wantLabels   []map[string]string // when set, labels of each output sample
	}{
		{
			name:    "one copy per claim on a shared GPU",
			samples: []collector.Metric{draSample("GPU-a")},
			deviceToPods: map[string][]PodInfo{
				"GPU-a": {draPod("trainer", "claim-1"), draPod("eval", "claim-2")},
			},
			want: []map[string]string{fullDRAAttributes("trainer", "claim-1"), fullDRAAttributes("eval", "claim-2")},
		},
		{
			name:         "a GPU with no claim keeps its sample",
			samples:      []collector.Metric{draSample("GPU-a"), draSample("GPU-b")},
			deviceToPods: map[string][]PodInfo{"GPU-a": {draPod("trainer", "claim-1")}},
			want:         []map[string]string{fullDRAAttributes("trainer", "claim-1"), {}},
		},
		{
			name:         "no claims at all keep every sample",
			samples:      []collector.Metric{draSample("GPU-a")},
			deviceToPods: map[string][]PodInfo{},
			want:         []map[string]string{{}},
		},
		{
			name:         "DRA labels off leave the samples alone",
			disabled:     true,
			samples:      []collector.Metric{draSample("GPU-a")},
			deviceToPods: map[string][]PodInfo{"GPU-a": {draPod("trainer", "claim-1")}},
			want:         []map[string]string{{}},
		},
		{
			name: "a MIG claim adds the profile and MIG device UUID",
			samples: []collector.Metric{{
				GPU: "0", GPUUUID: "GPU-a", GPUInstanceID: "3", MigProfile: "1g.10gb",
				Labels: map[string]string{}, Attributes: map[string]string{},
			}},
			deviceToPods: map[string][]PodInfo{"0-3": {{
				Name: "inference", Namespace: "team", Container: "main",
				DynamicResources: &DynamicResourceInfo{
					ClaimName: "mig-claim", ClaimNamespace: "team", DriverName: DRAGPUDriverName,
					PoolName: "node-a", DeviceName: "gpu-0-mig-3",
					MIGInfo: &DRAMigDeviceInfo{Profile: "1g.10gb", MIGDeviceUUID: "MIG-x"},
				},
			}}},
			want: []map[string]string{{
				podAttribute: "inference", namespaceAttribute: "team", containerAttribute: "main",
				draClaimName: "mig-claim", draClaimNamespace: "team", draDriverName: DRAGPUDriverName,
				draPoolName: "node-a", draDeviceName: "gpu-0-mig-3",
				draMigProfile: "1g.10gb", draMigDeviceUUID: "MIG-x",
			}},
		},
		{
			name:    "a pod label named like a DRA attribute gets the pod_label_ name",
			samples: []collector.Metric{draSample("GPU-a")},
			deviceToPods: map[string][]PodInfo{"GPU-a": {func() PodInfo {
				pod := draPod("trainer", "claim-1")
				pod.Labels = map[string]string{draClaimName: "from-label", "app": "train"}
				return pod
			}()}},
			want: []map[string]string{fullDRAAttributes("trainer", "claim-1")},
			wantLabels: []map[string]string{{
				"Hostname": "node-a", "app": "train", "pod_label_" + draClaimName: "from-label",
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := &PodMapper{Config: &appconfig.Config{
				KubernetesEnableDRA: !tt.disabled, KubernetesGPUIdType: appconfig.GPUUID,
			}}
			metrics := collector.MetricsByCounter{counter: tt.samples}
			original := cloneMetric(tt.samples[0])

			require.NoError(t, mapper.enrichDRAMetrics(metrics, tt.deviceToPods, gpuGroup))

			got := metrics[counter]
			require.Len(t, got, len(tt.want))
			for i, attributes := range tt.want {
				assert.Equal(t, attributes, got[i].Attributes, "sample %d", i)
				if tt.wantLabels != nil {
					assert.Equal(t, tt.wantLabels[i], got[i].Labels, "sample %d", i)
				}
			}
			assert.Equal(t, original, tt.samples[0], "the input sample must not change")
		})
	}
}

// TestEnrichDRAMetricsRejectsAnUnknownGPUIDType checks that a GPU ID type the
// mapper cannot key devices by is an error, not a silent passthrough.
func TestEnrichDRAMetricsRejectsAnUnknownGPUIDType(t *testing.T) {
	mapper := &PodMapper{Config: &appconfig.Config{KubernetesEnableDRA: true, KubernetesGPUIdType: "serial"}}
	metrics := collector.MetricsByCounter{
		counters.Counter{FieldName: "DCGM_FI_DEV_GPU_UTIL"}: {draSample("GPU-a")},
	}

	err := mapper.enrichDRAMetrics(metrics, map[string][]PodInfo{}, func() dcgm.Field_Entity_Group { return dcgm.FE_GPU })

	require.ErrorContains(t, err, "unsupported KubernetesGPUIDType")
}
