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
	"fmt"
	"math"
	"testing"

	"go.uber.org/mock/gomock"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
)

// TestSourceIdentity verifies entity scope, native indexes, and GPU metadata are preserved for OTLP.
func TestSourceIdentity(t *testing.T) {
	tests := []struct {
		name   string
		group  dcgm.Field_Entity_Group
		parent dcgm.Field_Entity_Group
		values []dcgm.FieldValue_v1
		want   *SourceIdentity
	}{
		{"GPU PCIe zero", dcgm.FE_GPU, dcgm.FE_NONE, []dcgm.FieldValue_v1{sourceInt64Value(dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_GEN, 0)}, &SourceIdentity{EntityGroup: dcgm.FE_GPU, GPUModelName: "NVIDIA GPU", GPUSerial: "serial", PCIeMaxGeneration: new(int64)}},
		{"GPU acquired metadata", dcgm.FE_GPU, dcgm.FE_NONE, []dcgm.FieldValue_v1{
			sourceInt64Value(dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_GEN, 4),
			sourceInt64Value(dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH, 16),
			sourceInt64Value(dcgm.DCGM_FI_DEV_PCIE_LINK_GEN, 3),
			sourceInt64Value(dcgm.DCGM_FI_DEV_PCIE_LINK_WIDTH, 8),
			stringFieldValue(dcgm.DCGM_FI_DRIVER_VERSION, "refreshed-driver"),
		}, &SourceIdentity{
			EntityGroup: dcgm.FE_GPU, GPUModelName: "NVIDIA GPU", GPUSerial: "serial", GPUDriverVersion: "refreshed-driver",
			PCIeMaxGeneration: new(int64(4)), PCIeMaxWidth: new(int64(16)), PCIeGeneration: new(int64(3)), PCIeWidth: new(int64(8)),
		}},
		{"GI scope", dcgm.FE_GPU_I, dcgm.FE_NONE, nil, &SourceIdentity{EntityGroup: dcgm.FE_GPU_I, GPUModelName: "NVIDIA GPU", GPUSerial: "serial"}},
		{"CI scope", dcgm.FE_GPU_CI, dcgm.FE_NONE, nil, &SourceIdentity{EntityGroup: dcgm.FE_GPU_CI, GPUModelName: "NVIDIA GPU", GPUSerial: "serial"}},
		{"GPU link zero", dcgm.FE_LINK, dcgm.FE_GPU, nil, &SourceIdentity{EntityGroup: dcgm.FE_LINK, GPUModelName: "NVIDIA GPU", GPUSerial: "serial", NVLinkIndex: new(int64)}},
		{"switch link zero", dcgm.FE_LINK, dcgm.FE_SWITCH, nil, &SourceIdentity{EntityGroup: dcgm.FE_LINK, NVLinkIndex: new(int64), NVSwitchIndex: new(int64)}},
		{"switch zero", dcgm.FE_SWITCH, dcgm.FE_NONE, nil, &SourceIdentity{EntityGroup: dcgm.FE_SWITCH, NVSwitchIndex: new(int64)}},
		{"CPU", dcgm.FE_CPU, dcgm.FE_NONE, nil, &SourceIdentity{EntityGroup: dcgm.FE_CPU}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mi := devicemonitoring.Info{Entity: dcgm.GroupEntityPair{EntityGroupId: tt.group}, ParentType: tt.parent, DeviceInfo: dcgm.Device{Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA GPU", Serial: "serial"}}}
			require.Equal(t, tt.want, sourceIdentity(mi, tt.values, nil))
		})
	}
}

// TestSourceIdentityOptionalValues verifies unavailable or invalid fields do not replace discovered metadata.
func TestSourceIdentityOptionalValues(t *testing.T) {
	tests := []struct {
		name  string
		value dcgm.FieldValue_v1
	}{
		{"negative", sourceInt64Value(dcgm.DCGM_FI_DEV_PCIE_LINK_WIDTH, -1)},
		{"blank", sourceInt64Value(dcgm.DCGM_FI_DEV_PCIE_LINK_WIDTH, dcgm.DCGM_FT_INT64_BLANK)},
		{"failed", func() dcgm.FieldValue_v1 {
			v := sourceInt64Value(dcgm.DCGM_FI_DEV_PCIE_LINK_WIDTH, 16)
			v.Status = dcgm.DCGM_ST_NO_DATA
			return v
		}()},
		{"wrong type", dcgm.FieldValue_v1{FieldID: dcgm.DCGM_FI_DEV_PCIE_LINK_WIDTH, FieldType: dcgm.DCGM_FT_STRING}},
		{"blank driver", stringFieldValue(dcgm.DCGM_FI_DRIVER_VERSION, dcgm.DCGM_FT_STR_NOT_SUPPORTED)},
		{"failed driver", func() dcgm.FieldValue_v1 {
			v := stringFieldValue(dcgm.DCGM_FI_DRIVER_VERSION, "ignored-driver")
			v.Status = dcgm.DCGM_ST_NO_DATA
			return v
		}()},
		{"wrong driver type", func() dcgm.FieldValue_v1 {
			v := stringFieldValue(dcgm.DCGM_FI_DRIVER_VERSION, "ignored-driver")
			v.FieldType = dcgm.DCGM_FT_INT64
			return v
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mi := devicemonitoring.Info{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}, DeviceInfo: dcgm.Device{Identifiers: dcgm.DeviceIdentifiers{Model: dcgm.DCGM_FT_STR_NOT_SUPPORTED, DriverVersion: "discovered-driver"}}}
			source := sourceIdentity(mi, []dcgm.FieldValue_v1{tt.value}, nil)
			require.Equal(t, &SourceIdentity{EntityGroup: dcgm.FE_GPU, GPUDriverVersion: "discovered-driver"}, source)
		})
	}
}

// sourceInt64Value builds a native INT64 sample for source identity contract tests.
func sourceInt64Value(field dcgm.Short, value int64) dcgm.FieldValue_v1 {
	return dcgm.FieldValue_v1{FieldID: field, FieldType: dcgm.DCGM_FT_INT64, Value: createInt64ByteArray(value)}
}

// TestSourceIdentityNativeIndexRange checks the INT64 boundary for native switch and NVLink indexes.
func TestSourceIdentityNativeIndexRange(t *testing.T) {
	for _, tt := range []struct {
		name  string
		index uint
		want  *int64
	}{
		{"highest INT64", math.MaxInt64, func() *int64 { n := int64(math.MaxInt64); return &n }()},
		{"overflow", uint(math.MaxInt64) + 1, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mi := devicemonitoring.Info{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: tt.index}, ParentType: dcgm.FE_SWITCH, ParentId: tt.index}
			source := sourceIdentity(mi, nil, nil)
			require.Equal(t, tt.want, source.NVLinkIndex)
			require.Equal(t, tt.want, source.NVSwitchIndex)
		})
	}
}

// TestPreservePeerIdentity verifies peer UUID and PCI path lookup without fabricating missing identity.
func TestPreservePeerIdentity(t *testing.T) {
	tests := []struct {
		name       string
		peer       uint
		link       dcgm.P2PLinkType
		synthetic  bool
		uuid, path string
	}{
		{"matching second peer", 17, dcgm.P2PLinkSingleSwitch, false, "GPU-peer", "PIX"},
		{"NVLink not PCIe", 17, dcgm.SingleNVLINKLink, false, "GPU-peer", ""},
		{"unknown path", 17, dcgm.P2PLinkUnknown, false, "GPU-peer", ""},
		{"unknown peer", 9, dcgm.P2PLinkSingleSwitch, false, "", ""},
		{"synthetic peer", 17, dcgm.P2PLinkSingleSwitch, true, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &SourceIdentity{}
			devices := []deviceinfo.GPUInfo{{DeviceInfo: dcgm.Device{GPU: 0, UUID: "GPU-first"}}, {DeviceInfo: dcgm.Device{GPU: 17, UUID: "GPU-peer"}, SyntheticGPUUUID: tt.synthetic}}
			gpu := dcgm.Device{Topology: []dcgm.P2PLink{{GPU: 0, Link: dcgm.P2PLinkCrossCPU}, {GPU: 17, Link: tt.link}}}
			preservePeerIdentity(source, gpu, tt.peer, devices)
			require.Equal(t, tt.uuid, source.PeerGPUUUID)
			require.Equal(t, tt.path, source.PeerPCIPathType)
		})
	}
}

// TestCollectorSourceIdentity verifies direct and event collectors preserve identity only when OTLP is enabled.
func TestCollectorSourceIdentity(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("OTLP=%v", enabled), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			device := dcgm.Device{GPU: 0, UUID: "GPU-0", Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA GPU"}}
			provider := mockdeviceinfo.NewMockProvider(ctrl)
			provider.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
			if enabled {
				provider.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{DeviceInfo: device, SyntheticGPUUUID: true}).Times(2)
			}
			list := devicewatchlistmanager.NewWatchList(provider, nil, nil, nil, 1000)
			counter := counters.Counter{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "temperature", PromType: "gauge"}
			c := DCGMCollector{enableOTLP: enabled, replaceBlanksInModelName: true, deviceWatchList: *list, counters: []counters.Counter{counter}}
			mi := devicemonitoring.Info{DeviceInfo: device, Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}}
			metrics := make(MetricsByCounter)
			c.addMetrics(metrics, []dcgm.FieldValue_v1{sourceInt64Value(counter.FieldID, 42)}, mi, 123)
			require.Len(t, metrics[counter], 1)
			m := metrics[counter][0]
			require.Equal(t, "NVIDIA-GPU", m.GPUModelName)
			require.Equal(t, int64(123), m.Timestamp)
			base := baseExpCollector{config: &appconfig.Config{EnableOTLP: enabled, ReplaceBlanksInModelName: true}, deviceWatchList: *list}
			event := base.createMetric(nil, mi, "UUID", 1, provider, 123)
			require.Equal(t, int64(123), event.Timestamp)
			if enabled {
				require.Equal(t, "NVIDIA GPU", m.Source.GPUModelName)
				require.True(t, m.Source.SyntheticGPUUUID)
				require.Equal(t, m.Source, event.Source)
			} else {
				require.Nil(t, m.Source)
				require.Nil(t, event.Source)
			}
		})
	}
}

// TestScopedMetricsWithParentPCIe checks parent identity without adding Prometheus samples.
func TestScopedMetricsWithParentPCIe(t *testing.T) {
	firstGeneration, secondGeneration := int64(4), int64(5)
	const sampledGeneration = int64(6)
	tests := []struct {
		name             string
		group            dcgm.Field_Entity_Group
		gpu              uint
		prometheusFields []dcgm.Short
		wantCount        int
		wantGeneration   int64
	}{
		{name: "metadata parent emits no metric", group: dcgm.FE_GPU},
		{name: "selected physical metric", group: dcgm.FE_GPU, prometheusFields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}, wantCount: 1, wantGeneration: sampledGeneration},
		{name: "first instance parent", group: dcgm.FE_GPU_I, prometheusFields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}, wantCount: 1, wantGeneration: 4},
		{name: "second instance parent", group: dcgm.FE_GPU_I, gpu: 1, prometheusFields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}, wantCount: 1, wantGeneration: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counter := counters.Counter{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
			label := counters.Counter{FieldID: dcgm.DCGM_FI_DRIVER_VERSION, FieldName: "DCGM_FI_DRIVER_VERSION", PromType: "label"}
			list := devicewatchlistmanager.NewWatchList(nil, nil, []dcgm.Short{label.FieldID}, nil, 1000)
			c := DCGMCollector{enableOTLP: true, counters: []counters.Counter{counter, label}, deviceWatchList: *list}
			parents := map[uint]*SourceIdentity{0: {PCIeMaxGeneration: &firstGeneration}, 1: {PCIeMaxGeneration: &secondGeneration}}
			mi := devicemonitoring.Info{Entity: dcgm.GroupEntityPair{EntityGroupId: tt.group}, DeviceInfo: dcgm.Device{GPU: tt.gpu, UUID: fmt.Sprintf("GPU-%d", tt.gpu)}}
			values := []dcgm.FieldValue_v1{
				sourceInt64Value(dcgm.DCGM_FI_DEV_GPU_TEMP, 42),
				sourceInt64Value(label.FieldID, 123),
				sourceInt64Value(dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_GEN, sampledGeneration),
			}
			metrics := make(MetricsByCounter)
			c.addScopedMetrics(metrics, devicewatchlistmanager.ReadRequest{Entity: mi, PrometheusFields: tt.prometheusFields}, values, 123, parents)
			require.Len(t, metrics[counter], tt.wantCount)
			if tt.group == dcgm.FE_GPU {
				require.NotNil(t, parents[tt.gpu])
				require.NotNil(t, parents[tt.gpu].PCIeMaxGeneration)
				require.Equal(t, sampledGeneration, *parents[tt.gpu].PCIeMaxGeneration)
			}

			if tt.wantCount == 0 {
				return
			}
			require.NotNil(t, metrics[counter][0].Source.PCIeMaxGeneration)
			require.Equal(t, tt.wantGeneration, *metrics[counter][0].Source.PCIeMaxGeneration)
			require.Equal(t, tt.group, metrics[counter][0].Source.EntityGroup)
			require.Equal(t, "42", metrics[counter][0].Value)
			require.Equal(t, "123", metrics[counter][0].Labels[label.FieldName])
		})
	}
}
