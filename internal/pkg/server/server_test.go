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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func newMetricsTestServer(
	t *testing.T,
	ctrl *gomock.Controller,
	testCollector collector.Collector,
	transforms []transformation.Transform,
	maxConcurrent int,
) *MetricsServer {
	t.Helper()

	reg := registry.NewRegistry()
	entityCollectorTuple := collector.EntityCollectorTuple{}
	entityCollectorTuple.SetEntity(dcgm.FE_GPU)
	entityCollectorTuple.SetCollector(testCollector)
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
	mockDeviceWatchListManager.EXPECT().
		EntityWatchList(dcgm.FE_GPU).
		Return(defaultDeviceWatchList, true).
		AnyTimes()

	metricServer := &MetricsServer{
		deviceWatchListManager: mockDeviceWatchListManager,
		transformations:        transforms,
		scrapes:                newScrapeCoordinator(maxConcurrent),
	}
	metricServer.registry.Store(reg)

	return metricServer
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
			name:  "Returns 500 when Transformer returns context cancellation for a live request",
			group: dcgm.FE_GPU,
			collector: func() collector.Collector {
				mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(metrics, nil).AnyTimes()
				return mockCollector
			},
			transformer: func() transformation.Transform {
				mockTransformation := mocktransformation.NewMockTransform(ctrl)
				mockTransformation.EXPECT().Process(gomock.Any(), gomock.Any()).Return(context.Canceled).AnyTimes()
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
				scrapes: newScrapeCoordinator(1),
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

type blockingResponseWriter struct {
	*httptest.ResponseRecorder

	writeStarted chan struct{}
	releaseWrite <-chan struct{}
}

func (w *blockingResponseWriter) Write(response []byte) (int, error) {
	close(w.writeStarted)
	<-w.releaseWrite

	return w.ResponseRecorder.Write(response)
}

func TestMetricsReturnsServiceUnavailableWhileResponseIsBeingWritten(t *testing.T) {
	ctrl := gomock.NewController(t)

	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().GetMetrics().Return(getMetricsByCounterWithTestMetric(), nil)

	metricServer := newMetricsTestServer(t, ctrl, mockCollector, nil, 1)
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	firstWriter := &blockingResponseWriter{
		ResponseRecorder: httptest.NewRecorder(),
		writeStarted:     writeStarted,
		releaseWrite:     releaseWrite,
	}
	firstDone := make(chan struct{})

	go func() {
		defer close(firstDone)
		metricServer.Metrics(firstWriter, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	}()

	waitForSignal(t, writeStarted)

	overloaded := httptest.NewRecorder()
	metricServer.Metrics(overloaded, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	assert.Equal(t, http.StatusServiceUnavailable, overloaded.Code)
	assert.Equal(t, scrapeCapacityExceededMessage, strings.TrimSpace(overloaded.Body.String()))

	close(releaseWrite)
	waitForSignal(t, firstDone)
	assert.Equal(t, http.StatusOK, firstWriter.Code)
	assert.Equal(t, expectedResponse, firstWriter.Body.String())
}

func TestMetricsCoalescesOverlappingRequests(t *testing.T) {
	const requestCount = 4

	ctrl := gomock.NewController(t)
	producerStarted := make(chan struct{})
	releaseProducer := make(chan struct{})
	var gatherCalls atomic.Int32

	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().
		GetMetrics().
		DoAndReturn(func() (collector.MetricsByCounter, error) {
			gatherCalls.Add(1)
			close(producerStarted)
			<-releaseProducer

			return getMetricsByCounterWithTestMetric(), nil
		})

	metricServer := newMetricsTestServer(t, ctrl, mockCollector, nil, requestCount)
	waiting, responses := startMetricsRequests(metricServer, requestCount)

	waitForSignal(t, producerStarted)
	for _, waiter := range waiting {
		waitForSignal(t, waiter)
	}
	close(releaseProducer)

	for range requestCount {
		recorder := waitForMetricsResponse(t, responses)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, expectedResponse, recorder.Body.String())
	}
	assert.Equal(t, int32(1), gatherCalls.Load())
}

func TestMetricsCanceledJoinedRequestReleasesSlotWithoutCancelingProducer(t *testing.T) {
	ctrl := gomock.NewController(t)
	producerStarted := make(chan struct{})
	releaseProducer := make(chan struct{})
	var gatherCalls atomic.Int32

	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().
		GetMetrics().
		DoAndReturn(func() (collector.MetricsByCounter, error) {
			gatherCalls.Add(1)
			close(producerStarted)
			<-releaseProducer

			return getMetricsByCounterWithTestMetric(), nil
		})

	metricServer := newMetricsTestServer(t, ctrl, mockCollector, nil, 2)

	canceledContext := newControlledWaitContext()
	canceledResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/metrics", nil).WithContext(canceledContext)
		metricServer.Metrics(recorder, request)
		canceledResponse <- recorder
	}()

	waitForSignal(t, producerStarted)
	waitForSignal(t, canceledContext.waiting)

	joinedWaiting := make(chan struct{})
	joinedResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		ctx := &waitObservedContext{
			Context: context.Background(),
			waiting: joinedWaiting,
		}
		request := httptest.NewRequest(http.MethodGet, "/metrics", nil).WithContext(ctx)
		metricServer.Metrics(recorder, request)
		joinedResponse <- recorder
	}()

	waitForSignal(t, joinedWaiting)
	canceledContext.finish(context.Canceled)

	recorder := waitForMetricsResponse(t, canceledResponse)
	assert.Empty(t, recorder.Body.String())
	require.True(t, metricServer.scrapes.tryAcquire(), "canceled request did not release its admission slot")
	metricServer.scrapes.release()

	close(releaseProducer)
	recorder = waitForMetricsResponse(t, joinedResponse)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, expectedResponse, recorder.Body.String())
	assert.Equal(t, int32(1), gatherCalls.Load())
}

func TestMetricsSharedGatherErrorReturnsInternalServerError(t *testing.T) {
	const requestCount = 4

	ctrl := gomock.NewController(t)
	producerStarted := make(chan struct{})
	releaseProducer := make(chan struct{})
	var gatherCalls atomic.Int32

	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().
		GetMetrics().
		DoAndReturn(func() (collector.MetricsByCounter, error) {
			gatherCalls.Add(1)
			close(producerStarted)
			<-releaseProducer

			return nil, errors.New("gather failed")
		})

	metricServer := newMetricsTestServer(t, ctrl, mockCollector, nil, requestCount)
	waiting, responses := startMetricsRequests(metricServer, requestCount)

	waitForSignal(t, producerStarted)
	for _, waiter := range waiting {
		waitForSignal(t, waiter)
	}
	close(releaseProducer)

	for range requestCount {
		recorder := waitForMetricsResponse(t, responses)
		assert.Equal(t, http.StatusInternalServerError, recorder.Code)
		assert.Equal(t, internalServerError, strings.TrimSpace(recorder.Body.String()))
	}
	assert.Equal(t, int32(1), gatherCalls.Load())
}

func TestMetricsRecoversSharedProducerPanicAndStartsFreshFlight(t *testing.T) {
	const requestCount = 4

	ctrl := gomock.NewController(t)
	producerStarted := make(chan struct{})
	releaseProducer := make(chan struct{})
	var gatherCalls atomic.Int32
	var transformCalls atomic.Int32

	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().
		GetMetrics().
		DoAndReturn(func() (collector.MetricsByCounter, error) {
			if gatherCalls.Add(1) == 1 {
				close(producerStarted)
				<-releaseProducer
			}

			return getMetricsByCounterWithTestMetric(), nil
		}).
		Times(2)

	panickingTransformation := mocktransformation.NewMockTransform(ctrl)
	panickingTransformation.EXPECT().
		Process(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ collector.MetricsByCounter, _ deviceinfo.Provider) error {
			if transformCalls.Add(1) == 1 {
				panic("render panic")
			}

			return nil
		}).
		Times(2)

	metricServer := newMetricsTestServer(
		t,
		ctrl,
		mockCollector,
		[]transformation.Transform{panickingTransformation},
		requestCount,
	)
	reg := metricServer.registry.Load()
	waiting, responses := startMetricsRequests(metricServer, requestCount)

	waitForSignal(t, producerStarted)
	for _, waiter := range waiting {
		waitForSignal(t, waiter)
	}
	close(releaseProducer)

	for range requestCount {
		recorder := waitForMetricsResponse(t, responses)
		assert.Equal(t, http.StatusInternalServerError, recorder.Code)
		assert.Equal(t, internalServerError, strings.TrimSpace(recorder.Body.String()))
	}
	assert.Equal(t, int32(1), gatherCalls.Load())

	lockReleased := make(chan struct{})
	go func() {
		defer close(lockReleased)
		metricServer.SetRegistry(reg)
	}()
	waitForSignal(t, lockReleased)

	fresh := httptest.NewRecorder()
	metricServer.Metrics(fresh, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusOK, fresh.Code)
	assert.Equal(t, expectedResponse, fresh.Body.String())
	assert.Equal(t, int32(2), gatherCalls.Load())
	assert.Equal(t, int32(2), transformCalls.Load())
}

func TestMetricsDoesNotCacheCompletedFlight(t *testing.T) {
	ctrl := gomock.NewController(t)
	var gatherCalls atomic.Int32

	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().
		GetMetrics().
		DoAndReturn(func() (collector.MetricsByCounter, error) {
			if gatherCalls.Add(1) == 1 {
				return getMetricsByCounterWithTestMetricValue("41"), nil
			}

			return getMetricsByCounterWithTestMetricValue("42"), nil
		}).
		Times(2)

	metricServer := newMetricsTestServer(t, ctrl, mockCollector, nil, 1)
	first := httptest.NewRecorder()
	metricServer.Metrics(first, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	second := httptest.NewRecorder()
	metricServer.Metrics(second, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	assert.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, http.StatusOK, second.Code)
	assert.NotEqual(t, first.Body.String(), second.Body.String())
	assert.Contains(t, first.Body.String(), `hostname="testhost"} 41`)
	assert.Contains(t, second.Body.String(), `hostname="testhost"} 42`)
	assert.Equal(t, int32(2), gatherCalls.Load())
}

func TestMetricsReturnsImmediatelyForCanceledRequest(t *testing.T) {
	metricServer := &MetricsServer{
		scrapes: newScrapeCoordinator(1),
	}
	require.True(t, metricServer.scrapes.tryAcquire())
	defer metricServer.scrapes.release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil).WithContext(ctx)

	metricServer.Metrics(recorder, request)

	assert.Empty(t, recorder.Body.String())
}

func TestMetricsServerInitializesScrapeCoordinator(t *testing.T) {
	tests := []struct {
		name          string
		configuredMax int
		wantCapacity  int
		wantErr       string
	}{
		{
			name:          "configured value",
			configuredMax: 3,
			wantCapacity:  3,
		},
		{
			name:    "zero",
			wantErr: "max concurrent scrapes must be greater than zero: 0",
		},
		{
			name:          "negative",
			configuredMax: -1,
			wantErr:       "max concurrent scrapes must be greater than zero: -1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
			cfg := &appconfig.Config{
				Address:              ":0",
				MaxConcurrentScrapes: tt.configuredMax,
			}

			server, cleanup, err := NewMetricsServer(cfg, mockManager, registry.NewRegistry())
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Nil(t, server)
				assert.Nil(t, cleanup)

				return
			}

			require.NoError(t, err)
			defer cleanup()

			require.NotNil(t, server.scrapes)
			assert.Equal(t, tt.wantCapacity, cap(server.scrapes.slots))
		})
	}
}

func startMetricsRequests(
	metricServer *MetricsServer,
	requestCount int,
) ([]chan struct{}, <-chan *httptest.ResponseRecorder) {
	waiting := make([]chan struct{}, requestCount)
	responses := make(chan *httptest.ResponseRecorder, requestCount)

	for i := range requestCount {
		waiting[i] = make(chan struct{})
		ctx := &waitObservedContext{
			Context: context.Background(),
			waiting: waiting[i],
		}

		go func() {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil).WithContext(ctx)
			metricServer.Metrics(recorder, request)
			responses <- recorder
		}()
	}

	return waiting, responses
}

func waitForMetricsResponse(
	t *testing.T,
	responses <-chan *httptest.ResponseRecorder,
) *httptest.ResponseRecorder {
	t.Helper()

	select {
	case response := <-responses:
		return response
	case <-time.After(coordinatorTestTimeout):
		t.Fatal("timed out waiting for metrics response")

		return nil
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
		scrapes:         newScrapeCoordinator(1),
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

func TestHealthReturnsOKWithNoGPUCollectorsWhenNotRequired(t *testing.T) {
	// Default behaviour must not change: an exporter with no GPU collector still
	// reports healthy unless HealthRequireGPUs is set.
	metricServer := &MetricsServer{config: &appconfig.Config{}}
	metricServer.registry.Store(registry.NewRegistry())
	recorder := httptest.NewRecorder()
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Empty(t, recorder.Header().Get("X-GPU-Collectors"))
}

func TestHealthReturnsUnavailableWhenGPUCollectorsRequiredButAbsent(t *testing.T) {
	metricServer := &MetricsServer{config: &appconfig.Config{HealthRequireGPUs: true}}
	metricServer.registry.Store(registry.NewRegistry())
	recorder := httptest.NewRecorder()
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, "0", recorder.Header().Get("X-GPU-Collectors"))
	assert.Contains(t, recorder.Body.String(), "no GPU collector registered")
}

func TestHealthReturnsUnavailableWhenOnlyNonGPUCollectorsRegistered(t *testing.T) {
	// The incident shape: the registry is not empty, it holds a single non-GPU
	// collector (collector_count=1). A predicate that counted collectors rather
	// than keying on FE_GPU would report healthy here and be a no-op in exactly
	// the scenario this flag exists for.
	ctrl := gomock.NewController(t)
	reg := registry.NewRegistry()
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_CPU)
	tuple.SetCollector(mockcollectorpkg.NewMockCollector(ctrl))
	reg.Register(tuple)

	metricServer := &MetricsServer{config: &appconfig.Config{HealthRequireGPUs: true}}
	metricServer.registry.Store(reg)
	recorder := httptest.NewRecorder()
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.Equal(t, "0", recorder.Header().Get("X-GPU-Collectors"))
}

func TestHealthReturnsOKWhenGPUCollectorsRequiredAndPresent(t *testing.T) {
	ctrl := gomock.NewController(t)
	reg := registry.NewRegistry()
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(mockcollectorpkg.NewMockCollector(ctrl))
	reg.Register(tuple)

	metricServer := &MetricsServer{config: &appconfig.Config{HealthRequireGPUs: true}}
	metricServer.registry.Store(reg)
	recorder := httptest.NewRecorder()
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "true", recorder.Header().Get("X-Registry-Available"))
}

func TestHealthReturnsOKDuringReloadEvenWhenGPUCollectorsRequired(t *testing.T) {
	// Reload takes precedence: the registry is legitimately empty mid-reload and
	// must not be reported unhealthy, or every reload would restart the pod.
	metricServer := &MetricsServer{config: &appconfig.Config{HealthRequireGPUs: true}}
	metricServer.registry.Store(registry.NewRegistry())
	metricServer.SetReloadInProgress(true)
	recorder := httptest.NewRecorder()
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestPprofEndpointsDisabledByDefault(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	cfg := &appconfig.Config{Address: ":0", MaxConcurrentScrapes: appconfig.DefaultMaxConcurrentScrapes}
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
	cfg := &appconfig.Config{Address: ":0", MaxConcurrentScrapes: appconfig.DefaultMaxConcurrentScrapes, EnablePprof: true}
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
		Address:              ":0",
		MaxConcurrentScrapes: appconfig.DefaultMaxConcurrentScrapes,
		WebReadTimeout:       2 * time.Second,
		WebWriteTimeout:      45 * time.Second,
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
	cfg := &appconfig.Config{Address: ":0", MaxConcurrentScrapes: appconfig.DefaultMaxConcurrentScrapes}
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
		Address:              "127.0.0.1:0",
		MaxConcurrentScrapes: appconfig.DefaultMaxConcurrentScrapes,
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

func TestMetricsServerRunWaitsForDetachedScrape(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	cfg := &appconfig.Config{Address: "127.0.0.1:0", MaxConcurrentScrapes: appconfig.DefaultMaxConcurrentScrapes}
	srv, cleanup, err := NewMetricsServer(cfg, mockManager, registry.NewRegistry())
	require.NoError(t, err)
	defer cleanup()

	producerStarted := make(chan struct{})
	releaseProducer := make(chan struct{})
	requestContext, cancelRequest := context.WithCancel(context.Background())
	requestResult := make(chan scrapeResult, 1)
	go func() {
		response, err := srv.scrapes.do(requestContext, func() ([]byte, error) {
			close(producerStarted)
			<-releaseProducer

			return []byte("metrics"), nil
		})
		requestResult <- scrapeResult{response: response, err: err}
	}()

	waitForSignal(t, producerStarted)
	cancelRequest()
	result := waitForScrapeResult(t, requestResult)
	require.ErrorIs(t, result.err, context.Canceled)

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
		t.Fatal("metrics server stopped before the detached scrape producer")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseProducer)
	waitForSignal(t, done)
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

func TestMetricsWithRepeatedHPCJobIDs(t *testing.T) {
	mappingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(mappingDir, "0"), []byte("job1\njob2\njob1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(mappingDir, "1"), []byte("job1\n"), 0o600))
	ctrl := gomock.NewController(t)
	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().GetMetrics().DoAndReturn(func() (collector.MetricsByCounter, error) {
		metrics := getMetricsByCounterWithTestMetric()
		secondGPU := metrics[getTestMetric()][0]
		secondGPU.GPU = "1"
		secondGPU.GPUDevice = "nvidia1"
		secondGPU.GPUUUID = "GPU-00000000-0000-0000-0000-000000000001"
		metrics[getTestMetric()] = append(metrics[getTestMetric()], secondGPU)
		return metrics, nil
	}).Times(2)
	reg := registry.NewRegistry()
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(mockCollector)
	reg.Register(tuple)
	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{}).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(2)).AnyTimes()
	mockDeviceInfo.EXPECT().GPUs().Return(nil).AnyTimes()
	watchList := *devicewatchlistmanager.NewWatchList(mockDeviceInfo, []dcgm.Short{42}, nil, deviceWatcher, 1)
	watchManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	watchManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(watchList, true).Times(2)
	metricServer := &MetricsServer{
		deviceWatchListManager: watchManager,
		transformations:        transformation.GetTransformations(&appconfig.Config{HPCJobMappingDir: mappingDir}),
		scrapes:                newScrapeCoordinator(1),
	}
	metricServer.registry.Store(reg)
	recorder := httptest.NewRecorder()
	metricServer.Metrics(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, 2, strings.Count(recorder.Body.String(), `hpc_job="job1"`))
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `hpc_job="job2"`))

	require.NoError(t, os.WriteFile(filepath.Join(mappingDir, "0"), []byte("job3\njob3\n"), 0o600))
	recorder = httptest.NewRecorder()
	metricServer.Metrics(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `hpc_job="job1"`))
	require.NotContains(t, recorder.Body.String(), `hpc_job="job2"`)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `hpc_job="job3"`))
}
