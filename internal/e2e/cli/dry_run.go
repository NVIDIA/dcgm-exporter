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
	"fmt"
	"io"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// runDryRun probes enough host capability data to show scenario decisions without mutating the host or cluster.
func runDryRun(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, opts config.Tests) error {
	cfg := config.Config{Tests: opts}
	writeStep(stdout, "dry-run", "host and cluster changes are disabled")

	probeDCGMImage := ""
	expectedDCGMVersion := ""
	if dcgmImageNeeded(opts) {
		if err := validateRequiredImage("--dcgm-image", opts.DCGMImage); err != nil {
			return err
		}
		if err := ensureDCGMImageAvailable(ctx, runner, opts); err != nil {
			return err
		}
		probeDCGMImage = dcgmImage(opts)
		expectedDCGMVersion = strings.TrimSpace(opts.Dependencies.DCGMVersion)
		if expectedDCGMVersion == "" {
			return fmt.Errorf("selected DCGM-probed scenarios require --dcgm-version, E2E_DCGM_VERSION, or DCGM_VERSION")
		}
	}
	caps := capability.ProbeHost(ctx, runner, capability.ProbeOptions{
		DryRun:              true,
		FailureInjection:    featureModeEnabled(opts.DCGMFailureInjection),
		MIGConfigure:        opts.MIGConfigure,
		DCGMImage:           probeDCGMImage,
		DockerEnv:           dockerConfigEnv(opts),
		ExpectedDCGMVersion: expectedDCGMVersion,
	}).Snapshot
	writeSection(stdout, "Capabilities")
	writeCapabilitySummary(stdout, "Host", "host:", capability.HostNames(), caps, opts.Verbose)
	writeCapabilitySummary(stdout, "Cluster", "cluster:", capability.ClusterNames(), caps, opts.Verbose)
	writeCapabilitySummary(stdout, "Remote DCGM", "dcgm:", capability.DCGMNames(), caps, opts.Verbose)

	plan, err := scenario.Select(scenario.Catalog, caps, cfg)
	if err != nil {
		return err
	}
	writeSection(stdout, "Scenario plan")
	if err := plan.WriteSummary(stdout); err != nil {
		return err
	}
	writeVerbosePlanLabels(stdout, opts.Verbose, plan)
	fmt.Fprintln(stdout, "[e2e] PASS dcgm_exporter_e2e_scenario_plan")
	fmt.Fprintln(stdout, "[e2e] Dry-run passed: scenario planning completed")
	fmt.Fprintln(stdout, "[e2e] Dry-run finished: no scenarios executed")
	return nil
}

// writeCapabilitySummary prints capability status rows in the dry-run report.
func writeCapabilitySummary(w io.Writer, title, prefix string, names []string, caps capability.Snapshot, verbose bool) {
	fmt.Fprintf(w, "%s:\n", title)
	byStatus := map[capability.Status][]capability.Capability{}
	for _, name := range names {
		entry := caps.Lookup(prefix + name)
		byStatus[entry.Status] = append(byStatus[entry.Status], entry)
	}
	for _, status := range []capability.Status{capability.StatusSupported, capability.StatusUnsupported, capability.StatusUnknown} {
		entries := byStatus[status]
		if len(entries) == 0 {
			continue
		}
		fmt.Fprintf(w, "  %s:\n", status)
		for _, entry := range entries {
			name := strings.TrimPrefix(entry.Name, prefix)
			source := entry.Source
			if source != "" {
				source = " (" + source + ")"
			}
			fmt.Fprintf(w, "    %-30s %s%s\n", name, entry.Reason, source)
			writeVerbose(w, verbose && entry.Evidence != "", "capability %s evidence: %s", entry.Name, entry.Evidence)
		}
	}
}

// writeVerbosePlanLabels prints the raw Ginkgo selectors only when verbose output is requested.
func writeVerbosePlanLabels(w io.Writer, verbose bool, plan scenario.Plan) {
	writeVerbose(w, verbose, "selected Kubernetes labels: %s", labelFilterOrNone(plan.EmbeddedLabels))
	writeVerbose(w, verbose, "selected remote DCGM Kubernetes labels: %s", labelFilterOrNone(plan.RemoteLabels))
}
