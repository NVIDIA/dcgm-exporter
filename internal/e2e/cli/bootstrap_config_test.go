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
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// TestBootstrapEnvironment verifies the environment setting and its validation.
func TestBootstrapEnvironment(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		want       config.BootstrapMode
		wantSource config.ValueSource
		wantError  string
	}{
		{name: "unset", wantSource: config.ValueDefault},
		{name: "check", value: "check", want: config.BootstrapCheck, wantSource: config.ValueEnvironment},
		{name: "install", value: "install", want: config.BootstrapInstall, wantSource: config.ValueEnvironment},
		{name: "invalid", value: "repair", wantError: "check, install, or skip"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("E2E_BOOTSTRAP", test.value)
			mode, source, err := bootstrapFromEnvironment()
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("bootstrapFromEnvironment() error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode != test.want || source != test.wantSource {
				t.Fatalf("bootstrapFromEnvironment() = %q, %q", mode, source)
			}
		})
	}
}

// TestBootstrapCLIPrecedence verifies CLI values override environment values.
func TestBootstrapCLIPrecedence(t *testing.T) {
	t.Setenv("E2E_BOOTSTRAP", "check")
	var stderr bytes.Buffer
	opts, err := parseTests([]string{"--bootstrap=install"}, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}
	if opts.BootstrapMode != config.BootstrapInstall || opts.BootstrapSource != config.ValueCLI {
		t.Fatalf("parseTests() bootstrap = %#v", opts)
	}
}

// TestBootstrapComponentCLIPrecedence verifies that a command-line dependency mode overrides the environment value.
func TestBootstrapComponentCLIPrecedence(t *testing.T) {
	t.Setenv("E2E_BOOTSTRAP_COMPONENTS", "docker=check,host-dcgm=skip")
	var stderr bytes.Buffer
	opts, err := parseTests([]string{"--bootstrap-component", "docker=install"}, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}
	if opts.BootstrapModeFor(config.BootstrapDocker) != config.BootstrapInstall {
		t.Fatalf("docker bootstrap mode = %q, want install", opts.BootstrapModeFor(config.BootstrapDocker))
	}
	if opts.BootstrapModeFor(config.BootstrapHostDCGM) != config.BootstrapSkip {
		t.Fatalf("host DCGM bootstrap mode = %q, want skip", opts.BootstrapModeFor(config.BootstrapHostDCGM))
	}
}

// TestBootstrapCLIRejectsRemovedAndContradictoryFlags verifies that only the current bootstrap flags work.
func TestBootstrapCLIRejectsRemovedAndContradictoryFlags(t *testing.T) {
	var stderr bytes.Buffer
	_, err := parseTests([]string{"--install-deps"}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("parseTests() error = %v, want removed flag error", err)
	}
	if _, err := parseTests([]string{"--bootstrap=skip", "--bootstrap-only"}, &stderr); err == nil {
		t.Fatal("parseTests() accepted bootstrap-only with skip")
	}
	if _, err := parseTests([]string{"--bootstrap-component", "docker=check", "--bootstrap-component", "docker=install"}, &stderr); err == nil {
		t.Fatal("parseTests() accepted the same component twice")
	}
}

// TestBootstrapArtifactDefaults verifies source check and packaged install defaults.
func TestBootstrapArtifactDefaults(t *testing.T) {
	tests := []struct {
		artifact artifactMode
		want     config.BootstrapMode
	}{
		{artifact: artifactModeSource, want: config.BootstrapCheck},
		{artifact: artifactModePackage, want: config.BootstrapInstall},
	}
	for _, test := range tests {
		opts := config.TestOptions{}
		if err := normalizeBootstrapForArtifact(&opts, test.artifact); err != nil {
			t.Fatal(err)
		}
		if opts.BootstrapMode != test.want || opts.BootstrapSource != config.ValueDefault {
			t.Fatalf("normalizeBootstrapForArtifact(%q) = %#v", test.artifact, opts)
		}
	}
	opts := config.TestOptions{}
	if err := normalizeBootstrapForArtifact(&opts, artifactMode("unknown")); err == nil {
		t.Fatal("normalizeBootstrapForArtifact() accepted an unknown artifact mode")
	}
	opts = config.TestOptions{BootstrapMode: config.BootstrapSkip, BootstrapOnly: true}
	if err := normalizeBootstrapForArtifact(&opts, artifactModeSource); err == nil {
		t.Fatal("normalizeBootstrapForArtifact() accepted bootstrap-only with skip")
	}
	opts = config.TestOptions{
		BootstrapMode: config.BootstrapSkip,
		BootstrapOnly: true,
		BootstrapComponents: map[config.BootstrapComponent]config.BootstrapMode{
			config.BootstrapDocker: config.BootstrapCheck,
		},
	}
	if err := normalizeBootstrapForArtifact(&opts, artifactModeSource); err != nil {
		t.Fatalf("normalizeBootstrapForArtifact() rejected enabled component: %v", err)
	}
}

// TestBootstrapOnlyStopsBeforeSuiteExecution verifies the progressive stop point.
func TestBootstrapOnlyStopsBeforeSuiteExecution(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"check", "install"} {
		t.Run(mode, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runner := &fakeRunner{}

			err := runWithRootRunner([]string{
				"tests", "--suite", "static", "--bootstrap=" + mode, "--bootstrap-only",
			}, &stdout, &stderr, root, runner)
			if err != nil {
				t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
			}
			if runner.hasCommand("dcgm-exporter-static.test") {
				t.Fatalf("bootstrap-only launched a suite binary: %#v", runner.commands)
			}
			if strings.Contains(stdout.String(), "&&&&") {
				t.Fatalf("bootstrap-only emitted a result marker:\n%s", stdout.String())
			}
			if !strings.Contains(stdout.String(), "[e2e] bootstrap: "+mode+" (cli)") {
				t.Fatalf("bootstrap value source missing:\n%s", stdout.String())
			}
		})
	}
}
