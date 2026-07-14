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

package nvmlinjection

import (
	"math"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func pointer[T any](value T) *T { return &value }

func TestBatchesUseDCGMLimits(t *testing.T) {
	contract := Contract{Version: CurrentVersion}
	for id := 1; id <= 130; id++ {
		contract.Metrics = append(contract.Metrics, Metric{ID: id, Name: "DCGM_FI_REGULAR", Samples: []Sample{{EntityGroup: "GPU", Labels: map[string]string{"gpu": "0"}}}})
	}
	for id := 1000; id < 1066; id++ {
		contract.Metrics = append(contract.Metrics, Metric{ID: id, Name: "DCGM_FI_PROF", Profiling: true, Samples: []Sample{{EntityGroup: "GPU", Labels: map[string]string{"gpu": "0"}}}})
	}
	batches := contract.Batches()
	want := []int{127, 3, 64, 2}
	if len(batches) != len(want) {
		t.Fatalf("batches = %d, want %d", len(batches), len(want))
	}
	for index := range want {
		if len(batches[index]) != want[index] {
			t.Fatalf("batch %d size = %d, want %d", index, len(batches[index]), want[index])
		}
	}
}

func TestValidateFamilies(t *testing.T) {
	metric := Metric{Name: "DCGM_FI_DEV_GPU_TEMP", Samples: []Sample{{EntityGroup: "GPU", Labels: map[string]string{"gpu": "0", "UUID": "GPU-0"}}}}
	validFamilies := func() map[string]*dto.MetricFamily {
		return map[string]*dto.MetricFamily{
			metric.Name: {
				Name: pointer(metric.Name),
				Type: dto.MetricType_GAUGE.Enum(),
				Metric: []*dto.Metric{{
					Gauge: &dto.Gauge{Value: pointer(42.0)},
					Label: []*dto.LabelPair{{Name: pointer("gpu"), Value: pointer("0")}, {Name: pointer("UUID"), Value: pointer("GPU-0")}},
				}},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(map[string]*dto.MetricFamily)
		want   string
	}{
		{name: "valid"},
		{
			name: "invalid value",
			mutate: func(families map[string]*dto.MetricFamily) {
				families[metric.Name].Metric[0].Gauge.Value = pointer(math.NaN())
			},
			want: "invalid value",
		},
		{
			name: "missing family",
			mutate: func(families map[string]*dto.MetricFamily) {
				delete(families, metric.Name)
			},
			want: "is missing",
		},
		{
			name: "missing entity label",
			mutate: func(families map[string]*dto.MetricFamily) {
				families[metric.Name].Metric[0].Label = families[metric.Name].Metric[0].Label[:1]
			},
			want: "is missing GPU entity 0",
		},
		{
			name: "wrong entity label",
			mutate: func(families map[string]*dto.MetricFamily) {
				families[metric.Name].Metric[0].Label[1].Value = pointer("GPU-1")
			},
			want: "is missing GPU entity 0",
		},
		{
			name: "wrong sample count",
			mutate: func(families map[string]*dto.MetricFamily) {
				family := families[metric.Name]
				family.Metric = append(family.Metric, family.Metric[0])
			},
			want: "has 2 samples, expected 1",
		},
		{
			name: "wrong family type",
			mutate: func(families map[string]*dto.MetricFamily) {
				families[metric.Name].Type = dto.MetricType_COUNTER.Enum()
			},
			want: "has type COUNTER, expected GAUGE",
		},
		{
			name: "unexpected family",
			mutate: func(families map[string]*dto.MetricFamily) {
				name := "DCGM_FI_DEV_POWER_USAGE"
				families[name] = &dto.MetricFamily{Name: pointer(name), Type: dto.MetricType_GAUGE.Enum()}
			},
			want: `unexpected DCGM metric family "DCGM_FI_DEV_POWER_USAGE"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			families := validFamilies()
			if tt.mutate != nil {
				tt.mutate(families)
			}
			err := ValidateFamilies(families, []Metric{metric})
			if tt.want == "" {
				if err != nil {
					t.Fatalf("ValidateFamilies() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateFamilies() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSaneValueRejectsDCGMSentinels(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), float64(int32Blank), int64BlankFloat, fp64Blank + 2} {
		if saneValue(value) {
			t.Fatalf("saneValue(%v) = true", value)
		}
	}
	for _, value := range []float64{-1, 0, 42, fp64Blank + 4} {
		if !saneValue(value) {
			t.Fatalf("saneValue(%v) = false", value)
		}
	}
}
