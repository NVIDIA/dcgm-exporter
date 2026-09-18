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
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mockdevicewatcher "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatcher"
	mockos "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/os"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

func TestToMetric(t *testing.T) {
	fieldValue := [4096]byte{}
	fieldValue[0] = 42
	mi := devicemonitoring.Info{
		DeviceInfo: dcgm.Device{
			UUID: "fake0",
			Identifiers: dcgm.DeviceIdentifiers{
				Model: "NVIDIA T400 4GB",
			},
			PCI: dcgm.PCIInfo{
				BusID: "00000000:0000:0000.0",
			},
		},
	}
	values := []dcgm.FieldValue_v1{
		{
			FieldID:   150,
			FieldType: dcgm.DCGM_FT_INT64,
			Value:     fieldValue,
		},
	}

	c := []counters.Counter{
		{
			FieldID:   150,
			FieldName: "DCGM_FI_DEV_GPU_TEMP",
			PromType:  "gauge",
			Help:      "Temperature Help info",
		},
	}

	type testCase struct {
		replaceBlanksInModelName bool
		expectedGPUModelName     string
	}

	testCases := []testCase{
		{
			replaceBlanksInModelName: true,
			expectedGPUModelName:     "NVIDIA-T400-4GB",
		},
		{
			replaceBlanksInModelName: false,
			expectedGPUModelName:     "NVIDIA T400 4GB",
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("When replaceBlanksInModelName is %t", tc.replaceBlanksInModelName), func(t *testing.T) {
			metrics := make(map[counters.Counter][]Metric)
			toMetric(metrics, values, c, mi, false, "", tc.replaceBlanksInModelName)
			assert.Len(t, metrics, 1)
			// We get metric value with 0 index
			metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)]
			assert.Equal(t, "42", metricValues[0].Value)
			assert.Equal(t, tc.expectedGPUModelName, metricValues[0].GPUModelName)

			assert.Equal(t, mi.DeviceInfo.UUID, metricValues[0].GPUUUID)
			assert.Equal(t, mi.DeviceInfo.PCI.BusID, metricValues[0].GPUPCIBusID)
		})
	}
}

func TestToMetricLogsBlankValue(t *testing.T) {
	buf := setupDebugLogCapture(t)

	counter := counters.Counter{
		FieldID:   150,
		FieldName: "DCGM_FI_DEV_GPU_TEMP",
		PromType:  "gauge",
	}
	mi := devicemonitoring.Info{
		Entity:     dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		DeviceInfo: dcgm.Device{GPU: 0},
	}
	values := []dcgm.FieldValue_v1{
		{
			FieldID:   counter.FieldID,
			FieldType: dcgm.DCGM_FT_INT64,
			Value:     createInt64ByteArray(dcgm.DCGM_FT_INT64_BLANK),
		},
	}
	metrics := MetricsByCounter{}

	toMetric(metrics, values, []counters.Counter{counter}, mi, false, "host-a", false)

	assert.Empty(t, metrics)

	got := findLogRecord(t, buf, blankValueSkippedMessage)
	assert.Equal(t, blankValueSkippedMessage, got["msg"])
	assert.Equal(t, float64(counter.FieldID), got["fieldID"])
	assert.Equal(t, counter.FieldName, got["fieldName"])
	assert.Equal(t, dcgm.FE_GPU.String(), got["entityType"])
	assert.Equal(t, float64(0), got["entityID"])
}

func TestToMetricHonorsDCGMStatusBeforeValue(t *testing.T) {
	mi := devicemonitoring.Info{
		Entity:     dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		DeviceInfo: dcgm.Device{GPU: 0},
	}

	tests := []struct {
		name      string
		fieldID   dcgm.Short
		fieldType uint
		status    int
		value     [4096]byte
		wantValue string
		wantLog   string
	}{
		{
			name:      "non-OK numeric zero is skipped",
			fieldID:   dcgm.DCGM_FI_DEV_XID_ERRORS,
			fieldType: dcgm.DCGM_FT_INT64,
			status:    dcgm.DCGM_ST_NO_DATA,
			value:     createInt64ByteArray(0),
			wantLog:   nonOKStatusSkippedMessage,
		},
		{
			name:      "OK numeric zero is emitted",
			fieldID:   150,
			fieldType: dcgm.DCGM_FT_INT64,
			status:    dcgm.DCGM_ST_OK,
			value:     createInt64ByteArray(0),
			wantValue: "0",
		},
		{
			name:      "non-OK binary value is skipped before conversion",
			fieldID:   dcgm.DCGM_FI_DEV_VGPU_UTILIZATIONS,
			fieldType: dcgm.DCGM_FT_BINARY,
			status:    dcgm.DCGM_ST_NOT_SUPPORTED,
			wantLog:   nonOKStatusSkippedMessage,
		},
		{
			name:      "OK unsupported value is skipped before rendering",
			fieldID:   dcgm.DCGM_FI_DEV_VGPU_UTILIZATIONS,
			fieldType: dcgm.DCGM_FT_BINARY,
			status:    dcgm.DCGM_ST_OK,
			wantLog:   blankValueSkippedMessage,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buf := setupDebugLogCapture(t)
			counter := counters.Counter{
				FieldID:   test.fieldID,
				FieldName: fmt.Sprintf("FIELD_%d", test.fieldID),
				PromType:  "gauge",
			}
			metrics := MetricsByCounter{}

			toMetric(metrics, []dcgm.FieldValue_v1{
				{
					FieldID:   test.fieldID,
					FieldType: test.fieldType,
					Status:    test.status,
					Value:     test.value,
				},
			}, []counters.Counter{counter}, mi, false, "host-a", false)

			if test.wantValue != "" {
				require.Len(t, metrics[counter], 1)
				assert.Equal(t, test.wantValue, metrics[counter][0].Value)
				return
			}

			assert.Empty(t, metrics)
			got := findLogRecord(t, buf, test.wantLog)
			assert.Equal(t, float64(test.fieldID), got["fieldID"])
			assert.Equal(t, counter.FieldName, got["fieldName"])
			if test.wantLog == nonOKStatusSkippedMessage {
				assert.Equal(t, float64(test.status), got["status"])
			}
		})
	}
}

func TestToMetricWhenDCGM_FI_DEV_XID_ERRORSField(t *testing.T) {
	c := []counters.Counter{
		{
			FieldID:   dcgm.DCGM_FI_DEV_XID_ERRORS,
			FieldName: "DCGM_FI_DEV_GPU_TEMP",
			PromType:  "gauge",
			Help:      "Temperature Help info",
		},
	}

	mi := devicemonitoring.Info{
		DeviceInfo: dcgm.Device{
			UUID: "fake0",
			Identifiers: dcgm.DeviceIdentifiers{
				Model: "NVIDIA T400 4GB",
			},
			PCI: dcgm.PCIInfo{
				BusID: "00000000:0000:0000.0",
			},
		},
	}

	type testCase struct {
		name        string
		fieldValue  byte
		expectedErr string
	}

	testCases := []testCase{
		{
			name:        "when DCGM_FI_DEV_XID_ERRORS has no error",
			fieldValue:  0,
			expectedErr: xidErrCodeToText[0],
		},
		{
			name:        "when DCGM_FI_DEV_XID_ERRORS has known value",
			fieldValue:  42,
			expectedErr: xidErrCodeToText[42],
		},
		{
			name:        "when DCGM_FI_DEV_XID_ERRORS has unknown value",
			fieldValue:  255,
			expectedErr: unknownErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fieldValue := [4096]byte{}
			fieldValue[0] = tc.fieldValue
			values := []dcgm.FieldValue_v1{
				{
					FieldID:   dcgm.DCGM_FI_DEV_XID_ERRORS,
					FieldType: dcgm.DCGM_FT_INT64,
					Value:     fieldValue,
				},
			}

			metrics := make(map[counters.Counter][]Metric)
			toMetric(metrics, values, c, mi, false, "", false)
			assert.Len(t, metrics, 1)
			// We get metric value with 0 index
			metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)]
			assert.Equal(t, fmt.Sprint(tc.fieldValue), metricValues[0].Value)
			assert.Contains(t, metricValues[0].Attributes, "err_code")
			assert.Equal(t, fmt.Sprint(tc.fieldValue), metricValues[0].Attributes["err_code"])
			assert.Contains(t, metricValues[0].Attributes, "err_msg")
			assert.Equal(t, tc.expectedErr, metricValues[0].Attributes["err_msg"])

			assert.Equal(t, mi.DeviceInfo.UUID, metricValues[0].GPUUUID)
			assert.Equal(t, mi.DeviceInfo.PCI.BusID, metricValues[0].GPUPCIBusID)
		})
	}
}

func TestDCGMCollectorGetMetricsHandlesActionableDCGMErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantExit bool
	}{
		{
			name:     "connection not valid exits",
			err:      &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID},
			wantExit: true,
		},
		{
			name:     "uninitialized status exits",
			err:      &dcgm.Error{Code: dcgm.DCGM_ST_UNINITIALIZED},
			wantExit: true,
		},
		{
			name:     "library not found exits",
			err:      &dcgm.Error{Code: dcgm.DCGM_ST_LIBRARY_NOT_FOUND},
			wantExit: true,
		},
		{
			name:     "init error exits",
			err:      &dcgm.Error{Code: dcgm.DCGM_ST_INIT_ERROR},
			wantExit: true,
		},
		{
			name:     "nvml not loaded exits",
			err:      &dcgm.Error{Code: dcgm.DCGM_ST_NVML_NOT_LOADED},
			wantExit: true,
		},
		{
			name: "no permission returns error without exit",
			err:  &dcgm.Error{Code: dcgm.DCGM_ST_NO_PERMISSION},
		},
		{
			name: "requires root returns error without exit",
			err:  &dcgm.Error{Code: dcgm.DCGM_ST_REQUIRES_ROOT},
		},
		{
			name: "version mismatch returns error without exit",
			err:  &dcgm.Error{Code: dcgm.DCGM_ST_VER_MISMATCH},
		},
		{
			name: "non dcgm error returns error without exit",
			err:  assert.AnError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			realDCGM := dcgmprovider.Client()
			t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

			realOS := os
			t.Cleanup(func() { os = realOS })

			ctrl := gomock.NewController(t)
			mockDCGM := mockdcgm.NewMockDCGM(ctrl)
			dcgmprovider.SetClient(mockDCGM)

			mockOS := mockos.NewMockOS(ctrl)
			os = mockOS
			if tc.wantExit {
				mockOS.EXPECT().Exit(1)
			}

			counter := counters.Counter{FieldID: 150, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
			deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
			deviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
			deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{MajorRange: []int{-1}}).AnyTimes()
			deviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
			deviceInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
				DeviceInfo: dcgm.Device{GPU: 0, UUID: "GPU-0"},
			}).AnyTimes()

			watchList := *devicewatchlistmanager.NewWatchList(
				deviceInfo,
				[]dcgm.Short{counter.FieldID},
				nil,
				devicewatcher.NewDeviceWatcher(),
				1,
			)
			collector := &DCGMCollector{
				counters:        []counters.Counter{counter},
				deviceWatchList: watchList,
				hostname:        "host-a",
			}

			mockDCGM.EXPECT().
				EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
				Return(nil, tc.err)

			got, err := collector.GetMetrics()

			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func int64FieldValue(fieldID dcgm.Short, v byte) dcgm.FieldValue_v1 {
	fieldValue := [4096]byte{}
	fieldValue[0] = v
	return dcgm.FieldValue_v1{FieldID: fieldID, FieldType: dcgm.DCGM_FT_INT64, Value: fieldValue}
}

// profilingFieldValue builds a profiling sample with explicit status and timestamp metadata.
func profilingFieldValue(fieldID dcgm.Short, status int, timestamp int64, value byte) dcgm.FieldValue_v1 {
	fieldValue := int64FieldValue(fieldID, value)
	fieldValue.Status = status
	fieldValue.TS = timestamp
	return fieldValue
}

// testProfilingCounter returns the profiling counter used by collector lifecycle tests.
func testProfilingCounter() counters.Counter {
	return counters.Counter{
		FieldID:   dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE,
		FieldName: "DCGM_FI_PROF_GR_ENGINE_ACTIVE",
		PromType:  "gauge",
	}
}

// testGPUDeviceInfo returns a single-GPU provider for collector tests.
func testGPUDeviceInfo(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
	deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	deviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{MajorRange: []int{-1}}).AnyTimes()
	deviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	deviceInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{GPU: 0, UUID: "GPU-0"},
	}).AnyTimes()
	return deviceInfo
}

// newTestProfilingCollector creates a watched collector with one profiling field.
func newTestProfilingCollector(
	t *testing.T,
	deviceInfo deviceinfo.Provider,
	watcher devicewatcher.Watcher,
	interval time.Duration,
) *DCGMCollector {
	t.Helper()
	counter := testProfilingCounter()
	watchList := *devicewatchlistmanager.NewWatchListWithGroups(
		deviceInfo,
		[]dcgm.Short{counter.FieldID},
		nil,
		[]devicewatcher.FieldWatchGroup{{
			Name:         "profiling",
			Fields:       []dcgm.Short{counter.FieldID},
			IntervalMSec: interval.Milliseconds(),
		}},
		watcher,
		interval.Milliseconds(),
	)
	collector, err := NewDCGMCollector([]counters.Counter{counter}, "host-a", &appconfig.Config{}, watchList)
	require.NoError(t, err)
	return collector
}

// TestResolveProfilingStaleWindows verifies field filtering and interval validation.
func TestResolveProfilingStaleWindows(t *testing.T) {
	profilingCounter := counters.Counter{FieldID: 150, FieldName: "DCGM_FI_PROF_TEST"}
	nonProfilingCounter := counters.Counter{FieldID: 151, FieldName: "DCGM_FI_DEV_GPU_TEMP"}
	tests := []struct {
		name            string
		counterList     []counters.Counter
		watchedFields   []dcgm.Short
		fieldWatchGroup []devicewatcher.FieldWatchGroup
		want            map[dcgm.Short]time.Duration
		wantErr         string
	}{
		{
			name:            "profiling field uses twice its interval",
			counterList:     []counters.Counter{profilingCounter, nonProfilingCounter},
			watchedFields:   []dcgm.Short{150, 151},
			fieldWatchGroup: []devicewatcher.FieldWatchGroup{{Fields: []dcgm.Short{150, 151}, IntervalMSec: 75}},
			want:            map[dcgm.Short]time.Duration{150: 150 * time.Millisecond},
		},
		{
			name:            "non-profiling field ignores invalid interval",
			counterList:     []counters.Counter{nonProfilingCounter},
			watchedFields:   []dcgm.Short{151},
			fieldWatchGroup: []devicewatcher.FieldWatchGroup{{Fields: []dcgm.Short{151}, IntervalMSec: 0}},
		},
		{
			name:            "unwatched profiling field is ignored",
			counterList:     []counters.Counter{profilingCounter, nonProfilingCounter},
			watchedFields:   []dcgm.Short{151},
			fieldWatchGroup: []devicewatcher.FieldWatchGroup{{Fields: []dcgm.Short{151}, IntervalMSec: 75}},
		},
		{
			name:            "profiling field rejects invalid interval",
			counterList:     []counters.Counter{profilingCounter},
			watchedFields:   []dcgm.Short{150},
			fieldWatchGroup: []devicewatcher.FieldWatchGroup{{Fields: []dcgm.Short{150}, IntervalMSec: 0}},
			wantErr:         "invalid watch interval",
		},
		{
			name:          "profiling field requires configured interval",
			counterList:   []counters.Counter{profilingCounter},
			watchedFields: []dcgm.Short{150},
			wantErr:       "no configured watch interval",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			windows, err := resolveProfilingStaleWindows(
				test.counterList,
				test.watchedFields,
				test.fieldWatchGroup,
			)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.want, windows)
		})
	}
}

// TestObserveProfilingSamplesUsesTimestampNotValue proves zero values remain valid when timestamps advance.
func TestObserveProfilingSamplesUsesTimestampNotValue(t *testing.T) {
	fieldID := dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE
	entity := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	now := time.Unix(100, 0)
	collector := &DCGMCollector{
		profilingStaleWindows: map[dcgm.Short]time.Duration{fieldID: 100 * time.Millisecond},
		profilingSamples:      make(map[profilingSampleKey]profilingSample),
		now:                   func() time.Time { return now },
	}

	assert.Nil(t, collector.observeProfilingSamples(entity, []dcgm.FieldValue_v1{
		profilingFieldValue(fieldID, dcgm.DCGM_ST_OK, 1, 0),
	}))
	now = now.Add(100 * time.Millisecond)
	assert.Nil(t, collector.observeProfilingSamples(entity, []dcgm.FieldValue_v1{
		profilingFieldValue(fieldID, dcgm.DCGM_ST_OK, 2, 0),
	}))
}

// TestObserveProfilingSamplesDetectsFrozenTimestampAtThreshold verifies the exact stale boundary.
func TestObserveProfilingSamplesDetectsFrozenTimestampAtThreshold(t *testing.T) {
	fieldID := dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE
	entity := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	now := time.Unix(100, 0)
	collector := &DCGMCollector{
		profilingStaleWindows: map[dcgm.Short]time.Duration{fieldID: 100 * time.Millisecond},
		profilingSamples:      make(map[profilingSampleKey]profilingSample),
		now:                   func() time.Time { return now },
	}
	value := profilingFieldValue(fieldID, dcgm.DCGM_ST_OK, 10, 99)

	assert.Nil(t, collector.observeProfilingSamples(entity, []dcgm.FieldValue_v1{value}))
	now = now.Add(99 * time.Millisecond)
	assert.Nil(t, collector.observeProfilingSamples(entity, []dcgm.FieldValue_v1{value}))
	now = now.Add(time.Millisecond)
	reason := collector.observeProfilingSamples(entity, []dcgm.FieldValue_v1{value})
	require.NotNil(t, reason)
	assert.Equal(t, 100*time.Millisecond, reason.staleFor)
}

// TestObserveProfilingSamplesTracksEntitiesAndFieldsIndependently verifies freshness key isolation.
func TestObserveProfilingSamplesTracksEntitiesAndFieldsIndependently(t *testing.T) {
	fieldA := dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE
	fieldB := dcgm.DCGM_FI_PROF_SM_ACTIVE
	entityA := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	entityB := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 1}
	now := time.Unix(100, 0)
	collector := &DCGMCollector{
		profilingStaleWindows: map[dcgm.Short]time.Duration{
			fieldA: 100 * time.Millisecond,
			fieldB: 100 * time.Millisecond,
		},
		profilingSamples: make(map[profilingSampleKey]profilingSample),
		now:              func() time.Time { return now },
	}

	assert.Nil(t, collector.observeProfilingSamples(entityA, []dcgm.FieldValue_v1{
		profilingFieldValue(fieldA, dcgm.DCGM_ST_OK, 1, 0),
		profilingFieldValue(fieldB, dcgm.DCGM_ST_OK, 1, 0),
	}))
	assert.Nil(t, collector.observeProfilingSamples(entityB, []dcgm.FieldValue_v1{
		profilingFieldValue(fieldA, dcgm.DCGM_ST_OK, 1, 0),
	}))

	now = now.Add(100 * time.Millisecond)
	assert.Nil(t, collector.observeProfilingSamples(entityA, []dcgm.FieldValue_v1{
		profilingFieldValue(fieldB, dcgm.DCGM_ST_OK, 2, 0),
	}))
	assert.Nil(t, collector.observeProfilingSamples(entityB, []dcgm.FieldValue_v1{
		profilingFieldValue(fieldA, dcgm.DCGM_ST_OK, 2, 0),
	}))
	reason := collector.observeProfilingSamples(entityA, []dcgm.FieldValue_v1{
		profilingFieldValue(fieldA, dcgm.DCGM_ST_OK, 1, 0),
	})
	require.NotNil(t, reason)
	assert.Equal(t, entityA.EntityId, reason.key.entityID)
	assert.Equal(t, fieldA, reason.key.fieldID)
}

// TestObserveProfilingSamplesRepairsOnlyWatchLifecycleStatuses verifies field-status policy.
func TestObserveProfilingSamplesRepairsOnlyWatchLifecycleStatuses(t *testing.T) {
	fieldID := dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE
	entity := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	tests := []struct {
		name       string
		status     int
		wantRepair bool
	}{
		{name: "not watched", status: dcgm.DCGM_ST_NOT_WATCHED, wantRepair: true},
		{name: "stale data", status: dcgm.DCGM_ST_STALE_DATA, wantRepair: true},
		{name: "not configured", status: dcgm.DCGM_ST_NOT_CONFIGURED},
		{name: "profiling library error", status: dcgm.DCGM_ST_PROFILING_LIBRARY_ERROR},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &DCGMCollector{
				profilingStaleWindows: map[dcgm.Short]time.Duration{fieldID: 100 * time.Millisecond},
				profilingSamples:      make(map[profilingSampleKey]profilingSample),
			}
			reason := collector.observeProfilingSamples(entity, []dcgm.FieldValue_v1{
				profilingFieldValue(fieldID, tt.status, 1, 0),
			})
			assert.Equal(t, tt.wantRepair, reason != nil)
		})
	}
}

// TestDCGMCollectorRepairsOnlyWatchLifecycleAPIErrors verifies API-error classification.
func TestDCGMCollectorRepairsOnlyWatchLifecycleAPIErrors(t *testing.T) {
	fieldID := dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE
	tests := []struct {
		name       string
		err        error
		profiling  bool
		wantRepair bool
	}{
		{name: "not watched", err: &dcgm.Error{Code: dcgm.DCGM_ST_NOT_WATCHED}, profiling: true, wantRepair: true},
		{name: "stale data", err: &dcgm.Error{Code: dcgm.DCGM_ST_STALE_DATA}, profiling: true, wantRepair: true},
		{name: "not configured", err: &dcgm.Error{Code: dcgm.DCGM_ST_NOT_CONFIGURED}, profiling: true},
		{name: "profiling library error", err: &dcgm.Error{Code: dcgm.DCGM_ST_PROFILING_LIBRARY_ERROR}, profiling: true},
		{name: "fatal error", err: &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID}, profiling: true},
		{name: "non DCGM error", err: assert.AnError, profiling: true},
		{name: "no profiling fields", err: &dcgm.Error{Code: dcgm.DCGM_ST_NOT_WATCHED}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &DCGMCollector{}
			if tt.profiling {
				collector.profilingStaleWindows = map[dcgm.Short]time.Duration{fieldID: 2 * time.Second}
			}
			reason := collector.repairReasonForError(tt.err)
			assert.Equal(t, tt.wantRepair, reason != nil)
		})
	}
}

func stringFieldValue(fieldID dcgm.Short, v string) dcgm.FieldValue_v1 {
	fieldValue := [4096]byte{}
	copy(fieldValue[:], v)
	return dcgm.FieldValue_v1{FieldID: fieldID, FieldType: dcgm.DCGM_FT_STRING, Value: fieldValue}
}

func TestDCGMCollectorCleanupRunsAllCleanups(t *testing.T) {
	var calls int
	c := &DCGMCollector{
		cleanups: []func(){
			func() { calls++ },
			func() { calls++ },
		},
	}

	c.Cleanup()
	c.Cleanup()

	assert.Equal(t, 2, calls)
}

// TestDCGMCollectorRearmCleansOldWatchBeforeReplacement verifies watch operation ordering.
func TestDCGMCollectorRearmCleansOldWatchBeforeReplacement(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)
	deviceInfo := testGPUDeviceInfo(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)

	// Record teardown, replacement, refresh, and final cleanup in execution order.
	var steps []string
	initialWatch := watcher.EXPECT().
		WatchDeviceFieldGroups(gomock.Any(), deviceInfo).
		Return(nil, nil, []func(){func() { steps = append(steps, "old cleanup") }}, nil)
	replacementWatch := watcher.EXPECT().
		WatchDeviceFieldGroups(gomock.Any(), deviceInfo).
		DoAndReturn(func([]devicewatcher.FieldWatchGroup, deviceinfo.Provider) (
			[]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error,
		) {
			steps = append(steps, "replacement watch")
			return nil, nil, []func(){func() { steps = append(steps, "new cleanup") }}, nil
		})
	update := mockDCGM.EXPECT().UpdateAllFields().DoAndReturn(func() error {
		steps = append(steps, "field update")
		return nil
	})
	gomock.InOrder(initialWatch, replacementWatch, update)

	collector := newTestProfilingCollector(t, deviceInfo, watcher, time.Second)
	require.NoError(t, collector.rearmProfilingWatch())
	collector.Cleanup()
	collector.Cleanup()

	assert.Equal(t, []string{"old cleanup", "replacement watch", "field update", "new cleanup"}, steps)
}

// TestDCGMCollectorRearmCleansPartialWatchOnFailure verifies failed rewatch cleanup ownership.
func TestDCGMCollectorRearmCleansPartialWatchOnFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	deviceInfo := testGPUDeviceInfo(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	var oldCleanupCalls, partialCleanupCalls int

	watcher.EXPECT().
		WatchDeviceFieldGroups(gomock.Any(), deviceInfo).
		Return(nil, nil, []func(){func() { oldCleanupCalls++ }}, nil)
	watcher.EXPECT().
		WatchDeviceFieldGroups(gomock.Any(), deviceInfo).
		DoAndReturn(func([]devicewatcher.FieldWatchGroup, deviceinfo.Provider) (
			[]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error,
		) {
			assert.Equal(t, 1, oldCleanupCalls)
			return nil, nil, []func(){func() { partialCleanupCalls++ }}, assert.AnError
		})

	collector := newTestProfilingCollector(t, deviceInfo, watcher, time.Second)
	err := collector.rearmProfilingWatch()
	require.ErrorIs(t, err, assert.AnError)
	collector.Cleanup()

	assert.Equal(t, 1, oldCleanupCalls)
	assert.Equal(t, 1, partialCleanupCalls)
}

// TestDCGMCollectorRearmRetainsReplacementCleanupWhenUpdateFails verifies post-watch failure cleanup.
func TestDCGMCollectorRearmRetainsReplacementCleanupWhenUpdateFails(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)
	deviceInfo := testGPUDeviceInfo(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	var oldCleanupCalls, newCleanupCalls int

	watcher.EXPECT().
		WatchDeviceFieldGroups(gomock.Any(), deviceInfo).
		Return(nil, nil, []func(){func() { oldCleanupCalls++ }}, nil)
	watcher.EXPECT().
		WatchDeviceFieldGroups(gomock.Any(), deviceInfo).
		Return(nil, nil, []func(){func() { newCleanupCalls++ }}, nil)
	mockDCGM.EXPECT().UpdateAllFields().Return(assert.AnError)

	collector := newTestProfilingCollector(t, deviceInfo, watcher, time.Second)
	err := collector.rearmProfilingWatch()
	require.ErrorIs(t, err, assert.AnError)
	collector.Cleanup()

	assert.Equal(t, 1, oldCleanupCalls)
	assert.Equal(t, 1, newCleanupCalls)
}

// TestDCGMCollectorRepairsFrozenProfilingTimestampAndRetries verifies the silent-failure recovery path.
func TestDCGMCollectorRepairsFrozenProfilingTimestampAndRetries(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)
	deviceInfo := testGPUDeviceInfo(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	counter := testProfilingCounter()
	var cleanupCalls int

	watcher.EXPECT().
		WatchDeviceFieldGroups(gomock.Any(), deviceInfo).
		Return(nil, nil, []func(){func() { cleanupCalls++ }}, nil)
	collector := newTestProfilingCollector(t, deviceInfo, watcher, time.Second)
	now := time.Unix(100, 0)
	collector.now = func() time.Time { return now }

	// Establish an initial zero-valued sample as the freshness baseline.
	staleValue := profilingFieldValue(counter.FieldID, dcgm.DCGM_ST_OK, 10, 0)
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return([]dcgm.FieldValue_v1{staleValue}, nil)
	_, err := collector.GetMetrics()
	require.NoError(t, err)

	// At the stale threshold, rewatch and return only the retry's fresh sample.
	now = now.Add(2 * time.Second)
	staleRead := mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return([]dcgm.FieldValue_v1{staleValue}, nil)
	rewatch := watcher.EXPECT().
		WatchDeviceFieldGroups(gomock.Any(), deviceInfo).
		DoAndReturn(func([]devicewatcher.FieldWatchGroup, deviceinfo.Provider) (
			[]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error,
		) {
			assert.Equal(t, 1, cleanupCalls)
			return nil, nil, []func(){func() { cleanupCalls++ }}, nil
		})
	update := mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	freshRead := mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return([]dcgm.FieldValue_v1{profilingFieldValue(counter.FieldID, dcgm.DCGM_ST_OK, 11, 0)}, nil)
	gomock.InOrder(staleRead, rewatch, update, freshRead)

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	require.Len(t, got[counter], 1)
	assert.Equal(t, "0", got[counter][0].Value)
	collector.Cleanup()
	assert.Equal(t, 2, cleanupCalls)
}

func TestDCGMCollectorRejectsSameTimestampAfterRepair(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)
	deviceInfo := testGPUDeviceInfo(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	counter := testProfilingCounter()

	watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
	collector := newTestProfilingCollector(t, deviceInfo, watcher, time.Second)
	now := time.Unix(100, 0)
	collector.now = func() time.Time { return now }
	staleValue := []dcgm.FieldValue_v1{
		profilingFieldValue(counter.FieldID, dcgm.DCGM_ST_OK, 10, 0),
	}

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return(staleValue, nil)
	_, err := collector.GetMetrics()
	require.NoError(t, err)

	now = now.Add(2 * time.Second)
	firstStaleRead := mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return(staleValue, nil)
	rewatch := watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
	update := mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	retrySameTimestamp := mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return(staleValue, nil)
	gomock.InOrder(firstStaleRead, rewatch, update, retrySameTimestamp)

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	assert.Empty(t, got)
	collector.Cleanup()
}

func TestDCGMCollectorPreservesHealthyMetricsWhenRepairFails(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)
	deviceInfo := testGPUDeviceInfo(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	profilingCounter := testProfilingCounter()
	temperatureCounter := counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
		FieldName: "DCGM_FI_DEV_GPU_TEMP",
		PromType:  "gauge",
	}
	fields := []dcgm.Short{profilingCounter.FieldID, temperatureCounter.FieldID}
	watchList := *devicewatchlistmanager.NewWatchListWithGroups(
		deviceInfo,
		fields,
		nil,
		[]devicewatcher.FieldWatchGroup{{Name: "default", Fields: fields, IntervalMSec: 1000}},
		watcher,
		1000,
	)

	watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
	collector, err := NewDCGMCollector(
		[]counters.Counter{profilingCounter, temperatureCounter},
		"host-a",
		&appconfig.Config{},
		watchList,
	)
	require.NoError(t, err)

	mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(0), fields).Return([]dcgm.FieldValue_v1{
		profilingFieldValue(profilingCounter.FieldID, dcgm.DCGM_ST_NOT_WATCHED, 10, 0),
		int64FieldValue(temperatureCounter.FieldID, 42),
	}, nil)
	watcher.EXPECT().
		WatchDeviceFieldGroups(gomock.Any(), deviceInfo).
		Return(nil, nil, nil, assert.AnError)

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	assert.Empty(t, got[profilingCounter])
	require.Len(t, got[temperatureCounter], 1)
	assert.Equal(t, "42", got[temperatureCounter][0].Value)
	collector.Cleanup()
}

// TestDCGMCollectorRepairsProfilingLifecycleStatuses verifies immediate status-triggered repair.
func TestDCGMCollectorRepairsProfilingLifecycleStatuses(t *testing.T) {
	for _, status := range []int{dcgm.DCGM_ST_NOT_WATCHED, dcgm.DCGM_ST_STALE_DATA} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			realDCGM := dcgmprovider.Client()
			t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

			ctrl := gomock.NewController(t)
			mockDCGM := mockdcgm.NewMockDCGM(ctrl)
			dcgmprovider.SetClient(mockDCGM)
			deviceInfo := testGPUDeviceInfo(ctrl)
			watcher := mockdevicewatcher.NewMockWatcher(ctrl)
			counter := testProfilingCounter()

			watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
			collector := newTestProfilingCollector(t, deviceInfo, watcher, time.Second)
			collector.now = func() time.Time { return time.Unix(100, 0) }

			// A lifecycle status triggers teardown, rewatch, refresh, and one retry.
			firstRead := mockDCGM.EXPECT().
				EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
				Return([]dcgm.FieldValue_v1{profilingFieldValue(counter.FieldID, status, 10, 0)}, nil)
			rewatch := watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
			update := mockDCGM.EXPECT().UpdateAllFields().Return(nil)
			retry := mockDCGM.EXPECT().
				EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
				Return([]dcgm.FieldValue_v1{profilingFieldValue(counter.FieldID, dcgm.DCGM_ST_OK, 11, 5)}, nil)
			gomock.InOrder(firstRead, rewatch, update, retry)

			got, err := collector.GetMetrics()
			require.NoError(t, err)
			require.Len(t, got[counter], 1)
			assert.Equal(t, "5", got[counter][0].Value)
			collector.Cleanup()
		})
	}
}

// TestDCGMCollectorDoesNotRepairNonLifecycleProfilingStatuses verifies excluded status behavior.
func TestDCGMCollectorDoesNotRepairNonLifecycleProfilingStatuses(t *testing.T) {
	for _, status := range []int{dcgm.DCGM_ST_NOT_CONFIGURED, dcgm.DCGM_ST_PROFILING_LIBRARY_ERROR} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			realDCGM := dcgmprovider.Client()
			t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

			ctrl := gomock.NewController(t)
			mockDCGM := mockdcgm.NewMockDCGM(ctrl)
			dcgmprovider.SetClient(mockDCGM)
			deviceInfo := testGPUDeviceInfo(ctrl)
			watcher := mockdevicewatcher.NewMockWatcher(ctrl)
			counter := testProfilingCounter()

			watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
			collector := newTestProfilingCollector(t, deviceInfo, watcher, time.Second)
			mockDCGM.EXPECT().
				EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
				Return([]dcgm.FieldValue_v1{profilingFieldValue(counter.FieldID, status, 10, 5)}, nil)

			_, err := collector.GetMetrics()
			require.NoError(t, err)
			collector.Cleanup()
		})
	}
}

// TestDCGMCollectorDoesNotRefreshNonProfilingFields verifies the unchanged non-profiling path.
func TestDCGMCollectorDoesNotRefreshNonProfilingFields(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)
	deviceInfo := testGPUDeviceInfo(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	counter := counters.Counter{FieldID: 150, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	watchList := *devicewatchlistmanager.NewWatchList(deviceInfo, []dcgm.Short{counter.FieldID}, nil, watcher, 1000)

	watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
	collector, err := NewDCGMCollector([]counters.Counter{counter}, "host-a", &appconfig.Config{}, watchList)
	require.NoError(t, err)
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return([]dcgm.FieldValue_v1{int64FieldValue(counter.FieldID, 42)}, nil)

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	require.Len(t, got[counter], 1)
	collector.Cleanup()
}

// TestDCGMCollectorFallsBackAfterRetryFailure verifies the single-retry limit preserves scrape health.
func TestDCGMCollectorFallsBackAfterRetryFailure(t *testing.T) {
	tests := []struct {
		name      string
		retryVals []dcgm.FieldValue_v1
		retryErr  error
	}{
		{name: "API failure", retryErr: assert.AnError},
		{
			name: "repeated stale status",
			retryVals: []dcgm.FieldValue_v1{
				profilingFieldValue(dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE, dcgm.DCGM_ST_NOT_WATCHED, 10, 0),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			realDCGM := dcgmprovider.Client()
			t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

			ctrl := gomock.NewController(t)
			mockDCGM := mockdcgm.NewMockDCGM(ctrl)
			dcgmprovider.SetClient(mockDCGM)
			deviceInfo := testGPUDeviceInfo(ctrl)
			watcher := mockdevicewatcher.NewMockWatcher(ctrl)
			counter := testProfilingCounter()

			watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
			collector := newTestProfilingCollector(t, deviceInfo, watcher, time.Second)

			// Only one replacement watch is expected, even when the retry is still bad.
			mockDCGM.EXPECT().
				EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
				Return([]dcgm.FieldValue_v1{
					profilingFieldValue(counter.FieldID, dcgm.DCGM_ST_NOT_WATCHED, 10, 0),
				}, nil)
			watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
			mockDCGM.EXPECT().UpdateAllFields().Return(nil)
			mockDCGM.EXPECT().
				EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
				Return(tt.retryVals, tt.retryErr)

			got, err := collector.GetMetrics()
			require.NoError(t, err)
			assert.Empty(t, got)
			collector.Cleanup()
		})
	}
}

// TestDCGMCollectorRateLimitsFailedRepair verifies backoff after a failed rewatch.
func TestDCGMCollectorRateLimitsFailedRepair(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)
	deviceInfo := testGPUDeviceInfo(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	counter := testProfilingCounter()

	watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
	collector := newTestProfilingCollector(t, deviceInfo, watcher, time.Second)
	collector.now = func() time.Time { return time.Unix(100, 0) }
	repairableValue := []dcgm.FieldValue_v1{
		profilingFieldValue(counter.FieldID, dcgm.DCGM_ST_NOT_WATCHED, 10, 0),
	}

	// The first repair attempt tears down the old watch and fails during replacement.
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return(repairableValue, nil)
	watcher.EXPECT().
		WatchDeviceFieldGroups(gomock.Any(), deviceInfo).
		Return(nil, nil, []func(){func() {}}, assert.AnError)

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	assert.Empty(t, got)

	// The same stale status cannot trigger another watch operation during backoff.
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return(repairableValue, nil)
	got, err = collector.GetMetrics()
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestDCGMCollectorFatalErrorPrecedesProfilingRepair verifies existing fatal-status ordering.
func TestDCGMCollectorFatalErrorPrecedesProfilingRepair(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })
	realOS := os
	t.Cleanup(func() { os = realOS })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)
	mockOS := mockos.NewMockOS(ctrl)
	os = mockOS
	mockOS.EXPECT().Exit(1)

	counter := testProfilingCounter()
	deviceInfo := testGPUDeviceInfo(ctrl)
	watchList := *devicewatchlistmanager.NewWatchList(
		deviceInfo, []dcgm.Short{counter.FieldID}, nil, devicewatcher.NewDeviceWatcher(), 1000,
	)
	collector := &DCGMCollector{
		counters:              []counters.Counter{counter},
		deviceWatchList:       watchList,
		profilingStaleWindows: map[dcgm.Short]time.Duration{counter.FieldID: 2 * time.Second},
		profilingSamples:      make(map[profilingSampleKey]profilingSample),
	}
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return(nil, &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID})

	_, err := collector.GetMetrics()
	require.Error(t, err)
}

// TestDCGMCollectorConcurrentScrapesPerformOneRepair verifies lifecycle serialization.
func TestDCGMCollectorConcurrentScrapesPerformOneRepair(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)
	deviceInfo := testGPUDeviceInfo(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	counter := testProfilingCounter()

	watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
	collector := newTestProfilingCollector(t, deviceInfo, watcher, time.Second)
	now := time.Unix(100, 0)
	collector.now = func() time.Time { return now }

	// Seed one stale stream before starting concurrent scrapes.
	staleValue := []dcgm.FieldValue_v1{
		profilingFieldValue(counter.FieldID, dcgm.DCGM_ST_OK, 10, 0),
	}
	freshValue := []dcgm.FieldValue_v1{
		profilingFieldValue(counter.FieldID, dcgm.DCGM_ST_OK, 11, 0),
	}
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return(staleValue, nil)
	_, err := collector.GetMetrics()
	require.NoError(t, err)
	now = now.Add(2 * time.Second)

	// The first scrape repairs and retries; the serialized second scrape sees fresh data.
	firstStaleRead := mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return(staleValue, nil)
	rewatch := watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), deviceInfo).Return(nil, nil, nil, nil)
	update := mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	retryRead := mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return(freshValue, nil)
	secondScrapeRead := mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return(freshValue, nil)
	gomock.InOrder(firstStaleRead, rewatch, update, retryRead, secondScrapeRead)

	// Release both callers together to exercise the collector mutex.
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := collector.GetMetrics()
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	collector.Cleanup()
}

func TestDCGMCollectorGetMetricsSuccessAndFailure(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	counter := counters.Counter{FieldID: 150, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	gpuInfo := deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{
			GPU:         0,
			UUID:        "GPU-0",
			Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA T400 4GB"},
			PCI:         dcgm.PCIInfo{BusID: "0000:00:01.0"},
		},
	}
	deviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{MajorRange: []int{-1}}).AnyTimes()
	deviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	deviceInfo.EXPECT().GPU(uint(0)).Return(gpuInfo).AnyTimes()
	watchList := *devicewatchlistmanager.NewWatchList(
		deviceInfo,
		[]dcgm.Short{counter.FieldID},
		nil,
		devicewatcher.NewDeviceWatcher(),
		1,
	)

	t.Run("success", func(t *testing.T) {
		mockDCGM.EXPECT().
			EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
			Return([]dcgm.FieldValue_v1{int64FieldValue(counter.FieldID, 42)}, nil)

		collector := &DCGMCollector{
			counters:        []counters.Counter{counter},
			deviceWatchList: watchList,
			hostname:        "host-a",
		}

		got, err := collector.GetMetrics()

		require.NoError(t, err)
		require.Len(t, got[counter], 1)
		assert.Equal(t, "42", got[counter][0].Value)
		assert.Equal(t, "GPU-0", got[counter][0].GPUUUID)
	})

	t.Run("dcgm error is returned", func(t *testing.T) {
		mockDCGM.EXPECT().
			EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
			Return(nil, assert.AnError)

		collector := &DCGMCollector{
			counters:        []counters.Counter{counter},
			deviceWatchList: watchList,
			hostname:        "host-a",
		}

		got, err := collector.GetMetrics()

		require.ErrorIs(t, err, assert.AnError)
		assert.Nil(t, got)
	})
}

func TestDCGMCollectorGetMetricsFetchesLabelFieldsForMetricLabels(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	metricCounter := counters.Counter{FieldID: 150, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	labelCounter := counters.Counter{
		FieldID:   dcgm.DCGM_FI_DRIVER_VERSION,
		FieldName: "DCGM_FI_DRIVER_VERSION",
		PromType:  "label",
	}
	deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	deviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{MajorRange: []int{-1}}).AnyTimes()
	deviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	deviceInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{GPU: 0, UUID: "GPU-0"},
	}).AnyTimes()

	watchList := *devicewatchlistmanager.NewWatchList(
		deviceInfo,
		[]dcgm.Short{metricCounter.FieldID},
		[]dcgm.Short{labelCounter.FieldID},
		devicewatcher.NewDeviceWatcher(),
		1,
	)
	collector := &DCGMCollector{
		counters:        []counters.Counter{metricCounter, labelCounter},
		deviceWatchList: watchList,
		hostname:        "host-a",
	}

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{metricCounter.FieldID, labelCounter.FieldID}).
		Return([]dcgm.FieldValue_v1{
			int64FieldValue(metricCounter.FieldID, 42),
			stringFieldValue(labelCounter.FieldID, "555.55"),
		}, nil)

	got, err := collector.GetMetrics()

	require.NoError(t, err)
	require.Len(t, got[metricCounter], 1)
	assert.Equal(t, map[string]string{"DCGM_FI_DRIVER_VERSION": "555.55"}, got[metricCounter][0].Labels)
}

func TestDCGMCollectorGetMetricsReadsComputeInstanceFieldsAtSupportedScopes(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	counter := counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_FB_USED,
		FieldName: "DCGM_FI_DEV_FB_USED",
		PromType:  "gauge",
	}
	labelCounter := counters.Counter{
		FieldID:   dcgm.DCGM_FI_DRIVER_VERSION,
		FieldName: "DCGM_FI_DRIVER_VERSION",
		PromType:  "label",
	}
	instance := deviceinfo.GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{NvmlInstanceId: 3},
		ProfileName: "1g.10gb",
		EntityId:    7,
		ComputeInstances: []deviceinfo.ComputeInstanceInfo{
			{InstanceInfo: dcgm.MigEntityInfo{NvmlComputeInstanceId: 0}, EntityId: 21},
			{InstanceInfo: dcgm.MigEntityInfo{NvmlComputeInstanceId: 1}, EntityId: 22},
		},
	}
	deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	deviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	deviceInfo.EXPECT().GPUCount().Return(uint(2)).AnyTimes()
	deviceInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{
			GPU:         0,
			UUID:        "GPU-0",
			Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA B200"},
		},
		GPUInstances: []deviceinfo.GPUInstanceInfo{instance},
	}).AnyTimes()
	deviceInfo.EXPECT().GPU(uint(1)).Return(deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{
			GPU:         1,
			UUID:        "GPU-1",
			Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA B200"},
		},
	}).AnyTimes()
	watchList := *devicewatchlistmanager.NewWatchList(
		deviceInfo,
		[]dcgm.Short{counter.FieldID},
		[]dcgm.Short{labelCounter.FieldID},
		devicewatcher.NewDeviceWatcher(),
		1,
	)

	// The GPU-instance query must not include the compute-instance field.
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU_I, uint(7), []dcgm.Short{labelCounter.FieldID}).
		Return([]dcgm.FieldValue_v1{stringFieldValue(labelCounter.FieldID, "555.55")}, nil)
	// A whole GPU keeps the complete pre-4.8.4 field set even though DCGM
	// classifies framebuffer fields at compute-instance scope.
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(1), []dcgm.Short{counter.FieldID, labelCounter.FieldID}).
		Return([]dcgm.FieldValue_v1{
			int64FieldValue(counter.FieldID, 230),
			stringFieldValue(labelCounter.FieldID, "555.55"),
		}, nil)
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU_CI, uint(21), []dcgm.Short{counter.FieldID, labelCounter.FieldID}).
		Return([]dcgm.FieldValue_v1{
			int64FieldValue(counter.FieldID, 100),
			stringFieldValue(labelCounter.FieldID, "555.55"),
		}, nil)
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU_CI, uint(22), []dcgm.Short{counter.FieldID, labelCounter.FieldID}).
		Return([]dcgm.FieldValue_v1{
			int64FieldValue(counter.FieldID, 200),
			stringFieldValue(labelCounter.FieldID, "555.55"),
		}, nil)

	collector := &DCGMCollector{
		counters:                    []counters.Counter{counter, labelCounter},
		deviceWatchList:             watchList,
		scrapeFields:                []dcgm.Short{counter.FieldID, labelCounter.FieldID},
		nonComputeInstanceFields:    []dcgm.Short{labelCounter.FieldID},
		computeInstanceScrapeFields: []dcgm.Short{counter.FieldID, labelCounter.FieldID},
		hostname:                    "host-a",
	}

	got, err := collector.GetMetrics()

	require.NoError(t, err)
	require.Len(t, got[counter], 3)
	assert.Equal(t, "230", got[counter][0].Value)
	assert.Empty(t, got[counter][0].GPUComputeInstanceID)
	assert.Empty(t, got[counter][0].GPUInstanceID)
	assert.Equal(t, map[string]string{"DCGM_FI_DRIVER_VERSION": "555.55"}, got[counter][0].Labels)
	assert.Equal(t, "100", got[counter][1].Value)
	assert.Equal(t, "0", got[counter][1].GPUComputeInstanceID)
	assert.Equal(t, "3", got[counter][1].GPUInstanceID)
	assert.Equal(t, "1g.10gb", got[counter][1].MigProfile)
	assert.Equal(t, "200", got[counter][2].Value)
	assert.Equal(t, "1", got[counter][2].GPUComputeInstanceID)
}

func TestDCGMCollectorGetMetricsReadsDirectXIDFieldsAtAllScopes(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	counter := counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_XID_ERRORS,
		FieldName: "DCGM_FI_DEV_XID_ERRORS",
		PromType:  "gauge",
	}
	instance := deviceinfo.GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{NvmlInstanceId: 3},
		ProfileName: "1g.10gb",
		EntityId:    7,
		ComputeInstances: []deviceinfo.ComputeInstanceInfo{
			{InstanceInfo: dcgm.MigEntityInfo{NvmlComputeInstanceId: 0}, EntityId: 21},
		},
	}
	deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	deviceInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	deviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	deviceInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{
			GPU:         0,
			UUID:        "GPU-0",
			Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA B200"},
		},
		GPUInstances: []deviceinfo.GPUInstanceInfo{instance},
	}).AnyTimes()
	watchList := *devicewatchlistmanager.NewWatchList(
		deviceInfo,
		[]dcgm.Short{counter.FieldID},
		nil,
		devicewatcher.NewDeviceWatcher(),
		1,
	)

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{counter.FieldID}).
		Return([]dcgm.FieldValue_v1{int64FieldValue(counter.FieldID, 79)}, nil)
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU_I, uint(7), []dcgm.Short{counter.FieldID}).
		Return([]dcgm.FieldValue_v1{int64FieldValue(counter.FieldID, 31)}, nil)
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU_CI, uint(21), []dcgm.Short{counter.FieldID}).
		Return([]dcgm.FieldValue_v1{int64FieldValue(counter.FieldID, 48)}, nil)

	collector := &DCGMCollector{
		counters:                    []counters.Counter{counter},
		deviceWatchList:             watchList,
		scrapeFields:                []dcgm.Short{counter.FieldID},
		nonComputeInstanceFields:    []dcgm.Short{counter.FieldID},
		computeInstanceScrapeFields: []dcgm.Short{counter.FieldID},
		parentGPUScrapeFields:       []dcgm.Short{counter.FieldID},
		hostname:                    "host-a",
	}

	got, err := collector.GetMetrics()

	require.NoError(t, err)
	require.Len(t, got[counter], 3)
	valuesByScope := make(map[string]string)
	for _, metric := range got[counter] {
		valuesByScope[metric.GPUInstanceID+":"+metric.GPUComputeInstanceID] = metric.Value
	}
	assert.Equal(t, map[string]string{
		":":   "79",
		"3:":  "31",
		"3:0": "48",
	}, valuesByScope)
}

func TestSplitComputeInstanceFieldsKeepsLabelsWithComputeInstanceMetrics(t *testing.T) {
	metricField := dcgm.DCGM_FI_DEV_GPU_TEMP
	computeInstanceField := dcgm.DCGM_FI_DEV_FB_USED
	multiScopeField := dcgm.DCGM_FI_DEV_XID_ERRORS
	labelField := dcgm.DCGM_FI_DRIVER_VERSION

	nonComputeInstanceFields, computeInstanceFields := splitComputeInstanceFields(
		[]dcgm.Short{metricField, computeInstanceField, multiScopeField, labelField},
		[]dcgm.Short{computeInstanceField, multiScopeField},
		[]dcgm.Short{multiScopeField},
		[]dcgm.Short{labelField},
	)

	assert.Equal(t, []dcgm.Short{metricField, multiScopeField, labelField}, nonComputeInstanceFields)
	assert.Equal(t, []dcgm.Short{computeInstanceField, multiScopeField, labelField}, computeInstanceFields)
}

func TestFieldsForScrapeKeepsComputeInstanceFieldsOnSelectedFullGPU(t *testing.T) {
	metricField := dcgm.DCGM_FI_DEV_GPU_TEMP
	computeInstanceField := dcgm.DCGM_FI_DEV_FB_USED
	collector := &DCGMCollector{
		scrapeFields:                []dcgm.Short{metricField, computeInstanceField},
		nonComputeInstanceFields:    []dcgm.Short{metricField},
		computeInstanceScrapeFields: []dcgm.Short{computeInstanceField},
	}

	tests := []struct {
		name        string
		entityGroup dcgm.Field_Entity_Group
		want        []dcgm.Short
	}{
		{
			name:        "selected full GPU",
			entityGroup: dcgm.FE_GPU,
			want:        []dcgm.Short{metricField, computeInstanceField},
		},
		{
			name:        "GPU instance",
			entityGroup: dcgm.FE_GPU_I,
			want:        []dcgm.Short{metricField},
		},
		{
			name:        "compute instance",
			entityGroup: dcgm.FE_GPU_CI,
			want:        []dcgm.Short{computeInstanceField},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, collector.fieldsForScrape(devicemonitoring.Info{
				Entity: dcgm.GroupEntityPair{EntityGroupId: tt.entityGroup},
			}))
		})
	}
}

// TestDCGMCollectorLatestValuesBatchesLargeFieldSets verifies collectors can
// scrape field sets at, just above, and several times the DCGM request limit.
func TestDCGMCollectorLatestValuesBatchesLargeFieldSets(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	for _, tt := range []struct {
		name       string
		fieldCount int
	}{
		{name: "exactly one batch", fieldCount: maxLatestValueFieldBatch},
		{name: "one field over", fieldCount: maxLatestValueFieldBatch + 1},
		{name: "multiple batches", fieldCount: 300},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockDCGM := mockdcgm.NewMockDCGM(ctrl)
			dcgmprovider.SetClient(mockDCGM)

			fields := make([]dcgm.Short, tt.fieldCount)
			for index := range fields {
				fields[index] = dcgm.Short(index + 1)
			}
			values := make([]dcgm.FieldValue_v1, len(fields))
			for index, fieldID := range fields {
				values[index] = dcgm.FieldValue_v1{FieldID: fieldID}
			}
			watchList := *devicewatchlistmanager.NewWatchList(nil, fields, nil, nil, 1)
			collector := &DCGMCollector{deviceWatchList: watchList}

			for start := 0; start < len(fields); start += maxLatestValueFieldBatch {
				end := min(start+maxLatestValueFieldBatch, len(fields))
				mockDCGM.EXPECT().
					EntityGetLatestValues(dcgm.FE_GPU, uint(0), fields[start:end]).
					Return(values[start:end], nil)
			}

			got, err := collector.latestValues(devicemonitoring.Info{
				Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
			})

			require.NoError(t, err)
			gotFieldIDs := make([]dcgm.Short, len(got))
			for index, value := range got {
				gotFieldIDs[index] = value.FieldID
			}
			assert.Equal(t, fields, gotFieldIDs)
		})
	}
}

// TestDCGMCollectorLatestValuesBatchesLargeLinkFieldSets verifies the link API
// receives the same bounded batches while preserving parent-entity identity.
func TestDCGMCollectorLatestValuesBatchesLargeLinkFieldSets(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	fields := make([]dcgm.Short, 200)
	for index := range fields {
		fields[index] = dcgm.Short(index + 1)
	}
	values := make([]dcgm.FieldValue_v1, len(fields))
	for index, fieldID := range fields {
		values[index] = dcgm.FieldValue_v1{FieldID: fieldID}
	}
	watchList := *devicewatchlistmanager.NewWatchList(nil, fields, nil, nil, 1)
	collector := &DCGMCollector{deviceWatchList: watchList}

	mockDCGM.EXPECT().
		LinkGetLatestValues(uint(4), dcgm.FE_GPU, uint(0), fields[:maxLatestValueFieldBatch]).
		Return(values[:maxLatestValueFieldBatch], nil)
	mockDCGM.EXPECT().
		LinkGetLatestValues(uint(4), dcgm.FE_GPU, uint(0), fields[maxLatestValueFieldBatch:]).
		Return(values[maxLatestValueFieldBatch:], nil)

	got, err := collector.latestValues(devicemonitoring.Info{
		Entity:     dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: 4},
		ParentType: dcgm.FE_GPU,
		ParentId:   0,
	})

	require.NoError(t, err)
	gotFieldIDs := make([]dcgm.Short, len(got))
	for index, value := range got {
		gotFieldIDs[index] = value.FieldID
	}
	assert.Equal(t, fields, gotFieldIDs)
}

// TestDCGMCollectorLatestValuesAddsBatchFailureContext verifies scrape errors
// identify the failed entity and field slice while preserving the DCGM cause.
func TestDCGMCollectorLatestValuesAddsBatchFailureContext(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	fields := make([]dcgm.Short, maxLatestValueFieldBatch+1)
	for index := range fields {
		fields[index] = dcgm.Short(index + 1)
	}
	watchList := *devicewatchlistmanager.NewWatchList(nil, fields, nil, nil, 1)
	collector := &DCGMCollector{deviceWatchList: watchList}
	batchErr := fmt.Errorf("DCGM batch failed")
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(7), fields[:maxLatestValueFieldBatch]).
		Return(make([]dcgm.FieldValue_v1, maxLatestValueFieldBatch), nil)
	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(7), fields[maxLatestValueFieldBatch:]).
		Return(nil, batchErr)

	_, err := collector.latestValues(devicemonitoring.Info{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 7},
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, batchErr)
	assert.Contains(t, err.Error(), fmt.Sprintf(
		"entity group %d entity 7 field batch [128:129]",
		dcgm.FE_GPU,
	))
}

func TestNewDCGMCollectorValidationAndNilConfig(t *testing.T) {
	_, err := NewDCGMCollector(nil, "host-a", &appconfig.Config{}, devicewatchlistmanager.WatchList{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deviceWatchList is empty")

	watchList := *devicewatchlistmanager.NewWatchList(
		mockdeviceinfo.NewMockProvider(gomock.NewController(t)),
		[]dcgm.Short{150},
		nil,
		nil,
		1,
	)
	got, err := NewDCGMCollector([]counters.Counter{{FieldID: 150}}, "host-a", nil, watchList)
	require.NoError(t, err)
	assert.Equal(t, "host-a", got.hostname)
}

func TestNewDCGMCollectorCachesScrapeFields(t *testing.T) {
	tests := []struct {
		name         string
		deviceFields []dcgm.Short
		labelFields  []dcgm.Short
		config       *appconfig.Config
		want         []dcgm.Short
	}{
		{
			name:         "device fields only",
			deviceFields: []dcgm.Short{1, 2},
			config:       &appconfig.Config{},
			want:         []dcgm.Short{1, 2},
		},
		{
			name:         "device plus label fields",
			deviceFields: []dcgm.Short{1, 2},
			labelFields:  []dcgm.Short{3, 4},
			config:       &appconfig.Config{},
			want:         []dcgm.Short{1, 2, 3, 4},
		},
		{
			name:         "duplicate fields",
			deviceFields: []dcgm.Short{1, 2, 1},
			labelFields:  []dcgm.Short{2, 3, 1},
			config:       &appconfig.Config{},
			want:         []dcgm.Short{1, 2, 3},
		},
		{
			name:         "nil config",
			deviceFields: []dcgm.Short{1},
			labelFields:  []dcgm.Short{2},
			want:         []dcgm.Short{1, 2},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			watcher := mockdevicewatcher.NewMockWatcher(gomock.NewController(t))
			if test.config != nil {
				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return(nil, nil, nil, nil)
			}
			watchList := *devicewatchlistmanager.NewWatchList(
				nil,
				test.deviceFields,
				test.labelFields,
				watcher,
				1,
			)

			got, err := NewDCGMCollector(nil, "host-a", test.config, watchList)
			require.NoError(t, err)
			assert.Equal(t, test.want, got.scrapeFields)
			assert.Empty(t, got.parentGPUScrapeFields)

			test.deviceFields[0] = 99
			if len(test.labelFields) > 0 {
				test.labelFields[0] = 98
			}
			assert.Equal(t, test.want, got.scrapeFields)
		})
	}
}

func TestDCGMCollectorGetMetricsEntityBranches(t *testing.T) {
	tests := []struct {
		name       string
		infoType   dcgm.Field_Entity_Group
		setupInfo  func(*mockdeviceinfo.MockProvider)
		expectDCGM func(*mockdcgm.MockDCGM, dcgm.Short)
		assertion  func(*testing.T, Metric)
	}{
		{
			name:     "switch entity",
			infoType: dcgm.FE_SWITCH,
			setupInfo: func(info *mockdeviceinfo.MockProvider) {
				info.EXPECT().Switches().Return([]deviceinfo.SwitchInfo{{EntityId: 3}}).AnyTimes()
				info.EXPECT().IsSwitchWatched(uint(3)).Return(true).AnyTimes()
			},
			expectDCGM: func(mockDCGM *mockdcgm.MockDCGM, fieldID dcgm.Short) {
				mockDCGM.EXPECT().
					EntityGetLatestValues(dcgm.FE_SWITCH, uint(3), []dcgm.Short{fieldID}).
					Return([]dcgm.FieldValue_v1{int64FieldValue(fieldID, 11)}, nil)
			},
			assertion: func(t *testing.T, got Metric) {
				assert.Equal(t, "11", got.Value)
				assert.Equal(t, "nvswitch3", got.NvSwitch)
				assert.Empty(t, got.NvLink)
			},
		},
		{
			name:     "cpu entity",
			infoType: dcgm.FE_CPU,
			setupInfo: func(info *mockdeviceinfo.MockProvider) {
				info.EXPECT().CPUs().Return([]deviceinfo.CPUInfo{{EntityId: 2, Serial: "CPU-SERIAL-123"}}).AnyTimes()
				info.EXPECT().IsCPUWatched(uint(2)).Return(true).AnyTimes()
			},
			expectDCGM: func(mockDCGM *mockdcgm.MockDCGM, fieldID dcgm.Short) {
				mockDCGM.EXPECT().
					EntityGetLatestValues(dcgm.FE_CPU, uint(2), []dcgm.Short{fieldID}).
					Return([]dcgm.FieldValue_v1{int64FieldValue(fieldID, 22)}, nil)
			},
			assertion: func(t *testing.T, got Metric) {
				assert.Equal(t, "22", got.Value)
				assert.Equal(t, "2", got.GPU)
				assert.Equal(t, "CPU-SERIAL-123", got.CPUSerial)
			},
		},
		{
			name:     "cpu core entity",
			infoType: dcgm.FE_CPU_CORE,
			setupInfo: func(info *mockdeviceinfo.MockProvider) {
				info.EXPECT().CPUs().Return([]deviceinfo.CPUInfo{{EntityId: 2, Cores: []uint{11}, Serial: "CPU-SERIAL-123"}}).AnyTimes()
				info.EXPECT().IsCPUWatched(uint(2)).Return(true).AnyTimes()
				info.EXPECT().IsCoreWatched(uint(11), uint(2)).Return(true).AnyTimes()
			},
			expectDCGM: func(mockDCGM *mockdcgm.MockDCGM, fieldID dcgm.Short) {
				mockDCGM.EXPECT().
					EntityGetLatestValues(dcgm.FE_CPU_CORE, uint(11), []dcgm.Short{fieldID}).
					Return([]dcgm.FieldValue_v1{int64FieldValue(fieldID, 23)}, nil)
			},
			assertion: func(t *testing.T, got Metric) {
				assert.Equal(t, "23", got.Value)
				assert.Equal(t, "11", got.GPU)
				assert.Equal(t, "2", got.GPUDevice)
				assert.Empty(t, got.CPUSerial)
			},
		},
		{
			name:     "gpu nvlink entity",
			infoType: dcgm.FE_LINK,
			setupInfo: func(info *mockdeviceinfo.MockProvider) {
				info.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
				info.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
					DeviceInfo: dcgm.Device{
						GPU:         0,
						UUID:        "GPU-0",
						Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA T400 4GB"},
					},
					NvLinks: []dcgm.NvLinkStatus{{
						Index:      4,
						State:      dcgm.LS_UP,
						ParentId:   0,
						ParentType: dcgm.FE_GPU,
					}},
				}).AnyTimes()
				info.EXPECT().Switches().Return(nil).AnyTimes()
			},
			expectDCGM: func(mockDCGM *mockdcgm.MockDCGM, fieldID dcgm.Short) {
				mockDCGM.EXPECT().
					LinkGetLatestValues(uint(4), dcgm.FE_GPU, uint(0), []dcgm.Short{fieldID}).
					Return([]dcgm.FieldValue_v1{int64FieldValue(fieldID, 33)}, nil)
			},
			assertion: func(t *testing.T, got Metric) {
				assert.Equal(t, "33", got.Value)
				assert.Equal(t, "4", got.NvLink)
				assert.Equal(t, "GPU-0", got.GPUUUID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			realDCGM := dcgmprovider.Client()
			t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

			ctrl := gomock.NewController(t)
			mockDCGM := mockdcgm.NewMockDCGM(ctrl)
			dcgmprovider.SetClient(mockDCGM)

			counter := counters.Counter{FieldID: 150, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
			deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
			deviceInfo.EXPECT().InfoType().Return(tt.infoType).AnyTimes()
			tt.setupInfo(deviceInfo)
			tt.expectDCGM(mockDCGM, counter.FieldID)

			watchList := *devicewatchlistmanager.NewWatchList(
				deviceInfo,
				[]dcgm.Short{counter.FieldID},
				nil,
				devicewatcher.NewDeviceWatcher(),
				1,
			)
			collector := &DCGMCollector{
				counters:        []counters.Counter{counter},
				deviceWatchList: watchList,
				hostname:        "host-a",
			}

			got, err := collector.GetMetrics()

			require.NoError(t, err)
			require.Len(t, got[counter], 1)
			tt.assertion(t, got[counter][0])
		})
	}
}

func TestCPUSerialForEntityReturnsEmptyWhenMissing(t *testing.T) {
	ctrl := gomock.NewController(t)
	info := mockdeviceinfo.NewMockProvider(ctrl)
	info.EXPECT().CPUs().Return([]deviceinfo.CPUInfo{
		{EntityId: 2, Serial: "CPU-SERIAL-123"},
	})

	assert.Empty(t, cpuSerialForEntity(info, 9))
}

func TestToSwitchMetric(t *testing.T) {
	labelCounter := counters.Counter{FieldID: 1, FieldName: "switch_label", PromType: "label"}
	valueCounter := counters.Counter{FieldID: 2, FieldName: "switch_temp", PromType: "gauge"}
	unavailableLabel := stringFieldValue(labelCounter.FieldID, "should-not-overwrite")
	unavailableLabel.Status = dcgm.DCGM_ST_NO_DATA
	metrics := MetricsByCounter{}
	mi := devicemonitoring.Info{
		Entity:     dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: 3},
		ParentId:   9,
		ParentType: dcgm.FE_SWITCH,
	}

	toSwitchMetric(metrics, []dcgm.FieldValue_v1{
		stringFieldValue(labelCounter.FieldID, "fabric-a"),
		unavailableLabel,
		int64FieldValue(valueCounter.FieldID, 77),
		int64FieldValue(999, 1), // adversarial: field not configured
	}, []counters.Counter{labelCounter, valueCounter}, mi, true, "host-a")

	require.Len(t, metrics[valueCounter], 1)
	got := metrics[valueCounter][0]
	assert.Equal(t, "77", got.Value)
	assert.Equal(t, "uuid", got.UUID)
	assert.Equal(t, "3", got.NvLink)
	assert.Equal(t, "nvswitch9", got.NvSwitch)
	assert.Equal(t, "host-a", got.Hostname)
	assert.Equal(t, map[string]string{"switch_label": "fabric-a"}, got.Labels)
}

// TestToSwitchMetricUsesEntityIDForEachSwitch verifies that FE_SWITCH metrics
// retain distinct switch identities instead of their shared parent ID.
func TestToSwitchMetricUsesEntityIDForEachSwitch(t *testing.T) {
	valueCounter := counters.Counter{FieldID: 2, FieldName: "switch_temp", PromType: "gauge"}
	metrics := MetricsByCounter{}

	for _, sample := range []struct {
		entityID uint
		value    byte
	}{{entityID: 0, value: 40}, {entityID: 3, value: 43}} {
		toSwitchMetric(metrics, []dcgm.FieldValue_v1{
			int64FieldValue(valueCounter.FieldID, sample.value),
		}, []counters.Counter{valueCounter}, devicemonitoring.Info{
			Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_SWITCH, EntityId: sample.entityID},
		}, false, "host-a")
	}

	require.Len(t, metrics[valueCounter], 2)
	assert.Equal(t, "nvswitch0", metrics[valueCounter][0].NvSwitch)
	assert.Equal(t, "nvswitch3", metrics[valueCounter][1].NvSwitch)
	assert.Empty(t, metrics[valueCounter][0].NvLink)
	assert.Empty(t, metrics[valueCounter][1].NvLink)
}

func TestToCPUMetric(t *testing.T) {
	labelCounter := counters.Counter{FieldID: 1, FieldName: "cpu_label", PromType: "label"}
	valueCounter := counters.Counter{FieldID: 2, FieldName: "cpu_util", PromType: "gauge"}
	metrics := MetricsByCounter{}
	mi := devicemonitoring.Info{
		Entity:     dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: 11},
		ParentId:   2,
		ParentType: dcgm.FE_CPU,
	}

	toCPUMetric(metrics, []dcgm.FieldValue_v1{
		stringFieldValue(labelCounter.FieldID, "socket-a"),
		int64FieldValue(valueCounter.FieldID, 91),
	}, []counters.Counter{labelCounter, valueCounter}, mi, false, "host-a", "")

	require.Len(t, metrics[valueCounter], 1)
	got := metrics[valueCounter][0]
	assert.Equal(t, "91", got.Value)
	assert.Equal(t, "UUID", got.UUID)
	assert.Equal(t, "11", got.GPU)
	assert.Equal(t, "2", got.GPUDevice)
	assert.Equal(t, map[string]string{"cpu_label": "socket-a"}, got.Labels)
	assert.Empty(t, got.CPUSerial)
}

func TestToGPUNvLinkMetric(t *testing.T) {
	labelCounter := counters.Counter{FieldID: 1, FieldName: "link_label", PromType: "label"}
	valueCounter := counters.Counter{FieldID: 2, FieldName: "link_errors", PromType: "gauge"}
	metrics := MetricsByCounter{}
	mi := devicemonitoring.Info{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: 5},
		DeviceInfo: dcgm.Device{
			GPU:  4,
			UUID: "GPU-4",
			Identifiers: dcgm.DeviceIdentifiers{
				Model: "NVIDIA T400 4GB",
			},
			PCI: dcgm.PCIInfo{BusID: "0000:04:00.0"},
		},
		ParentType: dcgm.FE_GPU,
	}

	toGPUNvLinkMetric(metrics, []dcgm.FieldValue_v1{
		stringFieldValue(labelCounter.FieldID, "link-a"),
		int64FieldValue(valueCounter.FieldID, 12),
	}, []counters.Counter{labelCounter, valueCounter}, mi, "host-a")

	require.Len(t, metrics[valueCounter], 1)
	got := metrics[valueCounter][0]
	assert.Equal(t, "12", got.Value)
	assert.Equal(t, "4", got.GPU)
	assert.Equal(t, "GPU-4", got.GPUUUID)
	assert.Equal(t, "5", got.NvLink)
	assert.Equal(t, "nvidia4", got.GPUDevice)
	assert.Equal(t, "NVIDIA T400 4GB", got.GPUModelName)
	assert.Equal(t, "0000:04:00.0", got.GPUPCIBusID)
	assert.Equal(t, map[string]string{"link_label": "link-a"}, got.Labels)
}

func TestToMetricWithMIGInstanceAndLabels(t *testing.T) {
	labelCounter := counters.Counter{FieldID: 1, FieldName: "driver", PromType: "label"}
	valueCounter := counters.Counter{FieldID: 2, FieldName: "gpu_temp", PromType: "gauge"}
	metrics := MetricsByCounter{}
	mi := devicemonitoring.Info{
		DeviceInfo: dcgm.Device{
			GPU:         2,
			UUID:        "GPU-2",
			Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA A100"},
		},
		InstanceInfo: &deviceinfo.GPUInstanceInfo{
			Info:        dcgm.MigEntityInfo{NvmlInstanceId: 8},
			ProfileName: "1g.10gb",
		},
		ParentType: dcgm.FE_GPU,
	}

	toMetric(metrics, []dcgm.FieldValue_v1{
		stringFieldValue(labelCounter.FieldID, "555.55"),
		int64FieldValue(valueCounter.FieldID, 64),
	}, []counters.Counter{labelCounter, valueCounter}, mi, true, "host-a", false)

	require.Len(t, metrics[valueCounter], 1)
	got := metrics[valueCounter][0]
	assert.Equal(t, "uuid", got.UUID)
	assert.Equal(t, "1g.10gb", got.MigProfile)
	assert.Equal(t, "8", got.GPUInstanceID)
	assert.Equal(t, map[string]string{"driver": "555.55"}, got.Labels)
}

func TestToMetricSuppressesCollectedHostnameLabelsWhenNoHostnameIsEnabled(t *testing.T) {
	hostnameCounter := counters.Counter{FieldID: 1, FieldName: "Hostname", PromType: "label"}
	valueCounter := counters.Counter{FieldID: 2, FieldName: "DCGM_FI_PROF_PIPE_TENSOR_ACTIVE", PromType: "gauge"}
	mi := devicemonitoring.Info{
		DeviceInfo: dcgm.Device{
			GPU:         0,
			UUID:        "GPU-0",
			Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA GB200"},
		},
		ParentType: dcgm.FE_GPU,
	}

	t.Run("no hostname omits collected hostname label", func(t *testing.T) {
		metrics := MetricsByCounter{}

		toMetric(metrics, []dcgm.FieldValue_v1{
			stringFieldValue(hostnameCounter.FieldID, "k3d-node"),
			int64FieldValue(valueCounter.FieldID, 7),
		}, []counters.Counter{hostnameCounter, valueCounter}, mi, false, "", false)

		require.Len(t, metrics[valueCounter], 1)
		assert.Empty(t, metrics[valueCounter][0].Hostname)
		assert.NotContains(t, metrics[valueCounter][0].Labels, "Hostname")
	})

	t.Run("normal hostname keeps collected hostname label", func(t *testing.T) {
		metrics := MetricsByCounter{}

		toMetric(metrics, []dcgm.FieldValue_v1{
			stringFieldValue(hostnameCounter.FieldID, "k3d-node"),
			int64FieldValue(valueCounter.FieldID, 7),
		}, []counters.Counter{hostnameCounter, valueCounter}, mi, false, "exporter-node", false)

		require.Len(t, metrics[valueCounter], 1)
		assert.Equal(t, "exporter-node", metrics[valueCounter][0].Hostname)
		assert.Equal(t, "k3d-node", metrics[valueCounter][0].Labels["Hostname"])
	})
}

func TestToString(t *testing.T) {
	tests := []struct {
		name  string
		value dcgm.FieldValue_v1
		want  string
	}{
		{
			name:  "positive integer",
			value: dcgm.FieldValue_v1{FieldType: dcgm.DCGM_FT_INT64, Value: createInt64ByteArray(42)},
			want:  "42",
		},
		{
			name:  "negative integer",
			value: dcgm.FieldValue_v1{FieldType: dcgm.DCGM_FT_INT64, Value: createInt64ByteArray(-42)},
			want:  "-42",
		},
		{
			name:  "double",
			value: dcgm.FieldValue_v1{FieldType: dcgm.DCGM_FT_DOUBLE, Value: createFloat64ByteArray(-3.5)},
			want:  "-3.500000",
		},
		{
			name:  "string",
			value: dcgm.FieldValue_v1{FieldType: dcgm.DCGM_FT_STRING, Value: createStringByteArray("value")},
			want:  "value",
		},
		{
			name:  "non-OK status",
			value: dcgm.FieldValue_v1{Status: dcgm.DCGM_ST_NOT_WATCHED, FieldType: dcgm.DCGM_FT_INT64},
			want:  skipDCGMValue,
		},
		{
			name:  "blank integer",
			value: dcgm.FieldValue_v1{FieldType: dcgm.DCGM_FT_INT64, Value: createInt64ByteArray(dcgm.DCGM_FT_INT64_BLANK)},
			want:  skipDCGMValue,
		},
		{
			name:  "blank double",
			value: dcgm.FieldValue_v1{FieldType: dcgm.DCGM_FT_DOUBLE, Value: createFloat64ByteArray(dcgm.DCGM_FT_FP64_BLANK)},
			want:  skipDCGMValue,
		},
		{
			name:  "blank string",
			value: dcgm.FieldValue_v1{FieldType: dcgm.DCGM_FT_STRING, Value: createStringByteArray(dcgm.DCGM_FT_STR_BLANK)},
			want:  skipDCGMValue,
		},
		{
			name:  "unsupported field type",
			value: dcgm.FieldValue_v1{FieldType: dcgm.DCGM_FT_BINARY},
			want:  skipDCGMValue,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, toString(test.value))
		})
	}
}
