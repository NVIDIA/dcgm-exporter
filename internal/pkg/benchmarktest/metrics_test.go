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

package benchmarktest

import (
	"testing"
)

func TestSyntheticGPUMetrics(t *testing.T) {
	benchmarkCase := Case{FieldCount: 3, GPUCount: 2}
	metricsByCounter := SyntheticGPUMetrics(benchmarkCase)

	if got := len(metricsByCounter); got != benchmarkCase.FieldCount {
		t.Fatalf("counter family count = %d, want %d", got, benchmarkCase.FieldCount)
	}

	series := 0
	for counter, metrics := range metricsByCounter {
		if counter.PromType != "gauge" {
			t.Fatalf("%s type = %q, want gauge", counter.FieldName, counter.PromType)
		}
		if got := len(metrics); got != benchmarkCase.GPUCount {
			t.Fatalf("%s metric count = %d, want %d", counter.FieldName, got, benchmarkCase.GPUCount)
		}
		series += len(metrics)
	}

	if series != benchmarkCase.Series() {
		t.Fatalf("series count = %d, want %d", series, benchmarkCase.Series())
	}
}
