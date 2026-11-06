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

package devicewatchlistmanager

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// WindowOverrideError is a window override that selects a field that is not a
// window counter. It is the only problem CheckOverrides reports that is not
// about the watch groups, so a caller that words the two apart can tell them
// apart with errors.As. Its text is the problem's own.
type WindowOverrideError struct {
	Err error
}

// Error returns the text of the problem.
func (e *WindowOverrideError) Error() string { return e.Err.Error() }

// Unwrap returns the problem.
func (e *WindowOverrideError) Unwrap() error { return e.Err }

// CheckOverrides checks the overrides and window overrides of a config against
// the metric definitions of its source: a window override must select window
// counters, and each override must match fields without overlapping another.
// It returns the first problem as it is: a *WindowOverrideError for the window
// overrides, or the watch group problem (an *UnmatchedWatchGroupError when an
// override matches nothing). Startup and config validate both call it, each
// adding its own context.
func CheckOverrides(config *appconfig.Config, set *counters.CounterSet) error {
	if err := checkWindowOverrides(config, set); err != nil {
		return &WindowOverrideError{Err: err}
	}

	return ValidateWatchGroups(set.WatchCounters(), config.WatchGroups)
}

// CheckOverridesInLocalSource is CheckOverrides for startup, before DCGM
// exists. set holds the metric definitions of a file or inline source, as
// ReadLocalCounterSet returns them; it is nil when they could not be read. It
// checks them only when the config has a window override or an override read
// from a version 2 file, so a version 1 watch group keeps the check it gets
// when the registry is built. Without definitions there is nothing to check:
// the later load reports an unreadable or invalid source or a ConfigMap.
func CheckOverridesInLocalSource(config *appconfig.Config, set *counters.CounterSet) error {
	hasPath := len(config.WindowOverrides) > 0
	for _, group := range config.WatchGroups {
		hasPath = hasPath || group.Path != ""
	}

	if !hasPath || set == nil {
		return nil
	}

	return CheckOverrides(config, set)
}

// ReadLocalCounterSet reads and checks the metric definitions of a file or
// inline source. It returns nil when that is not possible yet: the source is a
// ConfigMap, or cannot be read, or is not valid.
func ReadLocalCounterSet(source appconfig.MetricSource) *counters.CounterSet {
	records, _, err := counters.ReadLocalRecords(source)
	if err != nil {
		return nil
	}

	set, err := counters.ValidateRecords(records)
	if err != nil {
		return nil
	}

	return set
}

// checkWindowOverrides rejects a window override that selects a field of the
// metric definitions that is not a window counter.
func checkWindowOverrides(config *appconfig.Config, set *counters.CounterSet) error {
	if len(config.WindowOverrides) == 0 {
		return nil
	}

	names := make([]string, 0, len(set.DCGMCounters)+len(set.ExporterCounters))
	for _, counter := range set.DCGMCounters {
		names = append(names, counter.FieldName)
	}

	for _, counter := range set.ExporterCounters {
		names = append(names, counter.FieldName)
	}

	return rejectNonWindowFields(config.WindowOverrides, names)
}

// rejectNonWindowFields reports the first match item of a window override that
// selects one of the metric source's fields that is not a window counter. The
// caller reads the source, so a relative metric file is already resolved.
func rejectNonWindowFields(overrides []appconfig.WindowOverride, fieldNames []string) error {
	for _, override := range overrides {
		for i, pattern := range override.Match {
			for _, name := range fieldNames {
				if _, isWindow := appconfig.CountWindowOf(name); MatchesField(appconfig.FieldMatcher{pattern}, name) && !isWindow {
					return fmt.Errorf(
						"%s.match[%d]: selects %s, which is not a window counter (only %s can have a window)",
						override.Path, i, name, strings.Join(appconfig.CountWindowCounters(), " and "),
					)
				}
			}
		}
	}
	return nil
}
