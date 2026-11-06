/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

// TestIsDCGMExpP2PStatusEnabled checks that the helper returns true when the P2P status counter is in the
// list, and false for an empty list or a list of unrelated counters.
func TestIsDCGMExpP2PStatusEnabled(t *testing.T) {
	tests := []struct {
		name string
		arg  counters.CounterList
		want bool
	}{
		{
			name: "empty",
			arg:  counters.CounterList{},
			want: false,
		},
		{
			name: "counter not present",
			arg: counters.CounterList{
				counters.Counter{FieldID: 1, FieldName: "random1"},
				counters.Counter{FieldID: 2, FieldName: "random2"},
			},
			want: false,
		},
		{
			name: "counter present",
			arg: counters.CounterList{
				counters.Counter{FieldID: 1, FieldName: counters.DCGMExpP2PStatus},
				counters.Counter{FieldID: 2, FieldName: "random2"},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, IsDCGMExpP2PStatusEnabled(tt.arg), "unexpected response")
		})
	}
}

// TestP2PStatusToString maps known P2P link statuses to names and unknown codes to the unknown label.
func TestP2PStatusToString(t *testing.T) {
	tests := []struct {
		name   string
		input  uint64
		output string
	}{
		{LinkStatusOK, 0, LinkStatusOK},
		{LinkStatusChipsetNotSupported, 1, LinkStatusChipsetNotSupported},
		{LinkStatusGPUNotSupported, 2, LinkStatusGPUNotSupported},
		{LinkStatusTopologyNotSupported, 3, LinkStatusTopologyNotSupported},
		{LinkStatusDisabledByRegKey, 4, LinkStatusDisabledByRegKey},
		{LinkStatusNotSupported, 5, LinkStatusNotSupported},
		{LinkStatusUnknown, 6, LinkStatusUnknown},
		{LinkStatusUnknown, 99, LinkStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.output, p2pStatusToString(tt.input))
		})
	}
}

func TestP2PStatusPreservesGPUIdentityWhenDetached(t *testing.T) {
	for _, detached := range []int{-1, 0, 1, 2, 3} {
		t.Run(fmt.Sprintf("detached_%d", detached), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mockdcgm.NewMockDCGM(ctrl)
			info := mockdeviceinfo.NewMockProvider(ctrl)
			info.EXPECT().GPUCount().Return(uint(3)).AnyTimes()
			var gpus []deviceinfo.GPUInfo
			for id := uint(0); id < 3; id++ {
				status := dcgm.EntityStatusOk
				if detached == int(id) || detached == 3 {
					status = dcgm.EntityStatusDetached
				}
				gpu := deviceinfo.GPUInfo{
					Status:       status,
					DeviceInfo:   dcgm.Device{GPU: id, UUID: fmt.Sprintf("GPU-%d", id)},
					GPUInstances: []deviceinfo.GPUInstanceInfo{{EntityId: 100 + id}},
				}
				gpus = append(gpus, gpu)
				info.EXPECT().GPU(id).Return(gpu).AnyTimes()
			}
			info.EXPECT().GPUs().Return(gpus).AnyTimes()
			client.EXPECT().GetNvLinkP2PStatus().Return(dcgm.NvLinkP2PStatus{Gpus: [][]dcgm.Link_State{
				{0, 1, 2}, {3, 0, 4}, {5, 6, 0},
			}}, nil)
			counter := counters.Counter{FieldName: counters.DCGMExpP2PStatus}
			collector := &p2pStatusCollector{
				baseExpCollector:   baseExpCollector{client: client, counter: counter, config: &appconfig.Config{EnableOTLP: true}},
				deviceInfoProvider: info,
			}
			metrics, err := collector.GetMetrics()
			require.NoError(t, err)
			wantCount := 2
			switch detached {
			case -1:
				wantCount = 6
			case 3:
				wantCount = 0
			}
			require.Len(t, metrics[counter], wantCount)
			wantValues := map[string]string{"0:1": "1", "0:2": "2", "1:0": "3", "1:2": "4", "2:0": "5", "2:1": "6"}
			seen := make(map[string]bool)
			for _, metric := range metrics[counter] {
				pair := metric.GPU + ":" + metric.Labels[PeerGPULabel]
				require.False(t, seen[pair], "duplicate P2P sample")
				seen[pair] = true
				require.Equal(t, wantValues[pair], metric.Value, "matrix row and column must retain their GPU IDs")
				require.NotEqual(t, fmt.Sprint(detached), metric.GPU)
				require.NotEqual(t, fmt.Sprint(detached), metric.Labels[PeerGPULabel])
				require.Equal(t, "GPU-"+metric.GPU, metric.GPUUUID)
				require.NotNil(t, metric.Source)
				require.Equal(t, "GPU-"+metric.Labels[PeerGPULabel], metric.Source.PeerGPUUUID)
				require.Empty(t, metric.GPUInstanceID, "P2P rows describe physical GPUs")
			}
		})
	}
}

// TestNewP2PStatusCollector checks that construction fails and returns no collector for an empty counter
// list, and returns a collector without error when the P2P status counter is present.
func TestNewP2PStatusCollector(t *testing.T) {
	counter := counters.Counter{FieldID: 1, FieldName: counters.DCGMExpP2PStatus}
	counterList := counters.CounterList{counter}
	config := &appconfig.Config{}
	hostname := "testhost"

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Set up mock DCGM provider
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	mockDCGM.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	// Set up required DCGM provider expectations
	mockDCGM.EXPECT().GetAllDeviceCount().Return(uint(1), nil).AnyTimes()
	mockDCGM.EXPECT().GetDeviceInfo(gomock.Eq(uint(0))).Return(dcgm.Device{GPU: 0}, nil).AnyTimes()
	mockDCGM.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil).AnyTimes()
	mockDCGM.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).AnyTimes()

	/******** Mock Device Info *********/
	gOpts := appconfig.DeviceOptions{
		Flex: true,
	}

	mockDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
	mockDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	mockDeviceInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{GPU: 0}}).AnyTimes()

	// Create a real device watcher
	deviceWatcher := devicewatcher.NewDeviceWatcher()
	deviceWatchList := *devicewatchlistmanager.NewWatchList(mockDeviceInfo, []dcgm.Short{42}, nil, deviceWatcher, int64(1))

	t.Run("returns error when collector is disabled", func(t *testing.T) {
		c, err := NewP2PStatusCollector(counters.CounterList{}, hostname, config, deviceWatchList)
		assert.Nil(t, c)
		assert.Error(t, err)
	})

	t.Run("returns collector when enabled", func(t *testing.T) {
		c, err := NewP2PStatusCollector(counterList, hostname, config, deviceWatchList)
		assert.NotNil(t, c)
		assert.NoError(t, err)
	})
}

// fakeP2PStatus supplies a deterministic peer-to-peer matrix to collector tests.
type fakeP2PStatus struct {
	Gpus [][]dcgm.Link_State
}

// toDCGM converts the test matrix into the shape returned by DCGM.
func (f fakeP2PStatus) toDCGM() dcgm.NvLinkP2PStatus {
	return dcgm.NvLinkP2PStatus{Gpus: f.Gpus}
}

// TestP2PStatusCollector_GetMetrics exports off-diagonal link statuses and returns DCGM query errors.
func TestP2PStatusCollector_GetMetrics(t *testing.T) {
	counter := counters.Counter{FieldID: 1, FieldName: counters.DCGMExpP2PStatus}
	counterList := counters.CounterList{counter}
	config := &appconfig.Config{}
	hostname := "testhost"

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Set up mock DCGM provider
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	mockDCGM.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	// Set up required DCGM provider expectations
	mockDCGM.EXPECT().GetAllDeviceCount().Return(uint(2), nil).AnyTimes()
	mockDCGM.EXPECT().GetDeviceInfo(gomock.Eq(uint(0))).Return(dcgm.Device{GPU: 0}, nil).AnyTimes()
	mockDCGM.EXPECT().GetDeviceInfo(gomock.Eq(uint(1))).Return(dcgm.Device{GPU: 1}, nil).AnyTimes()
	mockDCGM.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil).AnyTimes()
	mockDCGM.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).AnyTimes()

	/******** Mock Device Info *********/
	gOpts := appconfig.DeviceOptions{
		Flex: true,
	}

	mockDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
	mockDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(2)).AnyTimes()
	mockDeviceInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{GPU: 0}}).AnyTimes()
	mockDeviceInfo.EXPECT().GPU(uint(1)).Return(deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{GPU: 1}}).AnyTimes()

	// Create a real device watcher
	deviceWatcher := devicewatcher.NewDeviceWatcher()
	deviceWatchList := *devicewatchlistmanager.NewWatchList(mockDeviceInfo, []dcgm.Short{42}, nil, deviceWatcher, int64(1))

	// Set up the GetNvLinkP2PStatus expectation before creating the collector
	fakeStatus := fakeP2PStatus{
		Gpus: [][]dcgm.Link_State{
			{0, 1},
			{1, 0},
		},
	}
	mockDCGM.EXPECT().GetNvLinkP2PStatus().Return(fakeStatus.toDCGM(), nil).Times(1)

	c, err := NewP2PStatusCollector(counterList, hostname, config, deviceWatchList)
	require.NoError(t, err)

	metrics, err := c.GetMetrics()
	assert.NoError(t, err)
	assert.Len(t, metrics, 1)
	metricValues := metrics[counter]
	assert.Len(t, metricValues, 2) // 2 off-diagonal links
	// Check labels and values
	for _, m := range metricValues {
		assert.Contains(t, m.Labels, PeerGPULabel)
		assert.Contains(t, m.Labels, LinkStatusLabel)
	}

	// Error case
	mockDCGM.EXPECT().GetNvLinkP2PStatus().Return(dcgm.NvLinkP2PStatus{}, errors.New("fail")).Times(1)
	_, err = c.GetMetrics()
	assert.Error(t, err)
}

// TestP2PStatusCollector_GetMetricsIsolatesLabelsPerEntity gives two GPUs different values for the same label
// field and checks that each GPU's P2P metric carries only its own GPU's value.
func TestP2PStatusCollector_GetMetricsIsolatesLabelsPerEntity(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	mockDCGM.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()

	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })
	dcgmprovider.SetClient(mockDCGM)

	labelFieldID := dcgm.Short(3)
	labelName := "label_field"
	counter := counters.Counter{FieldID: 1, FieldName: counters.DCGMExpP2PStatus}
	labelCounter := counters.Counter{FieldID: labelFieldID, FieldName: labelName, PromType: "label"}
	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	watchList := devicewatchlistmanager.NewWatchList(
		deviceInfo,
		nil,
		[]dcgm.Short{labelFieldID},
		devicewatcher.NewDeviceWatcher(),
		1,
	)
	collector := &p2pStatusCollector{
		baseExpCollector: baseExpCollector{
			deviceWatchList: *watchList,
			counter:         counter,
			labelsCounters:  []counters.Counter{labelCounter},
			config:          &appconfig.Config{},
		},
		deviceInfoProvider: deviceInfo,
	}

	mockDCGM.EXPECT().GetNvLinkP2PStatus().Return(dcgm.NvLinkP2PStatus{Gpus: [][]dcgm.Link_State{
		{0, 1},
		{1, 0},
	}}, nil)
	mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{labelFieldID}).
		Return([]dcgm.FieldValue_v1{{
			FieldID:   labelFieldID,
			FieldType: dcgm.DCGM_FT_STRING,
			Value:     testutils.StrToByteArray("gpu0-label"),
		}}, nil)
	mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(1), []dcgm.Short{labelFieldID}).
		Return([]dcgm.FieldValue_v1{{
			FieldID:   labelFieldID,
			FieldType: dcgm.DCGM_FT_STRING,
			Value:     testutils.StrToByteArray("gpu1-label"),
		}}, nil)

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	require.Len(t, got[counter], 2)

	for _, metric := range got[counter] {
		assert.Equal(t, "gpu"+metric.GPU+"-label", metric.Labels[labelName])
	}
}

// TestP2PSourceIdentityUsesOwnSnapshot verifies UUID provenance and peer identity follow the P2P device snapshot.
func TestP2PSourceIdentityUsesOwnSnapshot(t *testing.T) {
	for _, synthetic := range []bool{false, true} {
		t.Run(fmt.Sprintf("P2P synthetic=%v", synthetic), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			provider := func(synthetic bool) *mockdeviceinfo.MockProvider {
				p := mockdeviceinfo.NewMockProvider(ctrl)
				gpus := []deviceinfo.GPUInfo{
					{DeviceInfo: dcgm.Device{GPU: 0, UUID: "GPU-0"}, SyntheticGPUUUID: synthetic},
					{DeviceInfo: dcgm.Device{GPU: 1, UUID: "GPU-1"}, SyntheticGPUUUID: synthetic},
				}
				p.EXPECT().GPUCount().Return(uint(2)).AnyTimes()
				p.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
				p.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
				p.EXPECT().GPUs().Return(gpus).AnyTimes()
				for i := range gpus {
					p.EXPECT().GPU(uint(i)).Return(gpus[i]).AnyTimes()
				}
				return p
			}
			watchSnapshot := provider(!synthetic)
			p2pSnapshot := provider(synthetic)
			list := devicewatchlistmanager.NewWatchList(watchSnapshot, nil, nil, nil, 1000)
			client := mockdcgm.NewMockDCGM(ctrl)
			client.EXPECT().GetNvLinkP2PStatus().Return(dcgm.NvLinkP2PStatus{Gpus: [][]dcgm.Link_State{{0, 1}, {1, 0}}}, nil)
			counter := counters.Counter{FieldName: counters.DCGMExpP2PStatus}
			c := p2pStatusCollector{baseExpCollector: baseExpCollector{client: client, config: &appconfig.Config{EnableOTLP: true}, counter: counter, deviceWatchList: *list}, deviceInfoProvider: p2pSnapshot}
			c.now = func() time.Time { return time.UnixMicro(123) }
			metrics, err := c.GetMetrics()
			require.NoError(t, err)
			require.Len(t, metrics[counter], 2)
			for _, m := range metrics[counter] {
				require.NotNil(t, m.Source)
				assert.Equal(t, int64(123), m.Timestamp)
				assert.Equal(t, synthetic, m.Source.SyntheticGPUUUID, "provenance must follow the metric's own discovery snapshot")
				if synthetic {
					assert.Empty(t, m.Source.PeerGPUUUID)
				} else {
					assert.NotEmpty(t, m.Source.PeerGPUUUID)
				}
			}
		})
	}
}
