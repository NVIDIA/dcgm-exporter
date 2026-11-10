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

package nvidiasmi_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// TestParseGPUReadings covers reported values, placeholders and malformed rows.
func TestParseGPUReadings(t *testing.T) {
	fields := []string{"temperature.gpu", "power.draw"}
	tests := []struct {
		name    string
		output  string
		want    nvidiasmi.Readings
		wantErr string
	}{
		{
			name:   "two GPUs",
			output: "GPU-aaa, 31, 141.25\nGPU-bbb, [N/A], N/A\n",
			want: nvidiasmi.Readings{
				"GPU-aaa": {"temperature.gpu": reading(31, "31"), "power.draw": reading(141.25, "141.25")},
				"GPU-bbb": {"temperature.gpu": {Raw: "[N/A]"}, "power.draw": {Raw: "N/A"}},
			},
		},
		{name: "empty output", output: "", wantErr: "listed no GPUs"},
		{name: "wrong column count", output: "GPU-aaa, 31\n", wantErr: "wrong number of fields"},
		{name: "missing UUID", output: "[N/A], 31, 1\n", wantErr: "has no UUID"},
		{name: "text where a number belongs", output: "GPU-aaa, hot, 1\n", wantErr: "temperature.gpu"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nvidiasmi.ParseGPUReadings([]byte(tc.output), fields)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseGPUReadings() error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGPUReadings() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseGPUReadings() =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

// TestQueryGPUReadingsAsksOnlyForTheGivenFields builds one query from the sorted, unique fields.
func TestQueryGPUReadingsAsksOnlyForTheGivenFields(t *testing.T) {
	var gotArgs []string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("GPU-aaa, 31, 1000\n"), nil
	}

	got, err := nvidiasmi.QueryGPUReadings(context.Background(), run, []string{"temperature.gpu", "clocks.sm", "temperature.gpu"})
	if err != nil {
		t.Fatalf("QueryGPUReadings() error = %v", err)
	}
	wantArgs := []string{"--query-gpu=uuid,clocks.sm,temperature.gpu", "--format=csv,noheader,nounits"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("runner args = %q, want %q", gotArgs, wantArgs)
	}
	if got["GPU-aaa"]["clocks.sm"].Value != 31 || got["GPU-aaa"]["temperature.gpu"].Value != 1000 {
		t.Fatalf("QueryGPUReadings() = %+v, want values matched to the sorted field order", got)
	}
}

// TestQueryGPUReadingsRunsNothingWithoutFields skips nvidia-smi when no field is wanted.
func TestQueryGPUReadingsRunsNothingWithoutFields(t *testing.T) {
	run := func(context.Context, ...string) ([]byte, error) {
		t.Fatal("nvidia-smi should not run without fields")
		return nil, nil
	}

	got, err := nvidiasmi.QueryGPUReadings(context.Background(), run, nil)

	if err != nil || len(got) != 0 {
		t.Fatalf("QueryGPUReadings() = %+v, %v, want empty readings", got, err)
	}
}

// TestQueryGPUReadingsWrapsRunnerErrors names the failed step.
func TestQueryGPUReadingsWrapsRunnerErrors(t *testing.T) {
	failing := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("not found") }

	_, err := nvidiasmi.QueryGPUReadings(context.Background(), failing, []string{"clocks.sm"})

	if err == nil || !strings.Contains(err.Error(), "query GPU readings with nvidia-smi: not found") {
		t.Fatalf("QueryGPUReadings() error = %v, want wrapped runner error", err)
	}
}

// reading builds a reported Reading for table expectations.
func reading(value float64, raw string) nvidiasmi.Reading {
	return nvidiasmi.Reading{Value: value, Reported: true, Raw: raw}
}
