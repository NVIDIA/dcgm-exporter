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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// textWidth is the width that wrapped text lines stay within.
const textWidth = 100

// maxValueColumn caps the width of the value column; a longer value overflows its row.
const maxValueColumn = 40

// explainText writes the text view: the settings that differ from the
// defaults, a summary of the collected fields, and what was not checked.
// verbose adds every setting and every field.
func explainText(data explainData, verbose bool) []byte {
	var out bytes.Buffer
	file := "none (startup defaults)"
	if data.Path != "" {
		file = EscapeText(data.Path)
		if data.InputVersion == 1 {
			file += " (version 1, deprecated; removed in 4.10)"
		}
	}

	fmt.Fprintf(&out, "Config file   %s\n\n", file)
	heading := "Settings that differ from defaults"
	if verbose {
		heading = "All settings"
	}

	out.WriteString(heading + "\n")
	writeSettingSections(&out, data.Settings, verbose)
	if len(data.FlagOnly) > 0 {
		out.WriteString("\nFlag-only settings (no config file key)\n")
		writeSettingRows(&out, data.FlagOnly, func(origin settingRow) string { return origin.Path })
	}

	writeFieldSummary(&out, data, verbose)
	writeWrappedList(&out, "\nNot checked (needs the running exporter): ", notCheckedLabels(data.notChecked))
	if !verbose {
		out.WriteString("Use --verbose for all settings and all fields.\n")
	}

	return out.Bytes()
}

// notCheckedLabels joins the labels of the checks that could not run.
func notCheckedLabels(list []notChecked) string {
	labels := make([]string, 0, len(list))
	for _, item := range list {
		labels = append(labels, item.label)
	}

	return strings.Join(labels, ", ")
}

// writeWrappedList writes lead and then text, breaking the text after a comma
// before a line passes textWidth; continuation lines are indented by two spaces.
func writeWrappedList(out *bytes.Buffer, lead, text string) {
	line := strings.TrimLeft(lead, "\n")
	out.WriteString(lead[:len(lead)-len(line)])
	for i, word := range strings.SplitAfter(text, ", ") {
		if i > 0 && utf8.RuneCountInString(line)+utf8.RuneCountInString(word) > textWidth {
			out.WriteString(strings.TrimRight(line, " ") + "\n")
			line = "  "
		}

		line += word
	}

	out.WriteString(strings.TrimRight(line, " ") + "\n")
}

// writeSettingSections writes the settings under a heading for each top-level
// key (metrics[0], outputs, sources, ...), with paths relative to the heading.
// Unless verbose, the keys of a metrics override are summarized under the
// fields, not listed.
func writeSettingSections(out *bytes.Buffer, origins []settingRow, verbose bool) {
	var shown []settingRow
	for _, origin := range origins {
		if verbose || !origin.override {
			shown = append(shown, origin)
		}
	}

	if len(shown) == 0 {
		out.WriteString("  none\n")
		return
	}

	// Rows are grouped by section, in the order the file lists them.
	var order []string
	bySection := map[string][]settingRow{}
	for _, origin := range shown {
		section, _ := splitSection(origin.Path)
		if _, seen := bySection[section]; !seen {
			order = append(order, section)
		}

		bySection[section] = append(bySection[section], origin)
	}

	sort.SliceStable(order, func(i, j int) bool { return sectionRank(order[i]) < sectionRank(order[j]) })
	for _, section := range order {
		out.WriteString(EscapeText(section) + "\n")
		writeSettingRows(out, bySection[section], func(origin settingRow) string {
			_, relative := splitSection(origin.Path)
			return relative
		})
	}
}

// sectionRanks puts the sections in the order of a file.
var sectionRanks = map[string]int{"metrics[0]": 0, "outputs": 1, "sources": 2, "server": 3, "observability": 4, "debug_dump": 5}

// sectionRank is the position of a section; an unknown one goes last.
func sectionRank(section string) int {
	if rank, found := sectionRanks[section]; found {
		return rank
	}

	return len(sectionRanks)
}

// splitSection splits "sources.dcgm.watch.max_keep_age" into "sources" and
// "dcgm.watch.max_keep_age"; a quoted key such as outputs["a.b"] stays whole.
func splitSection(path string) (section, relative string) {
	if end := strings.IndexAny(path, ".["); end > 0 {
		if path[end] == '[' && strings.HasPrefix(path[end:], `["`) {
			return path[:end], path[end:]
		}

		if path[end] == '[' {
			// An index belongs to the section name: metrics[0].
			if close := strings.IndexByte(path, ']'); close >= 0 {
				end = close + 1
			}
		}

		if end < len(path) && path[end] == '.' {
			return path[:end], path[end+1:]
		}

		return path[:end], strings.TrimPrefix(path[end:], ".")
	}

	return path, ""
}

// writeSettingRows writes one row per setting as aligned columns: the name
// (from name), the value and the source. A replaced value and an inactive
// reason go on lines of their own under the row.
func writeSettingRows(out *bytes.Buffer, origins []settingRow, name func(settingRow) string) {
	nameWidth, valueWidth := 0, 0
	for _, origin := range origins {
		nameWidth = max(nameWidth, utf8.RuneCountInString(EscapeText(name(origin))))
		shown := settingValueText(origin.Path, origin.Value)
		valueWidth = min(max(valueWidth, utf8.RuneCountInString(shown)), maxValueColumn)
	}

	for _, origin := range origins {
		fmt.Fprintf(out, "  %s  %s  %s\n", pad(EscapeText(name(origin)), nameWidth),
			pad(settingValueText(origin.Path, origin.Value), valueWidth), sourceText(origin))

		for _, lost := range origin.Replaced {
			fmt.Fprintf(out, "    replaces %s value: %s%s\n", sourceWithKey(lost.settingSource, origin.Path),
				settingValueText(lost.Path, lost.Value), rawSuffix(lost.Raw))
		}

		if origin.Note != "" {
			fmt.Fprintf(out, "    note: %s\n", EscapeText(origin.Note))
		}

		if origin.Inactive != "" {
			fmt.Fprintf(out, "    inactive: %s\n", EscapeText(origin.Inactive))
		}
	}
}

// rawSuffix is the text as it was set, in parentheses, when it differs from the shown value.
func rawSuffix(raw string) string {
	if raw == "" {
		return ""
	}

	return " (" + EscapeText(raw) + ")"
}

// pad fills text with spaces to width, counting characters.
func pad(text string, width int) string {
	if missing := width - utf8.RuneCountInString(text); missing > 0 {
		return text + strings.Repeat(" ", missing)
	}

	return text
}

// sourceText is the short form of a row's source. A file source is "file:LINE",
// because the header names the file; it also names the key when another key set
// the value, such as the enrichment switch that clears a path.
func sourceText(origin settingRow) string {
	return sourceWithKey(origin.Source, origin.Path)
}

// sourceWithKey is compactSource, and names the key of the file when a
// different key than the row's set the value, such as a version 1 key or the
// enrichment switch that clears a path.
func sourceWithKey(source settingSource, rowPath string) string {
	text := compactSource(source)
	if source.Kind == appconfig.OriginFile && source.Path != "" && source.Path != rowPath {
		text += " (" + EscapeText(source.Path) + ")"
	}

	return text
}

// compactSource is "file:LINE" for the config file (the header names it), and
// the flag or environment variable otherwise.
func compactSource(source settingSource) string {
	if source.Kind == appconfig.OriginFile {
		if source.Line > 0 {
			return fmt.Sprintf("file:%d", source.Line)
		}

		return "file"
	}

	return EscapeText(source.describe())
}

// settingValueText is valueText, except that inline metric definitions, which
// are long, are counted; the field list shows them in full.
func settingValueText(path string, value any) string {
	if definitions, ok := value.([]any); ok && (path == "metrics[0].fields" || path == "metrics.fields") {
		return plural(len(definitions), "inline definition")
	}

	return valueText(value)
}

// valueText writes a setting value on one line: strings as they are, everything else as JSON.
func valueText(value any) string {
	if text, ok := value.(string); ok {
		if text == "" {
			return `""`
		}

		return EscapeText(text)
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return EscapeText(fmt.Sprint(value))
	}

	return EscapeText(string(encoded))
}

// writeFieldSummary writes one line for the fields and one for each override;
// verbose adds what each match item selected and every field. When the metric
// definitions were not read there are no fields to count, but the overrides are
// still listed.
func writeFieldSummary(out *bytes.Buffer, data explainData, verbose bool) {
	out.WriteByte('\n')
	counts := map[int]int{}
	if data.metrics.read {
		for _, field := range data.Fields {
			if field.override >= 0 {
				counts[field.override]++
			}
		}

		fmt.Fprintf(out, "Fields   %d from %s, every %s, retention %s\n",
			len(data.Fields), EscapeText(data.metrics.source), data.every, data.retention)
	} else {
		fmt.Fprintf(out, "Fields   not read: %s\n", EscapeText(data.metrics.source))
	}

	for index, override := range data.Overrides {
		count := ""
		if data.metrics.read {
			count = "  (" + plural(counts[index], "field") + ")"
		}

		fmt.Fprintf(out, "  override %s%s  every %s, retention %s\n",
			overrideLabel(override.Name), count, override.Every, retentionText(override.Retention))
		if verbose && data.metrics.read {
			writeMatchLines(out, override)
		}
	}

	if verbose && data.metrics.read {
		writeFieldLines(out, data.Fields)
	}
}

// writeMatchLines writes the fields that each match item of an override selected.
func writeMatchLines(out *bytes.Buffer, override inspectionOverride) {
	for _, match := range override.Match {
		selected := "no fields"
		if len(match.Fields) > 0 {
			selected = strings.Join(match.Fields, ", ")
		}

		fmt.Fprintf(out, "    match %s -> %s\n", EscapeText(match.Item), EscapeText(selected))
	}
}

// retentionText writes "10m, no sample limit" or "10m, 5 samples".
func retentionText(retention *fieldRetention) string {
	if retention == nil {
		return ""
	}

	if retention.MaxKeepSamples == 0 {
		return retention.MaxKeepAge + ", no sample limit"
	}

	return fmt.Sprintf("%s, %d samples", retention.MaxKeepAge, retention.MaxKeepSamples)
}

// writeFieldLines writes the collected fields as aligned columns: the name, the
// Prometheus type, the DCGM field id, how it is collected, and the window or
// override that applies.
func writeFieldLines(out *bytes.Buffer, fields []inspectionField) {
	rows := make([][]string, 0, len(fields))
	widths := make([]int, 5)
	for _, field := range fields {
		row := make([]string, 5)
		row[0], row[1] = EscapeText(field.Name), EscapeText(field.PrometheusType)
		if field.DCGMFieldID != 0 {
			row[2] = fmt.Sprintf("id %d", field.DCGMFieldID)
		}

		if field.Every != "" {
			row[3] = fmt.Sprintf("every %s, retention %s", field.Every, retentionText(field.Retention))
		}

		switch {
		case field.Window != "":
			row[4] = "window " + field.Window
		case field.Override != "":
			row[4] = "override " + overrideLabel(field.Override)
		}

		for i, cell := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}

		rows = append(rows, row)
	}

	for _, row := range rows {
		line := "    "
		for i, cell := range row {
			line += pad(cell, widths[i]) + "  "
		}

		out.WriteString(strings.TrimRight(line, " ") + "\n")
	}
}

// overrideLabel names an override for text output. An override the file left
// unnamed is known by its path, so the label says it was not named.
func overrideLabel(name string) string {
	if appconfig.IsGeneratedOverrideName(name) {
		return name + " (unnamed)"
	}

	return EscapeText(name)
}
