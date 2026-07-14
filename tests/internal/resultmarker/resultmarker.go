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

// Package resultmarker emits machine-readable result markers for Ginkgo specs.
package resultmarker

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
)

const markerPrefix = "dcgm_exporter_e2e"

var (
	bracketRE                 = regexp.MustCompile(`\[[^]]+\]`)
	camelBoundaryRE           = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	invalidRE                 = regexp.MustCompile(`[^a-z0-9]+`)
	collapseRE                = regexp.MustCompile(`_+`)
	output          io.Writer = os.Stdout
)

// EnabledFromEnv returns the first configured boolean value found in names.
func EnabledFromEnv(names ...string) bool {
	for _, name := range names {
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		enabled, err := strconv.ParseBool(value)
		return err == nil && enabled
	}
	return false
}

// Register installs per-spec marker hooks and returns true for package-level assignment.
func Register(enabled func() bool, names map[string]string) bool {
	return register(enabled, func(report ginkgo.SpecReport) string {
		return specName(report, names)
	})
}

// RegisterSuite installs hooks that derive marker names from suite scenario labels.
func RegisterSuite(enabled func() bool, suite string) bool {
	return register(enabled, func(report ginkgo.SpecReport) string {
		return suiteSpecName(report, suite)
	})
}

// register installs marker hooks using the supplied report name resolver.
func register(enabled func() bool, nameFor func(ginkgo.SpecReport) string) bool {
	var mu sync.Mutex
	started := map[string]struct{}{}

	ginkgo.BeforeEach(func() {
		if !enabled() {
			return
		}

		name := nameFor(ginkgo.CurrentSpecReport())
		mu.Lock()
		started[name] = struct{}{}
		mu.Unlock()

		line("RUNNING", name)
	})

	ginkgo.ReportAfterEach(func(report ginkgo.SpecReport) {
		if !enabled() {
			return
		}

		name := nameFor(report)
		mu.Lock()
		_, ok := started[name]
		delete(started, name)
		mu.Unlock()
		if !ok {
			return
		}

		line(terminalStatus(report), name)
	})

	return true
}

// line prints one complete reserved marker line.
func line(status, text string) {
	fmt.Fprintf(output, "&&&& %s %s\n", status, safeText(text))
}

// terminalStatus maps Ginkgo outcomes to terminal marker states.
func terminalStatus(report ginkgo.SpecReport) string {
	switch report.State {
	case types.SpecStatePassed:
		return "PASSED"
	case types.SpecStateSkipped:
		return "WAIVED"
	default:
		return "FAILED"
	}
}

// specName returns the stable parser-facing name for one Ginkgo spec.
func specName(report ginkgo.SpecReport, names map[string]string) string {
	return nameWithLeaf(scenarioBase(report.Labels(), names), report.LeafNodeText)
}

// suiteSpecName derives a stable suite-qualified name from the first scenario label.
func suiteSpecName(report ginkgo.SpecReport, suite string) string {
	base := markerPrefix + "_" + slug(suite) + "_unlabeled"
	if labels := report.Labels(); len(labels) > 0 {
		base = markerPrefix + "_" + slug(suite) + "_" + scenarioSlug(labels[0])
	}
	return nameWithLeaf(base, report.LeafNodeText)
}

// nameWithLeaf appends normalized Ginkgo spec text to a scenario marker base.
func nameWithLeaf(base, text string) string {
	leaf := slug(text)
	if leaf == "" {
		return base
	}
	return base + "_" + leaf
}

// scenarioBase picks the first catalog label or a safe unlabeled fallback.
func scenarioBase(labels []string, names map[string]string) string {
	for _, label := range labels {
		if base, ok := names[label]; ok {
			return base
		}
	}
	return markerPrefix + "_unlabeled"
}

// safeText normalizes marker text to one parser-friendly line.
func safeText(text string) string {
	text = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(text)
	return strings.Join(strings.Fields(text), " ")
}

// scenarioSlug converts a camelCase scenario label into snake_case.
func scenarioSlug(text string) string {
	return slug(camelBoundaryRE.ReplaceAllString(text, `${1}_${2}`))
}

// slug converts free-form Ginkgo text into a stable test-name suffix.
func slug(text string) string {
	text = bracketRE.ReplaceAllString(text, " ")
	text = strings.ToLower(text)
	text = invalidRE.ReplaceAllString(text, "_")
	text = collapseRE.ReplaceAllString(text, "_")
	return strings.Trim(text, "_")
}
