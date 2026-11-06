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

package k8s

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	dto "github.com/prometheus/client_model/go"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// Families and metric rows the selected-subset steps and the MIG pod
// attribution scenario collect.
const (
	gpuTempRow       = "DCGM_FI_DEV_GPU_TEMP_CELSIUS, gauge, GPU temperature (in C)."
	switchTempFamily = "DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT"
	switchTempRow    = switchTempFamily + ", gauge, NVSwitch temperature."
	switchLinkFamily = "DCGM_FI_DEV_NVSWITCH_LINK_THROUGHPUT_TX"
	switchLinkRow    = switchLinkFamily + ", counter, NVSwitch link transmit throughput."
	cpuUtilFamily    = "DCGM_FI_DEV_CPU_UTIL_TOTAL"
	cpuUtilRow       = cpuUtilFamily + ", gauge, Total CPU utilization."
	fbUsedRow        = "DCGM_FI_DEV_FB_USED_BYTES, gauge, Used framebuffer memory in bytes."
)

// Label names and the text before a switch ID in the exporter's output. The
// exporter prints a switch as "nvswitch0"; links, CPUs, cores and GPU
// instances are plain numbers.
const (
	switchLabel  = "nvswitch"
	switchPrefix = "nvswitch"
	linkLabel    = "nvlink"
	cpuLabel     = "cpu"
	coreLabel    = "cpucore"
	gpuLabel     = "gpu"
	instanceID   = "GPU_I_ID"
)

// selectedMemberCount is how many links of a switch, or cores of a CPU, a step selects.
const selectedMemberCount = 2

// errSelectionCoversAll means the default run shows no more entities than the
// step would select, so a run that ignored the selection would pass too.
var errSelectionCoversAll = errors.New("the default run shows no more entities than the step selects")

// entitySelection is the entity a selected-subset step selects, and the links
// of a switch or the cores of a CPU it selects with it.
type entitySelection struct {
	// entityID is the switch or CPU number, without the output's label prefix.
	entityID string
	// members are the link or core numbers; empty selects the entity alone.
	members []string
}

// metricLabels converts one Prometheus metric's labels into a lookup map.
func metricLabels(metric *dto.Metric) map[string]string {
	labels := map[string]string{}
	for _, label := range metric.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}

// familyLabelSets returns the labels of every sample of family.
func familyLabelSets(families map[string]*dto.MetricFamily, family string) []map[string]string {
	var sets []map[string]string
	for _, metric := range families[family].GetMetric() {
		sets = append(sets, metricLabels(metric))
	}
	return sets
}

// labelsContain reports whether labels holds every name and value in want.
func labelsContain(labels, want map[string]string) bool {
	for name, value := range want {
		if labels[name] != value {
			return false
		}
	}
	return true
}

// distinctIDs returns the distinct numbers that label takes on the samples
// that include every pair in where, lowest first. It removes prefix from each
// value first, and fails on a value that is not prefix plus a number.
func distinctIDs(sets []map[string]string, label, prefix string, where map[string]string) ([]int, error) {
	var ids []int
	for _, labels := range sets {
		value, ok := labels[label]
		if !ok || !labelsContain(labels, where) {
			continue
		}

		number, found := strings.CutPrefix(value, prefix)
		id, err := strconv.Atoi(number)
		if err != nil || !found {
			return nil, fmt.Errorf("%s=%q is not %q plus a number", label, value, prefix)
		}

		ids = append(ids, id)
	}

	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// idStrings converts numbers to their decimal text.
func idStrings(ids []int) []string {
	text := make([]string, len(ids))
	for i, id := range ids {
		text[i] = strconv.Itoa(id)
	}
	return text
}

// scrapeLabelSets parses metrics and returns the labels of every sample of
// family. It fails when the family has no sample.
func scrapeLabelSets(metrics []byte, family string) ([]map[string]string, error) {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return nil, err
	}

	sets := familyLabelSets(families, family)
	if len(sets) == 0 {
		return nil, fmt.Errorf("no %s sample was emitted", family)
	}
	return sets, nil
}

// entitySignature returns a function that describes the entities a scrape
// holds for family: the sorted distinct combinations of the named labels. Two
// scrapes with the same signature report the same entities.
func entitySignature(family string, labels ...string) func([]byte) (string, error) {
	return func(metrics []byte) (string, error) {
		sets, err := scrapeLabelSets(metrics, family)
		if err != nil {
			return "", err
		}

		combinations := make([]string, 0, len(sets))
		for _, set := range sets {
			var parts []string
			for _, label := range labels {
				if value, ok := set[label]; ok {
					parts = append(parts, label+"="+value)
				}
			}
			combinations = append(combinations, strings.Join(parts, ","))
		}

		slices.Sort(combinations)
		return strings.Join(slices.Compact(combinations), ";"), nil
	}
}

// settledScrape returns a check that accepts a scrape only when the previous
// scrape it saw had the same signature. The default run's scrape is taken as
// the baseline only after the exporter has reported the same entities twice.
func settledScrape(signature func([]byte) (string, error)) func([]byte) error {
	previous := ""
	return func(metrics []byte) error {
		current, err := signature(metrics)
		if err != nil {
			return err
		}

		settled := current == previous
		previous = current
		if !settled {
			return errors.New("waiting for two scrapes that report the same entities")
		}
		return nil
	}
}

// selectHighestEntity selects the highest entity that family reports under
// label (after prefix). It returns errSelectionCoversAll when there is only one.
func selectHighestEntity(metrics []byte, family, label, prefix string) (entitySelection, error) {
	sets, err := scrapeLabelSets(metrics, family)
	if err != nil {
		return entitySelection{}, err
	}

	ids, err := distinctIDs(sets, label, prefix, nil)
	if err != nil {
		return entitySelection{}, err
	}

	if len(ids) < 2 {
		return entitySelection{}, fmt.Errorf("%s reports %d %s: %w", family, len(ids), label, errSelectionCoversAll)
	}

	return entitySelection{entityID: strconv.Itoa(ids[len(ids)-1])}, nil
}

// selectHighestMembers selects the highest entity under label and the
// selectedMemberCount highest numbers of memberLabel on it (all of them when
// it has fewer). It returns errSelectionCoversAll when that is everything the
// scrape reports.
func selectHighestMembers(metrics []byte, family, label, prefix, memberLabel string) (entitySelection, error) {
	sets, err := scrapeLabelSets(metrics, family)
	if err != nil {
		return entitySelection{}, err
	}

	entities, err := distinctIDs(sets, label, prefix, nil)
	if err != nil {
		return entitySelection{}, err
	}

	if len(entities) == 0 {
		return entitySelection{}, fmt.Errorf("%s has no %s label", family, label)
	}

	entityID := strconv.Itoa(entities[len(entities)-1])
	members, err := distinctIDs(sets, memberLabel, "", map[string]string{label: prefix + entityID})
	if err != nil {
		return entitySelection{}, err
	}

	if len(members) == 0 {
		return entitySelection{}, fmt.Errorf("%s reports no %s on %s %s: %w", family, memberLabel, label, entityID, errSelectionCoversAll)
	}

	// An entity with fewer members than the step selects gives all of them.
	count := min(selectedMemberCount, len(members))
	if len(entities) == 1 && len(members) == count {
		return entitySelection{}, fmt.Errorf("%s reports one %s with only %s %v: %w", family, label, memberLabel, members, errSelectionCoversAll)
	}

	return entitySelection{entityID: entityID, members: idStrings(members[len(members)-count:])}, nil
}

// requireSelected checks that every value in want is among the values the
// samples carry for label, and that no sample carries another value.
func requireSelected(sets []map[string]string, family, label string, want []string) error {
	var problems []error
	seen := map[string]bool{}
	for _, labels := range sets {
		value := labels[label]
		seen[value] = true
		if !slices.Contains(want, value) {
			problems = append(problems, fmt.Errorf("%s has %s=%q, want one of %v", family, label, value, want))
		}
	}

	for _, value := range want {
		if !seen[value] {
			problems = append(problems, fmt.Errorf("%s has no %s=%q sample", family, label, value))
		}
	}
	return errors.Join(problems...)
}

// onlySelected returns a check that family has samples, that each label in
// allowed takes exactly the allowed values on them, and that every allowed
// value appears. A run that ignores the selection reports other entities and
// fails; so does a run that reports less than the selection.
func onlySelected(family string, allowed map[string][]string) func([]byte) error {
	return func(metrics []byte) error {
		sets, err := scrapeLabelSets(metrics, family)
		if err != nil {
			return err
		}

		var problems []error
		for label, values := range allowed {
			problems = append(problems, requireSelected(sets, family, label, values))
		}
		return errors.Join(problems...)
	}
}

// onlySelectedEntity returns a check that family has samples for the selected
// entity only. labelValue is the label's value for that entity.
func onlySelectedEntity(family, label, labelValue string) func([]byte) error {
	return onlySelected(family, map[string][]string{label: {labelValue}})
}

// onlySelectedLinks returns a check that the link family has samples for the
// selected switch and its selected links only.
func onlySelectedLinks(selection entitySelection) func([]byte) error {
	return onlySelected(switchLinkFamily, map[string][]string{
		switchLabel: {switchPrefix + selection.entityID},
		linkLabel:   selection.members,
	})
}

// onlySelectedCores returns a check that every CPU family sample names the
// selected CPU, and that the samples with a core label report exactly the
// selected cores. A CPU-level sample has no core label and is not a core.
func onlySelectedCores(selection entitySelection) func([]byte) error {
	return func(metrics []byte) error {
		sets, err := scrapeLabelSets(metrics, cpuUtilFamily)
		if err != nil {
			return err
		}

		var coreSets []map[string]string
		for _, labels := range sets {
			if _, ok := labels[coreLabel]; ok {
				coreSets = append(coreSets, labels)
			}
		}
		return errors.Join(
			requireSelected(sets, cpuUtilFamily, cpuLabel, []string{selection.entityID}),
			requireSelected(coreSets, cpuUtilFamily, coreLabel, selection.members),
		)
	}
}

// migInstancePairs returns the sorted distinct "gpu/GPU_I_ID" pairs of the
// DCGM samples that carry a GPU instance ID. A GPU instance ID repeats on
// every GPU, so the pair names one instance.
func migInstancePairs(metrics []byte) ([]string, error) {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return nil, err
	}

	var pairs []string
	for family := range families {
		if !strings.HasPrefix(family, "DCGM_FI_") {
			continue
		}

		for _, labels := range familyLabelSets(families, family) {
			if labels[gpuLabel] != "" && labels[instanceID] != "" {
				pairs = append(pairs, labels[gpuLabel]+"/"+labels[instanceID])
			}
		}
	}

	if len(pairs) == 0 {
		return nil, errors.New("no DCGM sample carries a GPU instance ID")
	}

	slices.Sort(pairs)
	return slices.Compact(pairs), nil
}

// migSignature describes the GPU instances a scrape reports, for settledScrape.
func migSignature(metrics []byte) (string, error) {
	pairs, err := migInstancePairs(metrics)
	return strings.Join(pairs, ";"), err
}

// requireSeveralMIGInstances returns errSelectionCoversAll unless the default
// run shows more than one GPU instance, because selecting one instance would
// otherwise change nothing.
func requireSeveralMIGInstances(baseline []byte) error {
	pairs, err := migInstancePairs(baseline)
	if err != nil {
		return err
	}

	if len(pairs) < 2 {
		return fmt.Errorf("the default run shows only GPU instance %v: %w", pairs, errSelectionCoversAll)
	}
	return nil
}

// onlyOneMIGInstance checks that the scrape reports exactly one GPU instance
// and that its GPU_I_ID is id. A GPU instance ID repeats on every MIG GPU, so
// the GPU is not named; the selector picks one DCGM entity, so one pair is what
// a working selection reports.
func onlyOneMIGInstance(metrics []byte, id string) error {
	pairs, err := migInstancePairs(metrics)
	if err != nil {
		return err
	}

	if len(pairs) != 1 || !strings.HasSuffix(pairs[0], "/"+id) {
		return fmt.Errorf("GPU instances reported = %v, want only one with %s=%s", pairs, instanceID, id)
	}
	return nil
}

// switchSelector returns the --switch-devices selector for the selection: the
// switch, and its links when the selection has them.
func switchSelector(selection entitySelection) string {
	return "g:" + selection.entityID + membersSelector(selection)
}

// cpuSelector returns the --cpu-devices selector for the selection: the CPU
// with the selected cores, or with all its cores when none are selected.
func cpuSelector(selection entitySelection) string {
	if len(selection.members) == 0 {
		return "g:" + selection.entityID + "+i"
	}
	return "g:" + selection.entityID + membersSelector(selection)
}

// membersSelector returns the minor part of a device selector, or nothing.
func membersSelector(selection entitySelection) string {
	if len(selection.members) == 0 {
		return ""
	}
	return "+i:" + strings.Join(selection.members, ",")
}
