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
	"encoding/json"
	"fmt"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// settingView is one effective setting as inspection reports it: the version 2
// key that holds it (empty when only a flag can set it) and its value as that
// key would be written.
type settingView struct {
	// field is the name that origin tracking uses for the setting.
	field string
	// flag is the root flag that sets the setting, empty when none does.
	flag string
	path string
	// omitted says the document leaves the key out in this configuration, for
	// example the embedded log level while that log is off. The key exists; the
	// setting cannot be written.
	omitted bool
	// hidden says the key is left out and no input set it, so there is nothing to list.
	hidden bool
	// omittedBecause says why, for the warning.
	omittedBecause string
	// omittedLabel is the row's label, when the key's own path would give the
	// same setting different names, such as the index of a window override.
	omittedLabel string
	// substituted says what the document writes instead of the setting's value,
	// when it cannot write the value.
	substituted string
	// renamedTo is the name the document writes for a watch group whose own name
	// an override cannot carry; value is the name the input gave.
	renamedTo string
	// value is a plain JSON value: a string, number, boolean, list or map.
	value any
}

// label names the setting for people: its key, or its flag when it has no key.
func (v settingView) label() string {
	if v.omittedLabel != "" {
		return v.omittedLabel
	}

	if v.path != "" {
		return v.path
	}

	return "--" + v.flag
}

// markOmitted says the document leaves the setting's key out, for the reason
// the row gives. The row's label was already copied to the view.
func (v *settingView) markOmitted(row settingSpec) {
	v.omitted, v.omittedBecause, v.hidden = true, row.omittedBecause, !row.showWhenOmitted
}

// settingSpec is a setting before its value is looked up.
type settingSpec struct {
	field, flag, path string
	// lookup is the path to read the value from when it differs from path,
	// because the real output name may contain dots.
	lookup string
	// omittedBecause names the feature whose being off makes the document leave
	// the key out. A row with a fallback value must have one.
	omittedBecause string
	// forceOmit says the document writes a different value than the setting
	// has, so the setting is read from fallback and reported as left out.
	forceOmit bool
	// showWhenOmitted keeps the row in the lists even when the document leaves
	// the key out, because the setting has an effective value to show.
	showWhenOmitted bool
	// omittedLabel names the setting, when the key's
	// own path would be misleading.
	omittedLabel string
	// substitution is the text for a forceOmit row.
	substitution string
	// renamedTo is the name the document writes instead of the one in fallback.
	renamedTo string
	// fallback is the value used when the document has no key for the setting
	// in this configuration.
	fallback any
}

// settingSpecs lists every setting inspection reports: the settings table of
// appconfig, each with how the document presents it.
func settingSpecs(c *appconfig.Config, names appconfig.ConfigNames) []settingSpec {
	_, windows := appconfig.OverridePositions(c)
	settings := appconfig.SettingKeys(c, names)
	rows := make([]settingSpec, 0, len(settings))
	for _, setting := range settings {
		row := settingSpec{field: setting.Field, flag: setting.Flag, path: setting.Key}
		presentRow(&row, c, windows)
		rows = append(rows, row)
	}

	return rows
}

// presentRow fills in how the document shows a setting whose value or key
// needs more than the settings table says. windows holds the position of each
// counter window override that the document writes.
func presentRow(row *settingSpec, c *appconfig.Config, windows map[string]int) {
	switch row.field {
	case "MetricSource":
		if c.MetricSource.Kind == appconfig.MetricSourceConfigMap {
			row.fallback = c.MetricSource.ConfigMap.Namespace + ":" + c.MetricSource.ConfigMap.Name
		}
	case appconfig.FieldXIDCountWindowSize, appconfig.FieldClockEventsCountWindowSize:
		presentCountWindow(row, c, windows)
	case "Address":
		row.lookup = "outputs." + appconfig.DefaultOutputName + ".listen"
	case "GPUBindUnbindPollInterval":
		row.omittedBecause = "GPU bind/unbind watching is off"
		row.fallback = c.GPUBindUnbindPollInterval.String()
		row.forceOmit = !c.EnableGPUBindUnbindWatch && c.GPUBindUnbindPollInterval <= 0
		row.substitution = "written as 1s because the config file rejects a value of 0s or less; " +
			"it has no effect while GPU bind/unbind watching is off"
	case "DCGMLogLevel":
		row.fallback = appconfig.EmbeddedLogLevelName(c.DCGMLogLevel)
		row.omittedBecause = "the embedded DCGM log is off"
	case "EnableDCGMLog":
		row.fallback, row.omittedBecause = c.EnableDCGMLog, "the embedded DCGM log is off"
	case "UseOldNamespace":
		row.fallback = c.UseOldNamespace
	case "NoHostname":
		row.fallback = c.NoHostname
	case "ReplaceBlanksInModelName":
		row.fallback = c.ReplaceBlanksInModelName
	case "UseFakeGPUs":
		row.fallback = c.UseFakeGPUs
	case "DisableStartupValidate":
		row.fallback = c.DisableStartupValidate
	default:
		presentWatchGroupName(row, c)
	}
}

// presentCountWindow shows a counter's sampling window. The document writes the
// window as an override only while it differs from the default; at the default
// the row is shown under a label that names the counter.
func presentCountWindow(row *settingSpec, c *appconfig.Config, windows map[string]int) {
	window := countWindowOfField(row.field)
	counter := window.Counter

	_, written := windows[counter]
	row.showWhenOmitted = true
	row.fallback = windowText(*window.Setting(c))
	row.omittedBecause = "it is at its default"
	row.forceOmit = !written
	row.omittedLabel = "metrics[0].overrides[" + counter + "].window"
	if row.path == "" {
		// A window at its default has no key in the document. The row is still
		// a document row, listed under its label rather than as a flag-only one.
		row.path = row.omittedLabel
	}
}

// countWindowOfField returns the count window whose setting field is field.
func countWindowOfField(field string) appconfig.CountWindow {
	for _, window := range appconfig.CountWindows {
		if window.Field == field {
			return window
		}
	}

	return appconfig.CountWindow{}
}

// presentWatchGroupName gives the name row of an override its fallback: the
// group's name, for when the document leaves the key out.
func presentWatchGroupName(row *settingSpec, c *appconfig.Config) {
	var index int
	if _, err := fmt.Sscanf(row.field, "WatchGroups[%d].Name", &index); err == nil && index < len(c.WatchGroups) {
		row.fallback = c.WatchGroups[index].Name

		for _, rename := range appconfig.RenamedOverrides(c) {
			if rename.Group == index {
				row.renamedTo = rename.To
			}
		}
	}
}

// settingViews looks up the value of every setting row for c.
func settingViews(c *appconfig.Config, names appconfig.ConfigNames) []settingView {
	// Values are read from a document that uses the default output name, so an
	// output name with a dot cannot break the lookup.
	document := documentValues(c, appconfig.ConfigNames{Metrics: names.Metrics})
	rows := settingSpecs(c, names)
	views := make([]settingView, 0, len(rows))
	for _, row := range rows {
		views = append(views, viewOfRow(row, document))
	}

	return views
}

// viewOfRow turns a setting row into a view: its value in the document, or why the
// document leaves it out.
func viewOfRow(row settingSpec, document any) settingView {
	view := settingView{
		field: row.field, flag: row.flag, path: row.path, value: row.fallback, omittedLabel: row.omittedLabel,
	}
	if row.forceOmit {
		// The document does not hold this value: it writes a different one, or
		// leaves the key out, and the key's path may now belong to another setting.
		if row.substitution != "" {
			view.substituted = row.substitution
		} else {
			view.markOmitted(row)
		}

		return view
	}

	if row.renamedTo != "" {
		// The document holds a changed name, so the name the input gave is
		// kept from the config instead.
		view.renamedTo = row.renamedTo

		return view
	}

	if row.path == "" {
		return view
	}

	lookup := row.lookup
	if lookup == "" {
		lookup = row.path
	}

	value, found := appconfig.LookupValue(document, lookup)
	switch {
	case found:
		view.value = value
	case row.fallback != nil:
		// The document leaves this key out in this configuration, for example
		// the embedded log block while that log is off.
		view.markOmitted(row)
	default:
		message := "configinspect: setting %s names the key %s, which is not in the effective config"
		panic(fmt.Sprintf(message, row.field, row.path))
	}

	return view
}

// documentValues renders the document of c as plain JSON values.
func documentValues(c *appconfig.Config, names appconfig.ConfigNames) any {
	data, err := json.Marshal(effectiveDocument(c, names))
	if err != nil {
		panic("configinspect: the effective config cannot be encoded: " + err.Error())
	}

	var values any
	if err := json.Unmarshal(data, &values); err != nil {
		panic("configinspect: the effective config cannot be decoded: " + err.Error())
	}

	return values
}

// windowText writes a counter window as the document would.
func windowText(milliseconds int) string {
	return appconfig.FormatDuration(time.Duration(milliseconds) * time.Millisecond)
}
