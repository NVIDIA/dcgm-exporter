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

package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// devicePluginConfigFlag is the Helm flag prefix that carries the time-slicing
// config file.
const devicePluginConfigFlag = "config.map.default="

// runK8sGroupWithRunner runs one Kubernetes group on a fresh owned-cluster
// run with default options and the given fake runner, and returns its error.
func runK8sGroupWithRunner(t *testing.T, runner *fakeRunner, group scenario.PlanGroup) error {
	t.Helper()

	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	run := newK8sSuiteRun(
		context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	return run.runK8sGroup(&opts, group)
}

// devicePluginConfigFile returns the time-slicing config path a device plugin
// Helm command passes, or "" when it passes none.
func devicePluginConfigFile(command e2eexec.Command) string {
	for _, arg := range command.Args {
		if path, found := strings.CutPrefix(arg, devicePluginConfigFlag); found {
			return path
		}
	}
	return ""
}

// TestRunK8sGroupSetsRenameByDefaultPerGroup checks that only the renamed GPU
// group asks the device plugin to advertise nvidia.com/gpu.shared. The config
// file is read while the Helm command runs, because it is removed afterward.
func TestRunK8sGroupSetsRenameByDefaultPerGroup(t *testing.T) {
	tests := []struct {
		group scenario.PlanGroup
		want  string
	}{
		{group: scenario.PlanGroup{Name: "embedded-shared-gpu", Labels: []string{"sharedGpu"}}, want: "renameByDefault: false"},
		{group: scenario.PlanGroup{Name: "embedded-renamed-gpu", Labels: []string{"customResourceNames"}}, want: "renameByDefault: true"},
	}

	for _, tc := range tests {
		t.Run(tc.group.Name, func(t *testing.T) {
			runner, configs := recordDevicePluginConfigs(t)
			if err := runK8sGroupWithRunner(t, runner, tc.group); err != nil {
				t.Fatalf("runK8sGroup() error = %v", err)
			}

			if len(*configs) != 1 || !strings.Contains((*configs)[0], tc.want) {
				t.Fatalf("device plugin configs = %q, want one containing %q", *configs, tc.want)
			}
		})
	}
}

// recordDevicePluginConfigs returns a fake runner that keeps the content of
// every time-slicing config file passed to Helm, and the list it fills.
func recordDevicePluginConfigs(t *testing.T) (*fakeRunner, *[]string) {
	t.Helper()

	configs := &[]string{}
	runner := &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		path := devicePluginConfigFile(command)
		if path == "" {
			return e2eexec.Result{}, false
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read device plugin config %s: %v", path, err)
		}
		*configs = append(*configs, string(data))
		return e2eexec.Result{}, false
	}}
	return runner, configs
}

// TestRunK8sGroupResetsDevicePluginAfterFailedConfiguration checks that a
// failed device plugin change is followed by the default device plugin, so
// later groups do not run with a half-applied MIG or time-slicing setup.
func TestRunK8sGroupResetsDevicePluginAfterFailedConfiguration(t *testing.T) {
	for _, tc := range []struct {
		group     scenario.PlanGroup
		configArg string
	}{
		{group: scenario.PlanGroup{Name: "embedded-mig", Labels: []string{"mig"}}, configArg: "migStrategy=mixed"},
		{group: scenario.PlanGroup{Name: "embedded-shared-gpu", Labels: []string{"sharedGpu"}}, configArg: devicePluginConfigFlag},
		{group: scenario.PlanGroup{Name: "embedded-renamed-gpu", Labels: []string{"customResourceNames"}}, configArg: devicePluginConfigFlag},
	} {
		t.Run(tc.group.Name, func(t *testing.T) {
			runner, failed := failFirstDevicePluginChange(tc.configArg)
			err := runK8sGroupWithRunner(t, runner, tc.group)
			if err == nil || !strings.Contains(err.Error(), "device plugin install failed") {
				t.Fatalf("runK8sGroup() error = %v, want the device plugin failure", err)
			}

			requireDevicePluginResetAfter(t, runner, *failed, tc.configArg)
		})
	}
}

// failFirstDevicePluginChange returns a fake runner that fails the first
// device plugin install carrying configArg, and the index of that command
// (-1 until it runs).
func failFirstDevicePluginChange(configArg string) (*fakeRunner, *int) {
	failed := -1
	runner := &fakeRunner{}
	runner.resultMatcher = func(command e2eexec.Command) (e2eexec.Result, bool) {
		if failed >= 0 || !isDevicePluginInstall(command) || !commandHasArgPrefix(command, configArg) {
			return e2eexec.Result{}, false
		}

		failed = len(runner.commands) - 1
		return e2eexec.Result{ExitCode: 1, Stderr: []byte("device plugin install failed")}, true
	}
	return runner, &failed
}

// requireDevicePluginResetAfter checks that the failed change at index failed
// was followed by a default device plugin install and that the suite did not run.
func requireDevicePluginResetAfter(t *testing.T, runner *fakeRunner, failed int, configArg string) {
	t.Helper()

	if failed < 0 {
		t.Fatalf("device plugin configuration was not attempted: %#v", runner.commands)
	}
	reset := runner.commandLastIndex("helm", "upgrade", "--install", "nvidia-device-plugin")
	if reset <= failed || commandHasArgPrefix(runner.commands[reset], configArg) {
		t.Fatalf("default device plugin was not restored after the failure at %d: %#v", failed, runner.commands)
	}
	if runner.commandIndex("/tmp/dcgm-exporter-k8s.test") >= 0 {
		t.Fatal("the suite ran after a failed device plugin configuration")
	}
}

// isDevicePluginInstall reports whether the command installs or upgrades the
// NVIDIA device plugin Helm release.
func isDevicePluginInstall(command e2eexec.Command) bool {
	return command.Name == "helm" && containsArgs(command.Args, "upgrade", "--install", "nvidia-device-plugin")
}

// commandHasArgPrefix reports whether any argument starts with prefix.
func commandHasArgPrefix(command e2eexec.Command, prefix string) bool {
	for _, arg := range command.Args {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

// TestSkipExternalOwnedClusterScenarios skips customResourceNames, and only
// it, on an external cluster with a stated reason.
func TestSkipExternalOwnedClusterScenarios(t *testing.T) {
	plan := scenario.Plan{
		Scenarios: []scenario.PlannedScenario{
			{Scenario: scenario.Scenario{Name: "customResourceNames"}, Outcome: scenario.OutcomeSelected},
			{Scenario: scenario.Scenario{Name: "default"}, Outcome: scenario.OutcomeSelected},
		},
		EmbeddedLabels: []string{"customResourceNames", "default"},
		Groups: []scenario.PlanGroup{
			{Name: "embedded-baseline", Labels: []string{"default"}},
			{Name: "embedded-renamed-gpu", Labels: []string{"customResourceNames"}},
		},
	}

	got := skipExternalOwnedClusterScenarios(plan)

	if len(got.Groups) != 1 || got.Groups[0].Name != "embedded-baseline" {
		t.Fatalf("groups = %#v, want only embedded-baseline", got.Groups)
	}
	skipped := got.Scenarios[0]
	if skipped.Outcome != scenario.OutcomeSkipped || !strings.Contains(skipped.Reason, "nvidia.com/gpu.shared") {
		t.Fatalf("customResourceNames = %#v, want skipped with a reason naming nvidia.com/gpu.shared", skipped)
	}
	if got.Scenarios[1].Outcome != scenario.OutcomeSelected {
		t.Fatalf("default = %#v, want selected", got.Scenarios[1])
	}
}
