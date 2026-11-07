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

package registry

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	collectorpkg "github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
)

type mockCollector struct {
	mock.Mock
}

// delayedCleanupCollector models a collector whose Cleanup request returns
// before a native worker has released its resources. Registry retirement uses
// its CleanupComplete signal to prove that provider teardown must still wait.
type delayedCleanupCollector struct {
	cleanupStarted chan struct{}
	cleanupRelease chan struct{}
	cleanupDone    chan struct{}
	cleanupOnce    sync.Once
}

// GetMetrics makes this test collector usable by the registry without adding
// unrelated scrape behavior to a retirement-focused test.
func (c *delayedCleanupCollector) GetMetrics() (collectorpkg.MetricsByCounter, error) {
	return collectorpkg.MetricsByCounter{}, nil
}

// Cleanup starts delayed cleanup exactly once, matching a poller that has
// asked a native call to stop but cannot yet release its field handles.
func (c *delayedCleanupCollector) Cleanup() {
	c.cleanupOnce.Do(func() {
		close(c.cleanupStarted)
		go func() {
			<-c.cleanupRelease
			close(c.cleanupDone)
		}()
	})
}

// CleanupComplete reports when this test collector has released its simulated
// native handles, which is the contract Registry.Retire waits for.
func (c *delayedCleanupCollector) CleanupComplete() <-chan struct{} {
	return c.cleanupDone
}

func (m *mockCollector) GetMetrics() (collectorpkg.MetricsByCounter, error) {
	args := m.Called()
	return args.Get(0).(collectorpkg.MetricsByCounter), args.Error(1)
}

func (m *mockCollector) Cleanup() {
	m.Called()
}

// TestRegistryRetireWaitsForDeferredCollectorCleanup proves a registry does
// not report completion while a collector's cleanup worker still owns native
// resources.
func TestRegistryRetireWaitsForDeferredCollectorCleanup(t *testing.T) {
	reg := NewRegistry()
	delayed := &delayedCleanupCollector{
		cleanupStarted: make(chan struct{}),
		cleanupRelease: make(chan struct{}),
		cleanupDone:    make(chan struct{}),
	}
	tuple := collectorpkg.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(delayed)
	require.NoError(t, reg.Register(tuple))

	done := reg.Retire()
	select {
	case <-delayed.cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("registry did not request collector cleanup")
	}

	select {
	case <-done:
		t.Fatal("registry retired before deferred collector cleanup finished")
	default:
	}
	require.ErrorIs(t, reg.Register(tuple), ErrRegistryShuttingDown)

	close(delayed.cleanupRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("registry did not complete after collector cleanup finished")
	}
	require.NoError(t, reg.RetirementError())
}

// TestRegistryGatherAdmittedUsesExistingAdmission verifies a request admitted
// before retirement can finish gathering without trying to enter a second time.
func TestRegistryGatherAdmittedUsesExistingAdmission(t *testing.T) {
	reg := NewRegistry()
	collector := new(mockCollector)
	collector.On("GetMetrics").Return(collectorpkg.MetricsByCounter{}, nil).Once()
	collector.On("Cleanup").Return().Once()
	tuple := collectorpkg.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(collector)
	require.NoError(t, reg.Register(tuple))
	require.NoError(t, reg.BeginUse())

	retired := reg.Retire()
	require.Eventually(t, func() bool {
		reg.mtx.Lock()
		defer reg.mtx.Unlock()
		return reg.shuttingDown
	}, time.Second, time.Millisecond)

	_, err := reg.GatherAdmitted()
	require.NoError(t, err)
	reg.EndUse()

	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("registry did not retire after its admitted gather finished")
	}
	collector.AssertExpectations(t)
}

func TestRegistry_Gather(t *testing.T) {
	collector := new(mockCollector)

	metrics := collectorpkg.MetricsByCounter{}
	counterA := counters.Counter{
		FieldID:   155,
		FieldName: "DCGM_FI_DEV_POWER_USAGE",
		PromType:  "gauge",
	}

	metrics[counterA] = append(metrics[counterA], collectorpkg.Metric{
		GPU:        "0",
		Counter:    counterA,
		Attributes: map[string]string{},
	})

	counterB := counters.Counter{
		FieldName: "DCGM_FI_EXP_CLOCK_THROTTLE_REASONS_COUNT",
		PromType:  "gauge",
	}

	metrics[counterB] = append(metrics[counterB], collectorpkg.Metric{
		GPU:        "0",
		Counter:    counterB,
		Value:      "42",
		Attributes: map[string]string{},
	})

	type test struct {
		name           string
		collectorState func() *mock.Call
		assert         func(MetricsByCounterGroup, error)
	}

	tests := []test{
		{
			name: "When collector return no errors",
			collectorState: func() *mock.Call {
				cs := collector.On("GetMetrics").Return(metrics, nil)
				cs.On("Cleanup").Return()
				return cs
			},
			assert: func(mbcg MetricsByCounterGroup, err error) {
				require.NoError(t, err)
				require.Contains(t, mbcg, dcgm.FE_GPU)
				require.Len(t, mbcg, 1)
				require.Len(t, mbcg[dcgm.FE_GPU], 2)
			},
		},
		{
			name: "When collector return errors",
			collectorState: func() *mock.Call {
				cs := collector.On("GetMetrics").Return(collectorpkg.MetricsByCounter{}, errors.New("Boom!"))
				cs.On("Cleanup").Return()
				return cs
			},
			assert: func(mbcg MetricsByCounterGroup, err error) {
				require.Error(t, err)
				require.Len(t, mbcg, 0)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()
			newEntityCollectorTuple := collectorpkg.EntityCollectorTuple{}
			newEntityCollectorTuple.SetEntity(dcgm.FE_GPU)
			newEntityCollectorTuple.SetCollector(collector)
			require.NoError(t, reg.Register(newEntityCollectorTuple))
			mockCall := tc.collectorState()
			got, err := reg.Gather()
			tc.assert(got, err)
			mockCall.Unset()
			reg.Cleanup()
		})
	}
}

// TestRegistryGatherReturnsEveryCollectorError verifies a terminal DCGM error
// cannot be hidden when another collector fails first.
func TestRegistryGatherReturnsEveryCollectorError(t *testing.T) {
	reg := NewRegistry()
	nonfatalErr := errors.New("collector without permission")
	fatalErr := &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID}
	nonfatalStarted := make(chan struct{})
	nonfatalCollector := new(mockCollector)
	nonfatalCollector.On("GetMetrics").Run(func(mock.Arguments) {
		close(nonfatalStarted)
	}).Return(collectorpkg.MetricsByCounter{}, nonfatalErr).Once()
	fatalCollector := new(mockCollector)
	fatalCollector.On("GetMetrics").Run(func(mock.Arguments) {
		<-nonfatalStarted
	}).Return(collectorpkg.MetricsByCounter{}, fatalErr).Once()

	for _, c := range []collectorpkg.Collector{nonfatalCollector, fatalCollector} {
		tuple := collectorpkg.EntityCollectorTuple{}
		tuple.SetEntity(dcgm.FE_GPU)
		tuple.SetCollector(c)
		require.NoError(t, reg.Register(tuple))
	}

	_, err := reg.Gather()
	require.ErrorIs(t, err, nonfatalErr)
	require.ErrorIs(t, err, fatalErr)
	assert.True(t, dcgmerrors.IsFatalScrapeError(err))
	nonfatalCollector.AssertExpectations(t)
	fatalCollector.AssertExpectations(t)
}

func TestRegistry_GatherMergesMetricsFromCollectorsForSameCounter(t *testing.T) {
	counter := counters.Counter{
		FieldID:   155,
		FieldName: "DCGM_FI_DEV_POWER_USAGE",
		PromType:  "gauge",
	}
	metricA := collectorpkg.Metric{
		GPU:        "0",
		Counter:    counter,
		Value:      "10",
		Attributes: map[string]string{},
	}
	metricB := collectorpkg.Metric{
		GPU:        "1",
		Counter:    counter,
		Value:      "20",
		Attributes: map[string]string{},
	}
	metricsA := collectorpkg.MetricsByCounter{
		counter: {metricA},
	}
	metricsB := collectorpkg.MetricsByCounter{
		counter: {metricB},
	}

	collectorA := new(mockCollector)
	collectorA.On("GetMetrics").Return(metricsA, nil).Once()
	collectorA.On("Cleanup").Return().Once()
	collectorB := new(mockCollector)
	collectorB.On("GetMetrics").Return(metricsB, nil).Once()
	collectorB.On("Cleanup").Return().Once()

	reg := NewRegistry()
	for _, c := range []collectorpkg.Collector{collectorA, collectorB} {
		newEntityCollectorTuple := collectorpkg.EntityCollectorTuple{}
		newEntityCollectorTuple.SetEntity(dcgm.FE_GPU)
		newEntityCollectorTuple.SetCollector(c)
		require.NoError(t, reg.Register(newEntityCollectorTuple))
	}
	t.Cleanup(func() {
		reg.Cleanup()
		collectorA.AssertExpectations(t)
		collectorB.AssertExpectations(t)
	})

	got, err := reg.Gather()
	require.NoError(t, err)
	require.Contains(t, got, dcgm.FE_GPU)
	require.Contains(t, got[dcgm.FE_GPU], counter)

	metricsA[counter][0].Value = "changed"
	metricsB[counter][0].Value = "changed"
	assert.ElementsMatch(t, []collectorpkg.Metric{metricA, metricB}, got[dcgm.FE_GPU][counter])
}

func TestRegistryGatherDetachesCollectorSlices(t *testing.T) {
	counter := counters.Counter{
		FieldID:   155,
		FieldName: "DCGM_FI_DEV_POWER_USAGE",
		PromType:  "gauge",
	}
	tests := []struct {
		name   string
		source []collectorpkg.Metric
	}{
		{
			name: "one metric",
			source: []collectorpkg.Metric{
				{GPU: "0", Value: "10"},
			},
		},
		{
			name: "multiple metrics",
			source: []collectorpkg.Metric{
				{GPU: "0", Value: "10"},
				{GPU: "1", Value: "20"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := test.source
			collector := new(mockCollector)
			collector.On("GetMetrics").Return(collectorpkg.MetricsByCounter{counter: source}, nil).Once()
			collector.On("Cleanup").Return().Once()

			reg := NewRegistry()
			entityCollector := collectorpkg.EntityCollectorTuple{}
			entityCollector.SetEntity(dcgm.FE_GPU)
			entityCollector.SetCollector(collector)
			require.NoError(t, reg.Register(entityCollector))
			t.Cleanup(func() {
				reg.Cleanup()
				collector.AssertExpectations(t)
			})

			got, err := reg.Gather()
			require.NoError(t, err)

			source[0].Value = "changed"
			assert.Equal(t, "10", got[dcgm.FE_GPU][counter][0].Value)
		})
	}
}

func TestRegistry_Register_Accepts_Duplicates_(t *testing.T) {
	reg := NewRegistry()
	collector := new(mockCollector)

	newEntityCollectorTuple1 := collectorpkg.EntityCollectorTuple{}
	newEntityCollectorTuple1.SetEntity(dcgm.FE_GPU)
	newEntityCollectorTuple1.SetCollector(collector)

	newEntityCollectorTuple2 := collectorpkg.EntityCollectorTuple{}
	newEntityCollectorTuple2.SetEntity(dcgm.FE_GPU)
	newEntityCollectorTuple2.SetCollector(collector)

	require.NoError(t, reg.Register(newEntityCollectorTuple1))
	require.NoError(t, reg.Register(newEntityCollectorTuple2))
	assert.Len(t, reg.collectorGroups, 1)
	assert.Len(t, reg.collectorGroupsSeen, 1)
}

// TestRegistryCollectorCountIsKeyedByEntityGroup checks non-GPU collectors do not satisfy the GPU health gate.
func TestRegistryCollectorCountIsKeyedByEntityGroup(t *testing.T) {
	reg := NewRegistry()

	cpuTuple := collectorpkg.EntityCollectorTuple{}
	cpuTuple.SetEntity(dcgm.FE_CPU)
	cpuTuple.SetCollector(&mockCollector{})
	require.NoError(t, reg.Register(cpuTuple))

	assert.Equal(t, 1, reg.CollectorCount(dcgm.FE_CPU))
	assert.Equal(t, 0, reg.CollectorCount(dcgm.FE_GPU))

	gpuTuple := collectorpkg.EntityCollectorTuple{}
	gpuTuple.SetEntity(dcgm.FE_GPU)
	gpuTuple.SetCollector(&mockCollector{})
	require.NoError(t, reg.Register(gpuTuple))

	assert.Equal(t, 1, reg.CollectorCount(dcgm.FE_GPU))
	assert.Equal(t, 0, reg.CollectorCount(dcgm.FE_SWITCH))
}

// eventCollector records its Cleanup in a shared event log.
type eventCollector struct {
	name   string
	events *[]string
}

// GetMetrics returns no metrics.
func (c *eventCollector) GetMetrics() (collectorpkg.MetricsByCounter, error) {
	return collectorpkg.MetricsByCounter{}, nil
}

// Cleanup records the collector's name.
func (c *eventCollector) Cleanup() {
	*c.events = append(*c.events, c.name)
}

// pollerCollector is an eventCollector with a background worker that is done
// as soon as Cleanup returns.
type pollerCollector struct {
	eventCollector
	done chan struct{}
}

// Cleanup records the collector's name and finishes its worker.
func (c *pollerCollector) Cleanup() {
	c.eventCollector.Cleanup()
	close(c.done)
}

// CleanupComplete closes when the worker is done.
func (c *pollerCollector) CleanupComplete() <-chan struct{} {
	return c.done
}

// TestRegistryRetireRestoresOnceForAllCollectors retires a registry with a
// background poller and five other collectors, and expects the poller to stop
// first, then every other collector to be cleaned up inside one deferred
// restore scope, so DCGM restores surviving watches once.
func TestRegistryRetireRestoresOnceForAllCollectors(t *testing.T) {
	var events []string
	previous := deferRestores
	deferRestores = func() func() {
		events = append(events, "begin restore scope")
		return func() { events = append(events, "restore once") }
	}
	t.Cleanup(func() { deferRestores = previous })

	reg := NewRegistry()
	poller := &pollerCollector{eventCollector: eventCollector{name: "poller", events: &events}, done: make(chan struct{})}
	register := func(c collectorpkg.Collector) {
		tuple := collectorpkg.EntityCollectorTuple{}
		tuple.SetEntity(dcgm.FE_GPU)
		tuple.SetCollector(c)
		require.NoError(t, reg.Register(tuple))
	}

	register(poller)
	for i := range 5 {
		register(&eventCollector{name: fmt.Sprintf("collector %d", i), events: &events})
	}

	<-reg.Retire()

	require.NoError(t, reg.RetirementError())
	assert.Equal(t, []string{
		"poller",
		"begin restore scope",
		"collector 0", "collector 1", "collector 2", "collector 3", "collector 4",
		"restore once",
	}, events)
}
