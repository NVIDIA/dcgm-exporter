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
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockcollectorpkg "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/collector"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mockdevicewatchlistmanager "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatchlistmanager"
	mocktransformation "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/transformation"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/transformation"
)

const expectedResponse = "# HELP TEST_METRIC \n" +
	"# TYPE TEST_METRIC gauge\n" +
	`TEST_METRIC{gpu="0",` +
	`UUID="GPU-00000000-0000-0000-0000-000000000000",` +
	`pci_bus_id="",` +
	`device="nvidia0",` +
	`modelName="NVIDIA T400 4GB",` +
	`hostname="testhost"} 42` + "\n"

var deviceWatcher = devicewatcher.NewDeviceWatcher()

func getMetricsByCounterWithTestMetric() collector.MetricsByCounter {
	return getMetricsByCounterWithTestMetricValue("42")
}

func getMetricsByCounterWithTestMetricValue(value string) collector.MetricsByCounter {
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
		Value:        value,
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
				assert.Equal(t, prometheusTextContentType, recorder.Header().Get("Content-Type"))
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
				mockTransformation.EXPECT().Name().Return("mock-transformer").AnyTimes()
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
		{
			name:  "Returns 500 when renderer rejects invalid metric value",
			group: dcgm.FE_GPU,
			collector: func() collector.Collector {
				invalidMetrics := getMetricsByCounterWithTestMetricValue(collector.FailedToConvert)
				mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(invalidMetrics, nil).AnyTimes()
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
			mockDeviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()

			defaultDeviceWatchList := *devicewatchlistmanager.NewWatchList(
				mockDeviceInfo,
				[]dcgm.Short{42},
				nil,
				deviceWatcher,
				1,
			)

			metricServer := &MetricsServer{
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
			metricServer.registry.Store(reg)

			recorder := httptest.NewRecorder()
			metricServer.Metrics(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			if tt.assert != nil {
				tt.assert(t, recorder)
			}
		})
	}
}

func TestMetricsReleasesRuntimeLockWhenRenderPanics(t *testing.T) {
	ctrl := gomock.NewController(t)

	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().GetMetrics().Return(getMetricsByCounterWithTestMetric(), nil)

	reg := registry.NewRegistry()
	entityCollectorTuple := collector.EntityCollectorTuple{}
	entityCollectorTuple.SetEntity(dcgm.FE_GPU)
	entityCollectorTuple.SetCollector(mockCollector)
	reg.Register(entityCollectorTuple)

	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{}).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()

	defaultDeviceWatchList := *devicewatchlistmanager.NewWatchList(
		mockDeviceInfo,
		[]dcgm.Short{42},
		nil,
		deviceWatcher,
		1,
	)

	mockDeviceWatchListManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	mockDeviceWatchListManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(defaultDeviceWatchList, true)

	panickingTransformation := mocktransformation.NewMockTransform(ctrl)
	panickingTransformation.EXPECT().
		Process(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ collector.MetricsByCounter, _ deviceinfo.Provider) error {
			panic("render panic")
		})

	metricServer := &MetricsServer{
		deviceWatchListManager: mockDeviceWatchListManager,
		transformations:        []transformation.Transform{panickingTransformation},
	}
	metricServer.registry.Store(reg)

	require.PanicsWithValue(t, "render panic", func() {
		metricServer.Metrics(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		metricServer.SetRegistry(registry.NewRegistry())
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SetRegistry blocked after Metrics panic; runtime read lock was not released")
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
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(0)).AnyTimes()

	defaultDeviceWatchList := *devicewatchlistmanager.NewWatchList(
		mockDeviceInfo,
		[]dcgm.Short{42},
		nil,
		deviceWatcher,
		1,
	)

	metricServer := &MetricsServer{
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
	metricServer.registry.Store(reg)
	recorder := &mockResponseWriter{}
	metricServer.Metrics(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
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
	// Set a registry so the code path reaches the write call
	metricServer.registry.Store(registry.NewRegistry())
	recorder := &mockResponseWriter{}
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
}

func TestHealthReturnsOKWhenRegistryIsNil(t *testing.T) {
	metricServer := &MetricsServer{}
	metricServer.registry.Store(nil)
	recorder := httptest.NewRecorder()
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "false", recorder.Header().Get("X-Registry-Available"))
	assert.Equal(t, "true", recorder.Header().Get("X-Reload-In-Progress"))
	assert.Contains(t, recorder.Body.String(), "OK - reload in progress")
}

func TestHealthReturnsOKDuringReload(t *testing.T) {
	metricServer := &MetricsServer{}
	metricServer.SetReloadInProgress(true)
	recorder := httptest.NewRecorder()
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "true", recorder.Header().Get("X-Reload-In-Progress"))
}

func TestHealthReturnsOKWithRegistryAvailable(t *testing.T) {
	metricServer := &MetricsServer{}
	reg := registry.NewRegistry()
	metricServer.registry.Store(reg)
	recorder := httptest.NewRecorder()
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "true", recorder.Header().Get("X-Registry-Available"))
	assert.NotEqual(t, "true", recorder.Header().Get("X-Reload-In-Progress"))
}

func TestPprofEndpointsDisabledByDefault(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	cfg := &appconfig.Config{Address: ":0"}
	srv, cleanup, err := NewMetricsServer(cfg, mockManager, registry.NewRegistry())
	require.NoError(t, err)
	defer cleanup()

	router := srv.server.Handler

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.NotContains(t, rec.Body.String(), "pprof")
}

func TestPprofEndpointsEnabledWhenFlagSet(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	cfg := &appconfig.Config{Address: ":0", EnablePprof: true}
	srv, cleanup, err := NewMetricsServer(cfg, mockManager, registry.NewRegistry())
	require.NoError(t, err)
	defer cleanup()

	router := srv.server.Handler

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Contains(t, rec.Body.String(), "pprof")
}

func TestNewMetricsServerConfiguresHTTPTimeouts(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	cfg := &appconfig.Config{
		Address:         ":0",
		WebReadTimeout:  2 * time.Second,
		WebWriteTimeout: 45 * time.Second,
	}
	srv, cleanup, err := NewMetricsServer(cfg, mockManager, registry.NewRegistry())
	require.NoError(t, err)
	defer cleanup()

	assert.Equal(t, 2*time.Second, srv.server.ReadTimeout)
	assert.Equal(t, 45*time.Second, srv.server.WriteTimeout)
}

func TestNewMetricsServerDefaultsHTTPTimeouts(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	cfg := &appconfig.Config{Address: ":0"}
	srv, cleanup, err := NewMetricsServer(cfg, mockManager, registry.NewRegistry())
	require.NoError(t, err)
	defer cleanup()

	assert.Equal(t, appconfig.DefaultWebReadTimeout, srv.server.ReadTimeout)
	assert.Equal(t, appconfig.DefaultWebWriteTimeout, srv.server.WriteTimeout)
}

func TestShutdownTimeoutUsesEffectiveWriteTimeout(t *testing.T) {
	tests := []struct {
		name         string
		writeTimeout time.Duration
		want         time.Duration
	}{
		{name: "configured write timeout", writeTimeout: 45 * time.Second, want: 45 * time.Second},
		{name: "default write timeout", want: appconfig.DefaultWebWriteTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shutdownTimeout(tt.writeTimeout))
		})
	}
}

func TestMetricsServerRunStartsAndStops(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	cfg := &appconfig.Config{
		Address: "127.0.0.1:0",
		DumpConfig: appconfig.DumpConfig{
			Enabled:   true,
			Directory: t.TempDir(),
			Retention: 1,
		},
	}
	srv, cleanup, err := NewMetricsServer(cfg, mockManager, registry.NewRegistry())
	require.NoError(t, err)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan interface{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Run(ctx, stop)
	}()

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("metrics server did not stop")
	}
}

func TestDumpMetricsToJSON(t *testing.T) {
	t.Run("empty registry", func(t *testing.T) {
		metricServer := &MetricsServer{}
		metricServer.registry.Store(registry.NewRegistry())

		data, err := metricServer.DumpMetricsToJSON()

		require.NoError(t, err)
		assert.JSONEq(t, `{"error":"no metrics found"}`, string(data))
	})

	t.Run("registry gather error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
		mockCollector.EXPECT().GetMetrics().Return(nil, errors.New("gather failed"))

		reg := registry.NewRegistry()
		tuple := collector.EntityCollectorTuple{}
		tuple.SetEntity(dcgm.FE_GPU)
		tuple.SetCollector(mockCollector)
		reg.Register(tuple)

		metricServer := &MetricsServer{}
		metricServer.registry.Store(reg)

		data, err := metricServer.DumpMetricsToJSON()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "gather failed")
		assert.Nil(t, data)
	})

	t.Run("metrics", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
		mockCollector.EXPECT().GetMetrics().Return(getMetricsByCounterWithTestMetric(), nil)

		reg := registry.NewRegistry()
		tuple := collector.EntityCollectorTuple{}
		tuple.SetEntity(dcgm.FE_GPU)
		tuple.SetCollector(mockCollector)
		reg.Register(tuple)

		metricServer := &MetricsServer{}
		metricServer.registry.Store(reg)

		data, err := metricServer.DumpMetricsToJSON()

		require.NoError(t, err)
		assert.Contains(t, string(data), "TEST_METRIC")
		assert.Contains(t, string(data), "testhost")
	})
}
