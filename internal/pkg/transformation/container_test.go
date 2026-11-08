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

package transformation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

// fakeContainerRuntime supplies attribution or a failure without runtime I/O.
type fakeContainerRuntime struct {
	byGPU map[string][]containerInfo
	err   error
}

// ContainersByGPU returns the fixture map and configured query error.
func (f fakeContainerRuntime) ContainersByGPU(context.Context, deviceinfo.Provider) (map[string][]containerInfo, error) {
	return f.byGPU, f.err
}

// TestContainerMapperProcessAddsContainerMetrics checks added copies by UUID/index while preserving unallocated originals.
func TestContainerMapperProcessAddsContainerMetrics(t *testing.T) {
	counter := counters.Counter{FieldID: 155, FieldName: "DCGM_FI_DEV_POWER_USAGE", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{GPU: "0", GPUUUID: "GPU-0", GPUDevice: "nvidia0", Attributes: map[string]string{}},
		{GPU: "1", GPUUUID: "GPU-1", GPUDevice: "nvidia1", Attributes: map[string]string{}},
		{GPU: "2", GPUUUID: "GPU-2", GPUDevice: "nvidia2", Attributes: map[string]string{}},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer"}},
		"1":     {{Name: "indexer"}},
	}}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 5)
	assert.NotContains(t, got[0].Attributes, containerAttribute)
	assert.Equal(t, "trainer", got[1].Attributes[containerAttribute])
	assert.NotContains(t, got[2].Attributes, containerAttribute)
	assert.Equal(t, "indexer", got[3].Attributes[containerAttribute])
	assert.NotContains(t, got[4].Attributes, containerAttribute)
}

// TestContainerMapperProcessAddsContainerMetricsForDistinctSamples keeps each sample's XID and job identity in its container copy.
func TestContainerMapperProcessAddsContainerMetricsForDistinctSamples(t *testing.T) {
	counter := counters.Counter{FieldID: 157, FieldName: "DCGM_EXP_XID_ERRORS_TOTAL", PromType: "counter"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{
			GPU:        "0",
			GPUUUID:    "GPU-0",
			GPUDevice:  "nvidia0",
			Labels:     map[string]string{"xid": "42"},
			Attributes: map[string]string{hpcJobAttribute: "job-1"},
		},
		{
			GPU:        "0",
			GPUUUID:    "GPU-0",
			GPUDevice:  "nvidia0",
			Labels:     map[string]string{"xid": "46"},
			Attributes: map[string]string{hpcJobAttribute: "job-2"},
		},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer"}},
	}}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 4)
	assert.NotContains(t, got[0].Attributes, containerAttribute)
	assert.Equal(t, "trainer", got[1].Attributes[containerAttribute])
	assert.Equal(t, "42", got[1].Labels["xid"])
	assert.Equal(t, "job-1", got[1].Attributes[hpcJobAttribute])
	assert.NotContains(t, got[2].Attributes, containerAttribute)
	assert.Equal(t, "trainer", got[3].Attributes[containerAttribute])
	assert.Equal(t, "46", got[3].Labels["xid"])
	assert.Equal(t, "job-2", got[3].Attributes[hpcJobAttribute])
}

// TestContainerMapperProcessAddsMetricForEachContainerOnGPU checks one added copy per container sharing a GPU.
func TestContainerMapperProcessAddsMetricForEachContainerOnGPU(t *testing.T) {
	counter := counters.Counter{FieldID: 158, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{GPU: "0", GPUUUID: "GPU-0", GPUDevice: "nvidia0", Attributes: map[string]string{}},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer-a"}, {Name: "trainer-b"}},
	}}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 3)
	assert.NotContains(t, got[0].Attributes, containerAttribute)
	assert.Equal(t, "trainer-a", got[1].Attributes[containerAttribute])
	assert.Equal(t, "trainer-b", got[2].Attributes[containerAttribute])
}

// TestContainerMapperProcessAddsContainerMetricForMIGInstance checks GPU.instance attribution for MIG container copies.
func TestContainerMapperProcessAddsContainerMetricForMIGInstance(t *testing.T) {
	counter := counters.Counter{FieldID: 158, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{
			GPU:           "0",
			GPUUUID:       "GPU-0",
			GPUDevice:     "nvidia0",
			MigProfile:    "1g.10gb",
			GPUInstanceID: "8",
			Attributes:    map[string]string{},
		},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"0.8": {{Name: "mig-container"}},
	}}}

	err := mapper.Process(metrics, testMIGDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 2)
	assert.NotContains(t, got[0].Attributes, containerAttribute)
	assert.Equal(t, "mig-container", got[1].Attributes[containerAttribute])
}

// TestContainerMapperProcessInitializesNilAttributes keeps the original map nil and allocates attributes only for the copy.
func TestContainerMapperProcessInitializesNilAttributes(t *testing.T) {
	counter := counters.Counter{FieldID: 158, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{GPU: "0", GPUUUID: "GPU-0", GPUDevice: "nvidia0"},
	}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer"}},
	}}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	got := metrics[counter]
	require.Len(t, got, 2)
	assert.NotContains(t, got[0].Attributes, containerAttribute)
	assert.Equal(t, "trainer", got[1].Attributes[containerAttribute])
}

// TestContainerMapperProcessSkipsRuntimeWithoutGPUInventory checks no runtime query is made without discovered GPUs.
func TestContainerMapperProcessSkipsRuntimeWithoutGPUInventory(t *testing.T) {
	counter := counters.Counter{FieldID: 158, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{
		{GPU: "0", GPUUUID: "GPU-0", GPUDevice: "nvidia0", Attributes: map[string]string{}},
	}}
	runtime := &countingContainerRuntime{byGPU: map[string][]containerInfo{
		"GPU-0": {{Name: "trainer"}},
	}}
	mapper := &containerMapper{runtime: runtime}

	err := mapper.Process(metrics, runtimeTestDeviceInfo{})
	require.NoError(t, err)

	assert.Zero(t, runtime.calls)
	require.Len(t, metrics[counter], 1)
	assert.NotContains(t, metrics[counter][0].Attributes, containerAttribute)
}

// TestContainerMapperProcessPreservesAttributesAndAvoidsDuplicateLabels deduplicates names and preserves preexisting container/job attribution.
func TestContainerMapperProcessPreservesAttributesAndAvoidsDuplicateLabels(t *testing.T) {
	for _, tt := range []struct {
		name     string
		variants int
	}{
		{name: "OTLP disabled"},
		{name: "single raw identity", variants: 1},
		{name: "raw identity collision", variants: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			counter := counters.Counter{FieldID: 156, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
			var identities []collector.ContainerIdentity
			var source *collector.SourceIdentity
			if tt.variants > 0 {
				source = &collector.SourceIdentity{}
				identities = []collector.ContainerIdentity{{Name: "train\ner", ID: "first-full-id"}}
			}
			firstIdentity := identities
			if tt.variants == 2 {
				identities = append(identities, collector.ContainerIdentity{Name: "train_er", ID: "second-full-id"})
			}
			infos := []containerInfo{{Name: "trainer", Identities: firstIdentity}, {Name: "trainer", Identities: identities}, {Name: ""}}
			metrics := collector.MetricsByCounter{counter: {
				{GPU: "0", GPUUUID: "GPU-0", GPUDevice: "nvidia0", MigProfile: "1g.10gb", GPUInstanceID: "8", Attributes: map[string]string{hpcJobAttribute: "job-1"}, Source: source},
				{GPU: "1", GPUUUID: "GPU-1", Attributes: map[string]string{containerAttribute: "existing"}},
			}}
			mapper := &containerMapper{runtime: fakeContainerRuntime{byGPU: map[string][]containerInfo{
				"0.8":     {{Name: "mig-first"}},
				"GPU-0":   infos,
				"0":       append(append([]containerInfo(nil), infos...), containerInfo{Name: "index-only"}),
				"nvidia0": {{Name: "device-only"}},
				"GPU-1":   {{Name: "should-not-overwrite"}},
			}}}

			require.NoError(t, mapper.Process(metrics, testDeviceInfo()))
			got := metrics[counter]
			require.Len(t, got, 6)
			names := make([]string, len(got))
			for i, metric := range got {
				names[i] = metric.Attributes[containerAttribute]
			}
			assert.Equal(t, []string{"", "mig-first", "trainer", "index-only", "device-only", "existing"}, names)
			assert.Equal(t, "job-1", got[2].Attributes[hpcJobAttribute])
			assertContainerIdentityCopies(t, got, identities, infos)
		})
	}
}

func assertContainerIdentityCopies(t *testing.T, metrics []collector.Metric, identities []collector.ContainerIdentity, infos []containerInfo) {
	t.Helper()
	if len(identities) == 0 {
		for _, metric := range metrics {
			assert.Nil(t, metric.Source)
		}
		return
	}
	assert.Equal(t, identities, metrics[2].Source.Containers)
	assert.Empty(t, metrics[0].Source.Containers)
	metrics[2].Source.Containers[0].ID = "changed"
	assert.Equal(t, "first-full-id", infos[0].Identities[0].ID)
}

// TestContainerMapperProcessLeavesMetricsOnRuntimeError keeps the original scrape after a runtime query failure.
func TestContainerMapperProcessLeavesMetricsOnRuntimeError(t *testing.T) {
	counter := counters.Counter{FieldID: 157, FieldName: "DCGM_FI_DEV_POWER_USAGE", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{{GPU: "0", GPUUUID: "GPU-0", Attributes: map[string]string{}}}}
	mapper := &containerMapper{runtime: fakeContainerRuntime{err: fmt.Errorf("runtime unavailable")}}

	err := mapper.Process(metrics, testDeviceInfo())
	require.NoError(t, err)

	require.Len(t, metrics[counter], 1)
	assert.NotContains(t, metrics[counter][0].Attributes, containerAttribute)
}

// TestContainerMapperName checks the transform identifier used in logs and tests.
func TestContainerMapperName(t *testing.T) {
	assert.Equal(t, "containerMapper", (&containerMapper{}).Name())
}

// countingContainerRuntime counts runtime queries made by the mapper.
type countingContainerRuntime struct {
	byGPU map[string][]containerInfo
	calls int
}

// ContainersByGPU records one query and returns the configured attribution.
func (f *countingContainerRuntime) ContainersByGPU(context.Context, deviceinfo.Provider) (map[string][]containerInfo, error) {
	f.calls++
	return f.byGPU, nil
}

// A runtime that fails on every scrape is reported at most once per interval,
// and the labels return when it recovers.
func TestContainerMapperWarnsAboutAFailingRuntimeAtMostOncePerInterval(t *testing.T) {
	logs := captureLogs(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	mapper := &containerMapper{
		runtime:             fakeContainerRuntime{err: errors.New("runtime unavailable")},
		unavailableWarnings: warningLimiter{interval: 5 * time.Minute, now: clock.Now},
	}
	counter := counters.Counter{FieldID: 157, FieldName: "DCGM_FI_DEV_POWER_USAGE", PromType: "gauge"}
	scrape := func() []collector.Metric {
		metrics := collector.MetricsByCounter{counter: {{GPU: "0", GPUUUID: "GPU-0", Attributes: map[string]string{}}}}
		require.NoError(t, mapper.Process(metrics, testDeviceInfo()))
		return metrics[counter]
	}

	for range 3 {
		assert.Len(t, scrape(), 1)
	}
	assert.Equal(t, 1, logs.count("failed to collect runtime container labels"))

	clock.now = clock.now.Add(5 * time.Minute)
	scrape()
	assert.Equal(t, 2, logs.count("failed to collect runtime container labels"))

	mapper.runtime = fakeContainerRuntime{byGPU: map[string][]containerInfo{"GPU-0": {{Name: "trainer"}}}}
	got := scrape()
	require.Len(t, got, 2)
	assert.Equal(t, "trainer", got[1].Attributes[containerAttribute])
}

// The real runtime client dials on every refresh, so a socket that appears
// after startup is used by the next scrape with no restart.
func TestContainerMapperUsesARuntimeSocketThatAppearsLater(t *testing.T) {
	logs := captureLogs(t)
	socket := filepath.Join(t.TempDir(), "runtime.sock")
	mapper := newContainerMapper(&appconfig.Config{ContainerRuntimeSocket: socket, CollectInterval: 1})
	counter := counters.Counter{FieldID: 157, FieldName: "DCGM_FI_DEV_POWER_USAGE", PromType: "gauge"}
	scrape := func() []collector.Metric {
		metrics := collector.MetricsByCounter{counter: {{GPU: "0", GPUUUID: "GPU-0", Attributes: map[string]string{}}}}
		require.NoError(t, mapper.Process(metrics, testDeviceInfo()))
		return metrics[counter]
	}

	assert.Len(t, scrape(), 1, "no runtime yet, so no container labels and no failed scrape")
	assert.Len(t, scrape(), 1)
	assert.Equal(t, 1, logs.count("failed to collect runtime container labels"))

	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/json" {
			_, _ = w.Write([]byte(`[{"Id":"c1"}]`))
			return
		}
		_, _ = w.Write([]byte(`{"Id":"c1","Name":"/trainer","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=all"]}}`))
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	require.Eventually(t, func() bool {
		metrics := collector.MetricsByCounter{counter: {{GPU: "0", GPUUUID: "GPU-0", Attributes: map[string]string{}}}}
		if err := mapper.Process(metrics, testDeviceInfo()); err != nil {
			return false
		}
		got := metrics[counter]
		return len(got) == 2 && got[1].Attributes[containerAttribute] == "trainer"
	}, 5*time.Second, 20*time.Millisecond)
}
