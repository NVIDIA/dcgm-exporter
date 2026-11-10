/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// TestTestOptionsFromEnvParsesModes verifies mode variables are normalized into typed values.
func TestTestOptionsFromEnvParsesModes(t *testing.T) {
	t.Setenv("E2E_K3D_CGROUP_V1", " TRUE ")
	t.Setenv("E2E_SHARED_GPU_CONFIGURE", "Auto")
	t.Setenv("E2E_DRA_CONFIGURE", "FALSE")
	t.Setenv("E2E_GPU_OPERATOR", " Install")

	opts, err := testOptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if opts.K3dCgroupV1 != config.FeatureOn || opts.SharedGPUConfigure != config.FeatureAuto ||
		opts.DRAConfigure != config.FeatureOff || opts.GPUOperator != config.GPUOperatorInstall {
		t.Fatalf("modes = %q %q %q %q", opts.K3dCgroupV1, opts.SharedGPUConfigure, opts.DRAConfigure, opts.GPUOperator)
	}
}

// TestInvalidModeEnvironmentLosesToAFlag verifies a bad mode variable does not stop a
// run whose flag supplies a valid value, and is reported when no flag replaces it.
func TestInvalidModeEnvironmentLosesToAFlag(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want string
		set  func(*featureModeFlags, string)
		get  func(config.TestOptions) string
	}{
		{env: "E2E_K3D_CGROUP_V1", want: "--k3d-cgroup-v1 must be auto, true, or false", set: func(m *featureModeFlags, v string) { m.k3dCgroupV1 = v }, get: func(o config.TestOptions) string { return string(o.K3dCgroupV1) }},
		{env: "E2E_SHARED_GPU_CONFIGURE", want: "--shared-gpu-configure must be auto, true, or false", set: func(m *featureModeFlags, v string) { m.sharedGPUConfigure = v }, get: func(o config.TestOptions) string { return string(o.SharedGPUConfigure) }},
		{env: "E2E_DRA_CONFIGURE", want: "--dra-configure must be auto, true, or false", set: func(m *featureModeFlags, v string) { m.draConfigure = v }, get: func(o config.TestOptions) string { return string(o.DRAConfigure) }},
		{env: "E2E_GPU_OPERATOR", want: "--gpu-operator must be auto, existing, install, or false", set: func(m *featureModeFlags, v string) { m.gpuOperator = v }, get: func(o config.TestOptions) string { return string(o.GPUOperator) }},
	} {
		t.Run(tc.env, func(t *testing.T) {
			for _, name := range modeVariables {
				t.Setenv(name, "auto")
			}
			t.Setenv(tc.env, "sometimes")

			checkInvalidModeLosesToAFlag(t, tc.want, tc.set, tc.get)
		})
	}
}

// checkInvalidModeLosesToAFlag checks, with one mode variable already set to a
// bad value, that the run fails with want when no flag replaces it, and that a
// flag of "false" set through set replaces it and is read back through get.
func checkInvalidModeLosesToAFlag(t *testing.T, want string, set func(*featureModeFlags, string), get func(config.TestOptions) string) {
	t.Helper()

	opts, err := testOptionsFromEnv()
	if err != nil {
		t.Fatalf("testOptionsFromEnv() error = %v, want none", err)
	}

	fromEnv := featureModeFlags{
		k3dCgroupV1:        string(opts.K3dCgroupV1),
		sharedGPUConfigure: string(opts.SharedGPUConfigure),
		draConfigure:       string(opts.DRAConfigure),
		gpuOperator:        string(opts.GPUOperator),
	}
	if err := applyFeatureModeFlags(&opts, fromEnv); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("no flag: error = %v, want %q", err, want)
	}

	withFlag := fromEnv
	set(&withFlag, "false")
	if err := applyFeatureModeFlags(&opts, withFlag); err != nil {
		t.Fatalf("valid flag: error = %v, want none", err)
	}
	if got := get(opts); got != "false" {
		t.Fatalf("valid flag: mode = %q, want the flag's %q", got, "false")
	}
}

// modeVariables lists the environment variables that hold feature modes.
var modeVariables = []string{"E2E_K3D_CGROUP_V1", "E2E_SHARED_GPU_CONFIGURE", "E2E_DRA_CONFIGURE", "E2E_GPU_OPERATOR"}

// TestEmptyModeKeepsTheDefault verifies a mode variable set to nothing or only
// spaces, which the CLI library passes on as the flag value, keeps each mode's
// default instead of failing the run or turning the feature off.
func TestEmptyModeKeepsTheDefault(t *testing.T) {
	for _, value := range []string{"", "  "} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			checkBlankModesKeepDefaults(t, value)
		})
	}
}

// checkBlankModesKeepDefaults sets every mode variable to value and checks the
// parsed options hold the documented defaults: cgroup v1 auto, shared GPU and
// DRA setup true, and GPU Operator install.
func checkBlankModesKeepDefaults(t *testing.T, value string) {
	t.Helper()

	for _, name := range modeVariables {
		t.Setenv(name, value)
	}
	opts, err := testOptionsFromEnv()
	if err != nil {
		t.Fatalf("testOptionsFromEnv() error = %v, want none", err)
	}

	blank := featureModeFlags{k3dCgroupV1: value, sharedGPUConfigure: value, draConfigure: value, gpuOperator: value}
	if err := applyFeatureModeFlags(&opts, blank); err != nil {
		t.Fatalf("applyFeatureModeFlags(empty) error = %v, want none", err)
	}
	if opts.K3dCgroupV1 != config.FeatureAuto || opts.SharedGPUConfigure != config.FeatureOn ||
		opts.DRAConfigure != config.FeatureOn || opts.GPUOperator != config.GPUOperatorInstall {
		t.Fatalf("modes = %q %q %q %q, want defaults auto true true install",
			opts.K3dCgroupV1, opts.SharedGPUConfigure, opts.DRAConfigure, opts.GPUOperator)
	}
}

// TestInvalidModeEnvironmentIsReportedWithABlankFlag verifies a blank flag value does
// not hide an invalid mode variable: the error names the flag.
func TestInvalidModeEnvironmentIsReportedWithABlankFlag(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want string
	}{
		{env: "E2E_K3D_CGROUP_V1", want: "--k3d-cgroup-v1 must be auto, true, or false"},
		{env: "E2E_SHARED_GPU_CONFIGURE", want: "--shared-gpu-configure must be auto, true, or false"},
		{env: "E2E_DRA_CONFIGURE", want: "--dra-configure must be auto, true, or false"},
		{env: "E2E_GPU_OPERATOR", want: "--gpu-operator must be auto, existing, install, or false"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			for _, name := range modeVariables {
				t.Setenv(name, "auto")
			}
			t.Setenv(tc.env, "sometimes")

			opts, err := testOptionsFromEnv()
			if err != nil {
				t.Fatalf("testOptionsFromEnv() error = %v, want none", err)
			}

			if err := applyFeatureModeFlags(&opts, featureModeFlags{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("blank flag: error = %v, want %q", err, tc.want)
			}
		})
	}
}
