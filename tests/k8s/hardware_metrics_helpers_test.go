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
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	ginkgoTypes "github.com/onsi/ginkgo/v2/types"
	corev1 "k8s.io/api/core/v1"
)

// TestHardwareBasicMetricsDeploymentCachesSetupFailure checks a failed Pod setup is not retried.
func TestHardwareBasicMetricsDeploymentCachesSetupFailure(t *testing.T) {
	setupErr := errors.New("setup failed")
	calls := 0
	deployment := newHardwareBasicMetricsDeployment(func(context.Context) (*corev1.Pod, error) {
		calls++
		return nil, setupErr
	})

	for _, label := range []string{"profiling", "nvlink", "nvlinkIntegrity", "p2pStatus", "fieldUnsupported"} {
		_, err := deployment.podFor(context.Background())
		if !errors.Is(err, setupErr) {
			t.Fatalf("%s setup error = %v, want %v", label, err, setupErr)
		}
	}
	if calls != 1 {
		t.Fatalf("setup calls = %d, want 1", calls)
	}
}

// TestHardwareBasicMetricsDeploymentCachesCleanup checks cleanup runs at most once.
func TestHardwareBasicMetricsDeploymentCachesCleanup(t *testing.T) {
	deployment := newHardwareBasicMetricsDeployment(nil)
	calls := 0
	deployment.runCleanupOnce(func() { calls++ })
	deployment.runCleanupOnce(func() { calls++ })
	if calls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", calls)
	}
}

// TestHardwareBasicMetricsRowsIncludeUnsupportedFieldOnlyWhenSelected checks optional unsupported-field rows.
func TestHardwareBasicMetricsRowsIncludeUnsupportedFieldOnlyWhenSelected(t *testing.T) {
	rows := hardwareBasicMetricsRowsFor("DCGM_FI_DEV_TEST_UNSUPPORTED", true, false)
	if !containsRow(rows, "DCGM_FI_DEV_TEST_UNSUPPORTED") {
		t.Fatalf("selected fieldUnsupported rows should include unsupported candidate: %#v", rows)
	}

	rows = hardwareBasicMetricsRowsFor("DCGM_FI_DEV_TEST_UNSUPPORTED", false, false)
	if containsRow(rows, "DCGM_FI_DEV_TEST_UNSUPPORTED") {
		t.Fatalf("unselected fieldUnsupported rows should not include unsupported candidate: %#v", rows)
	}
}

// TestHardwareBasicMetricsRowsIncludeNVLinkIntegrityOnlyWhenSelected checks optional NVLink integrity rows.
func TestHardwareBasicMetricsRowsIncludeNVLinkIntegrityOnlyWhenSelected(t *testing.T) {
	if rows := hardwareBasicMetricsRowsFor("", false, true); !containsRow(rows, "DCGM_FI_DEV_NVLINK_INTEGRITY_ERROR_TOTAL") {
		t.Fatalf("selected nvlinkIntegrity rows should include its exact field: %#v", rows)
	}
	if rows := hardwareBasicMetricsRowsFor("DCGM_FI_DEV_TEST_UNSUPPORTED", false, false); containsRow(rows, "DCGM_FI_DEV_NVLINK_INTEGRITY_ERROR_TOTAL") {
		t.Fatalf("unselected nvlinkIntegrity rows should omit its field: %#v", rows)
	}
}

// TestLabelFilterSelectsScenario checks Ginkgo label-filter selection for representative expressions.
func TestLabelFilterSelectsScenario(t *testing.T) {
	tests := map[string]bool{
		"":                           true,
		"fieldUnsupported":           true,
		"nvlink || fieldUnsupported": true,
		"nvlink":                     false,
		"!fieldUnsupported":          false,
		"!(fieldUnsupported)":        false,
		"fieldUnsupportedExtra":      false,
	}

	for filter, want := range tests {
		if got := labelFilterSelectsScenario(filter, "fieldUnsupported"); got != want {
			t.Fatalf("labelFilterSelectsScenario(%q) = %v, want %v", filter, got, want)
		}
	}
}

// containsRow reports whether any collector row contains the requested field text.
func containsRow(rows []string, needle string) bool {
	for _, row := range rows {
		if strings.Contains(row, needle) {
			return true
		}
	}
	return false
}

// hardwareBasicMetricsSetup creates the Pod shared by basic-metrics scenarios.
type hardwareBasicMetricsSetup func(context.Context) (*corev1.Pod, error)

// hardwareBasicMetricsDeployment caches one setup result and guards cleanup against repeats.
type hardwareBasicMetricsDeployment struct {
	setup       hardwareBasicMetricsSetup
	setupOnce   sync.Once
	cleanupOnce sync.Once
	pod         *corev1.Pod
	err         error
}

// newHardwareBasicMetricsDeployment creates the per-suite setup and cleanup cache.
func newHardwareBasicMetricsDeployment(setup hardwareBasicMetricsSetup) *hardwareBasicMetricsDeployment {
	return &hardwareBasicMetricsDeployment{setup: setup}
}

// podFor runs setup once and returns its cached Pod or error to each scenario.
func (d *hardwareBasicMetricsDeployment) podFor(ctx context.Context) (*corev1.Pod, error) {
	d.setupOnce.Do(func() {
		d.pod, d.err = d.setup(ctx)
	})
	return d.pod, d.err
}

// runCleanupOnce invokes cleanup only for the first caller.
func (d *hardwareBasicMetricsDeployment) runCleanupOnce(cleanup func()) {
	d.cleanupOnce.Do(cleanup)
}

// hardwareBasicMetricsRowsFor builds collector rows for the selected optional-field scenarios.
func hardwareBasicMetricsRowsFor(unsupportedField string, includeUnsupported, includeNVLinkIntegrity bool) []string {
	rows := []string{
		"DCGM_FI_DEV_GPU_TEMP_CELSIUS, gauge, GPU temperature (in C).",
		"DCGM_FI_PROF_GR_ENGINE_ACTIVE, gauge, Ratio of time the graphics engine is active.",
		"DCGM_FI_PROF_DRAM_ACTIVE, gauge, Ratio of cycles active in device memory.",
		"DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL, gauge, Total NVLink bandwidth.",
		"DCGM_EXP_P2P_STATUS, gauge, Current NVLink P2P status per peer link.",
		"DCGM_FI_DEV_C2C_LINK_COUNT, gauge, Number of C2C links.",
		"DCGM_FI_DEV_C2C_LINK_STATUS, gauge, C2C link status.",
	}
	if includeUnsupported {
		candidate := strings.TrimSpace(unsupportedField)
		if candidate != "" {
			rows = append(rows, candidate+", gauge, candidate field for unsupported-field omission validation.")
		}
	}
	if includeNVLinkIntegrity {
		rows = append(rows, "DCGM_FI_DEV_NVLINK_INTEGRITY_ERROR_TOTAL, counter, NVLink 5+ integrity errors.")
	}
	return rows
}

// labelFilterSelectsScenario reports whether a Ginkgo filter selects the supplied scenario label.
func labelFilterSelectsScenario(filter, label string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	parsed, err := ginkgoTypes.ParseLabelFilter(filter)
	if err != nil {
		return false
	}
	return parsed([]string{label})
}
