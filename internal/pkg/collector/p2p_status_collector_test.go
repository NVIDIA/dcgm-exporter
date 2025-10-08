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
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

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
				{FieldID: 1, FieldName: "random1"},
				{FieldID: 2, FieldName: "random2"},
			},
			want: false,
		},
		{
			name: "counter present",
			arg: counters.CounterList{
				{FieldID: 1, FieldName: counters.DCGMExpP2PStatus},
				{FieldID: 2, FieldName: "random2"},
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

func TestP2PStatusToString(t *testing.T) {
	tests := []struct {
		name   string
		input  uint64
		output string
	}{
		{LinkStatusOK, 0, LinkStatusOK},
		{LinkStatusChipsetNotSupported, 1, LinkStatusChipsetNotSupported},
		{LinkStatusTopologyNotSupported, 2, LinkStatusTopologyNotSupported},
		{LinkStatusDisabledByRegKey, 3, LinkStatusDisabledByRegKey},
		{LinkStatusNotSupported, 4, LinkStatusNotSupported},
		{LinkStatusUnknown, 99, LinkStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.output, p2pStatusToString(tt.input))
		})
	}
}

func TestNewP2PStatusCollector(t *testing.T) {
	counter := counters.Counter{FieldID: 1, FieldName: counters.DCGMExpP2PStatus}
	counterList := counters.CounterList{counter}
	config := &appconfig.Config{}
	hostname := "testhost"

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Set up mock DCGM provider
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
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

type fakeP2PStatus struct {
	Gpus [][]dcgm.Link_State
}

func (f fakeP2PStatus) toDCGM() dcgm.NvLinkP2PStatus {
	return dcgm.NvLinkP2PStatus{Gpus: f.Gpus}
}

func TestP2PStatusCollector_GetMetrics(t *testing.T) {
	counter := counters.Counter{FieldID: 1, FieldName: counters.DCGMExpP2PStatus}
	counterList := counters.CounterList{counter}
	config := &appconfig.Config{}
	hostname := "testhost"

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Set up mock DCGM provider
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
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
