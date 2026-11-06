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

package k8s

import (
	"strings"
	"testing"
)

// Scrape lines in the shape the exporter serves on a MIG node: gpu 0 is a
// whole GPU, gpu 1 has two MIG instances.
const (
	migWholeGPUPower  = `DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="0",UUID="GPU-0"} 150`
	migParentGPUPower = `DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="1",UUID="GPU-1"} 201`
	migInstance1Power = `DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="1",UUID="GPU-1",GPU_I_ID="1",GPU_I_PROFILE="3g.90gb"} 201`
	migWholeGPUFB     = `DCGM_FI_DEV_FB_USED_BYTES{gpu="0",UUID="GPU-0"} 1024`
	migInstance1FB    = `DCGM_FI_DEV_FB_USED_BYTES{gpu="1",UUID="GPU-1",GPU_I_ID="1",GPU_I_PROFILE="3g.90gb"} 512`
	migInstance2FB    = `DCGM_FI_DEV_FB_USED_BYTES{gpu="1",UUID="GPU-1",GPU_I_ID="2",GPU_I_PROFILE="3g.90gb"} 256`
)

// migScrape joins scrape lines into a metrics body.
func migScrape(lines ...string) []byte {
	return []byte(strings.Join(lines, "\n") + "\n")
}

// TestMetricsReportGPULevelFieldsOnParentGPUs checks that the scenario check
// passes the shape of a fixed exporter and rejects one series per instance.
func TestMetricsReportGPULevelFieldsOnParentGPUs(t *testing.T) {
	assertMIGHelperError(t, metricsReportGPULevelFieldsOnParentGPUs(migScrape(migParentGPUPower, migInstance1FB)), "")
	assertMIGHelperError(t, metricsReportGPULevelFieldsOnParentGPUs(migScrape(migInstance1Power, migInstance1FB)),
		`GPU-level family "DCGM_FI_DEV_BOARD_POWER_WATTS" has a sample with MIG labels on gpu="1"`)
}

// TestMetricsHaveOnlySelectedMIGInstanceSamples checks instance selection allows parent-GPU series only.
func TestMetricsHaveOnlySelectedMIGInstanceSamples(t *testing.T) {
	tests := []struct {
		name       string
		metrics    []byte
		instanceID string
		wantErr    string
	}{
		{
			name:    "instances and their parent GPU",
			metrics: migScrape(migParentGPUPower, migInstance1FB, migInstance2FB),
		},
		{
			name:       "one selected instance and its parent GPU",
			metrics:    migScrape(migParentGPUPower, migInstance1FB),
			instanceID: "1",
		},
		{
			name:       "a second instance when one was selected",
			metrics:    migScrape(migParentGPUPower, migInstance1FB, migInstance2FB),
			instanceID: "1",
			wantErr:    `GPU_I_ID="2", expected only "1"`,
		},
		{
			name:    "a whole GPU without selected instances",
			metrics: migScrape(migWholeGPUFB, migInstance1FB),
			wantErr: `has gpu="0", which has no selected MIG instance`,
		},
		{
			name:    "compute-instance sample without GPU-instance labels",
			metrics: migScrape(migParentGPUPower, migInstance1FB, `DCGM_FI_DEV_FB_FREE_BYTES{gpu="1",UUID="GPU-1",GPU_CI_ID="0"} 512`),
			wantErr: `GPU_CI_ID="0" without GPU-instance labels`,
		},
		{
			name:    "no instance samples",
			metrics: migScrape(migParentGPUPower),
			wantErr: `has gpu="1", which has no selected MIG instance`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertMIGHelperError(t, metricsHaveOnlySelectedMIGInstanceSamples(test.metrics, test.instanceID), test.wantErr)
		})
	}
}

// TestFirstFullGPUIDSkipsMIGParentGPUs checks full-GPU discovery ignores parent-GPU series.
func TestFirstFullGPUIDSkipsMIGParentGPUs(t *testing.T) {
	gpu, err := firstFullGPUID(migScrape(migParentGPUPower, migInstance1FB, migWholeGPUPower, migWholeGPUFB))
	if err != nil {
		t.Fatalf("firstFullGPUID() error = %v", err)
	}
	if gpu != "0" {
		t.Fatalf("firstFullGPUID() = %q, want %q", gpu, "0")
	}

	if _, err := firstFullGPUID(migScrape(migParentGPUPower, migInstance1FB)); err == nil {
		t.Fatal("firstFullGPUID() found a full GPU on a node where every GPU has MIG instances")
	}
}

// TestMetricsHaveMixedMIGAndFullGPUSamples checks mixed mode needs a GPU without MIG instances.
func TestMetricsHaveMixedMIGAndFullGPUSamples(t *testing.T) {
	if err := metricsHaveMixedMIGAndFullGPUSamples(migScrape(migWholeGPUPower, migParentGPUPower, migInstance1FB)); err != nil {
		t.Fatalf("mixed node: %v", err)
	}
	err := metricsHaveMixedMIGAndFullGPUSamples(migScrape(migParentGPUPower, migInstance1FB))
	assertMIGHelperError(t, err, "no full GPU DCGM sample was emitted")
}

// assertMIGHelperError checks that err is nil when want is empty, or contains want.
func assertMIGHelperError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}
