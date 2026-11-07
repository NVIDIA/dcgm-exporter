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
package transformation

import (
	"bytes"
	"fmt"
	"log/slog"
	"maps"
	stdos "os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/sys/unix"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/rendermetrics"
)

// hpcTestConfig maps directory with the default mapping file limits.
func hpcTestConfig(directory string) *appconfig.Config {
	return &appconfig.Config{
		HPCJobMappingDir:            directory,
		HPCJobMappingMaxFileBytes:   appconfig.DefaultHPCJobMappingMaxFileBytes,
		HPCJobMappingMaxJobIDBytes:  appconfig.DefaultHPCJobMappingMaxJobIDBytes,
		HPCJobMappingMaxJobsPerFile: appconfig.DefaultHPCJobMappingMaxJobsPerFile,
	}
}

// TestHPCProcess labels each mapped sample per job, keeps unmapped samples, and leaves the source samples unchanged.
func TestHPCProcess(t *testing.T) {
	dir := t.TempDir()
	for name, jobs := range map[string]string{"0": "job1-0\n", "1": "job1-1\njob2-1\njob1-1\n", "3.0": "job3.0-0\n"} {
		require.NoError(t, stdos.WriteFile(filepath.Join(dir, name), []byte(jobs), 0o600))
	}
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "99"), []byte("unselected"), 0o600))

	counter := counters.Counter{}
	original := []collector.Metric{
		{GPU: "0", Value: "42", Attributes: map[string]string{"base": "kept"}},
		{GPU: "1", Value: "451"},
		{GPU: "2", Value: "1984"},
		{GPU: "3", GPUInstanceID: "0", MigProfile: "profile", Value: "3"},
	}
	metrics := collector.MetricsByCounter{counter: original}

	mapper := newHPCMapper(hpcTestConfig(dir))
	require.Equal(t, "hpcMapper", mapper.Name())
	require.NoError(t, mapper.Process(metrics, nil))

	got := metrics[counter]
	require.Len(t, got, 5)
	require.Equal(t, []string{"job1-0", "job1-1", "job2-1", "", "job3.0-0"}, []string{
		got[0].Attributes[hpcJobAttribute], got[1].Attributes[hpcJobAttribute], got[2].Attributes[hpcJobAttribute], got[3].Attributes[hpcJobAttribute], got[4].Attributes[hpcJobAttribute],
	})
	require.NotContains(t, got[3].Attributes, hpcJobAttribute, "an unmapped GPU has no hpc_job label")
	require.Equal(t, "1984", got[3].Value)
	require.Equal(t, "kept", got[0].Attributes["base"])
	require.NotContains(t, original[0].Attributes, hpcJobAttribute)
}

// TestHPCInvalidMappingPreservesOtherMappings checks an oversized file only drops its own GPU's labels and an unsafe GPU name is never opened.
func TestHPCInvalidMappingPreservesOtherMappings(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte(strings.Repeat("x", 257)), 0o600))
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "1"), []byte("valid"), 0o600))

	counter := counters.Counter{}
	metrics := collector.MetricsByCounter{counter: {{GPU: "0", Value: "base"}, {GPU: "1", Value: "other"}, {GPU: "../outside", Value: "unsafe"}}}

	// A real file at the traversal target makes the test fail if the mapper opens it.
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "..", "outside"), []byte("traversed\n"), 0o600))
	require.NoError(t, newHPCMapper(hpcTestConfig(dir)).Process(metrics, nil))

	require.Len(t, metrics[counter], 3)
	require.Empty(t, metrics[counter][0].Attributes)
	require.Equal(t, "valid", metrics[counter][1].Attributes[hpcJobAttribute])
	require.Empty(t, metrics[counter][2].Attributes)
}

// TestHPCLogsDoNotContainInput checks a warning about an invalid file does not log the file's content.
func TestHPCLogsDoNotContainInput(t *testing.T) {
	dir := t.TempDir()
	marker := strings.Repeat("private-job-id", 25)
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte(marker), 0o600))

	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	counter := counters.Counter{}
	metrics := collector.MetricsByCounter{counter: {{GPU: "0", Value: "base"}}}
	require.NoError(t, newHPCMapper(hpcTestConfig(dir)).Process(metrics, nil))

	require.NotContains(t, logs.String(), marker)
	require.Contains(t, logs.String(), "is not usable")
	require.Empty(t, metrics[counter][0].Attributes)
}

// TestHPCRepeatedReadsCloseDescriptors checks repeated scrapes do not leak file descriptors.
func TestHPCRepeatedReadsCloseDescriptors(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte("job"), 0o600))

	before, err := stdos.ReadDir("/proc/self/fd")
	require.NoError(t, err)

	counter := counters.Counter{}
	mapper := newHPCMapper(hpcTestConfig(dir))
	for range 100 {
		metrics := collector.MetricsByCounter{counter: {{GPU: "0"}, {GPU: "1"}}}
		require.NoError(t, mapper.Process(metrics, nil))
		require.Len(t, metrics[counter], 2)
	}

	after, err := stdos.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	require.Equal(t, len(before), len(after))
}

// TestHPCOpensOnlySelectedMappings checks files not named after an exported GPU are never opened.
func TestHPCOpensOnlySelectedMappings(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte("job"), 0o600))
	unrelated := filepath.Join(dir, "99")
	require.NoError(t, stdos.WriteFile(unrelated, []byte("unselected"), 0o600))

	events, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	require.NoError(t, err)
	defer unix.Close(events)
	_, err = unix.InotifyAddWatch(events, unrelated, unix.IN_OPEN)
	require.NoError(t, err)

	counter := counters.Counter{}
	metrics := collector.MetricsByCounter{counter: {{GPU: "0"}}}
	require.NoError(t, newHPCMapper(hpcTestConfig(dir)).Process(metrics, nil))
	require.Equal(t, "job", metrics[counter][0].Attributes[hpcJobAttribute])

	// Open notifications are queued synchronously, so no polling or sleep is needed.
	var buffer [4096]byte
	_, err = unix.Read(events, buffer[:])
	require.ErrorIs(t, err, unix.EAGAIN, "unselected mapping was opened")
}

// TestHPCProcessIgnoresBlankAndZeroJobLines checks that idle ("0"), blank, and whitespace-padded
// mapping lines add no extra job label.
func TestHPCProcessIgnoresBlankAndZeroJobLines(t *testing.T) {
	mappingDir := t.TempDir()
	require.NoError(t, stdos.WriteFile(mappingDir+"/0", []byte("0"), 0o600))
	require.NoError(t, stdos.WriteFile(mappingDir+"/1", []byte("  4242 \n\n4242\n"), 0o600))
	require.NoError(t, stdos.WriteFile(mappingDir+"/2", []byte("0\n77"), 0o600))

	counter := counters.Counter{FieldID: 150, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: {
		{GPU: "0", Value: "40", Counter: counter, Attributes: map[string]string{}},
		{GPU: "1", Value: "41", Counter: counter, Attributes: map[string]string{}},
		{GPU: "2", Value: "42", Counter: counter, Attributes: map[string]string{}},
	}}

	mapper := newHPCMapper(hpcTestConfig(mappingDir))
	require.NoError(t, mapper.Process(metrics, nil))

	jobsByGPU := make(map[string][]string)
	for _, metric := range metrics[counter] {
		job, labeled := metric.Attributes[hpcJobAttribute]
		if labeled {
			jobsByGPU[metric.GPU] = append(jobsByGPU[metric.GPU], job)
		} else {
			jobsByGPU[metric.GPU] = append(jobsByGPU[metric.GPU], "<none>")
		}
	}

	tests := []struct {
		gpu  string
		jobs []string
	}{
		{gpu: "0", jobs: []string{"<none>"}},
		{gpu: "1", jobs: []string{"4242"}},
		{gpu: "2", jobs: []string{"77"}},
	}
	for _, tt := range tests {
		t.Run("gpu "+tt.gpu, func(t *testing.T) {
			assert.Equal(t, tt.jobs, jobsByGPU[tt.gpu])
		})
	}
}

// TestHPCProcessPreservesCollidingPodLabel verifies combined Kubernetes and HPC enrichment
// retains both label values and produces valid Prometheus exposition.
func TestHPCProcessPreservesCollidingPodLabel(t *testing.T) {
	counter := counters.Counter{
		FieldID:   150,
		FieldName: "DCGM_FI_DEV_GPU_TEMP",
		PromType:  "gauge",
		Help:      "GPU temperature",
	}
	tests := []struct {
		name       string
		labels     map[string]string
		wantLabels map[string]string
	}{
		{
			name: "available fallback",
			labels: map[string]string{
				hpcJobAttribute: "tenant-label",
			},
			wantLabels: map[string]string{
				podLabelPrefix + hpcJobAttribute: "tenant-label",
			},
		},
		{
			name: "occupied fallback",
			labels: map[string]string{
				hpcJobAttribute:                  "tenant-label",
				podLabelPrefix + hpcJobAttribute: "existing-prefixed-label",
			},
			wantLabels: map[string]string{
				podLabelPrefix + hpcJobAttribute:                "existing-prefixed-label",
				podLabelPrefix + hpcJobAttribute + "_conflict1": "tenant-label",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mappingDir := t.TempDir()
			require.NoError(t, stdos.WriteFile(mappingDir+"/0", []byte("scheduler-job\n"), 0o600))

			originalLabels := maps.Clone(tt.labels)
			original := collector.Metric{
				GPU:          "0",
				GPUUUID:      "GPU-test-uuid",
				GPUDevice:    "nvidia0",
				GPUModelName: "Test GPU",
				Hostname:     "test-host",
				UUID:         "UUID",
				Value:        "42",
				Counter:      counter,
				Attributes:   map[string]string{podAttribute: "tenant-pod"},
				Labels:       maps.Clone(tt.labels),
			}
			unmapped := original
			unmapped.GPU = "1"
			unmapped.GPUUUID = "GPU-unmapped-uuid"
			unmapped.GPUDevice = "nvidia1"
			unmapped.Labels = maps.Clone(tt.labels)
			unmapped.Attributes = map[string]string{podAttribute: "unmapped-pod"}
			metrics := collector.MetricsByCounter{counter: {original, unmapped}}

			mapper := newHPCMapper(hpcTestConfig(mappingDir))
			require.NoError(t, mapper.Process(metrics, nil))

			require.Len(t, metrics[counter], 2)
			mappedResult := metrics[counter][0]
			assert.Equal(t, "scheduler-job", mappedResult.Attributes[hpcJobAttribute])
			assert.NotContains(t, mappedResult.Labels, hpcJobAttribute)
			assert.Equal(t, tt.wantLabels, mappedResult.Labels)
			assert.Equal(t, originalLabels, original.Labels, "source metric labels must remain unchanged")
			assert.NotContains(t, original.Attributes, hpcJobAttribute, "source metric attributes must remain unchanged")

			unmappedResult := metrics[counter][1]
			assert.Equal(t, tt.labels, unmappedResult.Labels, "an unmapped GPU must remain unchanged")
			assert.NotContains(t, unmappedResult.Attributes, hpcJobAttribute)

			var rendered bytes.Buffer
			require.NoError(t, rendermetrics.RenderGroup(&rendered, dcgm.FE_GPU, metrics))
			parser := expfmt.NewTextParser(model.LegacyValidation)
			_, err := parser.TextToMetricFamilies(bytes.NewReader(rendered.Bytes()))
			require.NoError(t, err)
			assert.Contains(t, rendered.String(), `hpc_job="scheduler-job"`)
			for label, value := range tt.wantLabels {
				assert.Contains(t, rendered.String(), fmt.Sprintf(`%s="%s"`, label, value))
			}
		})
	}
}

// A mapping directory that is missing at first is not an error: scrapes go on
// without job labels, the warning repeats only after its interval, and the
// labels appear as soon as the directory does.
func TestHPCMapperUsesAMappingDirectoryThatAppearsLater(t *testing.T) {
	logs := captureLogs(t)
	dir := filepath.Join(t.TempDir(), "job-mapping")
	clock := &fakeClock{now: time.Unix(1000, 0)}
	mapper := newHPCMapper(hpcTestConfig(dir))
	mapper.unavailableWarnings = warningLimiter{interval: 5 * time.Minute, now: clock.Now}
	scrape, _ := hpcScrape(t, mapper)

	for range 3 {
		got := scrape()
		require.Len(t, got, 1)
		assert.NotContains(t, got[0].Attributes, hpcJobAttribute)
	}
	assert.Equal(t, 1, logs.count("HPC job mapping"+" '"+dir+"' is not usable"), "one warning for three scrapes")

	clock.now = clock.now.Add(5 * time.Minute)
	scrape()
	assert.Equal(t, 2, logs.count("is not usable"), "the warning repeats after its interval")

	require.NoError(t, stdos.Mkdir(dir, 0o700))
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte("job-7\n"), 0o600))
	got := scrape()
	require.Len(t, got, 1)
	assert.Equal(t, "job-7", got[0].Attributes[hpcJobAttribute])
}

// hpcScrape returns a function that runs one scrape of a single GPU 0 sample
// through mapper and returns the processed samples.
func hpcScrape(t *testing.T, mapper *hpcMapper) (func() []collector.Metric, counters.Counter) {
	t.Helper()
	ctrl := gomock.NewController(t)
	provider := mockdeviceinfo.NewMockProvider(ctrl)
	provider.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	provider.EXPECT().GPUs().Return(nil).AnyTimes()

	counter := counters.Counter{FieldID: 1001, FieldName: "DCGM_FI_PROF_GR_ENGINE_ACTIVE", PromType: "gauge"}

	return func() []collector.Metric {
		metrics := collector.MetricsByCounter{counter: {{
			GPU: "0", Counter: counter, Value: "1", Attributes: map[string]string{},
		}}}
		require.NoError(t, mapper.Process(metrics, provider))

		return metrics[counter]
	}, counter
}

// A mapping directory removed after a good scrape is not an error: scrapes go
// on without job labels and warn once, and the labels come back with the
// directory.
func TestHPCMapperKeepsServingWhenTheMappingDirectoryIsRemoved(t *testing.T) {
	logs := captureLogs(t)
	dir := filepath.Join(t.TempDir(), "job-mapping")
	require.NoError(t, stdos.Mkdir(dir, 0o700))
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte("job-7\n"), 0o600))
	scrape, _ := hpcScrape(t, newHPCMapper(hpcTestConfig(dir)))

	got := scrape()
	require.Len(t, got, 1)
	assert.Equal(t, "job-7", got[0].Attributes[hpcJobAttribute])

	require.NoError(t, stdos.RemoveAll(dir))
	for range 3 {
		got = scrape()
		require.Len(t, got, 1)
		assert.NotContains(t, got[0].Attributes, hpcJobAttribute)
	}
	assert.Equal(t, 1, logs.count("HPC job mapping"+" '"+dir+"' is not usable"), "one warning for three scrapes")

	require.NoError(t, stdos.Mkdir(dir, 0o700))
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte("job-8\n"), 0o600))
	got = scrape()
	require.Len(t, got, 1)
	assert.Equal(t, "job-8", got[0].Attributes[hpcJobAttribute])
}

// A mapping directory with no file named after a GPU leaves every sample as
// it was. A subdirectory named after GPU 0 is not a mapping file either.
func TestHPCMapperIgnoresADirectoryWithoutGPUFiles(t *testing.T) {
	logs := captureLogs(t)
	dir := t.TempDir()
	for _, name := range []string{"README", "jobs.txt", ".gitkeep", "1e5"} {
		require.NoError(t, stdos.WriteFile(filepath.Join(dir, name), []byte("job-7\n"), 0o600))
	}
	require.NoError(t, stdos.Mkdir(filepath.Join(dir, "0"), 0o700))

	scrape, counter := hpcScrape(t, newHPCMapper(hpcTestConfig(dir)))

	assert.Equal(t, []collector.Metric{{
		GPU: "0", Counter: counter, Value: "1", Attributes: map[string]string{},
	}}, scrape())
	assert.Equal(t, 0, logs.count("level=WARN"), "an ignored file is not a warning")
}

// A directory the exporter may not read, and a file in it that it may not
// open, must not fail the scrape.
func TestHPCMapperPermissionErrorsDoNotFailTheScrape(t *testing.T) {
	if stdos.Geteuid() == 0 {
		t.Skip("this host ignores file permissions")
	}

	logs := captureLogs(t)
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	require.NoError(t, stdos.Mkdir(locked, 0o000))
	t.Cleanup(func() { _ = stdos.Chmod(locked, 0o700) })

	readable := t.TempDir()
	require.NoError(t, stdos.WriteFile(filepath.Join(readable, "0"), []byte("job-0\n"), 0o000))
	require.NoError(t, stdos.WriteFile(filepath.Join(readable, "1"), []byte("job-1\n"), 0o600))

	ctrl := gomock.NewController(t)
	provider := mockdeviceinfo.NewMockProvider(ctrl)
	provider.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	provider.EXPECT().GPUs().Return(nil).AnyTimes()
	counter := counters.Counter{FieldID: 1001, FieldName: "DCGM_FI_PROF_GR_ENGINE_ACTIVE", PromType: "gauge"}

	for _, tt := range []struct {
		name, dir string
		wantJob   map[string]string
	}{
		{"directory", locked, map[string]string{"0": "", "1": ""}},
		{"one file", readable, map[string]string{"0": "", "1": "job-1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mapper := newHPCMapper(hpcTestConfig(tt.dir))
			metrics := collector.MetricsByCounter{counter: {
				{GPU: "0", Counter: counter, Attributes: map[string]string{}},
				{GPU: "1", Counter: counter, Attributes: map[string]string{}},
			}}
			require.NoError(t, mapper.Process(metrics, provider))

			for _, metric := range metrics[counter] {
				if tt.wantJob[metric.GPU] == "" {
					assert.NotContains(t, metric.Attributes, hpcJobAttribute, "GPU %s", metric.GPU)
					continue
				}

				assert.Equal(t, tt.wantJob[metric.GPU], metric.Attributes[hpcJobAttribute], "GPU %s", metric.GPU)
			}
		})
	}

	assert.Contains(t, logs.String(), "permission denied")
}

// Two unreadable files are both reported, not only the first.
func TestHPCMapperWarnsAboutEachUnreadableFile(t *testing.T) {
	if stdos.Geteuid() == 0 {
		t.Skip("this host ignores file permissions")
	}

	logs := captureLogs(t)
	dir := t.TempDir()
	for _, name := range []string{"0", "1"} {
		require.NoError(t, stdos.WriteFile(filepath.Join(dir, name), []byte("job\n"), 0o000))
	}

	ctrl := gomock.NewController(t)
	provider := mockdeviceinfo.NewMockProvider(ctrl)
	provider.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	provider.EXPECT().GPUs().Return(nil).AnyTimes()

	counter := counters.Counter{FieldID: 1001, FieldName: "DCGM_FI_PROF_GR_ENGINE_ACTIVE", PromType: "gauge"}
	mapper := newHPCMapper(hpcTestConfig(dir))
	for range 2 {
		metrics := collector.MetricsByCounter{counter: {{GPU: "0", Counter: counter}, {GPU: "1", Counter: counter}}}
		require.NoError(t, mapper.Process(metrics, provider))
	}

	assert.Equal(t, 1, logs.count(filepath.Join(dir, "0")+"' is not usable"))
	assert.Equal(t, 1, logs.count(filepath.Join(dir, "1")+"' is not usable"))
}
