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
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

// hpcMapper copies GPU samples for jobs named by files in the configured directory.
type hpcMapper struct {
	Config              *appconfig.Config
	unavailableWarnings warningLimiter
}

// newHPCMapper creates the mapper. A directory that is missing or unreadable
// is not an error: Process checks it on every scrape, warns at most every few
// minutes, and uses the directory once it can be read.
func newHPCMapper(c *appconfig.Config) *hpcMapper {
	slog.Info(fmt.Sprintf("HPC job mapping is enabled and watch for the %q directory", c.HPCJobMappingDir))
	return &hpcMapper{Config: c}
}

// Name identifies the HPC transform in logs and tests.
func (p *hpcMapper) Name() string {
	return "hpcMapper"
}

// warnUnavailable logs that the mapping directory or one of its files cannot be
// read, at most once per interval, because the failure repeats on every scrape.
func (p *hpcMapper) warnUnavailable(location string, err error) {
	if !p.unavailableWarnings.allowFor(location) {
		return
	}

	slog.Warn(fmt.Sprintf("HPC job mapping '%s' is not usable; job labels are missing for it until it is. "+
		"The exporter keeps checking.", location), slog.String(logging.ErrorKey, err.Error()))
}

// Process copies samples per mapped job and keeps samples without a job mapping.
func (p *hpcMapper) Process(metrics collector.MetricsByCounter, _ deviceinfo.Provider) error {
	gpuToJobMap, ok := p.readJobMapping(metrics)
	if !ok {
		return nil
	}

	for counter := range metrics {
		var modifiedMetrics []collector.Metric

		for _, metric := range metrics[counter] {
			jobs := gpuToJobMap[hpcJobMapKey(metric)]
			if len(jobs) == 0 {
				modifiedMetrics = append(modifiedMetrics, metric)
				continue
			}

			for _, job := range jobs {
				modifiedMetrics = append(modifiedMetrics, metricWithHPCJob(metric, job))
			}
		}

		metrics[counter] = modifiedMetrics
	}

	return nil
}

// readJobMapping opens the directory once and reads only the files named after
// the GPUs and MIG instances in metrics. It reports false, after a rate-limited
// warning, when the directory cannot be opened, so the scrape goes on without
// job labels. A missing file or a directory is skipped quietly; an unsafe or
// invalid file warns and only leaves its own GPU without labels.
func (p *hpcMapper) readJobMapping(metrics collector.MetricsByCounter) (map[string][]string, bool) {
	dir := p.Config.HPCJobMappingDir

	directory, err := openMappingDirectory(dir)
	if err != nil {
		p.warnUnavailable(dir, err)
		return nil, false
	}
	defer directory.Close()

	gpuToJobMap := make(map[string][]string)
	for _, samples := range metrics {
		for _, metric := range samples {
			name := hpcJobMapKey(metric)
			if _, seen := gpuToJobMap[name]; seen || !validGPUFile.MatchString(name) {
				continue
			}

			jobs, err := readMappingAt(directory, name, p.Config)
			gpuToJobMap[name] = jobs

			if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errMappingIsDirectory) {
				p.warnUnavailable(path.Join(dir, name), err)
			}
		}
	}

	return gpuToJobMap, true
}

// hpcJobMapKey is the job file name that applies to a metric: the GPU, or the
// GPU and instance for a MIG sample.
func hpcJobMapKey(metric collector.Metric) string {
	if metric.MigProfile != "" {
		return fmt.Sprintf("%s.%s", metric.GPU, metric.GPUInstanceID)
	}
	return metric.GPU
}

// metricWithHPCJob copies a sample and labels the copy with one job.
func metricWithHPCJob(metric collector.Metric, job string) collector.Metric {
	modified := cloneMetric(metric)
	if modified.Attributes == nil {
		slog.Debug("modifiedMetric.Attributes is nil, making an empty map")
		modified.Attributes = make(map[string]string)
	}
	preserveHPCJobPodLabel(&modified)
	modified.Attributes[hpcJobAttribute] = job
	return modified
}

// preserveHPCJobPodLabel renames a Kubernetes pod label that would collide
// with the scheduler-owned hpc_job attribute added by this transform. FE_GPU
// is intentional: the generated pod_label_hpc_job[_conflictN] names do not
// overlap static built-ins of any supported group, and GPU rules also protect
// the configurable UUID label. Revisit the group if the fallback base changes.
func preserveHPCJobPodLabel(metric *collector.Metric) {
	value, exists := metric.Labels[hpcJobAttribute]
	if !exists {
		return
	}

	delete(metric.Labels, hpcJobAttribute)
	metric.Labels[availablePodLabelName(*metric, dcgm.FE_GPU, hpcJobAttribute)] = value
}

// validGPUFile matches Slurm job mapping filenames: plain GPU IDs (e.g. "0", "1")
// or MIG instance IDs in <GPU_ID>.<GPU_INSTANCE_ID> format (e.g. "2.0", "3.10").
var validGPUFile = regexp.MustCompile(`^\d+(\.\d+)?$`)
