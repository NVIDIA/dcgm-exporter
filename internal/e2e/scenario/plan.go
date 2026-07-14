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

package scenario

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// Outcome is the pre-execution planning outcome for a scenario.
type Outcome string

const (
	OutcomeSelected Outcome = "selected"
	OutcomeSkipped  Outcome = "skipped"
)

// PlannedScenario is one scenario decision.
type PlannedScenario struct {
	Scenario Scenario
	Outcome  Outcome
	Reason   string
}

// Plan is the selected and pre-execution skipped Kubernetes work.
type Plan struct {
	Scenarios      []PlannedScenario
	EmbeddedLabels []string
	RemoteLabels   []string
	Groups         []PlanGroup
}

// PlanGroup is one ordered execution group and its selected labels.
type PlanGroup struct {
	Name   string
	Labels []string
}

// Select returns the Kubernetes scenario plan.
func Select(cat []Scenario, caps capability.Snapshot, cfg config.Config) (Plan, error) {
	var plan Plan
	var requiredFailures []string
	groups := map[string]int{}

	for _, entry := range cat {
		if entry.Suite != SuiteK8s {
			continue
		}
		if !scenarioEnabled(entry, cfg) || !scenarioAllowed(entry, cfg) {
			continue
		}

		decision := decide(entry, caps)
		plan.Scenarios = append(plan.Scenarios, decision)
		if decision.Outcome == OutcomeSkipped && scenarioRequired(entry, caps, cfg) && !dcgmNotSupported(entry, caps) && !hardwareAbsent(entry, caps) {
			requiredFailures = append(requiredFailures, fmt.Sprintf("%s required but unsupported: %s", entry.Selector(), decision.Reason))
		}
		if decision.Outcome != OutcomeSelected {
			continue
		}

		if entry.Remote {
			plan.RemoteLabels = append(plan.RemoteLabels, entry.Name)
		} else {
			plan.EmbeddedLabels = append(plan.EmbeddedLabels, entry.Name)
		}

		groupName := entry.Group
		if groupName == "" {
			groupName = "embedded-baseline"
		}
		index, ok := groups[groupName]
		if !ok {
			groups[groupName] = len(plan.Groups)
			plan.Groups = append(plan.Groups, PlanGroup{Name: groupName})
			index = len(plan.Groups) - 1
		}
		plan.Groups[index].Labels = append(plan.Groups[index].Labels, entry.Name)
	}

	if len(requiredFailures) != 0 {
		return plan, errors.New(strings.Join(requiredFailures, "\n"))
	}
	return plan, nil
}

// SelectedForSuite returns selected scenarios for a non-capability-gated suite.
func SelectedForSuite(cat []Scenario, suite Suite, cfg config.Config) []Scenario {
	var selected []Scenario
	for _, entry := range cat {
		if entry.Suite != suite {
			continue
		}
		if scenarioEnabled(entry, cfg) && scenarioAllowed(entry, cfg) {
			selected = append(selected, entry)
		}
	}
	return selected
}

// WriteSummary writes the user-facing scenario plan summary.
func (p Plan) WriteSummary(w io.Writer) error {
	selected, skipped := p.counts()
	if _, err := fmt.Fprintf(w, "selected: %d\n", selected); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "skipped: %d\n", skipped); err != nil {
		return err
	}
	if len(p.Groups) == 0 {
		if _, err := fmt.Fprintln(w, "\nExecution groups: none"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(w, "\nExecution groups:"); err != nil {
			return err
		}
		for _, group := range p.Groups {
			if _, err := fmt.Fprintf(w, "  %-24s %d %s\n", group.Name, len(group.Labels), plural(len(group.Labels), "scenario", "scenarios")); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "    %s\n", strings.Join(group.Labels, ", ")); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(w, "\nSkipped scenarios:"); err != nil {
		return err
	}
	if skipped == 0 {
		_, err := fmt.Fprintln(w, "  none")
		return err
	}
	for _, group := range p.skippedByReason() {
		if _, err := fmt.Fprintf(w, "  %s:\n", group.Reason); err != nil {
			return err
		}
		for _, selector := range group.Selectors {
			if _, err := fmt.Fprintf(w, "    %s\n", selector); err != nil {
				return err
			}
		}
	}
	return nil
}

// skippedReasonGroup is one stable group of skipped scenarios sharing the same reason.
type skippedReasonGroup struct {
	Reason    string
	Selectors []string
}

// counts returns selected and skipped scenario totals.
func (p Plan) counts() (int, int) {
	selected := 0
	skipped := 0
	for _, item := range p.Scenarios {
		switch item.Outcome {
		case OutcomeSelected:
			selected++
		case OutcomeSkipped:
			skipped++
		}
	}
	return selected, skipped
}

// skippedByReason groups skipped scenarios by first-seen reason.
func (p Plan) skippedByReason() []skippedReasonGroup {
	indexes := map[string]int{}
	var groups []skippedReasonGroup
	for _, item := range p.Scenarios {
		if item.Outcome != OutcomeSkipped {
			continue
		}
		index, ok := indexes[item.Reason]
		if !ok {
			indexes[item.Reason] = len(groups)
			groups = append(groups, skippedReasonGroup{Reason: item.Reason})
			index = len(groups) - 1
		}
		groups[index].Selectors = append(groups[index].Selectors, item.Scenario.Selector())
	}
	return groups
}

// plural returns singular or plural for a count.
func plural(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

// decide evaluates one scenario's capability gates against the current snapshot.
func decide(entry Scenario, caps capability.Snapshot) PlannedScenario {
	gates := append([]string{}, entry.Capabilities...)
	gates = append(gates, entry.ExtraGates...)
	if len(gates) == 0 {
		return PlannedScenario{Scenario: entry, Outcome: OutcomeSelected, Reason: "always-run scenario"}
	}

	reasons := make([]string, 0, len(gates))
	for _, gate := range gates {
		entryCapability := caps.Lookup(gate)
		if entryCapability.Status != capability.StatusSupported {
			return PlannedScenario{Scenario: entry, Outcome: OutcomeSkipped, Reason: entryCapability.Reason}
		}
		reasons = append(reasons, entryCapability.Reason)
	}
	return PlannedScenario{Scenario: entry, Outcome: OutcomeSelected, Reason: strings.Join(reasons, "; ")}
}

// scenarioEnabled applies feature-mode defaults before selector filters are considered.
func scenarioEnabled(entry Scenario, cfg config.Config) bool {
	switch entry.Enabled {
	case "failure_injection":
		return featureModeEnabled(cfg.Tests.DCGMFailureInjection) || contains(cfg.Tests.Scenarios, entry.Selector())
	case "gpu_operator":
		return featureModeEnabled(cfg.Tests.GPUOperator) || contains(cfg.Tests.Scenarios, entry.Selector())
	case "nvml_injection":
		return cfg.Tests.DCGMNVMLInjectionYAML != "" || contains(cfg.Tests.Scenarios, entry.Selector())
	case "manual":
		return contains(cfg.Tests.Scenarios, entry.Selector())
	default:
		return true
	}
}

// scenarioAllowed applies explicit suite and scenario selection filters.
func scenarioAllowed(entry Scenario, cfg config.Config) bool {
	if contains(cfg.Tests.SkipSuites, "all") || contains(cfg.Tests.SkipSuites, string(entry.Suite)) {
		return false
	}
	if len(cfg.Tests.Suites) != 0 && !contains(cfg.Tests.Suites, "all") && !contains(cfg.Tests.Suites, string(entry.Suite)) {
		return false
	}
	if contains(cfg.Tests.SkipScenarios, entry.Selector()) {
		return false
	}
	if len(cfg.Tests.Scenarios) != 0 && !contains(cfg.Tests.Scenarios, entry.Selector()) {
		return false
	}
	return true
}

// contains reports whether value appears exactly in values.
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// featureModeEnabled treats any non-false feature mode as selectable coverage.
func featureModeEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

// scenarioRequired decides whether an unsupported scenario should fail setup or be skipped.
func scenarioRequired(entry Scenario, caps capability.Snapshot, cfg config.Config) bool {
	if cfg.Tests.DryRun {
		return false
	}
	if contains(cfg.Tests.Scenarios, entry.Selector()) {
		return true
	}
	if fullDefaultRun(cfg) && capabilityUnknown(entry, caps) {
		return true
	}
	switch entry.Required {
	case "failure_injection":
		return boolModeRequired(cfg.Tests.DCGMFailureInjection)
	case "failure_injection_nvlink":
		return boolModeRequired(cfg.Tests.DCGMFailureInjection)
	case "gpu_operator":
		return gpuOperatorRequired(cfg.Tests.GPUOperator)
	case "gpu_operator_shared":
		return gpuOperatorRequired(cfg.Tests.GPUOperator) && boolModeRequired(cfg.Tests.SharedGPUConfigure)
	case "gpu_operator_mig":
		return gpuOperatorRequired(cfg.Tests.GPUOperator) && boolModeRequired(cfg.Tests.MIGConfigure)
	case "gpu_operator_dra":
		return gpuOperatorRequired(cfg.Tests.GPUOperator) && boolModeRequired(cfg.Tests.DRAConfigure)
	case "gpu_operator_ipv6":
		return gpuOperatorRequired(cfg.Tests.GPUOperator) && ipFamilyRequestsIPv6(cfg.Tests.K3dIPFamily)
	case "dra":
		return boolModeRequired(cfg.Tests.DRAConfigure)
	case "shared_gpu":
		return boolModeRequired(cfg.Tests.SharedGPUConfigure)
	case "mig":
		return boolModeRequired(cfg.Tests.MIGConfigure)
	case "none", "":
		return false
	default:
		return false
	}
}

// fullDefaultRun reports whether defaults request the broadest automatic k8s coverage.
func fullDefaultRun(cfg config.Config) bool {
	return len(cfg.Tests.Suites) == 0 &&
		len(cfg.Tests.Scenarios) == 0 &&
		boolModeRequired(cfg.Tests.DRAConfigure) &&
		boolModeRequired(cfg.Tests.SharedGPUConfigure) &&
		boolModeRequired(cfg.Tests.DCGMFailureInjection) &&
		gpuOperatorRequired(cfg.Tests.GPUOperator)
}

// capabilityUnknown reports whether any gate for the scenario was not probed.
func capabilityUnknown(entry Scenario, caps capability.Snapshot) bool {
	gates := append([]string{}, entry.Capabilities...)
	gates = append(gates, entry.ExtraGates...)
	for _, gate := range gates {
		if caps.Lookup(gate).Status == capability.StatusUnknown {
			return true
		}
	}
	return false
}

// hardwareAbsent recognizes unsupported host gates caused by missing hardware rather than setup failure.
func hardwareAbsent(entry Scenario, caps capability.Snapshot) bool {
	gates := append([]string{}, entry.Capabilities...)
	gates = append(gates, entry.ExtraGates...)
	for _, gate := range gates {
		entryCapability := caps.Lookup(gate)
		if entryCapability.Status != capability.StatusUnsupported || !strings.HasPrefix(gate, "host:") {
			continue
		}
		reason := strings.ToLower(entryCapability.Reason)
		for _, marker := range []string{
			"not detected",
			"not enabled",
			"not present",
			"no full gpu",
			"requires at least two gpus",
			"did not report",
		} {
			if strings.Contains(reason, marker) {
				return true
			}
		}
	}
	return false
}

// dcgmNotSupported reports whether DCGM explicitly rejected a field or feature needed by the scenario.
func dcgmNotSupported(entry Scenario, caps capability.Snapshot) bool {
	gates := append([]string{}, entry.Capabilities...)
	gates = append(gates, entry.ExtraGates...)
	for _, gate := range gates {
		if caps.Lookup(gate).DCGMNotSupported {
			return true
		}
	}
	return false
}

// boolModeRequired treats explicit true-like modes as setup requirements.
func boolModeRequired(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto", "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

// gpuOperatorRequired treats install and existing modes as hard GPU Operator requirements.
func gpuOperatorRequired(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "install", "existing":
		return true
	default:
		return false
	}
}

// ipFamilyRequestsIPv6 reports whether a local-cluster IP-family option requires IPv6.
func ipFamilyRequestsIPv6(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ipv6", "dualstack":
		return true
	default:
		return false
	}
}
