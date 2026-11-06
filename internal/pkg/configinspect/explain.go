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
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

// explainData is the data of a config explain result.
type explainData struct {
	Path         string                `json:"path"`
	InputVersion int                   `json:"input_version,omitempty"`
	Config       *appconfig.YAMLConfig `json:"config"`
	// Settings lists every setting that is not at its built-in default, one
	// entry per key, with where its value came from.
	Settings []settingRow `json:"settings"`
	// FlagOnly lists the settings that only a flag can set (they have no
	// key) and that are not at their default.
	FlagOnly   []settingRow         `json:"flag_only"`
	Overrides  []inspectionOverride `json:"overrides"`
	Fields     []inspectionField    `json:"fields"`
	Warnings   []diagnostic         `json:"warnings"`
	NotChecked []string             `json:"not_checked"`

	// The rest is for the text view only.
	metrics    metricsSummary
	notChecked []notChecked
	// every and retention are the global collection interval and retention, as
	// written in the text summary.
	every, retention string
}

// settingRow says where one setting's value came from and which lower
// layers it replaced.
type settingRow struct {
	Path     string          `json:"path"`
	Value    any             `json:"value"`
	Source   settingSource   `json:"source"`
	Replaced []replacedValue `json:"replaced,omitempty"`
	Inactive string          `json:"inactive,omitempty"`
	// Note says how the running exporter treats an enrichment switch.
	Note string `json:"note,omitempty"`
	// override marks a key of a metrics override, which the text view
	// summarizes under the fields instead of listing.
	override bool
}

// replacedValue is a value that lost to a higher layer.
type replacedValue struct {
	settingSource
	Value any `json:"value"`
	// Raw is the text as it was set, left out when it is the same as Value.
	Raw string `json:"raw,omitempty"`
}

// millisecondFlags are the root flags whose value is a number of milliseconds,
// which the document writes as a duration.
var millisecondFlags = []string{
	appconfig.FlagCollectInterval, appconfig.FlagXIDCountWindowSize, appconfig.FlagClockEventsCountWindowSize,
}

// replacedEnvironmentValue is an environment value in the units the setting is
// shown in, with the raw text kept when the two differ.
func replacedEnvironmentValue(flagName, raw string) (value any, kept string) {
	if slices.Contains(millisecondFlags, flagName) {
		if milliseconds, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && milliseconds > 0 && milliseconds <= appconfig.MaxMilliseconds {
			return appconfig.FormatDuration(time.Duration(milliseconds) * time.Millisecond), raw
		}
	}

	return raw, ""
}

// inspectionOverride is one metrics override: how its fields are collected
// and what each of its match items resolved to.
type inspectionOverride struct {
	Name      string          `json:"name"`
	Every     string          `json:"every"`
	Retention *fieldRetention `json:"retention"`
	Match     []matchResolved `json:"match"`
}

// matchResolved is one match item of an override and the fields it selected.
type matchResolved struct {
	Item   string   `json:"item"`
	Fields []string `json:"fields"`
}

// inspectionField is one field the exporter would collect.
type inspectionField struct {
	Name string `json:"name"`
	// DCGMFieldID is set only for DCGM fields, not for exporter-owned ones.
	DCGMFieldID    int             `json:"dcgm_field_id,omitempty"`
	PrometheusType string          `json:"prometheus_type,omitempty"`
	Every          string          `json:"every,omitempty"`
	Retention      *fieldRetention `json:"retention,omitempty"`
	Window         string          `json:"window,omitempty"`
	// Override is the name of the metrics override the field belongs to.
	Override string `json:"override,omitempty"`
	// override is the index of that override, or -1 for none.
	override int
}

// fieldRetention is how much history DCGM keeps for a field.
type fieldRetention struct {
	MaxKeepAge     string `json:"max_keep_age"`
	MaxKeepSamples int64  `json:"max_keep_samples"`
}

// metricsSummary describes where the metric definitions came from, for the text view.
type metricsSummary struct {
	source string
	read   bool
}

// Explain writes where each setting came from and what will be collected, as
// text or json, with every setting and field when verbose. It reads the metric
// definitions and the web config file, and leaves the override checks to Validate.
func Explain(r *appconfig.Resolved, resolveErr error, format OutputFormat, verbose bool) (Result, error) {
	if resolveErr != nil {
		return failure(r, resolveErr), nil
	}

	inputs := readLocalInputs(r.Config, false)
	if len(inputs.problems) > 0 {
		return failure(r, errors.Join(inputs.problems...)), nil
	}

	addCountWindowWarnings(r, inputs)

	// The report below lists r.Warnings, so the count window warnings come first.
	data := buildExplainData(r, inputs, verbose)
	result := Result{Stderr: diagnosticsText(warningDiagnostics(r))}
	switch format {
	case FormatJSON:
		var err error
		result.Stdout, err = jsonText(inspectionEnvelope{FormatVersion: 1, Command: "config.explain", Data: data})
		return result, err
	case FormatText:
		result.Stdout = explainText(data, verbose)
		return result, nil
	default:
		return result, unsupportedFormat("explain", format)
	}
}

// buildExplainData collects the document, the settings and the fields. The JSON
// settings are the settings that differ from the defaults, or every setting
// when verbose.
func buildExplainData(r *appconfig.Resolved, inputs localInputs, verbose bool) explainData {
	data := explainData{
		Path:       r.Config.ConfigFile,
		Config:     effectiveDocument(r.Config, r.YAML.NamesOf()),
		Warnings:   warningDiagnostics(r),
		notChecked: notCheckedFor(r.Config),
		every:      appconfig.FormatDuration(time.Duration(r.Config.CollectInterval) * time.Millisecond),
		retention:  retentionText(retentionOf(r.Config.WatchRetention)),
	}
	if r.YAML != nil {
		data.InputVersion = r.YAML.Version
	}

	// Pick the origin lists: only the changed settings, or every setting when verbose.
	lists := inspectionSettings(r)
	data.Settings, data.FlagOnly = lists.changed, lists.flagOnlyChanged
	if verbose {
		data.Settings, data.FlagOnly = lists.all, lists.flagOnlyAll
	}

	data.NotChecked = notCheckedIDs(data.notChecked)

	// Describe the metric fields only when the metric definitions were read.
	data.Fields = []inspectionField{}
	data.metrics = metricsSummary{source: metricSourceText(r.Config.MetricSource)}
	if inputs.counters != nil {
		data.metrics.read = true
		data.Fields = inspectionFields(r.Config, inputs.counters)
	}

	data.Overrides = inspectionOverrides(r.Config, data.Fields)
	return data
}

// metricSourceText names the metric definitions' source.
func metricSourceText(source appconfig.MetricSource) string {
	switch source.Kind {
	case appconfig.MetricSourceFile:
		return source.File
	case appconfig.MetricSourceInline:
		return "inline definitions"
	default:
		return fmt.Sprintf("ConfigMap %s:%s", source.ConfigMap.Namespace, source.ConfigMap.Name)
	}
}

// settingLists holds the origin entries of every setting and, separately, the
// ones that are not at their built-in default; keyed settings and flag-only
// settings are kept apart.
type settingLists struct {
	all, changed                 []settingRow
	flagOnlyAll, flagOnlyChanged []settingRow
}

// settingLeaf is one key of a setting: a setting whose value is an object has
// one leaf per key, so every row of the output is a single value.
type settingLeaf struct {
	suffix string
	value  any
}

// settingLeaves splits an object value into its keys, in sorted order. Any
// other value, including a list, is one leaf.
func settingLeaves(value any) []settingLeaf {
	object, ok := value.(map[string]any)
	if !ok {
		return []settingLeaf{{value: value}}
	}

	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}

	sort.Strings(keys)
	var leaves []settingLeaf
	for _, key := range keys {
		for _, inner := range settingLeaves(object[key]) {
			leaves = append(leaves, settingLeaf{suffix: "." + key + inner.suffix, value: inner.value})
		}
	}

	return leaves
}

// leafValues maps field plus key suffix to the leaf value, for every view.
func leafValues(views []settingView) map[string]any {
	values := map[string]any{}
	for _, view := range views {
		for _, leaf := range settingLeaves(view.value) {
			values[view.field+leaf.suffix] = leaf.value
		}
	}

	return values
}

// unsetWorkloadFields are settings whose empty value means "not set"; the
// document and the lists show the default for them, which is then
// the default origin.
var unsetWorkloadFields = map[string]func(*appconfig.Config) string{
	"ContainerRuntimeSocket": func(c *appconfig.Config) string { return c.ContainerRuntimeSocket },
	"HPCJobMappingDir":       func(c *appconfig.Config) string { return c.HPCJobMappingDir },
}

// originForView is the origin of a setting's value. A workload path that no
// input set is shown with its default, so its origin is the default.
func originForView(r *appconfig.Resolved, view settingView) appconfig.SettingOrigin {
	origin := r.OriginOf(view.field)
	// An empty path that a flag or an environment variable gave stays that
	// input's value; only a path nothing set, or one a file switch cleared, is a default.
	if read, ok := unsetWorkloadFields[view.field]; ok && read(r.Config) == "" &&
		(origin.Kind == appconfig.OriginFile || origin.Kind == appconfig.OriginDefault) {
		return appconfig.SettingOrigin{Kind: appconfig.OriginDefault}
	}

	return origin
}

// fileSetValue says the config file really gave the setting a value. A path that
// the file's enrichment switch only cleared is not a value the file gave.
func fileSetValue(r *appconfig.Resolved, field string) bool {
	read, unset := unsetWorkloadFields[field]
	return !unset || r.FileConfig == nil || read(r.FileConfig) != ""
}

// inspectionSettings lists the origin of every key of every setting.
func inspectionSettings(r *appconfig.Resolved) settingLists {
	known := knownLeaves{defaults: leafValues(settingViews(appconfig.DefaultConfig(), appconfig.ConfigNames{}))}
	if r.FileConfig != nil {
		known.file = leafValues(settingViews(r.FileConfig, r.YAML.NamesOf()))
	}

	lists := settingLists{
		all: []settingRow{}, changed: []settingRow{},
		flagOnlyAll: []settingRow{}, flagOnlyChanged: []settingRow{},
	}

	for _, view := range settingViews(r.Config, r.YAML.NamesOf()) {
		origin := originForView(r, view)
		if view.hidden && origin.Kind == appconfig.OriginDefault {
			continue
		}

		for i, leaf := range settingLeaves(view.value) {
			entry, differs := settingEntry(r, view, origin, leaf, i == 0, known)
			lists.add(entry, view.path == "", differs)
		}
	}

	return lists
}

// knownLeaves holds the key values of the built-in defaults and of the config
// file alone, for comparing with the effective values.
type knownLeaves struct {
	defaults, file map[string]any
}

// renameNote explains why an override name is written differently than it was set.
const renameNote = "written as %q, because an override name may contain only letters, digits, _, - and ."

// settingEntry is the origin of one key of a setting, and whether it differs
// from the default or replaced a value from a lower layer. The environment
// values a flag replaced are listed on the first key only.
func settingEntry(
	r *appconfig.Resolved, view settingView, origin appconfig.SettingOrigin,
	leaf settingLeaf, first bool, known knownLeaves,
) (entry settingRow, differs bool) {
	entry = settingRow{
		Path: view.label() + leaf.suffix, Value: leaf.value, Source: sourceOf(r, origin),
		Inactive: inactive(r, view.field), Note: enrichmentNote(r, view.field),
		override: strings.HasPrefix(view.field, "WatchGroups["),
	}

	if view.renamedTo != "" {
		entry.Note = fmt.Sprintf(renameNote, view.renamedTo)
		// The terse text view lists override keys only under the fields, where
		// the note would be lost, so this row stays with the settings.
		entry.override = false
	}

	if first {
		entry.Replaced = replacedEnvironment(r, view)
	}

	fileOrigin, fromFile := r.FileOrigins[view.field]
	if fromFile && origin.Kind != appconfig.OriginFile && fileSetValue(r, view.field) {
		lost := replacedValue{settingSource: sourceOf(r, fileOrigin), Value: known.file[view.field+leaf.suffix]}
		entry.Replaced = append(entry.Replaced, lost)
	}

	defaultValue, hasDefault := known.defaults[view.field+leaf.suffix]
	differs = !hasDefault || !reflect.DeepEqual(leaf.value, defaultValue) || len(entry.Replaced) > 0
	return entry, differs
}

// add files an entry under the keyed or the flag-only list, and under the
// changed list when it differs from the default.
func (l *settingLists) add(entry settingRow, flagOnly, differs bool) {
	if flagOnly {
		l.flagOnlyAll = append(l.flagOnlyAll, entry)
		if differs {
			l.flagOnlyChanged = append(l.flagOnlyChanged, entry)
		}

		return
	}

	l.all = append(l.all, entry)
	if differs {
		l.changed = append(l.changed, entry)
	}
}

// replacedEnvironment lists the environment variables that a command line flag
// replaced, in the raw text they were set to.
func replacedEnvironment(r *appconfig.Resolved, view settingView) []replacedValue {
	var replaced []replacedValue
	for _, env := range r.ReplacedEnv[view.field] {
		shown, raw := replacedEnvironmentValue(view.flag, env.Value)
		source := settingSource{Kind: appconfig.OriginEnv, Name: env.Name}
		replaced = append(replaced, replacedValue{settingSource: source, Value: shown, Raw: raw})
	}

	return replaced
}

// inspectionFields lists every field the exporter would collect, with how often
// and how long DCGM keeps it.
func inspectionFields(config *appconfig.Config, set *counters.CounterSet) []inspectionField {
	fields := []inspectionField{}
	for _, counter := range set.WatchCounters() {
		// The pattern errors were reported when the overrides were checked, so
		// a bad pattern here leaves every field in the default group.
		watch, _ := devicewatchlistmanager.WatchOfField(config, counter.FieldName)
		field := inspectionField{
			override: watch.Group,
			Name:     counter.FieldName, DCGMFieldID: int(counter.FieldID), PrometheusType: counter.PromType,
			Every: appconfig.FormatDuration(watch.Interval), Retention: retentionOf(watch.Retention),
		}
		if watch.Group >= 0 {
			field.Override = config.WatchGroups[watch.Group].Name
		}

		fields = append(fields, field)
	}

	for _, counter := range set.ExporterCounters {
		field := inspectionField{Name: counter.FieldName, PrometheusType: counter.PromType, override: -1}
		if window, ok := appconfig.CountWindowOf(counter.FieldName); ok {
			field.Window = windowText(*window.Setting(config))
		}

		fields = append(fields, field)
	}

	return fields
}

// inspectionOverrides lists each metrics override with the fields every one of
// its match items selected, so a pattern can be checked against what it hit.
func inspectionOverrides(config *appconfig.Config, fields []inspectionField) []inspectionOverride {
	overrides := []inspectionOverride{}
	for index, group := range config.WatchGroups {
		overrides = append(overrides, inspectionOverride{
			Name:      group.Name,
			Every:     appconfig.FormatDuration(time.Duration(group.Interval) * time.Millisecond),
			Retention: retentionOf(group.Retention.Resolve(config.WatchRetention)),
			Match:     resolveMatchItems(group, index, fields),
		})
	}

	return overrides
}

// resolveMatchItems lists, for each match item of a watch group, the DCGM
// fields it selected. Only the group's own fields count: a field matched by an
// earlier override belongs to that one.
func resolveMatchItems(group appconfig.WatchGroup, index int, fields []inspectionField) []matchResolved {
	resolved := []matchResolved{}
	for _, item := range group.Fields {
		match := matchResolved{Item: item, Fields: []string{}}
		// The reader already rejected a bad item; it selects nothing here.
		if matcher, err := appconfig.ParseFieldMatcher([]string{item}, group.ExtendedMatch); err == nil {
			for _, field := range fields {
				if field.DCGMFieldID != 0 && field.override == index && devicewatchlistmanager.MatchesField(matcher, field.Name) {
					match.Fields = append(match.Fields, field.Name)
				}
			}
		}

		resolved = append(resolved, match)
	}

	return resolved
}

// retentionOf converts a retention policy to its inspection form.
func retentionOf(retention appconfig.WatchRetention) *fieldRetention {
	return &fieldRetention{
		MaxKeepAge: appconfig.FormatDuration(retention.MaxAge), MaxKeepSamples: retention.MaxSamples,
	}
}
