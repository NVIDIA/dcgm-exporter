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

// Package benchmarktest provides deterministic inputs shared by CPU-only benchmark boundaries.
package benchmarktest

import (
	"fmt"
	"strconv"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

const (
	// SyntheticMetricNamePrefix identifies metric families owned by this benchmark fixture.
	SyntheticMetricNamePrefix = "DCGM_EXPORTER_BENCHMARK_FIELD_"

	syntheticMetricHelp = "Synthetic metric used by CPU-only benchmarks."

	// defaultEmittedFieldCount is the 26 active entries in etc/default-counters.csv
	// minus DCGM_FI_DRIVER_VERSION, the one label-only entry that is not emitted as
	// its own numeric metric family.
	defaultEmittedFieldCount = 25

	// syntheticStressFieldCount is a round, synthetic scaling tier four times the
	// current default emitted field count. It does not represent a production preset.
	syntheticStressFieldCount = 4 * defaultEmittedFieldCount
)

// Case identifies one field-count and GPU-count combination in the shared benchmark matrix.
type Case struct {
	FieldCount int
	GPUCount   int
}

// Cases returns the canonical cases used to compare CPU-only benchmark boundaries.
func Cases() []Case {
	fieldCounts := [...]int{1, defaultEmittedFieldCount, syntheticStressFieldCount}
	gpuCounts := [...]int{1, 2, 4, 8}
	cases := make([]Case, 0, len(fieldCounts)*len(gpuCounts))

	for _, fieldCount := range fieldCounts {
		for _, gpuCount := range gpuCounts {
			cases = append(cases, Case{
				FieldCount: fieldCount,
				GPUCount:   gpuCount,
			})
		}
	}

	return cases
}

// Name returns the stable Go sub-benchmark name for the case.
func (c Case) Name() string {
	return fmt.Sprintf("fields=%d/gpus=%d", c.FieldCount, c.GPUCount)
}

// Series returns the number of Prometheus series emitted by the case.
func (c Case) Series() int {
	return c.FieldCount * c.GPUCount
}

// SyntheticGPUMetrics creates the collected metrics for one benchmark matrix case.
func SyntheticGPUMetrics(c Case) collector.MetricsByCounter {
	metricsByCounter := make(collector.MetricsByCounter, c.FieldCount)

	for fieldIndex := range c.FieldCount {
		counter := counters.Counter{
			FieldID:   dcgm.Short(1000 + fieldIndex),
			FieldName: fmt.Sprintf("%s%03d", SyntheticMetricNamePrefix, fieldIndex),
			PromType:  "gauge",
			Help:      syntheticMetricHelp,
		}
		metrics := make([]collector.Metric, c.GPUCount)

		for gpuIndex := range c.GPUCount {
			gpu := strconv.Itoa(gpuIndex)
			metrics[gpuIndex] = collector.Metric{
				Counter:      counter,
				Value:        strconv.Itoa((fieldIndex + 1) * (gpuIndex + 1)),
				GPU:          gpu,
				GPUUUID:      fmt.Sprintf("GPU-%08d-0000-0000-0000-%012d", gpuIndex, gpuIndex),
				GPUDevice:    "nvidia" + gpu,
				GPUModelName: "Synthetic GPU",
				GPUPCIBusID:  fmt.Sprintf("00000000:%02x:00.0", gpuIndex),
				UUID:         "UUID",
				Hostname:     "benchmark-host",
				Labels:       map[string]string{},
				Attributes:   map[string]string{},
			}
		}

		metricsByCounter[counter] = metrics
	}

	return metricsByCounter
}
