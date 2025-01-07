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

package server

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	mockcollectorpkg "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/collector"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mockdevicewatchlistmanager "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatchlistmanager"
	mocktransformation "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/transformation"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/transformation"
)

const expectedResponse = `# HELP TEST_METRIC 
# TYPE TEST_METRIC gauge
TEST_METRIC{gpu="0",UUID="GPU-00000000-0000-0000-0000-000000000000",pci_bus_id="",device="nvidia0",modelName="NVIDIA T400 4GB",Hostname="testhost"} 42
`

var deviceWatcher = devicewatcher.NewDeviceWatcher()

func getMetricsByCounterWithTestMetric() collector.MetricsByCounter {
	metrics := collector.MetricsByCounter{}
	counter := getTestMetric()

	metrics[counter] = append(metrics[counter], collector.Metric{
		GPU:          "0",
		GPUDevice:    "nvidia0",
		GPUModelName: "NVIDIA T400 4GB",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-00000000-0000-0000-0000-000000000000",
		Counter:      counter,
		Value:        "42",
		Attributes:   map[string]string{},
	})
	return metrics
}

func getTestMetric() counters.Counter {
	counter := counters.Counter{
		FieldID:   2000,
		FieldName: "TEST_METRIC",
		PromType:  "gauge",
	}
	return counter
}

func TestMetrics(t *testing.T) {
	ctrl := gomock.NewController(t)

	metrics := getMetricsByCounterWithTestMetric()

	tests := []struct {
		name        string
		group       dcgm.Field_Entity_Group
		collector   func() collector.Collector
		transformer func() transformation.Transform
		assert      func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:  "Returns 200",
			group: dcgm.FE_GPU,
			collector: func() collector.Collector {
				mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(metrics, nil).AnyTimes()
				return mockCollector
			},
			transformer: func() transformation.Transform {
				mockTransformation := mocktransformation.NewMockTransform(ctrl)
				mockTransformation.EXPECT().Process(gomock.Any(), gomock.Any())
				return mockTransformation
			},
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, recorder.Code)
				assert.Equal(t, expectedResponse, recorder.Body.String())
			},
		},
		{
			name:  "Returns 500 when Collector return error",
			group: dcgm.FE_GPU,
			collector: func() collector.Collector {
				mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(nil, errors.New("boom")).AnyTimes()
				return mockCollector
			},
			transformer: func() transformation.Transform {
				return mocktransformation.NewMockTransform(ctrl)
			},
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusInternalServerError, recorder.Code)
				assert.Equal(t, internalServerError, strings.TrimSpace(recorder.Body.String()))
			},
		},
		{
			name:  "Returns 500 when Transformer returns error",
			group: dcgm.FE_GPU,
			collector: func() collector.Collector {
				mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(metrics, nil).AnyTimes()
				return mockCollector
			},
			transformer: func() transformation.Transform {
				mockTransformation := mocktransformation.NewMockTransform(ctrl)
				mockTransformation.EXPECT().Process(gomock.Any(), gomock.Any()).Return(errors.New("boom")).AnyTimes()
				return mockTransformation
			},
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusInternalServerError, recorder.Code)
				assert.Equal(t, internalServerError, strings.TrimSpace(recorder.Body.String()))
			},
		},
		{
			name:  "Returns 500 when group is unknown",
			group: dcgm.FE_NONE,
			collector: func() collector.Collector {
				mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(metrics, nil).AnyTimes()
				return mockCollector
			},
			transformer: func() transformation.Transform {
				mockTransformation := mocktransformation.NewMockTransform(ctrl)
				mockTransformation.EXPECT().Process(gomock.Any(), gomock.Any())
				return mockTransformation
			},
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusInternalServerError, recorder.Code)
				assert.Equal(t, internalServerError, strings.TrimSpace(recorder.Body.String()))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := registry.NewRegistry()
			entityCollectorTuple := collector.EntityCollectorTuple{}
			entityCollectorTuple.SetEntity(tt.group)
			entityCollectorTuple.SetCollector(tt.collector())
			reg.Register(entityCollectorTuple)

			mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
			mockDeviceInfo.EXPECT().InfoType().Return(tt.group).AnyTimes()
			mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{}).AnyTimes()

			defaultDeviceWatchList := *devicewatchlistmanager.NewWatchList(
				mockDeviceInfo,
				[]dcgm.Short{42},
				nil,
				deviceWatcher,
				1,
			)

			metricServer := &MetricsServer{
				registry: reg,
				deviceWatchListManager: func(group dcgm.Field_Entity_Group) devicewatchlistmanager.Manager {
					mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
					mockDeviceWatchListManager.EXPECT().EntityWatchList(group).Return(defaultDeviceWatchList,
						true).AnyTimes()
					return mockDeviceWatchListManager
				}(tt.group),
				transformations: []transformation.Transform{
					tt.transformer(),
				},
			}

			recorder := httptest.NewRecorder()
			metricServer.Metrics(recorder, nil)
			if tt.assert != nil {
				tt.assert(t, recorder)
			}
		})
	}
}

// mockResponseWriter is a custom writer that simulates a network operation error.
type mockResponseWriter struct {
	httptest.ResponseRecorder
}

func (m *mockResponseWriter) Write([]byte) (int, error) {
	// Simulate a network operation error.
	return 0, &net.OpError{
		Op:     "write",
		Net:    "tcp",
		Source: nil,
		Addr:   nil,
		Err:    syscall.EPIPE,
	}
}

func TestMetricsReturnsErrorWhenClientClosedConnection(t *testing.T) {
	ctrl := gomock.NewController(t)

	metrics := getMetricsByCounterWithTestMetric()

	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().GetMetrics().Return(metrics, nil).AnyTimes()

	reg := registry.NewRegistry()
	entityCollectorTuple := collector.EntityCollectorTuple{}
	entityCollectorTuple.SetEntity(dcgm.FE_GPU)
	entityCollectorTuple.SetCollector(mockCollector)
	reg.Register(entityCollectorTuple)

	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_CPU).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{}).AnyTimes()

	defaultDeviceWatchList := *devicewatchlistmanager.NewWatchList(
		mockDeviceInfo,
		[]dcgm.Short{42},
		nil,
		deviceWatcher,
		1,
	)

	metricServer := &MetricsServer{
		registry: reg,
		deviceWatchListManager: func() devicewatchlistmanager.Manager {
			mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
			mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_CPU).Return(defaultDeviceWatchList,
				true).AnyTimes()
			mockDeviceWatchListManager.EXPECT().EntityWatchList(gomock.Any()).Return(devicewatchlistmanager.WatchList{},
				false).AnyTimes()
			return mockDeviceWatchListManager
		}(),
		transformations: []transformation.Transform{},
	}
	recorder := &mockResponseWriter{}
	metricServer.Metrics(recorder, nil)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Nil(t, recorder.Body)
}

func TestHealthReturnsOK(t *testing.T) {
	metricServer := &MetricsServer{}
	recorder := httptest.NewRecorder()
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestHealthReturnsOKWhenWriteReturnsError(t *testing.T) {
	metricServer := &MetricsServer{}
	recorder := &mockResponseWriter{}
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
}
