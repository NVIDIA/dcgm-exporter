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
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// fieldSupportVar is the variable the host, container and Kubernetes suites
// read DCGM field support evidence from. The suites own its format.
const fieldSupportVar = "E2E_DCGM_FIELD_SUPPORT"

// containerFieldSupportScenario is the container scenario whose metric
// contract reads the field support evidence.
const containerFieldSupportScenario = "imageStartup"

// dcgmFreeHostScenarios are host scenarios in a field-support group that
// never start DCGM, so they run on CPU-only runners and need no probe.
var dcgmFreeHostScenarios = map[string]bool{"configStartupErrors": true, "configInspection": true}

// needsFieldSupport reports whether a selected scenario of the suite gets the
// field support evidence: on the host, any scenario of a group that receives
// it and starts DCGM; in the container suite, image startup.
func needsFieldSupport(suiteName scenario.Suite, selected []scenario.Scenario) bool {
	for _, entry := range selected {
		if entry.Suite != suiteName {
			continue
		}

		switch suiteName {
		case scenario.SuiteHost:
			if hostGroupUsesFieldSupport(entry.Group) && !dcgmFreeHostScenarios[entry.Name] {
				return true
			}
		case scenario.SuiteContainer:
			if entry.Name == containerFieldSupportScenario {
				return true
			}
		}
	}
	return false
}

// fieldSupportRun holds what producing field support evidence needs: where
// the shipped CSV and the go-dcgm field table are, and how to run commands.
type fieldSupportRun struct {
	stdout   io.Writer
	root     string
	toolsDir string
	runner   e2eexec.Runner
	// wantDCGMVersion, when set, returns the DCGM version of the exporter
	// under test. The evidence is produced only when the probe's DCGM has the
	// same version, because field support can change between versions.
	wantDCGMVersion func(context.Context) (string, error)
	// withoutProfiling leaves profiling (DCGM_FI_PROF_*) rows out of the
	// supported list. The probe may run with more privilege than the exporter
	// under test, and profiling counters can need that privilege.
	withoutProfiling bool
}

// dcgmVersionLabel is the label exporter images record their DCGM version in.
const dcgmVersionLabel = "com.nvidia.dcgm.version"

// wantExporterDCGMVersion returns a wantDCGMVersion function for the exporter
// image: it makes the image available in the local Docker image store, pulling
// it when it is only in a registry, and reads its DCGM version label. An empty
// image, or one that cannot be pulled or inspected or has no label, is an error.
func wantExporterDCGMVersion(stdout io.Writer, runner e2eexec.Runner, opts config.TestOptions, image string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if strings.TrimSpace(image) == "" {
			return "", errors.New("no exporter image is configured")
		}

		return availableImageDCGMVersion(ctx, stdout, runner, opts, image)
	}
}

// availableImageDCGMVersion makes one exporter image available in the local
// Docker image store, pulling it when it is only in a registry, and returns
// its DCGM version.
func availableImageDCGMVersion(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.TestOptions, image string) (string, error) {
	if err := ensureDockerImageAvailable(ctx, stdout, runner, opts, image); err != nil {
		return "", err
	}
	return exporterImageDCGMVersion(ctx, runner, image, dockerConfigEnv(opts))
}

// exporterImageDCGMVersion reads the DCGM version label of one local exporter
// image and returns its major.minor.patch part.
func exporterImageDCGMVersion(ctx context.Context, runner e2eexec.Runner, image string, dockerEnv []string) (string, error) {
	result := runner.Run(ctx, e2eexec.Command{
		Name: "docker",
		Args: []string{"image", "inspect", "--format", `{{ index .Config.Labels "` + dcgmVersionLabel + `" }}`, image},
		Env:  dockerEnv,
	})
	if result.Err != nil || result.ExitCode != 0 {
		return "", fmt.Errorf("could not inspect exporter image %s: %s", image, resultText(result))
	}

	version := dcgmVersionRE.FindString(string(result.Stdout))
	if version == "" {
		return "", fmt.Errorf("exporter image %s has no %s label", image, dcgmVersionLabel)
	}
	return version, nil
}

// fieldSupportPreset reports whether the caller already set the evidence
// variable.
func fieldSupportPreset() bool {
	return strings.TrimSpace(os.Getenv(fieldSupportVar)) != ""
}

// fromEnvironment reports, and prints, whether the caller already set the
// evidence, which the suites then read as given.
func (r fieldSupportRun) fromEnvironment() bool {
	if !fieldSupportPreset() {
		return false
	}

	fmt.Fprintf(r.stdout, "[e2e] using %s from the environment\n", fieldSupportVar)
	return true
}

// probeDCGMVersion returns the DCGM version of the probe backend and a name
// for that backend.
func (r fieldSupportRun) probeDCGMVersion(ctx context.Context, probeOpts capability.ProbeOptions) (string, string, error) {
	if probeOpts.HostDCGMAddress != "" {
		version, err := hostDCGMVersion(ctx, r.runner)
		return version, "host dcgmi, hostengine " + probeOpts.HostDCGMAddress, err
	}

	version, err := dcgmImageVersion(ctx, r.runner, probeOpts.DCGMImage, probeOpts.DockerEnv)
	return version, "DCGM image " + probeOpts.DCGMImage, err
}

// env probes every row of the shipped default CSV with dcgmi dmon through
// probeOpts and returns the E2E_DCGM_FIELD_SUPPORT entry for the suite. It
// returns nothing when the variable is already set, so the suite reads the
// caller's evidence, or when no row could be classified; the suites then
// report missing rows as unqualified skips.
func (r fieldSupportRun) env(ctx context.Context, probeOpts capability.ProbeOptions) []string {
	writeSection(r.stdout, "DCGM field support")
	if r.fromEnvironment() {
		return nil
	}

	version, backend, err := r.probeDCGMVersion(ctx, probeOpts)
	if err != nil {
		fmt.Fprintf(r.stdout, "[e2e] probe DCGM version unknown (%s): %v\n", backend, err)
	} else {
		fmt.Fprintf(r.stdout, "[e2e] probe DCGM %s (%s)\n", version, backend)
	}

	if reason := r.versionMismatch(ctx, version, err); reason != "" {
		fmt.Fprintf(r.stdout, "[e2e] not probed: %s; missing CSV rows are reported as unqualified skips\n", reason)
		return nil
	}

	fields, unprobed, err := r.csvFields(ctx)
	if err != nil {
		fmt.Fprintf(r.stdout, "[e2e] not probed: %v; missing CSV rows are reported as unqualified skips\n", err)
		return nil
	}

	support := capability.ProbeFieldSupport(ctx, r.runner, probeOpts, fields)
	for name, reason := range unprobed {
		support.Unclassified[name] = reason
	}
	if r.withoutProfiling {
		support.WithoutProfiling("the exporter under test may not run with the privilege profiling needs")
	}
	writeFieldSupport(r.stdout, support)

	if len(support.Supported) == 0 && len(support.Unsupported) == 0 {
		fmt.Fprintln(r.stdout, "[e2e] no row was classified; missing CSV rows are reported as unqualified skips")
		return nil
	}

	document, err := json.Marshal(support)
	if err != nil {
		fmt.Fprintf(r.stdout, "[e2e] could not encode the evidence: %v; missing CSV rows are reported as unqualified skips\n", err)
		return nil
	}

	return []string{fieldSupportVar + "=" + string(document)}
}

// versionMismatch returns why the probe's DCGM cannot stand in for the
// exporter's DCGM, or "" when it can or no version is required.
func (r fieldSupportRun) versionMismatch(ctx context.Context, probeVersion string, probeErr error) string {
	if r.wantDCGMVersion == nil {
		return ""
	}

	want, err := r.wantDCGMVersion(ctx)
	switch {
	case err != nil:
		return fmt.Sprintf("the exporter's DCGM version is unknown (%v)", err)
	case probeErr != nil:
		return "the probe's DCGM version is unknown"
	case probeVersion != want:
		return fmt.Sprintf("the probe's DCGM %s differs from the exporter's DCGM %s", probeVersion, want)
	default:
		return ""
	}
}

// csvFields maps the enabled rows of the shipped default CSV to DCGM field IDs
// with the go-dcgm name table the exporter uses. Rows that are not DCGM
// fields are returned separately with the reason they were not probed.
func (r fieldSupportRun) csvFields(ctx context.Context) ([]capability.DCGMField, map[string]string, error) {
	names, err := csvRowNames(filepath.Join(r.root, "etc", "default-counters.csv"))
	if err != nil {
		return nil, nil, err
	}

	source, err := dcgmFieldsSource(ctx, r.root, r.toolsDir, r.runner)
	if err != nil {
		return nil, nil, err
	}

	ids, err := dcgmFieldIDs(source)
	if err != nil {
		return nil, nil, err
	}

	fields := make([]capability.DCGMField, 0, len(names))
	unprobed := map[string]string{}
	for _, name := range names {
		id, ok := ids[name]
		if !ok {
			unprobed[name] = "not a DCGM field name, so it was not probed"
			continue
		}
		fields = append(fields, capability.DCGMField{Name: name, ID: id})
	}
	return fields, unprobed, nil
}

// csvRowNames returns the first column of every enabled row of a metrics CSV.
func csvRowNames(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read the default counters CSV: %w", err)
	}

	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comment = '#'
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse the default counters CSV %s: %w", path, err)
	}

	names := make([]string, 0, len(records))
	for _, record := range records {
		if name := strings.TrimSpace(record[0]); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// writeFieldSupport prints the classification counts and every row that the
// suites will report as a skip, with its reason.
func writeFieldSupport(w io.Writer, support capability.FieldSupport) {
	fmt.Fprintf(w, "[e2e] %d supported, %d unsupported, %d unclassified\n",
		len(support.Supported), len(support.Unsupported), len(support.Unclassified))
	writeFieldReasons(w, "unsupported", support.Unsupported)
	writeFieldReasons(w, "unclassified", support.Unclassified)
}

// writeFieldReasons prints one line per row in name order.
func writeFieldReasons(w io.Writer, kind string, reasons map[string]string) {
	names := make([]string, 0, len(reasons))
	for name := range reasons {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		fmt.Fprintf(w, "[e2e] %s %s: %s\n", kind, name, reasons[name])
	}
}
