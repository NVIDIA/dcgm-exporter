//go:build e2e

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
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

type deploymentProfile string

const (
	dedicatedDeployment         deploymentProfile = "dedicated"
	hardwareBasicMetricsProfile deploymentProfile = "hardware-basic-metrics"
)

var scenarioDeploymentProfiles = map[string]deploymentProfile{
	"default":                      dedicatedDeployment,
	"configMatrix":                 dedicatedDeployment,
	"configMapData":                dedicatedDeployment,
	"yamlConfig":                   dedicatedDeployment,
	"oldNamespace":                 dedicatedDeployment,
	"clockEventsCounters":          dedicatedDeployment,
	"tls":                          dedicatedDeployment,
	"basicAuth":                    dedicatedDeployment,
	"tlsBasicAuth":                 dedicatedDeployment,
	"serviceAccess":                dedicatedDeployment,
	"podLabels":                    dedicatedDeployment,
	"podUID":                       dedicatedDeployment,
	"podLabelAllowlist":            dedicatedDeployment,
	"kubernetesGpuId":              dedicatedDeployment,
	"noHostname":                   dedicatedDeployment,
	"modelName":                    dedicatedDeployment,
	"pprof":                        dedicatedDeployment,
	"debugDump":                    dedicatedDeployment,
	"hpcJobMapping":                dedicatedDeployment,
	"customResourceNames":          dedicatedDeployment,
	"profiling":                    hardwareBasicMetricsProfile,
	"p2pStatus":                    hardwareBasicMetricsProfile,
	"fieldUnsupported":             hardwareBasicMetricsProfile,
	"nvlink":                       hardwareBasicMetricsProfile,
	"mig":                          dedicatedDeployment,
	"migFullGpuSelection":          dedicatedDeployment,
	"migInstanceSelection":         dedicatedDeployment,
	"migSpecificInstanceSelection": dedicatedDeployment,
	"migCombinedDeviceSelection":   dedicatedDeployment,
	"dra":                          dedicatedDeployment,
	"sharedGpu":                    dedicatedDeployment,
	"nvswitch":                     dedicatedDeployment,
	"graceCpu":                     dedicatedDeployment,
	"c2c":                          dedicatedDeployment,
	"remoteDcgm":                   dedicatedDeployment,
	"remoteDcgmRestart":            dedicatedDeployment,
	"failureXid":                   dedicatedDeployment,
	"failureXidCounters":           dedicatedDeployment,
	"failureGpuHealth":             dedicatedDeployment,
	"failureNvlinkHealth":          dedicatedDeployment,
	"gpuOperatorChart":             dedicatedDeployment,
	"gpuOperatorExporter":          dedicatedDeployment,
	"gpuOperatorSharedGpu":         dedicatedDeployment,
	"gpuOperatorMig":               dedicatedDeployment,
	"gpuOperatorDRA":               dedicatedDeployment,
	"gpuOperatorIPv6":              dedicatedDeployment,
}

func TestScenarioDeploymentProfiles(t *testing.T) {
	for _, entry := range scenario.Catalog {
		if entry.Suite != scenario.SuiteK8s {
			continue
		}
		if _, ok := scenarioDeploymentProfiles[entry.Name]; !ok {
			t.Fatalf("k8s scenario %q has no deployment profile", entry.Name)
		}
	}

	wantShared := map[string]struct{}{
		"profiling":        {},
		"nvlink":           {},
		"p2pStatus":        {},
		"fieldUnsupported": {},
	}
	for name, profile := range scenarioDeploymentProfiles {
		_, shouldShare := wantShared[name]
		if shouldShare && profile != hardwareBasicMetricsProfile {
			t.Fatalf("scenario %q profile = %q, want %q", name, profile, hardwareBasicMetricsProfile)
		}
		if !shouldShare && profile != dedicatedDeployment {
			t.Fatalf("scenario %q profile = %q, want %q", name, profile, dedicatedDeployment)
		}
	}
}

func TestHardwareBasicMetricsDeploymentCachesSetupFailure(t *testing.T) {
	setupErr := errors.New("setup failed")
	calls := 0
	deployment := newHardwareBasicMetricsDeployment(func(context.Context) (*corev1.Pod, error) {
		calls++
		return nil, setupErr
	})

	for _, label := range []string{"profiling", "nvlink", "p2pStatus", "fieldUnsupported"} {
		_, err := deployment.podFor(context.Background())
		if !errors.Is(err, setupErr) {
			t.Fatalf("%s setup error = %v, want %v", label, err, setupErr)
		}
	}
	if calls != 1 {
		t.Fatalf("setup calls = %d, want 1", calls)
	}
}

func TestHardwareBasicMetricsRowsIncludeUnsupportedFieldOnlyWhenSelected(t *testing.T) {
	oldField := testContext.unsupportedField
	defer func() { testContext.unsupportedField = oldField }()

	testContext.unsupportedField = "DCGM_FI_DEV_TEST_UNSUPPORTED"
	rows := hardwareBasicMetricsRowsFor(true)
	if !containsRow(rows, "DCGM_FI_DEV_TEST_UNSUPPORTED") {
		t.Fatalf("selected fieldUnsupported rows should include unsupported candidate: %#v", rows)
	}

	rows = hardwareBasicMetricsRowsFor(false)
	if containsRow(rows, "DCGM_FI_DEV_TEST_UNSUPPORTED") {
		t.Fatalf("unselected fieldUnsupported rows should not include unsupported candidate: %#v", rows)
	}
}

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

func containsRow(rows []string, needle string) bool {
	for _, row := range rows {
		if strings.Contains(row, needle) {
			return true
		}
	}
	return false
}
