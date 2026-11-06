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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/prometheus/exporter-toolkit/web"
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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/debug"
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
	require.NoError(t, reg.Register(entityCollectorTuple))

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
			name:  "Returns 200 for unscoped collector metrics",
			group: dcgm.FE_NONE,
			collector: func() collector.Collector {
				mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(metrics, nil).AnyTimes()
				return mockCollector
			},
			transformer: func() transformation.Transform {
				return mocktransformation.NewMockTransform(ctrl)
			},
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, recorder.Code)
				assert.Equal(t, "# HELP TEST_METRIC \n# TYPE TEST_METRIC gauge\nTEST_METRIC{hostname=\"testhost\"} 42\n", recorder.Body.String())
			},
		},
		{
			name:  "Returns 500 when group is unknown",
			group: dcgm.Field_Entity_Group(42),
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
			require.NoError(t, reg.Register(entityCollectorTuple))

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

// TestGatherAndRenderRetainsRegistryUntilRenderingCompletes verifies retirement
// waits for the complete old-runtime scrape path, including transformations
// after collector gathering has returned.
func TestGatherAndRenderRetainsRegistryUntilRenderingCompletes(t *testing.T) {
	ctrl := gomock.NewController(t)
	metrics := getMetricsByCounterWithTestMetric()
	testCollector := mockcollectorpkg.NewMockCollector(ctrl)
	testCollector.EXPECT().GetMetrics().Return(metrics, nil).Times(1)
	testCollector.EXPECT().Cleanup().Times(1)

	renderStarted := make(chan struct{})
	renderRelease := make(chan struct{})
	transformer := mocktransformation.NewMockTransform(ctrl)
	transformer.EXPECT().
		Process(gomock.Any(), gomock.Any()).
		DoAndReturn(func(collector.MetricsByCounter, deviceinfo.Provider) error {
			close(renderStarted)
			<-renderRelease
			return nil
		})

	metricServer := newMetricsTestServer(t, ctrl, testCollector, []transformation.Transform{transformer}, 1)
	currentRegistry := metricServer.registry.Load()
	recorded := bytes.Buffer{}
	rendered := make(chan error, 1)
	go func() {
		rendered <- metricServer.gatherAndRenderMetrics(context.Background(), &recorded)
	}()

	select {
	case <-renderStarted:
	case <-time.After(time.Second):
		t.Fatal("metric rendering did not start")
	}

	retired := currentRegistry.Retire()
	select {
	case <-retired:
		t.Fatal("registry retired while an old-runtime response was still rendering")
	default:
	}

	close(renderRelease)
	select {
	case err := <-rendered:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("metric rendering did not finish")
	}
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("registry did not retire after rendering finished")
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
	require.NoError(t, reg.Register(entityCollectorTuple))

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

// TestNewMetricsServerDefaultsHTTPTimeoutsAndSkipsDisabledDumper checks that the
// constructor supplies default timeouts without creating disabled debug support.
func TestNewMetricsServerDefaultsHTTPTimeoutsAndSkipsDisabledDumper(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	cfg := &appconfig.Config{Address: ":0", MaxConcurrentScrapes: appconfig.DefaultMaxConcurrentScrapes}
	srv, cleanup, err := NewMetricsServer(cfg, mockManager, registry.NewRegistry())
	require.NoError(t, err)
	defer cleanup()

	assert.Nil(t, srv.fileDumper)
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

// TestMetricsServerRunStartsAndStops checks that enabled debug dumping creates a runnable dumper.
func TestMetricsServerRunStartsAndStops(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	cfg := &appconfig.Config{
		Address:              "127.0.0.1:0",
		MaxConcurrentScrapes: appconfig.DefaultMaxConcurrentScrapes,
		DumpConfig:           appconfig.DefaultDumpConfig(),
	}
	cfg.DumpConfig.Enabled = true
	cfg.DumpConfig.Directory = t.TempDir()
	srv, cleanup, err := NewMetricsServer(cfg, mockManager, registry.NewRegistry())
	require.NoError(t, err)
	defer cleanup()
	assert.NotNil(t, srv.fileDumper)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan struct{})
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
	stop := make(chan struct{})
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

// TestMetricsServerMetricsReportsFatalDCGMError verifies that a fatal DCGM
// scrape remains an HTTP error and reaches Run through its bounded error path.
func TestMetricsServerMetricsReportsFatalDCGMError(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().GetMetrics().Return(nil, &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID})

	reg := registry.NewRegistry()
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(mockCollector)
	require.NoError(t, reg.Register(tuple))

	metricsServer := &MetricsServer{
		scrapes:        newScrapeCoordinator(1),
		terminalErrors: make(chan error, 1),
	}
	metricsServer.registry.Store(reg)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()

	metricsServer.Metrics(response, request)

	assert.Equal(t, http.StatusInternalServerError, response.Code)
	select {
	case err := <-metricsServer.terminalErrors:
		require.Error(t, err)
		assert.True(t, dcgmerrors.IsFatalScrapeError(err))
	default:
		t.Fatal("fatal scrape error was not reported")
	}
}

// TestMetricsServerReportsFatalDCGMErrorAfterRequestCancellation verifies the
// producer reports a terminal DCGM failure even after its HTTP waiter leaves.
func TestMetricsServerReportsFatalDCGMErrorAfterRequestCancellation(t *testing.T) {
	ctrl := gomock.NewController(t)
	producerStarted := make(chan struct{})
	releaseProducer := make(chan struct{})
	fatalErr := &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID}
	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().GetMetrics().DoAndReturn(func() (collector.MetricsByCounter, error) {
		close(producerStarted)
		<-releaseProducer
		return nil, fatalErr
	})

	reg := registry.NewRegistry()
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(mockCollector)
	require.NoError(t, reg.Register(tuple))

	metricsServer := &MetricsServer{
		scrapes:        newScrapeCoordinator(1),
		terminalErrors: make(chan error, 1),
	}
	metricsServer.registry.Store(reg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	responseDone := make(chan struct{})
	go func() {
		defer close(responseDone)
		metricsServer.Metrics(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil).WithContext(ctx))
	}()

	waitForSignal(t, producerStarted)
	cancel()
	waitForSignal(t, responseDone)
	close(releaseProducer)

	select {
	case err := <-metricsServer.terminalErrors:
		require.ErrorIs(t, err, fatalErr)
	case <-time.After(coordinatorTestTimeout):
		t.Fatal("fatal scrape error was not reported after request cancellation")
	}
}

// TestMetricsServerReportsOnlyFirstTerminalError verifies that concurrent
// fatal scrapes cannot replace or queue a second application shutdown reason.
func TestMetricsServerReportsOnlyFirstTerminalError(t *testing.T) {
	first := errors.New("first fatal scrape")
	metricsServer := &MetricsServer{terminalErrors: make(chan error, 1)}

	metricsServer.reportTerminalError(first)
	metricsServer.reportTerminalError(errors.New("second fatal scrape"))

	require.ErrorIs(t, <-metricsServer.terminalErrors, first)
	require.ErrorIs(t, metricsServer.TerminalError(), first)
	select {
	case err := <-metricsServer.terminalErrors:
		t.Fatalf("unexpected second terminal error: %v", err)
	default:
	}
}

// TestMetricsServerRunRetainsTerminalErrorWhenStopIsReady verifies that a
// shutdown signal cannot discard an already reported terminal failure.
func TestMetricsServerRunRetainsTerminalErrorWhenStopIsReady(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	cfg := &appconfig.Config{Address: "127.0.0.1:0", MaxConcurrentScrapes: appconfig.DefaultMaxConcurrentScrapes}
	srv, cleanup, err := NewMetricsServer(cfg, mockManager, registry.NewRegistry())
	require.NoError(t, err)
	defer cleanup()

	fatalErr := fmt.Errorf("fatal metrics scrape: %w", &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID})
	srv.reportTerminalError(fatalErr)
	stop := make(chan struct{})
	close(stop)

	err = srv.Run(context.Background(), stop)

	require.ErrorIs(t, err, fatalErr)
}

// TestJoinAvailableServerErrorRetainsListenerFailure verifies that a ready
// stop signal cannot turn an already-reported listener startup failure into a
// successful Run result.
func TestJoinAvailableServerErrorRetainsListenerFailure(t *testing.T) {
	listenerErr := errors.New("listen failed")
	serverErrors := make(chan error, 1)
	serverErrors <- listenerErr

	err := joinAvailableServerError(nil, serverErrors)

	require.ErrorIs(t, err, listenerErr)
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
		require.NoError(t, reg.Register(tuple))

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
		require.NoError(t, reg.Register(tuple))

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
	}).Times(3)
	reg := registry.NewRegistry()
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(mockCollector)
	require.NoError(t, reg.Register(tuple))
	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{}).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(2)).AnyTimes()
	mockDeviceInfo.EXPECT().GPUs().Return(nil).AnyTimes()
	watchList := *devicewatchlistmanager.NewWatchList(mockDeviceInfo, []dcgm.Short{42}, nil, deviceWatcher, 1)
	watchManager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	watchManager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(watchList, true).Times(3)
	metricServer := &MetricsServer{
		deviceWatchListManager: watchManager,
		transformations: transformation.GetTransformations(&appconfig.Config{
			HPCJobMappingDir:            mappingDir,
			HPCJobMappingMaxFileBytes:   appconfig.DefaultHPCJobMappingMaxFileBytes,
			HPCJobMappingMaxJobIDBytes:  appconfig.DefaultHPCJobMappingMaxJobIDBytes,
			HPCJobMappingMaxJobsPerFile: appconfig.DefaultHPCJobMappingMaxJobsPerFile,
		}),
		scrapes: newScrapeCoordinator(1),
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

	// Invalid input removes attribution for that GPU without removing its sample.
	marker := strings.Repeat("private-job", 30)
	require.NoError(t, os.WriteFile(filepath.Join(mappingDir, "0"), []byte(marker), 0o600))

	recorder = httptest.NewRecorder()
	metricServer.Metrics(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, 2, strings.Count(recorder.Body.String(), "TEST_METRIC{"))
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `hpc_job="job1"`))
	require.NotContains(t, recorder.Body.String(), `hpc_job="job3"`)
	require.NotContains(t, recorder.Body.String(), marker)
}

// TestMetricsContinueAfterDumpStorageError checks that a scrape still returns
// metrics when the dump directory is not writable.
func TestMetricsContinueAfterDumpStorageError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a non-root process to exercise filesystem permission denial")
	}
	ctrl := gomock.NewController(t)
	reg := registry.NewRegistry()
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().GetMetrics().Return(getMetricsByCounterWithTestMetric(), nil)
	tuple.SetCollector(mockCollector)
	require.NoError(t, reg.Register(tuple))
	info := mockdeviceinfo.NewMockProvider(ctrl)
	info.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	info.EXPECT().GOpts().Return(appconfig.DeviceOptions{}).AnyTimes()
	info.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	info.EXPECT().GPUs().Return(nil).AnyTimes()
	list := *devicewatchlistmanager.NewWatchList(info, []dcgm.Short{42}, nil, deviceWatcher, 1)
	manager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	manager.EXPECT().EntityWatchList(dcgm.FE_GPU).Return(list, true)
	cfg := appconfig.DefaultDumpConfig()
	cfg.Enabled = true
	cfg.Directory = filepath.Join(t.TempDir(), "readonly")
	require.NoError(t, os.Mkdir(cfg.Directory, 0o500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(cfg.Directory, 0o700)) })
	server := &MetricsServer{deviceWatchListManager: manager, scrapes: newScrapeCoordinator(1), fileDumper: debug.NewFileDumper(cfg)}
	server.registry.Store(reg)
	response := httptest.NewRecorder()
	server.Metrics(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "TEST_METRIC")
	entries, err := os.ReadDir(cfg.Directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestPagesSetSecurityHeadersThroughTheWebConfig checks the landing and health
// page headers behind a real web config, which applies its own headers first.
func TestPagesSetSecurityHeadersThroughTheWebConfig(t *testing.T) {
	tests := []struct {
		name          string
		webConfig     string
		wantPageCSP   string
		wantPageFrame string
		wantMetrics   http.Header
	}{
		{
			name:          "defaults",
			webConfig:     "{}\n",
			wantPageCSP:   contentSecurityPolicy,
			wantPageFrame: "DENY",
			wantMetrics:   http.Header{},
		},
		{
			name: "configured headers are kept",
			webConfig: "http_server_config:\n" +
				"  headers:\n" +
				"    Content-Security-Policy: \"default-src 'self'\"\n" +
				"    X-Frame-Options: sameorigin\n",
			wantPageCSP:   "default-src 'self'",
			wantPageFrame: "sameorigin",
			wantMetrics: http.Header{
				contentSecurityPolicyHeader: {"default-src 'self'"},
				frameOptionsHeader:          {"sameorigin"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL := serveWithWebConfig(t, tt.webConfig)

			for _, page := range []string{"/", "/health"} {
				response := getForTest(t, baseURL+page)
				assert.Equal(t, http.StatusOK, response.StatusCode, page)
				assert.Equal(t, "nosniff", response.Header.Get(contentTypeOptionsHeader), page)
				assert.Equal(t, tt.wantPageCSP, response.Header.Get(contentSecurityPolicyHeader), page)
				assert.Equal(t, tt.wantPageFrame, response.Header.Get(frameOptionsHeader), page)
			}

			response := getForTest(t, baseURL+"/metrics")
			assert.Equal(t, http.StatusOK, response.StatusCode)
			assert.Equal(t, prometheusTextContentType, response.Header.Get("Content-Type"))
			assert.Equal(t, "nosniff", response.Header.Get(contentTypeOptionsHeader))
			assert.Equal(t, tt.wantMetrics.Values(contentSecurityPolicyHeader), response.Header.Values(contentSecurityPolicyHeader))
			assert.Equal(t, tt.wantMetrics.Values(frameOptionsHeader), response.Header.Values(frameOptionsHeader))
		})
	}
}

// serveWithWebConfig serves a new metrics server through the exporter-toolkit
// web config handler on a loopback port and returns its base URL.
func serveWithWebConfig(t *testing.T, webConfig string) string {
	t.Helper()

	webConfigFile := filepath.Join(t.TempDir(), "web-config.yml")
	require.NoError(t, os.WriteFile(webConfigFile, []byte(webConfig), 0o600))

	ctrl := gomock.NewController(t)
	cfg := &appconfig.Config{
		Address:              "127.0.0.1:0",
		MaxConcurrentScrapes: appconfig.DefaultMaxConcurrentScrapes,
		WebConfigFile:        webConfigFile,
	}
	srv, cleanup, err := NewMetricsServer(cfg, mockdevicewatchlistmanager.NewMockManager(ctrl), registry.NewRegistry())
	require.NoError(t, err)
	t.Cleanup(cleanup)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	served := make(chan error, 1)
	go func() { served <- web.Serve(listener, srv.server, srv.webConfig, slog.Default()) }()
	t.Cleanup(func() {
		require.NoError(t, srv.server.Close())
		assert.ErrorIs(t, <-served, http.ErrServerClosed)
	})

	return "http://" + listener.Addr().String()
}

// getForTest sends a GET request and returns the response with its body read.
func getForTest(t *testing.T, url string) *http.Response {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()

	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)

	return response
}

// TestMetricsFENONEBypassesWatchListsAndTransformations checks that render
// passes FE_NONE metrics straight to the output: the strict mocks fail the test
// if a watch list or transformation is used.
func TestMetricsFENONEBypassesWatchListsAndTransformations(t *testing.T) {
	ctrl := gomock.NewController(t)
	manager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	transform := mocktransformation.NewMockTransform(ctrl)

	counter := counters.Counter{
		FieldName: counters.DCGMExpSystemHealthStatus,
		PromType:  "gauge",
		Help:      "Current DCGM-reported host-level health status; currently the IMEX health watch only.",
	}
	metrics := collector.MetricsByCounter{
		counter: []collector.Metric{
			{
				Counter:  counter,
				Value:    "0",
				Hostname: "test-host",
				Labels: map[string]string{
					"health_watch":          "IMEX",
					"health_error_code":     "DCGM_FR_OK",
					"health_error_severity": "NONE",
					"health_error_category": "NONE",
				},
				Attributes: map[string]string{},
			},
		},
	}

	metricServer := &MetricsServer{
		deviceWatchListManager: manager,
		transformations:        []transformation.Transform{transform},
	}
	var output bytes.Buffer
	err := metricServer.render(
		context.Background(),
		&output,
		manager,
		registry.MetricsByCounterGroup{dcgm.FE_NONE: metrics},
	)

	require.NoError(t, err)
	assert.Contains(t, output.String(), "DCGM_EXP_SYSTEM_HEALTH_STATUS")
	assert.Contains(t, output.String(), "hostname=\"test-host\"")
	assert.NotContains(t, output.String(), "gpu=\"")
}

// TestMetricsFENONEWritesMetricsDebugDump checks that render writes FE_NONE
// metrics to a "metrics-Host-" debug file and writes no device info file.
func TestMetricsFENONEWritesMetricsDebugDump(t *testing.T) {
	ctrl := gomock.NewController(t)
	manager := mockdevicewatchlistmanager.NewMockManager(ctrl)
	directory := t.TempDir()
	dumpConfig := appconfig.DefaultDumpConfig()
	dumpConfig.Enabled = true
	dumpConfig.Directory = directory
	dumpConfig.Compression = false
	metricServer := &MetricsServer{
		fileDumper: debug.NewFileDumper(dumpConfig),
	}

	var output bytes.Buffer
	err := metricServer.render(
		context.Background(),
		&output,
		manager,
		registry.MetricsByCounterGroup{dcgm.FE_NONE: getMetricsByCounterWithTestMetric()},
	)

	require.NoError(t, err)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.True(t, strings.HasPrefix(entries[0].Name(), "metrics-Host-"))

	data, err := os.ReadFile(filepath.Join(directory, entries[0].Name()))
	require.NoError(t, err)
	assert.Contains(t, string(data), "TEST_METRIC")
}
