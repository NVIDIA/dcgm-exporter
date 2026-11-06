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

// SelectionOptions adjusts how a caller handles capability evidence it intentionally cannot collect.
// The default leaves unknown capabilities as errors so ordinary local planning cannot silently use incomplete evidence.
type SelectionOptions struct {
	// SkipUnknownCapabilityPrefixes marks unknown capabilities in these namespaces as explicit skips.
	// An external Kubernetes caller uses the host namespace because controller facts do not describe its selected target.
	SkipUnknownCapabilityPrefixes []string
}

// Select returns the Kubernetes scenario plan.
func Select(cat []Scenario, caps capability.Snapshot, cfg config.Config) (Plan, error) {
	return SelectWithOptions(cat, caps, cfg, SelectionOptions{})
}

// SelectWithOptions returns the Kubernetes scenario plan using the caller's explicit unknown-capability policy.
// It preserves Select's strict behavior unless an option identifies evidence that the caller intentionally did not collect.
func SelectWithOptions(cat []Scenario, caps capability.Snapshot, cfg config.Config, options SelectionOptions) (Plan, error) {
	if err := ValidateCatalog(cat); err != nil {
		return Plan{}, err
	}
	var plan Plan
	groups := map[string]int{}

	for _, entry := range cat {
		if entry.Suite != SuiteK8s {
			continue
		}
		if !scenarioEnabled(entry, cfg) || !scenarioAllowed(entry, cfg) {
			continue
		}

		decision, err := decide(entry, caps, cfg.TestOptions.DryRun, options)
		if err != nil {
			return plan, err
		}
		plan.Scenarios = append(plan.Scenarios, decision)
		if decision.Outcome == OutcomeSelected {
			plan.addSelected(entry, groups)
		}
	}

	return plan, nil
}

// addSelected records a selected scenario's label as remote or embedded and adds
// it to its group, creating the group in first-seen order. groups maps names to indexes.
func (p *Plan) addSelected(entry Scenario, groups map[string]int) {
	if entry.Remote {
		p.RemoteLabels = append(p.RemoteLabels, entry.Name)
	} else {
		p.EmbeddedLabels = append(p.EmbeddedLabels, entry.Name)
	}
	index, ok := groups[entry.Group]
	if !ok {
		index = len(p.Groups)
		groups[entry.Group] = index
		p.Groups = append(p.Groups, PlanGroup{Name: entry.Group})
	}
	p.Groups[index].Labels = append(p.Groups[index].Labels, entry.Name)
}

// skipsUnknownCapability reports whether an unknown gate belongs to a namespace the caller cannot probe.
func (o SelectionOptions) skipsUnknownCapability(gate string) bool {
	for _, prefix := range o.SkipUnknownCapabilityPrefixes {
		if prefix != "" && strings.HasPrefix(gate, prefix) {
			return true
		}
	}
	return false
}

// GroupSelected groups scenarios by their first occurrence in selected.
func GroupSelected(selected []Scenario) []PlanGroup {
	indexes := make(map[string]int)
	var groups []PlanGroup
	for _, entry := range selected {
		index, ok := indexes[entry.Group]
		if !ok {
			indexes[entry.Group] = len(groups)
			groups = append(groups, PlanGroup{Name: entry.Group})
			index = len(groups) - 1
		}
		groups[index].Labels = append(groups[index].Labels, entry.Name)
	}
	return groups
}

// SkipSelectedLabel marks one selected scenario as skipped after planning completes.
func SkipSelectedLabel(plan Plan, label, reason string) Plan {
	for i := range plan.Scenarios {
		if plan.Scenarios[i].Scenario.Name != label || plan.Scenarios[i].Outcome != OutcomeSelected {
			continue
		}
		plan.Scenarios[i].Outcome = OutcomeSkipped
		plan.Scenarios[i].Reason = reason
		break
	}
	plan.EmbeddedLabels = removeString(plan.EmbeddedLabels, label)
	plan.RemoteLabels = removeString(plan.RemoteLabels, label)
	for i := range plan.Groups {
		plan.Groups[i].Labels = removeString(plan.Groups[i].Labels, label)
	}
	filtered := plan.Groups[:0]
	for _, group := range plan.Groups {
		if len(group.Labels) == 0 {
			continue
		}
		filtered = append(filtered, group)
	}
	plan.Groups = filtered
	return plan
}

// removeString drops one exact value from values.
func removeString(values []string, value string) []string {
	filtered := values[:0]
	for _, candidate := range values {
		if candidate == value {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
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

// decide evaluates coarse features before scenario-specific applicability.
func decide(entry Scenario, caps capability.Snapshot, dryRun bool, options SelectionOptions) (PlannedScenario, error) {
	required := entry.RequiredCapabilitiesFor(dryRun)
	if len(required) == 0 && len(entry.Applicability) == 0 {
		return PlannedScenario{Scenario: entry, Outcome: OutcomeSelected, Reason: "always-run scenario"}, nil
	}

	reasons, skipped, skip, err := evaluateGates(entry, caps, dryRun, options, "feature unsupported", required)
	if err != nil {
		return PlannedScenario{}, err
	}
	if skip {
		return skipped, nil
	}

	applicabilityReasons, skipped, skip, err := evaluateGates(
		entry,
		caps,
		dryRun,
		options,
		"scenario not applicable",
		entry.Applicability,
	)
	if err != nil {
		return PlannedScenario{}, err
	}
	if skip {
		return skipped, nil
	}

	reasons = append(reasons, applicabilityReasons...)
	return PlannedScenario{
		Scenario: entry,
		Outcome:  OutcomeSelected,
		Reason:   strings.Join(reasons, "; "),
	}, nil
}

// evaluateGates returns the first skip or unknown result for one gate tier.
func evaluateGates(
	entry Scenario,
	caps capability.Snapshot,
	dryRun bool,
	options SelectionOptions,
	skipPrefix string,
	gates []string,
) ([]string, PlannedScenario, bool, error) {
	reasons := make([]string, 0, len(gates))
	for _, gate := range gates {
		result := caps.Lookup(gate)
		switch result.Status {
		case capability.StatusSupported:
			reasons = append(reasons, result.Reason)
		case capability.StatusUnsupported:
			decision := PlannedScenario{
				Scenario: entry,
				Outcome:  OutcomeSkipped,
				Reason:   skipPrefix + ": " + result.Reason,
			}
			return nil, decision, true, nil
		case capability.StatusUnknown:
			if dryRun {
				decision := PlannedScenario{
					Scenario: entry,
					Outcome:  OutcomeSkipped,
					Reason:   "not evaluated during dry-run: " + result.Reason,
				}
				return nil, decision, true, nil
			}
			if options.skipsUnknownCapability(gate) {
				decision := PlannedScenario{
					Scenario: entry,
					Outcome:  OutcomeSkipped,
					Reason:   "not evaluated for the selected Kubernetes target: " + result.Reason,
				}
				return nil, decision, true, nil
			}
			return nil, PlannedScenario{}, false, fmt.Errorf(
				"%s capability probe failed for %s: %s (%s)",
				entry.Selector(),
				gate,
				result.Reason,
				result.Source,
			)
		default:
			return nil, PlannedScenario{}, false, fmt.Errorf(
				"%s capability probe returned invalid status %q for %s",
				entry.Selector(),
				result.Status,
				gate,
			)
		}
	}
	return reasons, PlannedScenario{}, false, nil
}

// scenarioEnabled applies feature-mode defaults before selector filters are considered.
func scenarioEnabled(entry Scenario, cfg config.Config) bool {
	switch entry.Enabled {
	case "gpu_operator":
		return cfg.TestOptions.GPUOperator.Enabled() || contains(cfg.TestOptions.Scenarios, entry.Selector())
	case "gpu_operator_chart":
		return (cfg.TestOptions.GPUOperator == config.GPUOperatorInstall && strings.TrimSpace(cfg.TestOptions.GPUOperatorChart) != "") || contains(cfg.TestOptions.Scenarios, entry.Selector())
	case "shared_gpu":
		return cfg.TestOptions.SharedGPUConfigure.Enabled() || contains(cfg.TestOptions.Scenarios, entry.Selector())
	case EnabledNVMLInjection:
		return contains(cfg.TestOptions.Scenarios, entry.Selector())
	case "nvsdm_mock":
		return contains(cfg.TestOptions.Scenarios, entry.Selector())
	case "manual":
		return contains(cfg.TestOptions.Scenarios, entry.Selector())
	default:
		return true
	}
}

// scenarioAllowed applies explicit suite and scenario selection filters.
func scenarioAllowed(entry Scenario, cfg config.Config) bool {
	if contains(cfg.TestOptions.SkipSuites, "all") || contains(cfg.TestOptions.SkipSuites, string(entry.Suite)) {
		return false
	}
	if len(cfg.TestOptions.Suites) != 0 && !contains(cfg.TestOptions.Suites, "all") && !contains(cfg.TestOptions.Suites, string(entry.Suite)) {
		return false
	}
	if contains(cfg.TestOptions.SkipScenarios, entry.Selector()) {
		return false
	}
	if len(cfg.TestOptions.Scenarios) != 0 && !contains(cfg.TestOptions.Scenarios, entry.Selector()) {
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
