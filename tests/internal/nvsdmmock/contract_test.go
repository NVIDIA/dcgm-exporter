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

package nvsdmmock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func pointer[T any](value T) *T { return &value }

func arbitraryContract() Contract {
	return Contract{
		Version:     CurrentVersion,
		SwitchCount: 2,
		LinkCount:   3,
		Metrics: []Metric{
			{
				ID:   701,
				Name: "DCGM_FI_DEV_ARBITRARY_SWITCH_VALUE",
				Type: "gauge",
				Samples: []Sample{
					{EntityGroup: EntityGroupSwitch, EntityID: 17, Labels: map[string]string{"nvswitch": "nvswitch17", "hostname": "host-a"}, Value: 12.5},
					{EntityGroup: EntityGroupSwitch, EntityID: 29, Labels: map[string]string{"nvswitch": "nvswitch29", "hostname": "host-a"}, Value: -8},
				},
			},
			{
				ID:   913,
				Name: "DCGM_FI_DEV_ARBITRARY_LINK_VALUE",
				Type: "gauge",
				Samples: []Sample{
					{EntityGroup: EntityGroupSwitch, EntityID: 17, Labels: map[string]string{"nvswitch": "nvswitch17", "hostname": "host-a"}, Value: 200},
					{EntityGroup: EntityGroupLink, EntityID: 4, ParentID: 17, Labels: map[string]string{"nvswitch": "nvswitch17", "nvlink": "4", "hostname": "host-a"}, Value: 99},
					{EntityGroup: EntityGroupLink, EntityID: 8, ParentID: 29, Labels: map[string]string{"nvswitch": "nvswitch29", "nvlink": "8", "hostname": "host-a"}, Value: 101},
				},
			},
		},
	}
}

func TestContractAcceptsArbitraryTopologyAndValues(t *testing.T) {
	contract := arbitraryContract()
	if err := contract.Validate(); err != nil {
		t.Fatal(err)
	}

	var collectors bytes.Buffer
	if err := WriteCollectors(&collectors, contract.Metrics); err != nil {
		t.Fatal(err)
	}
	for _, metric := range contract.Metrics {
		if !strings.Contains(collectors.String(), metric.Name+",gauge,") {
			t.Fatalf("collectors do not contain %q:\n%s", metric.Name, collectors.String())
		}
	}
}

func TestRead(t *testing.T) {
	valid, err := json.Marshal(arbitraryContract())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{name: "valid", payload: append(valid, '\n')},
		{name: "unknown field", payload: bytes.Replace(valid, []byte("{"), []byte(`{"unknown":true,`), 1), wantErr: `unknown field "unknown"`},
		{name: "malformed", payload: []byte(`{"version":`), wantErr: "decode NVSDM mock contract"},
		{name: "trailing JSON", payload: append(append([]byte(nil), valid...), []byte(`{"stale":true}`)...), wantErr: "unexpected trailing data"},
		{name: "trailing garbage", payload: append(append([]byte(nil), valid...), []byte(`garbage`)...), wantErr: "trailing data"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, err := Read(bytes.NewReader(test.payload))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Read() error = %v", err)
				}
				if err := contract.Validate(); err != nil {
					t.Fatalf("Read() contract validation error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Read() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestContractAcceptsEmptyZeroEntityTelemetry(t *testing.T) {
	if err := (Contract{Version: CurrentVersion}).Validate(); err != nil {
		t.Fatalf("Validate() zero-entity contract error = %v", err)
	}
	err := (Contract{Version: CurrentVersion, SwitchCount: 1}).Validate()
	if err == nil || !strings.Contains(err.Error(), "no supported switch/link metrics") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestContractRejectsMismatchedLinkParentIdentity(t *testing.T) {
	contract := arbitraryContract()
	contract.Metrics[1].Samples[1].ParentID = 29
	err := contract.Validate()
	if err == nil || !strings.Contains(err.Error(), "does not identify switch entity 29") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestContractRejectsSamplesWithZeroEntityCount(t *testing.T) {
	tests := []struct {
		name     string
		contract Contract
		want     string
	}{
		{
			name: "switch samples",
			contract: Contract{
				Version: CurrentVersion,
				Metrics: []Metric{arbitraryContract().Metrics[0]},
			},
			want: "switch samples require switchCount > 0",
		},
		{
			name: "link samples",
			contract: Contract{
				Version:     CurrentVersion,
				SwitchCount: 2,
				Metrics:     []Metric{arbitraryContract().Metrics[1]},
			},
			want: "link samples require linkCount > 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.contract.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBatchesUseDCGMFieldLimit(t *testing.T) {
	contract := Contract{Version: CurrentVersion, SwitchCount: 1}
	for id := 1; id <= RegularBatchSize+2; id++ {
		contract.Metrics = append(contract.Metrics, Metric{
			ID:      id,
			Name:    fmt.Sprintf("DCGM_FI_DEV_SWITCH_FIELD_%d", id),
			Type:    "gauge",
			Samples: []Sample{{EntityGroup: EntityGroupSwitch, Labels: map[string]string{"nvswitch": "nvswitch0", "hostname": "host-a"}}},
		})
	}
	batches := contract.Batches()
	if len(batches) != 2 || len(batches[0]) != RegularBatchSize || len(batches[1]) != 2 {
		t.Fatalf("batch sizes = %d/%d", len(batches[0]), len(batches[1]))
	}
}

func TestValidateFamiliesUsesExactDynamicContract(t *testing.T) {
	contract := arbitraryContract()
	families := familiesFor(contract.Metrics)
	if err := ValidateFamilies(families, contract.Metrics); err != nil {
		t.Fatal(err)
	}
}

func TestValidateFamiliesRejectsContractMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]*dto.MetricFamily, []Metric)
		want   string
	}{
		{
			name: "value",
			mutate: func(families map[string]*dto.MetricFamily, metrics []Metric) {
				families[metrics[0].Name].Metric[0].Gauge.Value = pointer(500.0)
			},
			want: "has value 500",
		},
		{
			name: "labels",
			mutate: func(families map[string]*dto.MetricFamily, metrics []Metric) {
				families[metrics[1].Name].Metric[0].Label[0].Value = pointer("wrong")
			},
			want: "missing sample with labels",
		},
		{
			name: "type",
			mutate: func(families map[string]*dto.MetricFamily, metrics []Metric) {
				families[metrics[0].Name].Type = dto.MetricType_COUNTER.Enum()
			},
			want: "expected GAUGE",
		},
		{
			name: "family",
			mutate: func(families map[string]*dto.MetricFamily, metrics []Metric) {
				delete(families, metrics[0].Name)
			},
			want: "is missing",
		},
		{
			name: "sample count",
			mutate: func(families map[string]*dto.MetricFamily, metrics []Metric) {
				families[metrics[0].Name].Metric = families[metrics[0].Name].Metric[:1]
			},
			want: "has 1 samples, expected 2",
		},
		{
			name: "unexpected family",
			mutate: func(families map[string]*dto.MetricFamily, _ []Metric) {
				families["DCGM_FI_DEV_UNEXPECTED"] = &dto.MetricFamily{Type: dto.MetricType_GAUGE.Enum()}
			},
			want: "unexpected DCGM metric family",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := arbitraryContract()
			families := familiesFor(contract.Metrics)
			test.mutate(families, contract.Metrics)
			err := ValidateFamilies(families, contract.Metrics)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateFamilies() error = %v, want %q", err, test.want)
			}
		})
	}
}

func familiesFor(metrics []Metric) map[string]*dto.MetricFamily {
	families := make(map[string]*dto.MetricFamily, len(metrics))
	for _, expected := range metrics {
		family := &dto.MetricFamily{Name: pointer(expected.Name), Type: dto.MetricType_GAUGE.Enum()}
		for _, sample := range expected.Samples {
			metric := &dto.Metric{Gauge: &dto.Gauge{Value: pointer(sample.Value)}}
			for name, value := range sample.Labels {
				metric.Label = append(metric.Label, &dto.LabelPair{Name: pointer(name), Value: pointer(value)})
			}
			family.Metric = append(family.Metric, metric)
		}
		families[expected.Name] = family
	}
	return families
}
