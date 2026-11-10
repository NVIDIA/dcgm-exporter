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
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/suite"
)

// fieldIDFixture is a go-dcgm const_fields.go excerpt with a current table,
// a legacy table, and a legacy name that a current name overrides.
const fieldIDFixture = `package dcgm

const DCGM_FI_DEV_GPU_TEMP Short = 150

var dcgmFields = map[string]Short{
	"DCGM_FI_DEV_GPU_TEMP_CELSIUS": 150,
	"DCGM_FI_DEV_ROW_REMAP_FAILED": 395,
	"DCGM_FI_SHARED":               1,
}

var legacyDCGMFields = map[string]Short{
	"DCGM_FI_DEV_GPU_TEMP": 150,
	"DCGM_FI_SHARED":       2,
}
`

// fieldSupportCSVFixture is a metrics CSV with two DCGM rows, an
// exporter-owned row, and a disabled row.
const fieldSupportCSVFixture = `# DCGM FIELD, Prometheus metric type, help message
DCGM_FI_DEV_GPU_TEMP_CELSIUS, gauge, GPU temperature (in C).
DCGM_FI_DEV_ROW_REMAP_FAILED, gauge, Whether remapping of rows has failed
DCGM_EXP_XID_ERRORS_COUNT, gauge, XID errors seen in the window.
# DCGM_FI_DEV_BOARD_POWER_WATTS, gauge, Power draw (in W).
`

// writeFieldSupportRoot writes the CSV and the go-dcgm field table where the
// CLI looks for them under root.
func writeFieldSupportRoot(t *testing.T, root string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc", "default-counters.csv"), []byte(fieldSupportCSVFixture), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc", "go-dcgm-const-fields.go"), []byte(fieldIDFixture), 0o600))
}

// dmonFieldSupportResult answers dcgmi dmon, run on the host or in the DCGM
// image, with a temperature column that has values and a row-remap column
// that is N/A on every GPU.
func dmonFieldSupportResult(command e2eexec.Command) (e2eexec.Result, bool) {
	if !hasArg(command.Args, "dmon") {
		return e2eexec.Result{}, false
	}
	return e2eexec.Result{Stdout: []byte("#Entity TMPTR RRFAIL\nID\nGPU 0   35    N/A\nGPU 1   36    N/A\n")}, true
}

// imageVersionResult answers the DCGM image's nv-hostengine --version with
// probeVersion and docker image inspect of the DCGM version label with
// labels[image]; an image not in labels is not in the local store. dcgmi
// dmon answers as in dmonFieldSupportResult.
func imageVersionResult(probeVersion string, labels map[string]string) func(e2eexec.Command) (e2eexec.Result, bool) {
	return func(command e2eexec.Command) (e2eexec.Result, bool) {
		switch {
		case command.Name == "docker" && hasArg(command.Args, "/usr/bin/nv-hostengine"):
			return e2eexec.Result{Stdout: []byte("version: " + probeVersion + "\n")}, true
		case command.Name == "docker" && hasArg(command.Args, "inspect") && hasArg(command.Args, "--format"):
			image := command.Args[len(command.Args)-1]
			label, ok := labels[image]
			if !ok {
				return e2eexec.Result{ExitCode: 1, Stderr: []byte("Error: No such image: " + image)}, true
			}
			return e2eexec.Result{Stdout: []byte(label + "\n")}, true
		}
		return dmonFieldSupportResult(command)
	}
}

// TestWantExporterDCGMVersion checks reading the DCGM version label of the
// exporter image a run uses.
func TestWantExporterDCGMVersion(t *testing.T) {
	labels := map[string]string{"exporter:a": "4.7.0", "exporter:b": "4.7.0-1", "exporter:bare": "<no value>"}
	tests := []struct {
		name    string
		image   string
		want    string
		wantErr string
	}{
		{name: "one image", image: "exporter:a", want: "4.7.0"},
		{name: "same version in a longer label", image: "exporter:b", want: "4.7.0"},
		{name: "image not local", image: "exporter:missing", wantErr: "could not inspect exporter image exporter:missing"},
		{name: "label missing", image: "exporter:bare", wantErr: "exporter image exporter:bare has no com.nvidia.dcgm.version label"},
		{name: "no image", image: "", wantErr: "no exporter image is configured"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{resultMatcher: imageVersionResult("4.7.0", labels)}

			got, err := wantExporterDCGMVersion(io.Discard, runner, config.TestOptions{}, test.image)(context.Background())

			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

// TestK8sFieldSupportRequiresTheExporterImageDCGMVersion checks that an
// owned cluster gets evidence only when the exporter image has the probe's
// DCGM version, and that otherwise the reason is printed and no probe runs.
func TestK8sFieldSupportRequiresTheExporterImageDCGMVersion(t *testing.T) {
	tests := []struct {
		name       string
		labels     map[string]string
		wantOutput string
	}{
		{name: "match", labels: map[string]string{"registry.example/dcgm-exporter:dev": "4.7.0"}},
		{
			name:       "mismatch",
			labels:     map[string]string{"registry.example/dcgm-exporter:dev": "4.6.0"},
			wantOutput: "not probed: the probe's DCGM 4.7.0 differs from the exporter's DCGM 4.6.0",
		},
		{
			name:       "unreadable",
			labels:     map[string]string{},
			wantOutput: "not probed: the exporter's DCGM version is unknown (could not inspect exporter image",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(fieldSupportVar, "")
			root := t.TempDir()
			writeFieldSupportRoot(t, root)
			runner := &fakeRunner{resultMatcher: imageVersionResult("4.7.0", test.labels)}
			var stdout bytes.Buffer
			opts := config.TestOptions{DCGMImage: "registry.example/dcgm:4.7.0", ExporterImage: "registry.example/dcgm-exporter:dev"}
			run := newK8sSuiteRun(
				context.Background(), &stdout, root, runner, config.Config{TestOptions: opts},
				"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
			)

			env := run.fieldSupportEnv(k8sProbeInputs{})

			if test.wantOutput == "" {
				require.NotEmpty(t, env)
				return
			}
			require.Empty(t, env)
			require.False(t, ranDmon(runner))
			require.Contains(t, stdout.String(), test.wantOutput)
			require.Contains(t, stdout.String(), "missing CSV rows are reported as unqualified skips")
		})
	}
}

// fieldSupportEvidence returns the E2E_DCGM_FIELD_SUPPORT entry of env, or "".
func fieldSupportEvidence(env []string) string {
	for _, entry := range env {
		if strings.HasPrefix(entry, fieldSupportVar+"=") {
			return entry
		}
	}
	return ""
}

// ranDmon reports whether the runner ran dcgmi dmon, on the host or in a container.
func ranDmon(runner *fakeRunner) bool {
	for _, command := range runner.commands {
		if hasArg(command.Args, "dmon") {
			return true
		}
	}
	return false
}

// requireDmonInImage requires a docker command that runs dcgmi dmon in image.
func requireDmonInImage(t *testing.T, runner *fakeRunner, image string) {
	t.Helper()

	for _, command := range runner.commands {
		if command.Name == "docker" && hasArg(command.Args, image) && hasArg(command.Args, "dmon") {
			return
		}
	}
	t.Fatalf("no docker command ran dcgmi dmon in %s; commands = %#v", image, runner.commands)
}

// catalogScenarios returns the catalog entries for the given selectors.
func catalogScenarios(t *testing.T, selectors ...string) []scenario.Scenario {
	t.Helper()

	var entries []scenario.Scenario
	for _, selector := range selectors {
		found := false
		for _, entry := range scenario.Catalog {
			if entry.Selector() == selector {
				entries = append(entries, entry)
				found = true
			}
		}
		require.True(t, found, selector)
	}
	return entries
}

// TestDCGMFieldIDs checks that the go-dcgm tables are read with the lookup
// order of dcgm.GetFieldID.
func TestDCGMFieldIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "const_fields.go")
	require.NoError(t, os.WriteFile(path, []byte(fieldIDFixture), 0o600))

	ids, err := dcgmFieldIDs(path)

	require.NoError(t, err)
	require.Equal(t, map[string]int{
		"DCGM_FI_DEV_GPU_TEMP_CELSIUS": 150,
		"DCGM_FI_DEV_ROW_REMAP_FAILED": 395,
		"DCGM_FI_DEV_GPU_TEMP":         150,
		"DCGM_FI_SHARED":               1,
	}, ids)
}

// TestDCGMFieldIDsRejectsUnexpectedTables checks that a field table the CLI
// cannot read is an error instead of an empty mapping.
func TestDCGMFieldIDsRejectsUnexpectedTables(t *testing.T) {
	tests := map[string]string{
		"no current table": "package dcgm\n\nvar legacyDCGMFields = map[string]Short{\"A\": 1}\n",
		"computed ID":      "package dcgm\n\nvar dcgmFields = map[string]Short{\"A\": B}\n",
		"not a literal":    "package dcgm\n\nvar dcgmFields = newFields()\n",
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "const_fields.go")
			require.NoError(t, os.WriteFile(path, []byte(source), 0o600))

			_, err := dcgmFieldIDs(path)

			require.Error(t, err)
		})
	}
}

// TestFieldSupportEnvClassifiesCSVRows checks the document produced for the
// suites from a CSV and the rows printed as skips.
func TestFieldSupportEnvClassifiesCSVRows(t *testing.T) {
	t.Setenv(fieldSupportVar, "")
	root := t.TempDir()
	writeFieldSupportRoot(t, root)
	runner := &fakeRunner{resultMatcher: dmonFieldSupportResult}
	var stdout bytes.Buffer

	env := fieldSupportRun{stdout: &stdout, root: root, runner: runner}.env(
		context.Background(), capability.ProbeOptions{HostDCGMAddress: "localhost"})

	require.Equal(t, []string{
		`E2E_DCGM_FIELD_SUPPORT={"supported":["DCGM_FI_DEV_GPU_TEMP_CELSIUS"],"unsupported":` +
			`{"DCGM_FI_DEV_ROW_REMAP_FAILED":"DCGM returned no value (N/A) on every GPU during the probe (dcgmi dmon -e 150,395, field 395, 3 samples, 2 GPU sample lines)"}}`,
	}, env)
	requireOutput(t, stdout.String(),
		"[e2e] 1 supported, 1 unsupported, 1 unclassified",
		"[e2e] unclassified DCGM_EXP_XID_ERRORS_COUNT: not a DCGM field name, so it was not probed",
	)
}

// TestFieldSupportEnvKeepsTheCallersEvidence checks that evidence already in
// the environment is used as given, without a probe.
func TestFieldSupportEnvKeepsTheCallersEvidence(t *testing.T) {
	t.Setenv(fieldSupportVar, `{"supported":["DCGM_FI_DEV_GPU_TEMP_CELSIUS"]}`)
	runner := &fakeRunner{}
	var stdout bytes.Buffer

	env := fieldSupportRun{stdout: &stdout, root: t.TempDir(), runner: runner}.env(
		context.Background(), capability.ProbeOptions{HostDCGMAddress: "localhost"})

	require.Empty(t, env)
	require.Empty(t, runner.commands)
	require.Contains(t, stdout.String(), "using E2E_DCGM_FIELD_SUPPORT from the environment")
}

// TestFieldSupportEnvWithoutClassifiedRows checks that a probe that cannot
// run sets nothing, so the suites report missing rows as unqualified skips.
func TestFieldSupportEnvWithoutClassifiedRows(t *testing.T) {
	t.Setenv(fieldSupportVar, "")
	root := t.TempDir()
	writeFieldSupportRoot(t, root)
	runner := &fakeRunner{results: map[string]e2eexec.Result{"dcgmi": {ExitCode: 1, Stderr: []byte("Error: connection refused")}}}
	var stdout bytes.Buffer

	env := fieldSupportRun{stdout: &stdout, root: root, runner: runner}.env(
		context.Background(), capability.ProbeOptions{HostDCGMAddress: "localhost"})

	require.Empty(t, env)
	requireOutput(t, stdout.String(),
		"[e2e] 0 supported, 0 unsupported, 3 unclassified",
		"[e2e] no row was classified; missing CSV rows are reported as unqualified skips",
	)
}

// TestRunHostSuitePassesFieldSupportToHostGPUGroups checks that the CLI
// probes field support for a host scenario that reads it and passes the
// evidence to the host test binary.
func TestRunHostSuitePassesFieldSupportToHostGPUGroups(t *testing.T) {
	t.Setenv(fieldSupportVar, "")
	root := t.TempDir()
	writeFieldSupportRoot(t, root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tests", "host", "testdata"), 0o755))
	configurePackageTestEnvironment(t)
	for _, name := range []string{"dcgm-exporter-host.test", "dcgm-exporter"} {
		require.NoError(t, writeExecutableFile(filepath.Join(root, "bin", name)))
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{resultMatcher: dmonFieldSupportResult}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "host", "--scenario", "host/startupMetrics", "--bootstrap=skip"},
		&stdout, &stderr, root, runner,
	)
	require.NoError(t, err, "stderr = %s", stderr.String())

	hostCommand, ok := runner.commandNamedSuffix("dcgm-exporter-host.test")
	require.True(t, ok, "host binary was not run; commands = %#v", runner.commands)
	require.Contains(t, fieldSupportEvidence(hostCommand.Env), `"supported":["DCGM_FI_DEV_GPU_TEMP_CELSIUS"]`)
}

// writeHostSuiteRoot writes the binaries, CSV and field table a host suite run
// needs under root.
func writeHostSuiteRoot(t *testing.T, root string) {
	t.Helper()

	writeFieldSupportRoot(t, root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tests", "host", "testdata"), 0o755))
	configurePackageTestEnvironment(t)
	for _, name := range []string{"dcgm-exporter-host.test", "dcgm-exporter", "e2e-dcgm-probe"} {
		require.NoError(t, writeExecutableFile(filepath.Join(root, "bin", name)))
	}
}

// TestRunHostSuiteGivesFieldSupportOnlyToHostGPUGroups checks that the
// config-injection group, which may run on injected GPUs, gets no evidence
// while the baseline group in the same run does.
func TestRunHostSuiteGivesFieldSupportOnlyToHostGPUGroups(t *testing.T) {
	t.Setenv(fieldSupportVar, "")
	root := t.TempDir()
	writeHostSuiteRoot(t, root)

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{resultMatcher: dmonFieldSupportResult}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "host", "--scenario", "host/startupMetrics", "--scenario", "host/configFile", "--bootstrap=skip"},
		&stdout, &stderr, root, runner,
	)
	require.NoError(t, err, "stderr = %s", stderr.String())

	seen := map[string]string{}
	for _, command := range runner.commands {
		if strings.HasSuffix(command.Name, "dcgm-exporter-host.test") {
			for _, arg := range command.Args {
				if strings.HasPrefix(arg, "--ginkgo.label-filter=") {
					seen[strings.TrimPrefix(arg, "--ginkgo.label-filter=")] = fieldSupportEvidence(command.Env)
				}
			}
		}
	}
	require.NotEmpty(t, seen["startupMetrics"])
	require.Contains(t, seen, "configFile")
	require.Empty(t, seen["configFile"])
}

// TestRunHostSuiteRunsNoProbeForDCGMFreeScenarios checks that a host run of
// scenarios that never start DCGM does not probe field support.
func TestRunHostSuiteRunsNoProbeForDCGMFreeScenarios(t *testing.T) {
	t.Setenv(fieldSupportVar, "")
	root := t.TempDir()
	writeHostSuiteRoot(t, root)

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{resultMatcher: dmonFieldSupportResult}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "host", "--scenario", "host/configStartupErrors", "--bootstrap=skip"},
		&stdout, &stderr, root, runner,
	)
	require.NoError(t, err, "stderr = %s", stderr.String())

	require.False(t, ranDmon(runner), "commands = %#v", runner.commands)
	require.NotContains(t, stdout.String(), "DCGM field support")
}

// hostFallbackResult answers a host without a reachable hostengine: dcgmi
// discovery fails, dcgmi reports hostVersion, and the DCGM image reports
// imageVersion. dcgmi dmon answers as in dmonFieldSupportResult.
func hostFallbackResult(hostVersion, imageVersion string) func(e2eexec.Command) (e2eexec.Result, bool) {
	return func(command e2eexec.Command) (e2eexec.Result, bool) {
		switch {
		case command.Name == "dcgmi" && hasArg(command.Args, "discovery"):
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("Error: unable to connect to host engine")}, true
		case command.Name == "dcgmi" && hasArg(command.Args, "--version"):
			return e2eexec.Result{Stdout: []byte("dcgmi  version: " + hostVersion + "\n")}, true
		case command.Name == "docker" && hasArg(command.Args, "/usr/bin/nv-hostengine"):
			return e2eexec.Result{Stdout: []byte("version: " + imageVersion + "\n")}, true
		}
		return dmonFieldSupportResult(command)
	}
}

// TestRunHostSuiteFallsBackToTheDCGMImage checks that without a reachable
// host hostengine the DCGM image is probed, and that its evidence is used only
// when the image has the host's DCGM version.
func TestRunHostSuiteFallsBackToTheDCGMImage(t *testing.T) {
	tests := []struct {
		name         string
		imageVersion string
		wantEvidence bool
		wantOutput   string
	}{
		{name: "same version", imageVersion: "4.7.0", wantEvidence: true, wantOutput: "[e2e] probe DCGM 4.7.0 (DCGM image"},
		{name: "other version", imageVersion: "4.6.0", wantOutput: "the probe's DCGM 4.6.0 differs from the exporter's DCGM 4.7.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(fieldSupportVar, "")
			root := t.TempDir()
			writeHostSuiteRoot(t, root)

			var stdout, stderr bytes.Buffer
			runner := &fakeRunner{resultMatcher: hostFallbackResult("4.7.0", test.imageVersion)}
			err := runWithRootRunner(
				[]string{"tests", "--suite", "host", "--scenario", "host/startupMetrics", "--bootstrap=skip"},
				&stdout, &stderr, root, runner,
			)
			require.NoError(t, err, "stderr = %s", stderr.String())

			hostCommand, ok := runner.commandNamedSuffix("dcgm-exporter-host.test")
			require.True(t, ok)
			require.Equal(t, test.wantEvidence, fieldSupportEvidence(hostCommand.Env) != "")
			require.Equal(t, test.wantEvidence, ranDmon(runner))
			require.Contains(t, stdout.String(), test.wantOutput)
		})
	}
}

// TestRunContainerSuitePassesFieldSupport checks that the container suite
// probes the DCGM image and passes the evidence to the container test binary.
func TestRunContainerSuitePassesFieldSupport(t *testing.T) {
	t.Setenv(fieldSupportVar, "")
	root := t.TempDir()
	writeFieldSupportRoot(t, root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o755))
	require.NoError(t, writeExecutableFile(filepath.Join(root, "bin", "dcgm-exporter-container.test")))
	t.Setenv("E2E_EXPORTER_IMAGE", "registry.example/dcgm-exporter:dev-distroless-amd64@"+testImageDigest)
	t.Setenv("E2E_CONTAINER_TOOLKIT_TEST_IMAGE", "registry.example/cuda:dev")
	t.Setenv("NVIDIA_CONTAINER_TOOLKIT_VERSION", "1.19.1-1")

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{resultMatcher: imageVersionResult("4.7.0", map[string]string{
		"registry.example/dcgm-exporter@" + testImageDigest: "4.7.0",
	})}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "container", "--scenario", "container/imageStartup", "--dcgm-image", "registry.example/dcgm:4.7.0"},
		&stdout, &stderr, root, runner,
	)
	require.NoError(t, err, "stderr = %s", stderr.String())

	containerCommand, ok := runner.commandNamedSuffix("dcgm-exporter-container.test")
	require.True(t, ok, "container binary was not run; commands = %#v", runner.commands)
	require.Contains(t, fieldSupportEvidence(containerCommand.Env), `"supported":["DCGM_FI_DEV_GPU_TEMP_CELSIUS"]`)
	requireDmonInImage(t, runner, "registry.example/dcgm:4.7.0")
}

// TestRunK8sGroupPassesFieldSupportForOwnedClusters checks that an owned
// local cluster's evidence comes from the DCGM image when the capability
// probes have no backend of their own, and reaches the group command.
func TestRunK8sGroupPassesFieldSupportForOwnedClusters(t *testing.T) {
	t.Setenv(fieldSupportVar, "")
	root := t.TempDir()
	writeFieldSupportRoot(t, root)
	runner := &fakeRunner{resultMatcher: imageVersionResult("4.7.0", map[string]string{"registry.example/dcgm-exporter:dev": "4.7.0"})}
	opts := config.TestOptions{DCGMImage: "registry.example/dcgm:4.7.0", ExporterImage: "registry.example/dcgm-exporter:dev"}
	run := newK8sSuiteRun(
		context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)

	run.fieldSupport = run.fieldSupportEnv(k8sProbeInputs{})
	require.NoError(t, run.runK8sGroup(&opts, scenario.PlanGroup{Name: "default", Labels: []string{"default"}}))

	requireDmonInImage(t, runner, "registry.example/dcgm:4.7.0")
	command, ok := runner.commandNamedSuffix("dcgm-exporter-k8s.test")
	require.True(t, ok, "commands = %#v", runner.commands)
	require.Contains(t, fieldSupportEvidence(command.Env), `"supported":["DCGM_FI_DEV_GPU_TEMP_CELSIUS"]`)
}

// TestDCGMFieldIDsResolvesEveryShippedCSVRow checks that go-dcgm's name table
// has an ID for every enabled DCGM_FI_* row of the shipped CSV.
func TestDCGMFieldIDsResolvesEveryShippedCSVRow(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	source, err := dcgmFieldsSource(context.Background(), root, "", e2eexec.OSRunner{})
	if err != nil {
		t.Skipf("go-dcgm field definitions are not available here (no Go module cache or packaged copy): %v", err)
	}

	ids, err := dcgmFieldIDs(source)
	require.NoError(t, err)
	names, err := csvRowNames(filepath.Join(root, "etc", "default-counters.csv"))
	require.NoError(t, err)

	checked := 0
	for _, name := range names {
		if !strings.HasPrefix(name, "DCGM_FI_") {
			continue
		}
		require.Contains(t, ids, name, "go-dcgm has no field ID for CSV row %s", name)
		checked++
	}
	require.NotZero(t, checked)
}

// TestFieldSupportScope checks which scenarios and host groups get the field
// support evidence: host scenarios by group, except those that never start
// DCGM, and container image startup.
func TestFieldSupportScope(t *testing.T) {
	for _, selector := range []string{"host/defaultPreset", "host/startupMetrics", "host/gpuBindUnbindWatch", "host/systemdSocket"} {
		require.True(t, needsFieldSupport(scenario.SuiteHost, catalogScenarios(t, selector)), selector)
	}
	for _, selector := range []string{"host/configStartupErrors", "host/configInspection", "host/configFile", "host/nvmlInjectionMetrics"} {
		require.False(t, needsFieldSupport(scenario.SuiteHost, catalogScenarios(t, selector)), selector)
	}
	require.True(t, needsFieldSupport(scenario.SuiteContainer, catalogScenarios(t, "container/imageStartup")))
	require.False(t, needsFieldSupport(scenario.SuiteContainer, catalogScenarios(t, "host/startupMetrics")))

	require.True(t, hostGroupUsesFieldSupport(scenario.GroupHostBaseline))
	require.True(t, hostGroupUsesFieldSupport(scenario.GroupHostDefaultPreset))
	require.False(t, hostGroupUsesFieldSupport(scenario.GroupHostNVMLInjection))
	require.False(t, hostGroupUsesFieldSupport(scenario.GroupHostConfigInjection))
}

// TestK8sFieldSupportIsNotProbedForExternalClusters checks that a cluster the
// CLI did not create gets no evidence and the output says why.
func TestK8sFieldSupportIsNotProbedForExternalClusters(t *testing.T) {
	t.Setenv(fieldSupportVar, "")
	runner := &fakeRunner{}
	var stdout bytes.Buffer
	run := &k8sSuiteRun{
		ctx:        context.Background(),
		stdout:     &stdout,
		runner:     runner,
		clusterCfg: cluster.Config{Kubeconfig: "/tmp/kubeconfig"},
	}

	require.Empty(t, run.fieldSupportEnv(k8sProbeInputs{}))
	require.Empty(t, runner.commands)
	require.Contains(t, stdout.String(), "not probed for a cluster the CLI did not create")
}

// TestK8sGroupFieldSupportChecksTheGroupsDCGM checks that a GPU Operator
// group gets the evidence only when the DCGM its exporter reads has the
// probe's version, and that other groups always get it.
func TestK8sGroupFieldSupportChecksTheGroupsDCGM(t *testing.T) {
	const evidence = fieldSupportVar + `={"supported":["DCGM_FI_DEV_GPU_TEMP_CELSIUS"],"unsupported":{}}`
	exporter := "registry.example/dcgm-exporter:dev@" + testImageDigest
	operatorExporter := completeImage(exporter)
	tests := []struct {
		name          string
		group         string
		kubeconfig    string
		dcgmVersion   string
		labels        map[string]string
		remoteDCGM    string
		wantEvidence  bool
		wantOutput    string
		wantNoCommand bool
	}{
		{name: "plain group", group: "default", wantEvidence: true, wantNoCommand: true},
		{name: "operator group with the probed exporter image", group: "gpu-operator-baseline", wantEvidence: true, wantNoCommand: true},
		{
			name: "operator exporter image with the same DCGM", group: "gpu-operator-baseline", kubeconfig: "/tmp/kubeconfig",
			labels: map[string]string{operatorExporter: "4.7.0"}, wantEvidence: true,
		},
		{
			name: "operator exporter image with another DCGM", group: "gpu-operator-baseline", kubeconfig: "/tmp/kubeconfig",
			labels:     map[string]string{operatorExporter: "4.6.0"},
			wantOutput: "group gpu-operator-baseline gets no DCGM field support evidence: exporter image " + operatorExporter + " carries DCGM 4.6.0, the probe's DCGM is 4.7.0",
		},
		{
			name: "operator exporter image not readable", group: "gpu-operator-baseline", kubeconfig: "/tmp/kubeconfig",
			labels:     map[string]string{},
			wantOutput: "group gpu-operator-baseline gets no DCGM field support evidence: could not inspect exporter image",
		},
		{
			name: "standalone group with a user-supplied hostengine", group: "standalone-baseline", remoteDCGM: "dcgm.example:5555",
			wantOutput: "group standalone-baseline gets no DCGM field support evidence: the DCGM version of the user-supplied --remote-dcgm hostengine is unknown", wantNoCommand: true,
		},
		{name: "standalone group with the owned hostengine", group: "standalone-baseline", wantEvidence: true, wantNoCommand: true},
		{name: "remote DCGM image with the same DCGM", group: "gpu-operator-main-remote", dcgmVersion: "4.7.0", wantEvidence: true},
		{
			name: "remote DCGM image with another DCGM", group: "gpu-operator-main-dra", dcgmVersion: "4.6.0",
			wantOutput: "group gpu-operator-main-dra gets no DCGM field support evidence: DCGM image registry.example/dcgm:operator carries DCGM 4.6.0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{resultMatcher: imageVersionResult(test.dcgmVersion, test.labels)}
			var stdout bytes.Buffer
			opts := config.TestOptions{
				ExporterImage:        exporter,
				GPUOperatorDCGMImage: "registry.example/dcgm:operator",
				Kubeconfig:           test.kubeconfig,
				RemoteDCGM:           test.remoteDCGM,
			}
			run := newK8sSuiteRun(
				context.Background(), &stdout, t.TempDir(), runner, config.Config{TestOptions: opts},
				"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
			)
			run.fieldSupport = []string{evidence}
			run.fieldSupportDCGM = "4.7.0"

			got := run.groupFieldSupport(scenario.PlanGroup{Name: test.group})

			require.Equal(t, test.wantEvidence, len(got) == 1, stdout.String())
			require.Contains(t, stdout.String(), test.wantOutput)
			if test.wantNoCommand {
				require.Empty(t, runner.commands)
			}
		})
	}
}

// TestWantExporterDCGMVersionPullsAnImageOnlyInTheRegistry checks that an
// image missing from the local store is pulled before its label is read.
func TestWantExporterDCGMVersionPullsAnImageOnlyInTheRegistry(t *testing.T) {
	pulled := false
	runner := &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		switch {
		case command.Name == "docker" && hasArg(command.Args, "pull"):
			pulled = true
			return e2eexec.Result{}, true
		case command.Name == "docker" && hasArg(command.Args, "inspect") && hasArg(command.Args, "--format"):
			return e2eexec.Result{Stdout: []byte("4.7.0\n")}, true
		case command.Name == "docker" && hasArg(command.Args, "inspect"):
			if pulled {
				return e2eexec.Result{}, true
			}
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("Error: No such image")}, true
		}
		return e2eexec.Result{}, false
	}}

	got, err := wantExporterDCGMVersion(io.Discard, runner, config.TestOptions{}, "registry.example/exporter:a")(context.Background())

	require.NoError(t, err)
	require.Equal(t, "4.7.0", got)
	require.True(t, pulled)
}

// TestHostFieldSupportEnvDoesNotProbeWhenTheCallerSetEvidence checks that
// evidence from the environment starts no probe hostengine and runs no command.
func TestHostFieldSupportEnvDoesNotProbeWhenTheCallerSetEvidence(t *testing.T) {
	t.Setenv(fieldSupportVar, `{"supported":["DCGM_FI_DEV_GPU_TEMP_CELSIUS"]}`)
	runner := &fakeRunner{}
	var stdout bytes.Buffer
	manager := suite.TestBinaryManager{Root: t.TempDir()}

	env, err := hostFieldSupportEnv(context.Background(), &stdout, manager, runner, config.TestOptions{})

	require.NoError(t, err)
	require.Empty(t, env)
	require.Empty(t, runner.commands)
	require.Contains(t, stdout.String(), "using E2E_DCGM_FIELD_SUPPORT from the environment")
}

// TestK8sFieldSupportWithoutAProbeBackendRunsNoDocker checks that an owned
// cluster with no DCGM image to probe runs no docker command and says so.
func TestK8sFieldSupportWithoutAProbeBackendRunsNoDocker(t *testing.T) {
	t.Setenv(fieldSupportVar, "")
	root := t.TempDir()
	writeFieldSupportRoot(t, root)
	runner := &fakeRunner{}
	var stdout bytes.Buffer
	opts := config.TestOptions{ExporterImage: "registry.example/dcgm-exporter:dev"}
	run := newK8sSuiteRun(
		context.Background(), &stdout, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)

	env := run.fieldSupportEnv(k8sProbeInputs{})

	require.Empty(t, env)
	require.Empty(t, runner.commands)
	require.Contains(t, stdout.String(), "no DCGM image or host DCGM is configured to probe")
}
