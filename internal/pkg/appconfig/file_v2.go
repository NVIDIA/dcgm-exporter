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

package appconfig

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAMLMetricEntry is one named metric set: where its definitions come from,
// how often it is collected, and per-field overrides.
type YAMLMetricEntry struct {
	// Name is required and identifies the entry; this build supports one entry.
	Name      string            `yaml:"name" json:"name"`
	File      string            `yaml:"file,omitempty" json:"file,omitempty"`
	Fields    []YAMLMetricField `yaml:"fields,omitempty" json:"fields,omitempty"`
	Every     string            `yaml:"every,omitempty" json:"every,omitempty"`
	Overrides []YAMLOverride    `yaml:"overrides,omitempty" json:"overrides,omitempty"`
	Entities  *YAMLEntities     `yaml:"entities,omitempty" json:"entities,omitempty"`

	Enrichment *YAMLEnrichment `yaml:"enrichment,omitempty" json:"enrichment,omitempty"`
}

// YAMLMetricField is one inline metric definition in YAML form.
type YAMLMetricField struct {
	Name           string `yaml:"name" json:"name"`
	PrometheusType string `yaml:"prometheus_type" json:"prometheus_type"`
	Help           string `yaml:"help" json:"help"`
}

// YAMLOverride changes how some fields are collected. It sets either a
// collection interval (with optional retention) or a counter window.
type YAMLOverride struct {
	Name    string               `yaml:"name,omitempty" json:"name,omitempty"`
	Match   []string             `yaml:"match" json:"match"`
	Every   string               `yaml:"every,omitempty" json:"every,omitempty"`
	Window  string               `yaml:"window,omitempty" json:"window,omitempty"`
	Sources *YAMLOverrideSources `yaml:"sources,omitempty" json:"sources,omitempty"`
}

// YAMLOverrideSources holds source settings that can differ per override.
type YAMLOverrideSources struct {
	DCGM *YAMLOverrideDCGMSource `yaml:"dcgm,omitempty" json:"dcgm,omitempty"`
}

// YAMLOverrideDCGMSource holds the DCGM settings an override may change.
type YAMLOverrideDCGMSource struct {
	Watch *YAMLDCGMWatch `yaml:"watch,omitempty" json:"watch,omitempty"`
}

// YAMLDCGMWatch is the presence-aware YAML form of a field-watch retention policy.
// Pointer fields tell an omitted value from an explicit zero until it is applied to Config.
type YAMLDCGMWatch struct {
	MaxKeepAge     *string `yaml:"max_keep_age,omitempty" json:"max_keep_age,omitempty"`
	MaxKeepSamples *int64  `yaml:"max_keep_samples,omitempty" json:"max_keep_samples,omitempty"`
}

// parseYAMLV2Config reads a file in the current format. It rejects unknown keys
// and any setting that applying the file would reject.
func parseYAMLV2Config(root *yaml.Node, data []byte) (*YAMLConfig, error) {
	if err := rejectUnknownKeys(root, reflect.TypeOf(YAMLConfig{})); err != nil {
		return nil, err
	}

	var config YAMLConfig
	if err := decodeYAMLConfig(data, &config); err != nil {
		return nil, err
	}

	// Applying onto a scratch Config is the validation: it reports every error
	// that ApplyTo could report later.
	if err := config.applyV2(&Config{}, nil); err != nil {
		return nil, err
	}

	return &config, nil
}

// applyV2 writes every setting from the config file into config. Settings the
// file leaves out keep the value config already has.
//
// A non-nil rec also records the YAML key behind each setting that is written.
func (y *YAMLConfig) applyV2(config *Config, rec originRecorder) error {
	if err := y.applySources(config, rec); err != nil {
		return err
	}

	if err := y.applyWorkloadSources(config, rec); err != nil {
		return err
	}

	y.applyObservabilityMetrics(config, rec)

	if err := y.applyServerSettings(config, rec); err != nil {
		return err
	}

	if err := y.applyLogs(config, rec); err != nil {
		return err
	}

	if err := y.applyDebugDump(config, rec); err != nil {
		return err
	}

	// Metrics and outputs: check the shape first, then apply.
	if len(y.Metrics) > 1 {
		return keyErrorf("metrics", "only one entry is supported, got %d", len(y.Metrics))
	}

	if err := y.validateOutputs(); err != nil {
		return err
	}

	// applyListen reads the single output, so it must follow validateOutputs.
	y.applyListen(config, rec)

	if len(y.Metrics) == 1 {
		if err := y.Metrics[0].apply(config, "metrics[0]", rec); err != nil {
			return err
		}

		if err := y.Metrics[0].applyEnrichment(config, y.Sources, rec); err != nil {
			return err
		}

		return y.Metrics[0].Entities.apply(config, "metrics[0].entities", rec)
	}

	return nil
}

// Path suffixes for the interval, match and window keys, used when naming a key in an error or origin.
const (
	suffixEvery  = ".every"
	suffixMatch  = ".match"
	suffixWindow = ".window"
)

// isWindow reports whether the override sets a counter window.
func (o *YAMLOverride) isWindow() bool {
	return strings.TrimSpace(o.Window) != ""
}

// watchGroupIndexes says, for each override, the index of the watch group it
// becomes, or -1 for a window override. Applying and origin tracking both use
// it, so they agree on the group order.
func (m *YAMLMetricEntry) watchGroupIndexes() []int {
	indexes := make([]int, len(m.Overrides))
	next := 0

	for i := range m.Overrides {
		if m.Overrides[i].isWindow() {
			indexes[i] = -1
			continue
		}
		indexes[i] = next
		next++
	}

	return indexes
}

// apply applies one metric entry: its source, interval, interval overrides
// (as watch groups) and counter windows.
func (m *YAMLMetricEntry) apply(config *Config, entryPath string, rec originRecorder) error {
	if strings.TrimSpace(m.Name) == "" {
		return keyErrorf(entryPath+".name", msgRequired)
	}

	source, err := m.metricSource(entryPath)
	if err != nil {
		return err
	}

	config.MetricSource = source
	if len(m.Fields) > 0 {
		rec.record("MetricSource", entryPath+".fields")
	} else {
		rec.record("MetricSource", entryPath+".file")
	}

	if err := m.applyEvery(config, entryPath, rec); err != nil {
		return err
	}

	return m.applyOverrides(config, entryPath, rec)
}

// applyEvery sets the collection interval when the entry has an every value.
func (m *YAMLMetricEntry) applyEvery(config *Config, entryPath string, rec originRecorder) error {
	if strings.TrimSpace(m.Every) == "" {
		return nil
	}

	interval, err := parseYAMLDurationMillis(m.Every)
	if err != nil {
		return keyWrap(entryPath+suffixEvery, err)
	}

	config.CollectInterval = interval
	rec.record("CollectInterval", entryPath+suffixEvery)

	return nil
}

// applyOverrides turns the entry's overrides into watch groups and counter windows.
func (m *YAMLMetricEntry) applyOverrides(config *Config, entryPath string, rec originRecorder) error {
	// The overrides are rebuilt from the file; window overrides set counter
	// windows and the rest become watch groups.
	config.WatchGroups = nil
	config.WindowOverrides = nil
	rec.record("WatchGroups", entryPath)
	seenNames := map[string]int{}
	seenWindows := map[string]int{}
	groupIndexes := m.watchGroupIndexes()

	for i := range m.Overrides {
		override := &m.Overrides[i]
		overridePath := fmt.Sprintf("%s.overrides[%d]", entryPath, i)

		if err := override.validateShape(overridePath, seenNames, i); err != nil {
			return err
		}

		if groupIndexes[i] < 0 {
			if err := override.applyWindow(config, overridePath, seenWindows, i, rec); err != nil {
				return err
			}

			continue
		}

		group, err := override.watchGroup(overridePath)
		if err != nil {
			return err
		}

		group.Path = overridePath
		config.WatchGroups = append(config.WatchGroups, group)
		override.recordWatchGroup(rec, WatchGroupField(groupIndexes[i]), overridePath)
		rec.record("WatchGroups", entryPath+".overrides")
	}

	return nil
}

// recordWatchGroup records where each setting of one watch group came from.
// Retention is recorded only for the values the override sets.
func (o *YAMLOverride) recordWatchGroup(rec originRecorder, field, overridePath string) {
	if strings.TrimSpace(o.Name) != "" {
		rec.record(field+".Name", overridePath+".name")
	}
	rec.record(field+".Interval", overridePath+suffixEvery)
	rec.record(field+".Fields", overridePath+suffixMatch)
	if o.Sources != nil && o.Sources.DCGM != nil {
		o.Sources.DCGM.Watch.record(rec, field+".Retention", overridePath+".sources.dcgm.watch")
	}
}

// metricSource converts the entry's file or inline rows into the runtime metric source.
func (m *YAMLMetricEntry) metricSource(entryPath string) (MetricSource, error) {
	file := strings.TrimSpace(m.File)
	if (file != "") == (len(m.Fields) > 0) {
		return MetricSource{}, keyErrorf(entryPath, "exactly one of file or fields must be set")
	}

	if file != "" {
		return MetricSource{Kind: MetricSourceFile, File: file}, nil
	}

	rows := make([]yamlMetricRow, len(m.Fields))
	for i, field := range m.Fields {
		rows[i] = yamlMetricRow(field)
	}

	fields, err := inlineMetricFields(rows, entryPath+".fields", "prometheus_type")
	if err != nil {
		return MetricSource{}, err
	}

	return MetricSource{Kind: MetricSourceInline, Fields: fields}, nil
}

// validateShape checks the parts of an override that do not depend on whether
// it sets an interval or a window.
func (o *YAMLOverride) validateShape(overridePath string, seenNames map[string]int, index int) error {
	if name := strings.TrimSpace(o.Name); name != "" {
		if !validOverrideName(name) {
			return keyErrorf(overridePath+".name", "%q may contain only letters, digits, _, - and . (dot)", name)
		}

		if first, ok := seenNames[name]; ok {
			entryPath := overridePath[:strings.LastIndex(overridePath, ".overrides[")]
			return keyErrorf(overridePath+".name", "%q is already used by %s.overrides[%d]", name, entryPath, first)
		}

		seenNames[name] = index
	}

	if len(o.Match) == 0 {
		return keyErrorf(overridePath+suffixMatch, "must not be empty")
	}

	if _, err := o.matcher(); err != nil {
		return matchKeyError(overridePath, err)
	}

	// An override sets exactly one of an interval or a window.

	hasEvery := strings.TrimSpace(o.Every) != ""
	switch {
	case hasEvery && o.isWindow():
		return keyErrorf(overridePath, "set either every or window, not both")
	case !hasEvery && !o.isWindow():
		return keyErrorf(overridePath, "one of every or window is required")
	case o.isWindow() && o.Sources != nil:
		return keyErrorf(overridePath+".sources", "not allowed together with window")
	}

	return nil
}

// watchGroup converts an interval override into a runtime watch group.
func (o *YAMLOverride) watchGroup(overridePath string) (WatchGroup, error) {
	interval, err := parseYAMLDurationMillis(o.Every)
	if err != nil {
		return WatchGroup{}, keyWrap(overridePath+suffixEvery, err)
	}

	name := strings.TrimSpace(o.Name)
	if name == "" {
		name = overridePath
	}

	fields := make([]string, len(o.Match))
	for i, item := range o.Match {
		fields[i] = strings.TrimSpace(item)
	}

	var retention WatchRetentionOverride
	if o.Sources != nil && o.Sources.DCGM != nil && o.Sources.DCGM.Watch != nil {
		watchPath := overridePath + ".sources.dcgm.watch"
		retention, err = o.Sources.DCGM.Watch.retentionOverride(watchPath)
		if err != nil {
			return WatchGroup{}, err
		}

		if err := rejectZeroRetention(retention, watchPath, "max_keep_age", "max_keep_samples"); err != nil {
			return WatchGroup{}, err
		}
	}

	return WatchGroup{Name: name, Interval: interval, Fields: fields, Retention: retention, ExtendedMatch: true}, nil
}

// applyWindow sets the sampling window of the XID or clock-event count counter.
// The counter does not have to be in the metric file, and the override may
// match nothing. The override is kept in config.WindowOverrides so the metric
// source can be checked by the watch-list manager once it is read.
func (o *YAMLOverride) applyWindow(
	config *Config, overridePath string, seen map[string]int, index int, rec originRecorder,
) error {
	window, err := parseYAMLDurationMillis(o.Window)
	if err != nil {
		return keyWrap(overridePath+suffixWindow, err)
	}

	matcher, err := o.matcher()
	if err != nil {
		return matchKeyError(overridePath, err)
	}

	config.WindowOverrides = append(config.WindowOverrides, WindowOverride{Path: overridePath, Match: matcher})

	// Each window counter the override matches gets this window, and no two
	// overrides may set the same counter.
	for _, countWindow := range CountWindows {
		counter := countWindow.Counter
		if !matcher.Matches(counter) {
			continue
		}

		if first, ok := seen[counter]; ok {
			return keyErrorf(overridePath+suffixWindow, "%s already has a window in overrides[%d]", counter, first)
		}

		seen[counter] = index
		*countWindow.Setting(config) = window
		rec.record(countWindow.Field, overridePath+suffixWindow)
	}

	return nil
}

// matcher parses the override's match items, which narrow its own members.
func (o *YAMLOverride) matcher() (FieldMatcher, error) {
	return ParseFieldMatcher(o.Match, true)
}

// retentionOverride parses and validates retention values while keeping which ones were set.
func (r *YAMLDCGMWatch) retentionOverride(watchPath string) (WatchRetentionOverride, error) {
	return parseRetention(r.MaxKeepAge, r.MaxKeepSamples, watchPath, "max_keep_age", "max_keep_samples")
}

// validOverrideName says a user-given override name uses only letters, digits,
// _, - and . . An unnamed override is named by its YAML path, which always has
// brackets, so a user name can never equal a generated one.
func validOverrideName(name string) bool {
	for _, r := range name {
		letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

// record records only the retention values the file sets, under field.
func (r *YAMLDCGMWatch) record(rec originRecorder, field, watchPath string) {
	if r == nil {
		return
	}
	if r.MaxKeepAge != nil {
		rec.record(field+".MaxAge", watchPath+".max_keep_age")
	}
	if r.MaxKeepSamples != nil {
		rec.record(field+".MaxSamples", watchPath+".max_keep_samples")
	}
}

// matchKeyError locates a bad match item by its position in the list.
func matchKeyError(overridePath string, err error) error {
	var item *FieldPatternError
	if errors.As(err, &item) {
		return &keyError{Path: fmt.Sprintf("%s%s[%d]", overridePath, suffixMatch, item.Index), Msg: item.Err.Error(), Cause: item.Err}
	}

	return keyWrap(overridePath+suffixMatch, err)
}
