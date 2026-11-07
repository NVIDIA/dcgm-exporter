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
	"fmt"
	"io"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// runDryRun reports existing catalog selection and prerequisites without
// building binaries, probing hardware, resolving images, or creating resources.
func runDryRun(stdout io.Writer, opts config.TestOptions, selected []scenario.Scenario) error {
	writeStep(stdout, "dry-run", "host and cluster changes are disabled")
	writeDryRunSelection(stdout, selected)
	writeDryRunRequirements(stdout, opts, selected)
	writeSection(stdout, "Capabilities")
	fmt.Fprintln(stdout, "  not probed during dry-run; capability-gated Kubernetes scenarios remain unknown until execution")
	fmt.Fprintln(stdout, "[e2e] PASS dcgm_exporter_e2e_scenario_plan")
	fmt.Fprintln(stdout, "[e2e] Dry-run passed: selected work and prerequisites described")
	fmt.Fprintln(stdout, "[e2e] Dry-run finished: no scenarios executed")
	return nil
}

// writeDryRunSelection prints the selected scenario selectors grouped by suite.
func writeDryRunSelection(w io.Writer, selected []scenario.Scenario) {
	writeSection(w, "Selected work")
	for _, suiteName := range scenario.AllSuites() {
		var entries []scenario.Scenario
		for _, entry := range selected {
			if entry.Suite == suiteName {
				entries = append(entries, entry)
			}
		}
		if len(entries) == 0 {
			continue
		}
		fmt.Fprintf(w, "%s:\n", suiteName)
		for _, entry := range entries {
			fmt.Fprintf(w, "  %s\n", entry.Selector())
		}
	}
}

// writeDryRunRequirements prints the artifacts and preparation required by the selected scenarios.
func writeDryRunRequirements(w io.Writer, opts config.TestOptions, selected []scenario.Scenario) {
	writeSection(w, "Required artifacts and preparation")
	requirements := scenario.RequirementsFor(selected, runConditions(opts))
	if len(requirements) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	for _, item := range requirements {
		description := string(item.Requirement.Kind)
		if scenario.IsImageRequirement(item.Requirement) {
			if image, err := configuredContainerImage(opts, item.Requirement.Image); err == nil {
				description += " " + image
			} else {
				description += " " + string(item.Requirement.Image) + " (configure its image reference before execution)"
			}
		}
		fmt.Fprintf(w, "  %s: %s\n", description, strings.Join(item.RequiredBy, ", "))
	}
}

// containsCapabilityProbeInput reports whether inputs contains one exact capability-probe input.
func containsCapabilityProbeInput(inputs []scenario.CapabilityProbeInput, wanted scenario.CapabilityProbeInput) bool {
	for _, input := range inputs {
		if input == wanted {
			return true
		}
	}
	return false
}

// writeCapabilitySummary prints capability statuses and, in verbose mode, their evidence.
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
