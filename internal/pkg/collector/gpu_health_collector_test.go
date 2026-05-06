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

package collector

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

func TestNewGPUHealthStatusCollector(t *testing.T) {
	type testCase struct {
		name                 string
		counterList          counters.CounterList
		setDCGMproviderState func(*mockdcgm.MockDCGM)
		assertResult         func(Collector, error)
	}

	testCases := []testCase{
		{
			name:        "returns error when collector is disabled",
			counterList: []counters.Counter{},
			assertResult: func(c Collector, err error) {
				assert.Nil(t, c)
				assert.Error(t, err)
			},
		},
		{
			name: "returns no errors, whe collector is enabled",
			counterList: []counters.Counter{
				{
					FieldName: "DCGM_EXP_GPU_HEALTH_STATUS",
				},
			},
			setDCGMproviderState: func(mockDCGMProvider *mockdcgm.MockDCGM) {
				mockDCGMProvider.EXPECT().DestroyGroup(gomock.Any()).Return(errors.New("boom!")).Times(2)
			},
			assertResult: func(c Collector, err error) {
				assert.NotNil(t, c)
				assert.NoError(t, err)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Initialize the mock controller
			ctrl := gomock.NewController(t)

			mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

			realDCGM := dcgmprovider.Client()
			defer func() {
				dcgmprovider.SetClient(realDCGM)
			}()

			dcgmprovider.SetClient(mockDCGMProvider)
			if tc.setDCGMproviderState != nil {
				tc.setDCGMproviderState(mockDCGMProvider)
			}
			setDefaultExpectationsForGPUHealthStatusCollectorMockDCGMProvider(t, mockDCGMProvider)

			// Create a new collector
			collector, err := NewGPUHealthStatusCollector(tc.counterList,
				"",
				&appconfig.Config{},
				getDefaultDeviceWatchListForGPUHealthStatusCollectorMockDCGMProvider(ctrl),
			)

			tc.assertResult(collector, err)
			if collector != nil {
				// Cleanup the collector
				assert.NotPanics(t, func() {
					collector.Cleanup()
				})
			}
		})
	}
}

func setDefaultExpectationsForGPUHealthStatusCollectorMockDCGMProvider(t *testing.T, mockDCGMProvider *mockdcgm.MockDCGM) {
	t.Helper()
	setDefaultExpectationsForGPUHealthStatusCollectorMockDCGMProviderWithGroup(t, mockDCGMProvider,
		[]dcgm.GroupEntityPair{{EntityId: uint(0), EntityGroupId: dcgm.FE_GPU}}, nil)
}

func setDefaultExpectationsForGPUHealthStatusCollectorMockDCGMProviderWithGroup(
	t *testing.T,
	mockDCGMProvider *mockdcgm.MockDCGM,
	groupEntities []dcgm.GroupEntityPair,
	healthResponseOverride *dcgm.HealthResponse,
) {
	t.Helper()
	mockDCGMProvider.EXPECT().GetSupportedDevices().Return([]uint{0}, nil).AnyTimes()
	mockDCGMProvider.EXPECT().CreateGroup(gomock.Cond(func(x any) bool {
		return strings.HasPrefix(x.(string), "gpu_health_monitor_")
	})).Return(dcgm.GroupHandle{}, nil).AnyTimes()
	mockDCGMProvider.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Eq(uint(0))).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().HealthSet(gomock.Any(), gomock.Eq(dcgm.DCGM_HEALTH_WATCH_ALL)).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Eq(uint(0))).Return(dcgm.Device{}, nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).AnyTimes()
	mockDCGMProvider.EXPECT().CreateGroup(gomock.Cond(func(x any) bool {
		return strings.HasPrefix(x.(string), "gpu-collector-group")
	})).Return(dcgm.GroupHandle{}, nil).AnyTimes()
	mockDCGMProvider.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Eq(uint(0))).Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().FieldGroupCreate(gomock.Cond(func(x any) bool {
		return strings.HasPrefix(x.(string), "gpu-collector-fieldgroup")
	}), gomock.Any()).Return(dcgm.FieldHandle{}, nil).AnyTimes()
	mockDCGMProvider.EXPECT().WatchFieldsWithGroupEx(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()
	mockDCGMProvider.EXPECT().EntityGetLatestValues(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]dcgm.FieldValue_v1{}, nil).AnyTimes()

	healthCheckResponse := dcgm.HealthResponse{
		OverallHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
		Incidents: []dcgm.Incident{
			{
				System: dcgm.DCGM_HEALTH_WATCH_THERMAL,
				Health: dcgm.DCGM_HEALTH_RESULT_FAIL,
				Error: dcgm.DiagErrorDetail{
					Message: "boom!",
					Code:    dcgm.DCGM_FR_THERMAL_VIOLATIONS,
				},
				EntityInfo: dcgm.GroupEntityPair{
					EntityGroupId: dcgm.FE_GPU,
					EntityId:      uint(0),
				},
			},
		},
	}
	if healthResponseOverride != nil {
		healthCheckResponse = *healthResponseOverride
	}

	mockDCGMProvider.EXPECT().HealthCheck(gomock.Any()).Return(healthCheckResponse, nil).AnyTimes()
	mockDCGMProvider.EXPECT().GetGroupInfo(gomock.Any()).Return(&dcgm.GroupInfo{
		EntityList: groupEntities,
	}, nil).AnyTimes()
}

func getDefaultDeviceWatchListForGPUHealthStatusCollectorMockDCGMProvider(ctrl *gomock.Controller) devicewatchlistmanager.WatchList {
	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	mockDeviceInfo.EXPECT().GPU(uint(0)).Return(mockGPU).AnyTimes()

	return *devicewatchlistmanager.NewWatchList(mockDeviceInfo,
		[]dcgm.Short{42},
		[]dcgm.Short{524},
		deviceWatcher,
		int64(1))
}

func TestGPUHealthStatusCollector_GetMetrics_ErrorHandling(t *testing.T) {
	var counterList counters.CounterList = []counters.Counter{
		{
			FieldName: "DCGM_EXP_GPU_HEALTH_STATUS",
		},
		{
			FieldName: "DCGM_FI_DRIVER_VERSION",
			PromType:  "label",
			FieldID:   dcgm.DCGM_FI_DEV_VGPU_DRIVER_VERSION,
		},
	}

	type testCase struct {
		name                 string
		setDCGMproviderState func(*mockdcgm.MockDCGM)
		asserResult          func(MetricsByCounter, error)
	}

	testCases := []testCase{
		{
			name: "returns Metrics without errors",
			asserResult: func(metrics MetricsByCounter, err error) {
				require.NoError(t, err)
				// We expect 1 metric: DCGM_EXP_GPU_HEALTH_STATUS
				require.Len(t, metrics, 1)
				// We get metric value with 0 index
				metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)]
				assert.Len(t, metricValues, len(gpuHealthChecks), "number of metric values doesn't match to number of healthchecks")

				var thermalViolationsFound bool

				for _, value := range metricValues {
					healthWatch := value.Labels["health_watch"]
					healthErrorCode := value.Labels["health_error_code"]
					if healthWatch == "THERMAL" && healthErrorCode == "DCGM_FR_THERMAL_VIOLATIONS" {
						assert.Equal(t, "20", value.Value)
						thermalViolationsFound = true
					} else {
						assert.Equal(t, "0", value.Value)
					}
				}
				assert.True(t, thermalViolationsFound, "expected DCGM_FR_THERMAL_VIOLATIONS error not found")
			},
		},

		{
			name: "When HealthCheck returns error",
			setDCGMproviderState: func(mockDCGMProvider *mockdcgm.MockDCGM) {
				// Clear expectations for SomeMethod
				mockDCGMProvider.EXPECT().HealthCheck(gomock.Any()).Return(dcgm.HealthResponse{},
					errors.New("boom!"))
			},
			asserResult: func(metrics MetricsByCounter, err error) {
				assert.Error(t, err)
				assert.Empty(t, metrics)
			},
		},
		{
			name: "When GetGroupInfo returns error",
			setDCGMproviderState: func(mockDCGMProvider *mockdcgm.MockDCGM) {
				mockDCGMProvider.EXPECT().GetGroupInfo(gomock.Any()).Return(nil, errors.New("boom!")).AnyTimes()
			},
			asserResult: func(metrics MetricsByCounter, err error) {
				assert.Error(t, err)
				assert.Empty(t, metrics)
			},
		},
		{
			name: "When EntityGetLatestValues returns error",
			setDCGMproviderState: func(mockDCGMProvider *mockdcgm.MockDCGM) {
				mockDCGMProvider.EXPECT().EntityGetLatestValues(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]dcgm.FieldValue_v1{}, errors.New("boom!")).AnyTimes()
			},
			asserResult: func(metrics MetricsByCounter, err error) {
				assert.Error(t, err)
				assert.Empty(t, metrics)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Initialize the mock controller
			ctrl := gomock.NewController(t)

			mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

			realDCGM := dcgmprovider.Client()
			defer func() {
				dcgmprovider.SetClient(realDCGM)
			}()

			dcgmprovider.SetClient(mockDCGMProvider)

			// We need to set new expectations, and then set the default ones.
			if tc.setDCGMproviderState != nil {
				tc.setDCGMproviderState(mockDCGMProvider)
			}

			setDefaultExpectationsForGPUHealthStatusCollectorMockDCGMProvider(t, mockDCGMProvider)

			// Create a new collector
			collector, err := NewGPUHealthStatusCollector(counterList,
				"",
				&appconfig.Config{
					UseOldNamespace: true,
				},
				getDefaultDeviceWatchListForGPUHealthStatusCollectorMockDCGMProvider(ctrl),
			)

			require.NoError(t, err)

			metrics, err := collector.GetMetrics()

			tc.asserResult(metrics, err)

			ctrl.Finish() // This will finish the current controller
		})
	}
}

func TestIsDCGMExpGPUHealthStatusEnabled(t *testing.T) {
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
			name: "counter event count disabled",
			arg: counters.CounterList{
				counters.Counter{
					FieldID:   1,
					FieldName: "random1",
				},
				counters.Counter{
					FieldID:   2,
					FieldName: "random2",
				},
			},
			want: false,
		},
		{
			name: "counter event count enabled",
			arg: counters.CounterList{
				counters.Counter{
					FieldID:   1,
					FieldName: counters.DCGMExpGPUHealthStatus,
				},
				counters.Counter{
					FieldID:   2,
					FieldName: "random2",
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, IsDCGMExpGPUHealthStatusEnabled(tt.arg), "unexpected response")
		})
	}
}

func TestHealthSystemWatchToString(t *testing.T) {
	type testCase struct {
		name         string
		healthSystem dcgm.HealthSystem
		expected     string
	}

	testCases := []testCase{
		{
			name:         "returns POWER when dcgm.DCGM_HEALTH_WATCH_POWER",
			healthSystem: dcgm.DCGM_HEALTH_WATCH_POWER,
			expected:     "POWER",
		},
		{
			name:         "returns ALL when dcgm.DCGM_HEALTH_WATCH_ALL",
			healthSystem: dcgm.DCGM_HEALTH_WATCH_ALL,
			expected:     "ALL",
		},
		{
			name:         "returns CONNECTX when dcgm.DCGM_HEALTH_WATCH_CONNECTX",
			healthSystem: dcgm.DCGM_HEALTH_WATCH_CONNECTX,
			expected:     "CONNECTX",
		},
		{
			name:         "returns UNKNOWN(N) when dcgm.HealthSystem is unknown",
			healthSystem: dcgm.HealthSystem(100500),
			expected:     "UNKNOWN(100500)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := healthSystemWatchToString(tc.healthSystem)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestInitGPUHealthEntityIncidentDefaults_ReplacesNilInnerMap(t *testing.T) {
	key := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	byEntity := map[dcgm.GroupEntityPair]map[dcgm.HealthSystem]dcgm.Incident{
		key: nil,
	}

	initGPUHealthEntityIncidentDefaults(byEntity, key)

	require.NotNil(t, byEntity[key])
	for _, hs := range gpuHealthChecks {
		inc := byEntity[key][hs]
		assert.Equal(t, hs, inc.System)
		assert.Equal(t, dcgm.DCGM_HEALTH_RESULT_PASS, inc.Health)
	}
}

func TestInitGPUHealthEntityIncidentDefaults_Idempotent(t *testing.T) {
	key := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 1}
	byEntity := map[dcgm.GroupEntityPair]map[dcgm.HealthSystem]dcgm.Incident{}

	initGPUHealthEntityIncidentDefaults(byEntity, key)
	first := byEntity[key]
	initGPUHealthEntityIncidentDefaults(byEntity, key)
	require.Equal(t, first, byEntity[key])
}

func TestApplyGPUHealthIncidents_SkipsNilInnerMapWithoutPanic(t *testing.T) {
	key := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	byEntity := map[dcgm.GroupEntityPair]map[dcgm.HealthSystem]dcgm.Incident{
		key: nil,
	}
	inc := dcgm.Incident{
		EntityInfo: key,
		System:     dcgm.DCGM_HEALTH_WATCH_THERMAL,
		Health:     dcgm.DCGM_HEALTH_RESULT_FAIL,
	}

	assert.NotPanics(t, func() {
		applyGPUHealthIncidents(byEntity, []dcgm.Incident{inc})
	})
	assert.Nil(t, byEntity[key])
}

func TestApplyGPUHealthIncidents_AppliesIncident(t *testing.T) {
	key := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	byEntity := map[dcgm.GroupEntityPair]map[dcgm.HealthSystem]dcgm.Incident{}
	initGPUHealthEntityIncidentDefaults(byEntity, key)

	inc := dcgm.Incident{
		EntityInfo: key,
		System:     dcgm.DCGM_HEALTH_WATCH_THERMAL,
		Health:     dcgm.DCGM_HEALTH_RESULT_FAIL,
		Error: dcgm.DiagErrorDetail{
			Message: "thermal",
			Code:    dcgm.DCGM_FR_THERMAL_VIOLATIONS,
		},
	}
	applyGPUHealthIncidents(byEntity, []dcgm.Incident{inc})

	got := byEntity[key][dcgm.DCGM_HEALTH_WATCH_THERMAL]
	assert.Equal(t, dcgm.DCGM_HEALTH_RESULT_FAIL, got.Health)
	assert.Equal(t, "thermal", got.Error.Message)
}

func TestApplyGPUHealthIncidents_SkipsUnknownEntity(t *testing.T) {
	key := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	byEntity := map[dcgm.GroupEntityPair]map[dcgm.HealthSystem]dcgm.Incident{}
	initGPUHealthEntityIncidentDefaults(byEntity, key)

	unknown := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 999}
	inc := dcgm.Incident{
		EntityInfo: unknown,
		System:     dcgm.DCGM_HEALTH_WATCH_THERMAL,
		Health:     dcgm.DCGM_HEALTH_RESULT_FAIL,
	}

	assert.NotPanics(t, func() {
		applyGPUHealthIncidents(byEntity, []dcgm.Incident{inc})
	})
	_, ok := byEntity[unknown]
	assert.False(t, ok)
}

// GetGroupInfo can list an FE_GPU that is not part of monitoringInfoInGroup. This is a defensive
// regression test for group-vs-monitoring divergence: we only assert that GetMetrics completes
// without panic because this collector still emits metrics only for monitoringInfoInGroup.
func TestGPUHealthStatusCollector_GetMetrics_HealthGroupContainsUnmonitoredGPU_NoPanic(t *testing.T) {
	ctrl := gomock.NewController(t)

	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	groupList := []dcgm.GroupEntityPair{
		{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		{EntityGroupId: dcgm.FE_GPU, EntityId: 99},
	}
	customHealth := &dcgm.HealthResponse{
		OverallHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
		Incidents: []dcgm.Incident{
			{
				System: dcgm.DCGM_HEALTH_WATCH_THERMAL,
				Health: dcgm.DCGM_HEALTH_RESULT_FAIL,
				Error: dcgm.DiagErrorDetail{
					Message: "boom!",
					Code:    dcgm.DCGM_FR_THERMAL_VIOLATIONS,
				},
				EntityInfo: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 99},
			},
		},
	}
	setDefaultExpectationsForGPUHealthStatusCollectorMockDCGMProviderWithGroup(t, mockDCGMProvider, groupList, customHealth)

	counterList := counters.CounterList{
		{FieldName: counters.DCGMExpGPUHealthStatus},
	}

	collector, err := NewGPUHealthStatusCollector(counterList,
		"",
		&appconfig.Config{},
		getDefaultDeviceWatchListForGPUHealthStatusCollectorMockDCGMProvider(ctrl),
	)
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		metrics, gErr := collector.GetMetrics()
		require.NoError(t, gErr)
		require.Len(t, metrics, 1)
		for _, values := range metrics {
			assert.Len(t, values, len(gpuHealthChecks))
		}
	})
}

func TestHealthCheckErrorToString(t *testing.T) {
	testCases := []struct {
		name     string
		code     dcgm.HealthCheckErrorCode
		expected string
	}{
		{
			name:     "returns DCGM_FR_OK for DCGM_FR_OK",
			code:     dcgm.DCGM_FR_OK,
			expected: "DCGM_FR_OK",
		},
		{
			name:     "returns DCGM_FR_FALLEN_OFF_BUS for DCGM_FR_FALLEN_OFF_BUS",
			code:     dcgm.DCGM_FR_FALLEN_OFF_BUS,
			expected: "DCGM_FR_FALLEN_OFF_BUS",
		},
		{
			name:     "returns DCGM_FR_GFLOPS_THRESHOLD_VIOLATION (110)",
			code:     dcgm.DCGM_FR_GFLOPS_THRESHOLD_VIOLATION,
			expected: "DCGM_FR_GFLOPS_THRESHOLD_VIOLATION",
		},
		{
			name:     "returns DCGM_FR_BROKEN_P2P_NVLINK_WRITER_DEVICE (116)",
			code:     dcgm.DCGM_FR_BROKEN_P2P_NVLINK_WRITER_DEVICE,
			expected: "DCGM_FR_BROKEN_P2P_NVLINK_WRITER_DEVICE",
		},
		{
			name:     "returns DCGM_FR_UNKNOWN(N) for unmapped code",
			code:     dcgm.HealthCheckErrorCode(424242),
			expected: "DCGM_FR_UNKNOWN(424242)",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, healthCheckErrorToString(tc.code))
		})
	}
}

func TestApplyGPUHealthIncidents_WatchAllIncidentRoutedToAllSlot(t *testing.T) {
	key := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	byEntity := map[dcgm.GroupEntityPair]map[dcgm.HealthSystem]dcgm.Incident{}
	initGPUHealthEntityIncidentDefaults(byEntity, key)

	inc := dcgm.Incident{
		EntityInfo: key,
		System:     dcgm.DCGM_HEALTH_WATCH_ALL,
		Health:     dcgm.DCGM_HEALTH_RESULT_FAIL,
		Error: dcgm.DiagErrorDetail{
			Message: "GPU fallen off bus",
			Code:    dcgm.DCGM_FR_FALLEN_OFF_BUS,
		},
	}
	applyGPUHealthIncidents(byEntity, []dcgm.Incident{inc})

	got := byEntity[key][dcgm.DCGM_HEALTH_WATCH_ALL]
	assert.Equal(t, dcgm.DCGM_HEALTH_RESULT_FAIL, got.Health)
	assert.Equal(t, dcgm.DCGM_FR_FALLEN_OFF_BUS, got.Error.Code)

	// All other subsystem slots must stay PASS -- devastating XIDs belong to the ALL slot only,
	// they are not fanned out.
	for _, hs := range gpuHealthChecks {
		if hs == dcgm.DCGM_HEALTH_WATCH_ALL {
			continue
		}
		assert.Equalf(t, dcgm.DCGM_HEALTH_RESULT_PASS, byEntity[key][hs].Health,
			"subsystem %v should not be marked FAIL by a DCGM_HEALTH_WATCH_ALL incident",
			healthSystemWatchToString(hs))
	}
}

func TestApplyGPUHealthIncidents_WatchAllAndSubsystemIndependent(t *testing.T) {
	key := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	byEntity := map[dcgm.GroupEntityPair]map[dcgm.HealthSystem]dcgm.Incident{}
	initGPUHealthEntityIncidentDefaults(byEntity, key)

	incidents := []dcgm.Incident{
		{
			EntityInfo: key,
			System:     dcgm.DCGM_HEALTH_WATCH_PCIE,
			Health:     dcgm.DCGM_HEALTH_RESULT_FAIL,
			Error:      dcgm.DiagErrorDetail{Code: dcgm.DCGM_FR_PCI_REPLAY_RATE},
		},
		{
			EntityInfo: key,
			System:     dcgm.DCGM_HEALTH_WATCH_ALL,
			Health:     dcgm.DCGM_HEALTH_RESULT_FAIL,
			Error:      dcgm.DiagErrorDetail{Code: dcgm.DCGM_FR_FALLEN_OFF_BUS},
		},
	}
	applyGPUHealthIncidents(byEntity, incidents)

	assert.Equal(t, dcgm.DCGM_HEALTH_RESULT_FAIL, byEntity[key][dcgm.DCGM_HEALTH_WATCH_PCIE].Health)
	assert.Equal(t, dcgm.DCGM_FR_PCI_REPLAY_RATE, byEntity[key][dcgm.DCGM_HEALTH_WATCH_PCIE].Error.Code)
	assert.Equal(t, dcgm.DCGM_HEALTH_RESULT_FAIL, byEntity[key][dcgm.DCGM_HEALTH_WATCH_ALL].Health)
	assert.Equal(t, dcgm.DCGM_FR_FALLEN_OFF_BUS, byEntity[key][dcgm.DCGM_HEALTH_WATCH_ALL].Error.Code)

	for _, hs := range gpuHealthChecks {
		if hs == dcgm.DCGM_HEALTH_WATCH_PCIE || hs == dcgm.DCGM_HEALTH_WATCH_ALL {
			continue
		}
		assert.Equalf(t, dcgm.DCGM_HEALTH_RESULT_PASS, byEntity[key][hs].Health,
			"subsystem %v should remain PASS", healthSystemWatchToString(hs))
	}
}

// TestGPUHealthStatusCollector_GetMetrics_WatchAllIncident exercises the full scrape path with an
// injected DCGM_HEALTH_WATCH_ALL incident (representing a devastating XID such as GPU fallen off
// bus). It asserts the emitted row shape: one row per entry in gpuHealthChecks, with the ALL row
// carrying the incident.
func TestGPUHealthStatusCollector_GetMetrics_WatchAllIncident(t *testing.T) {
	ctrl := gomock.NewController(t)

	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	groupList := []dcgm.GroupEntityPair{
		{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
	}
	customHealth := &dcgm.HealthResponse{
		OverallHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
		Incidents: []dcgm.Incident{
			{
				System: dcgm.DCGM_HEALTH_WATCH_ALL,
				Health: dcgm.DCGM_HEALTH_RESULT_FAIL,
				Error: dcgm.DiagErrorDetail{
					Message: "GPU has fallen off the bus",
					Code:    dcgm.DCGM_FR_FALLEN_OFF_BUS,
				},
				EntityInfo: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
			},
		},
	}
	setDefaultExpectationsForGPUHealthStatusCollectorMockDCGMProviderWithGroup(t, mockDCGMProvider, groupList, customHealth)

	counterList := counters.CounterList{
		{FieldName: counters.DCGMExpGPUHealthStatus},
	}

	collector, err := NewGPUHealthStatusCollector(counterList,
		"",
		&appconfig.Config{},
		getDefaultDeviceWatchListForGPUHealthStatusCollectorMockDCGMProvider(ctrl),
	)
	require.NoError(t, err)

	metrics, err := collector.GetMetrics()
	require.NoError(t, err)
	require.Len(t, metrics, 1)

	var values []Metric
	for _, v := range metrics {
		values = v
	}
	assert.Len(t, values, len(gpuHealthChecks), "expected one row per entry in gpuHealthChecks")

	var allRowFound bool
	for _, value := range values {
		healthWatch := value.Labels["health_watch"]
		healthErrorCode := value.Labels["health_error_code"]
		switch healthWatch {
		case "ALL":
			allRowFound = true
			assert.Equal(t, "20", value.Value, "ALL row should carry FAIL (20) when a devastating XID is reported")
			assert.Equal(t, "DCGM_FR_FALLEN_OFF_BUS", healthErrorCode)
		default:
			assert.Equalf(t, "0", value.Value, "subsystem %q should remain PASS when only a DCGM_HEALTH_WATCH_ALL incident is present", healthWatch)
			assert.Equal(t, "DCGM_FR_OK", healthErrorCode)
		}
	}
	assert.True(t, allRowFound, "expected a row with health_watch=ALL")
}
