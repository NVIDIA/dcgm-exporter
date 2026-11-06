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
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// TestParseBootstrapComponents verifies that valid dependency mode settings are parsed.
func TestParseBootstrapComponents(t *testing.T) {
	components, err := config.ParseBootstrapComponents([]string{"docker=install", "host-dcgm=skip"})
	if err != nil {
		t.Fatal(err)
	}
	if components[config.BootstrapDocker] != config.BootstrapInstall || components[config.BootstrapHostDCGM] != config.BootstrapSkip {
		t.Fatalf("ParseBootstrapComponents() = %#v", components)
	}
}

// TestParseBootstrapComponentsRejectsBadValues verifies that malformed, unknown, and duplicate settings are rejected.
func TestParseBootstrapComponentsRejectsBadValues(t *testing.T) {
	for _, value := range []string{"docker", "unknown=install", "docker=repair", "docker=install,docker=check"} {
		values := strings.Split(value, ",")
		if _, err := config.ParseBootstrapComponents(values); err == nil {
			t.Fatalf("ParseBootstrapComponents(%q) succeeded", value)
		}
	}
}

// TestBootstrapModeForUsesNamedOverride verifies that a named mode takes precedence over the default mode.
func TestBootstrapModeForUsesNamedOverride(t *testing.T) {
	opts := config.TestOptions{
		BootstrapMode: config.BootstrapCheck,
		BootstrapComponents: map[config.BootstrapComponent]config.BootstrapMode{
			config.BootstrapDocker: config.BootstrapInstall,
		},
	}
	if got := opts.BootstrapModeFor(config.BootstrapDocker); got != config.BootstrapInstall {
		t.Fatalf("BootstrapModeFor(docker) = %q, want install", got)
	}
	if got := opts.BootstrapModeFor(config.BootstrapHostDCGM); got != config.BootstrapCheck {
		t.Fatalf("BootstrapModeFor(host-dcgm) = %q, want check", got)
	}
}
