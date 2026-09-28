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
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func pointer[T any](value T) *T { return &value }

func TestBatchesUseDCGMLimitsWithoutMIG(t *testing.T) {
	contract := Contract{Version: CurrentVersion}
	for id := 1; id <= 130; id++ {
		contract.Metrics = append(contract.Metrics, Metric{ID: id, Name: "DCGM_FI_REGULAR", Samples: []Sample{{EntityGroup: "GPU", Labels: map[string]string{"gpu": "0"}}}})
	}
	for id := 1000; id < 1066; id++ {
		contract.Metrics = append(contract.Metrics, Metric{ID: id, Name: "DCGM_FI_PROF", Profiling: true, Samples: []Sample{{EntityGroup: "GPU", Labels: map[string]string{"gpu": "0"}}}})
	}
	batches := contract.Batches()
	if len(batches) != 4 {
		t.Fatalf("batches = %d, want 4", len(batches))
	}
	if len(batches[0]) != 127 || len(batches[1]) != 3 {
		t.Fatalf("regular batch sizes = %d, %d; want 127, 3", len(batches[0]), len(batches[1]))
	}
	if len(batches[2]) != 64 || len(batches[3]) != 2 {
		t.Fatalf("whole-GPU profiling batch sizes = %d, %d; want 64, 2", len(batches[2]), len(batches[3]))
	}
}

func TestBatchesIsolateAllProfilingFieldsForMIGTopology(t *testing.T) {
	for _, entityGroup := range []string{gpuInstanceEntity, computeInstanceEntity} {
		t.Run(entityGroup, func(t *testing.T) {
			contract := Contract{Metrics: []Metric{
				{ID: 1001, Profiling: true, Samples: []Sample{{EntityGroup: "GPU"}}},
				{ID: 1002, Profiling: true, Samples: []Sample{{EntityGroup: "GPU"}}},
				{ID: 150, Samples: []Sample{{EntityGroup: entityGroup}}},
			}}

			batches := contract.Batches()
			if len(batches) != 3 || len(batches[0]) != 1 || batches[0][0].ID != 150 {
				t.Fatalf("Batches() = %#v", batches)
			}
			for index := 1; index < len(batches); index++ {
				if len(batches[index]) != 1 {
					t.Fatalf("MIG profiling batch %d size = %d, want 1", index-1, len(batches[index]))
				}
			}
		})
	}
}

func TestValidateFamilies(t *testing.T) {
	metric := Metric{
		Name: "DCGM_FI_DEV_GPU_TEMP",
		Samples: []Sample{{
			EntityGroup: "GPU",
			Labels:      map[string]string{"gpu": "0", "UUID": "GPU-0"},
			Value:       42,
		}},
	}
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
			name: "wrong value",
			mutate: func(families map[string]*dto.MetricFamily) {
				families[metric.Name].Metric[0].Gauge.Value = pointer(43.0)
			},
			want: "has value 43, expected 42",
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

func TestValidateRejectsInvalidObservedValue(t *testing.T) {
	contract := Contract{
		Version:     CurrentVersion,
		DeviceCount: 1,
		Metrics: []Metric{{
			ID:   150,
			Name: "DCGM_FI_DEV_GPU_TEMP",
			Samples: []Sample{{
				EntityGroup: "GPU",
				Labels:      map[string]string{"gpu": "0"},
				Value:       math.NaN(),
			}},
		}},
	}

	err := contract.Validate()
	if err == nil || !strings.Contains(err.Error(), "metrics[0].samples[0] has an invalid value") {
		t.Fatalf("Validate() error = %v, want invalid observed value", err)
	}
}

func TestValidateZeroDeviceContractHasNoMetrics(t *testing.T) {
	if err := (Contract{Version: CurrentVersion}).Validate(); err != nil {
		t.Fatalf("Validate() zero-device contract error = %v", err)
	}
	contract := Contract{
		Version: CurrentVersion,
		Metrics: []Metric{{
			ID:   150,
			Name: "DCGM_FI_DEV_GPU_TEMP",
			Samples: []Sample{{
				EntityGroup: "GPU",
				Labels:      map[string]string{"gpu": "0"},
				Value:       42,
			}},
		}},
	}
	err := contract.Validate()
	if err == nil || !strings.Contains(err.Error(), "zero-device fixture must not expose GPU metrics") {
		t.Fatalf("Validate() error = %v, want zero-device metric rejection", err)
	}
}

func TestValidateRejectsPreviousContractVersion(t *testing.T) {
	err := (Contract{Version: CurrentVersion - 1}).Validate()
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("version must be %d", CurrentVersion)) {
		t.Fatalf("Validate() error = %v, want current contract version", err)
	}
}

func TestValidateComputeInstanceSamples(t *testing.T) {
	newContract := func(entityGroup string) Contract {
		return Contract{ComputeInstanceCount: 1, Metrics: []Metric{{
			Name: "DCGM_FI_DEV_FB_USED",
			Samples: []Sample{{
				EntityGroup: entityGroup,
				Labels: map[string]string{
					"GPU_I_ID":      "3",
					"GPU_I_PROFILE": "1g.10gb",
					"GPU_CI_ID":     "1",
				},
			}},
		}}}
	}

	if err := newContract(computeInstanceEntity).ValidateComputeInstanceSamples(); err != nil {
		t.Fatalf("ValidateComputeInstanceSamples() error = %v", err)
	}

	for _, label := range []string{"GPU_I_ID", "GPU_I_PROFILE", "GPU_CI_ID"} {
		t.Run("missing "+label, func(t *testing.T) {
			contract := newContract(computeInstanceEntity)
			contract.Metrics[0].Samples[0].Labels[label] = ""
			err := contract.ValidateComputeInstanceSamples()
			if err == nil || !strings.Contains(err.Error(), label) {
				t.Fatalf("ValidateComputeInstanceSamples() error = %v, want %s", err, label)
			}
		})
	}

	withoutComputeInstances := newContract("GPU")
	withoutComputeInstances.Metrics[0].Samples[0].Labels = nil
	err := withoutComputeInstances.ValidateComputeInstanceSamples()
	if err == nil || !strings.Contains(err.Error(), "no compute-instance field samples") {
		t.Fatalf("ValidateComputeInstanceSamples() error = %v, want missing compute-instance sample", err)
	}

	for _, test := range []struct {
		name  string
		count int
		add   func(*Contract)
		want  string
	}{
		{
			name:  "missing declared compute instance",
			count: 2,
			want:  "count 2 does not match 1 sampled compute instances",
		},
		{
			name:  "extra sampled compute instance",
			count: 1,
			add: func(contract *Contract) {
				other := contract.Metrics[0].Samples[0]
				other.Labels = map[string]string{
					"GPU_I_ID":      "3",
					"GPU_I_PROFILE": "1g.10gb",
					"GPU_CI_ID":     "2",
				}
				contract.Metrics[0].Samples = append(contract.Metrics[0].Samples, other)
			},
			want: "count 1 does not match 2 sampled compute instances",
		},
		{
			name:  "same compute instance across fields",
			count: 1,
			add: func(contract *Contract) {
				contract.Metrics = append(contract.Metrics, Metric{
					Name:    "DCGM_FI_DEV_GPU_TEMP",
					Samples: contract.Metrics[0].Samples,
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			contract := newContract(computeInstanceEntity)
			contract.ComputeInstanceCount = test.count
			if test.add != nil {
				test.add(&contract)
			}

			err := contract.ValidateComputeInstanceSamples()
			if test.want == "" && err != nil {
				t.Fatalf("ValidateComputeInstanceSamples() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("ValidateComputeInstanceSamples() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestReadRejectsDeclaredComputeInstanceWithoutSample checks that validation is
// applied when a saved fixture is read, not only when it is produced.
func TestReadRejectsDeclaredComputeInstanceWithoutSample(t *testing.T) {
	contract := Contract{
		Version:              CurrentVersion,
		DeviceCount:          1,
		ComputeInstanceCount: 1,
		Metrics: []Metric{{
			ID:   150,
			Name: "DCGM_FI_DEV_GPU_TEMP",
			Samples: []Sample{{
				EntityGroup: "GPU",
				Labels:      map[string]string{"gpu": "0"},
				Value:       42,
			}},
		}},
	}

	encoded, err := json.Marshal(contract)
	if err != nil {
		t.Fatalf("marshal contract: %v", err)
	}
	_, err = Read(bytes.NewReader(encoded))
	if err == nil || !strings.Contains(err.Error(), "no compute-instance field samples") {
		t.Fatalf("Read() error = %v, want missing compute-instance sample", err)
	}
}

// TestReadRejectsIncompleteComputeInstanceCoverage checks that a fixture cannot
// claim more compute instances than its field samples cover.
func TestReadRejectsIncompleteComputeInstanceCoverage(t *testing.T) {
	contract := Contract{
		Version:              CurrentVersion,
		DeviceCount:          1,
		ComputeInstanceCount: 2,
		Metrics: []Metric{{
			ID:   150,
			Name: "DCGM_FI_DEV_GPU_TEMP",
			Samples: []Sample{{
				EntityGroup: computeInstanceEntity,
				Labels: map[string]string{
					"GPU_I_ID":      "3",
					"GPU_I_PROFILE": "1g.10gb",
					"GPU_CI_ID":     "1",
				},
				Value: 42,
			}},
		}},
	}

	encoded, err := json.Marshal(contract)
	if err != nil {
		t.Fatalf("marshal contract: %v", err)
	}
	_, err = Read(bytes.NewReader(encoded))
	if err == nil || !strings.Contains(err.Error(), "count 2 does not match 1 sampled compute instances") {
		t.Fatalf("Read() error = %v, want incomplete compute-instance coverage", err)
	}
}
