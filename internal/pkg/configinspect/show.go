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

package configinspect

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// Show writes the effective configuration as a document that loads
// back as the same settings, in the given format (FormatYAML or FormatJSON). It reads no
// local files. A configuration that did not resolve is reported as a failure.
func Show(r *appconfig.Resolved, resolveErr error, format OutputFormat) (Result, error) {
	if resolveErr != nil {
		return failure(r, resolveErr), nil
	}

	warnings := warningDiagnostics(r)
	warnings = append(warnings, notInDocumentWarnings(r)...)
	warnings = append(warnings, renamedOverrideWarnings(r)...)
	result := Result{Stderr: diagnosticsText(warnings)}

	document := effectiveDocument(r.Config, r.YAML.NamesOf())
	switch format {
	case FormatJSON:
		var err error
		result.Stdout, err = jsonText(document)
		return result, err
	case FormatYAML:
		return writeYAML(result, document)
	default:
		return result, unsupportedFormat("show", format)
	}
}

// writeYAML writes the document as YAML into the result's stdout.
func writeYAML(result Result, document *appconfig.YAMLConfig) (Result, error) {
	var node yaml.Node
	if err := node.Encode(document); err != nil {
		return result, err
	}

	quoteAmbiguousStrings(&node)
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(&node); err != nil {
		return result, err
	}

	if err := encoder.Close(); err != nil {
		return result, err
	}

	// Every invisible character sits in a double-quoted scalar, where an escape reads back as it.
	result.Stdout = []byte(escapeInvisible(buffer.String(),
		func(r rune) string { return fmt.Sprintf(`\U%08x`, r) },
		func(r rune) string { return fmt.Sprintf(`\u%04x`, r) }))
	return result, nil
}

// configMapReason is why a ConfigMap metric source is left out of the output.
const configMapReason = "the config file cannot name a ConfigMap, " +
	"so the output names the default metric file instead"

// notInDocumentWarnings lists the non-default settings that no config key
// can hold, because the document then does not describe them.
func notInDocumentWarnings(r *appconfig.Resolved) []diagnostic {
	defaults := defaultSettingValues()
	var warnings []diagnostic
	for _, view := range settingViews(r.Config, r.YAML.NamesOf()) {
		written := view.path != "" && !view.omitted && view.substituted == ""
		if written || reflect.DeepEqual(view.value, defaults[view.field]) {
			continue
		}

		source := sourceOf(r, r.OriginOf(view.field))
		var summary string
		switch {
		case view.substituted != "":
			summary = fmt.Sprintf("%s is %v, but %s", view.label(), view.value, view.substituted)
		case view.omitted:
			summary = leftOut("the key is left out while " + view.omittedBecause)
		case view.field == "MetricSource":
			summary = leftOut(configMapReason)
		default:
			summary = leftOut("the config file has no key for it")
		}

		warnings = append(warnings, diagnostic{Severity: severityWarning, Source: source, Summary: summary})
	}

	return warnings
}

// leftOut is the warning text for a setting that the output leaves out. The
// diagnostic prefix already names the setting and where it comes from.
func leftOut(reason string) string {
	return "left out of the output: " + reason
}

// renamedOverrideWarnings names each watch group whose name the document changes,
// because an override name may contain only letters, digits, _, - and . .
func renamedOverrideWarnings(r *appconfig.Resolved) []diagnostic {
	var warnings []diagnostic
	for _, rename := range appconfig.RenamedOverrides(r.Config) {
		source := sourceOf(r, r.OriginOf(fmt.Sprintf("WatchGroups[%d].Name", rename.Group)))
		warnings = append(warnings, diagnostic{
			Severity: severityWarning, Source: source,
			Summary: fmt.Sprintf("watch group %q is written as override %q, because an override name may "+
				"contain only letters, digits, _, - and .", rename.From, rename.To),
		})
	}

	return warnings
}

// quoteAmbiguousStrings puts double quotes around string values that are easy
// to misread as something else, such as ":9400" or "localhost:5555", and
// those with an invisible character, which Show writes as an escape. The
// quoted text loads back as the same string.
func quoteAmbiguousStrings(node *yaml.Node) {
	isString := node.Kind == yaml.ScalarNode && node.Tag == "!!str"
	if isString && (hasInvisible(node.Value) || node.Style == 0 && needsQuotes(node.Value)) {
		node.Style = yaml.DoubleQuotedStyle
	}

	for _, child := range node.Content {
		quoteAmbiguousStrings(child)
	}
}

// hasInvisible says text holds a character that isInvisible names.
func hasInvisible(text string) bool {
	return strings.ContainsFunc(text, isInvisible)
}

// needsQuotes says a plain string has a character that a reader may take for YAML syntax.
func needsQuotes(value string) bool {
	return strings.ContainsAny(value, ":#") || strings.ContainsAny(value[:min(1, len(value))], "{}[]*&!|>%@`'\",?-")
}
