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
	"slices"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
)

// createGinkgoConfig keeps scenario containers contiguous by disabling spec randomization.
func createGinkgoConfig() (types.SuiteConfig, types.ReporterConfig) {
	suiteConfig, reporterConfig := ginkgo.GinkgoConfiguration()
	suiteConfig.RandomizeAllSpecs = false
	return suiteConfig, reporterConfig
}

// appendNvidiaVisibleDevicesHelmValues adds GPU Operator env overrides when a value is supplied.
func appendNvidiaVisibleDevicesHelmValues(values []string, nvidiaVisibleDevices string) []string {
	if nvidiaVisibleDevices == "" {
		return values
	}
	return append(values,
		"extraEnv[0].name=NVIDIA_VISIBLE_DEVICES",
		"extraEnv[0].value="+nvidiaVisibleDevices)
}

// TestCreateGinkgoConfigKeepsScenarioContainersContiguous prevents Helm releases from interleaving.
func TestCreateGinkgoConfigKeepsScenarioContainersContiguous(t *testing.T) {
	suiteConfig, _ := createGinkgoConfig()

	if suiteConfig.RandomizeAllSpecs {
		t.Fatal("RandomizeAllSpecs must stay disabled so Helm-backed scenario containers do not interleave")
	}
}

// TestAppendNvidiaVisibleDevicesHelmValues checks the override arguments and the empty-value no-op.
func TestAppendNvidiaVisibleDevicesHelmValues(t *testing.T) {
	values := appendNvidiaVisibleDevicesHelmValues(nil, "nvidia.com/gpu=all")
	for _, want := range []string{
		"extraEnv[0].name=NVIDIA_VISIBLE_DEVICES",
		"extraEnv[0].value=nvidia.com/gpu=all",
	} {
		if !slices.Contains(values, want) {
			t.Errorf("appendNvidiaVisibleDevicesHelmValues() missing %q: %v", want, values)
		}
	}

	base := []string{"image.pullPolicy=IfNotPresent"}
	if got := appendNvidiaVisibleDevicesHelmValues(base, ""); !slices.Equal(got, base) {
		t.Fatalf("empty device values = %v, want %v", got, base)
	}
}
