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

package config_test

import (
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// TestParseFeatureMode checks the accepted spellings and what each mode means
// for scenario selection and setup.
func TestParseFeatureMode(t *testing.T) {
	tests := []struct {
		value        string
		want         config.FeatureMode
		wantEnabled  bool
		wantRequired bool
		wantOwned    bool
		wantExternal bool
	}{
		{value: "false", want: config.FeatureOff},
		{value: " FALSE ", want: config.FeatureOff},
		{value: "auto", want: config.FeatureAuto, wantEnabled: true, wantOwned: true},
		{value: "true", want: config.FeatureOn, wantEnabled: true, wantRequired: true, wantOwned: true, wantExternal: true},
		{value: " TRUE ", want: config.FeatureOn, wantEnabled: true, wantRequired: true, wantOwned: true, wantExternal: true},
	}

	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := config.ParseFeatureMode(test.value)
			if err != nil {
				t.Fatalf("ParseFeatureMode(%q) error = %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("ParseFeatureMode(%q) = %q, want %q", test.value, got, test.want)
			}

			if got.Enabled() != test.wantEnabled || got.Required() != test.wantRequired {
				t.Errorf("%q: Enabled=%t Required=%t, want %t %t", got, got.Enabled(), got.Required(), test.wantEnabled, test.wantRequired)
			}
			if got.SetUpOn(true) != test.wantOwned || got.SetUpOn(false) != test.wantExternal {
				t.Errorf("%q: SetUpOn(owned)=%t SetUpOn(external)=%t, want %t %t", got, got.SetUpOn(true), got.SetUpOn(false), test.wantOwned, test.wantExternal)
			}
		})
	}
}

// TestParseFeatureModeRejectsOtherValues checks that only false, auto and true
// are accepted.
func TestParseFeatureModeRejectsOtherValues(t *testing.T) {
	for _, value := range []string{"", "off", "install"} {
		if got, err := config.ParseFeatureMode(value); err == nil {
			t.Errorf("ParseFeatureMode(%q) = %q, want error", value, got)
		}
	}
}

// TestFeatureModeZeroValueIsOff checks that Enabled, Required and SetUpOn treat
// a mode left unset in code as off. The local k3d cgroup-v1 setup is the one
// caller that treats it as auto instead.
func TestFeatureModeZeroValueIsOff(t *testing.T) {
	var mode config.FeatureMode
	if mode.Enabled() || mode.Required() || mode.SetUpOn(true) {
		t.Fatalf("zero FeatureMode is not off")
	}

	var operator config.GPUOperatorMode
	if operator.Enabled() || operator.InstallOn(true) {
		t.Fatalf("zero GPUOperatorMode is not off")
	}
}

// TestParseGPUOperatorMode checks the accepted --gpu-operator values and where
// each one installs GPU Operator.
func TestParseGPUOperatorMode(t *testing.T) {
	tests := []struct {
		value        string
		want         config.GPUOperatorMode
		wantEnabled  bool
		wantOwned    bool
		wantExternal bool
	}{
		{value: "false", want: config.GPUOperatorOff},
		{value: "auto", want: config.GPUOperatorAuto, wantEnabled: true, wantOwned: true},
		{value: "existing", want: config.GPUOperatorExisting, wantEnabled: true},
		{value: " Install ", want: config.GPUOperatorInstall, wantEnabled: true, wantOwned: true, wantExternal: true},
	}

	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := config.ParseGPUOperatorMode(test.value)
			if err != nil {
				t.Fatalf("ParseGPUOperatorMode(%q) error = %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("ParseGPUOperatorMode(%q) = %q, want %q", test.value, got, test.want)
			}

			if got.Enabled() != test.wantEnabled {
				t.Errorf("%q: Enabled=%t, want %t", got, got.Enabled(), test.wantEnabled)
			}
			if got.InstallOn(true) != test.wantOwned || got.InstallOn(false) != test.wantExternal {
				t.Errorf("%q: InstallOn(owned)=%t InstallOn(external)=%t, want %t %t", got, got.InstallOn(true), got.InstallOn(false), test.wantOwned, test.wantExternal)
			}
		})
	}
}

// TestParseGPUOperatorModeRejectsOtherValues checks that a feature-mode value
// such as true is not a GPU Operator mode.
func TestParseGPUOperatorModeRejectsOtherValues(t *testing.T) {
	for _, value := range []string{"true", ""} {
		if got, err := config.ParseGPUOperatorMode(value); err == nil {
			t.Errorf("ParseGPUOperatorMode(%q) = %q, want error", value, got)
		}
	}
}
