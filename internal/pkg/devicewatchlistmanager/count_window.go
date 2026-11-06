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
	"cmp"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// historyLimit is one reason DCGM's history is shorter than a window.
type historyLimit struct {
	// keeps is how far back DCGM's history reaches.
	keeps time.Duration
	// reason says which setting limits the history, and by how much.
	reason string
}

// CountWindowWarnings returns a warning for each enabled windowed count metric
// whose window is longer than the history DCGM keeps of its source field. The
// metric then can count only what the history holds. The check uses the watch
// the exporter installs for the source field (see WatchOfField). The message
// names the settings, so the warning carries no origin of its own. It is a
// warning, never an error.
func CountWindowWarnings(r *appconfig.Resolved, set *counters.CounterSet) []appconfig.ResolutionWarning {
	names := counterNamesByFieldID(set.WatchCounters())

	var warnings []appconfig.ResolutionWarning
	for _, window := range appconfig.CountWindows {
		source, ok := counters.CountSourceOf(window.Counter)
		if !ok || !slices.ContainsFunc(set.ExporterCounters, func(c counters.Counter) bool {
			return c.FieldName == window.Counter
		}) {
			continue
		}

		// A watch group that cannot be read is reported by validation.
		watch, err := WatchOfField(r.Config, names[source.FieldID])
		if err != nil {
			continue
		}

		if warning, found := countWindowWarning(r, window, source, watch); found {
			warnings = append(warnings, warning)
		}
	}

	return warnings
}

// countWindowWarning says the history of the source field is shorter than the
// window. DCGM drops a sample once it is older than the age limit, so the age
// limit applies to every count. The sample limit counts samples taken on the
// watch interval, so it limits time only for a field polled at that interval
// (clock events); an XID sample is recorded only when an XID event fires, so
// the sample limit caps the number of events and gives no time limit. A limit
// of zero is off. When both limits cut the window short, the one that keeps
// less history is named.
func countWindowWarning(
	r *appconfig.Resolved, window appconfig.CountWindow, source counters.CountSource, watch FieldWatch,
) (appconfig.ResolutionWarning, bool) {
	size := window.Duration(r.Config)

	var limits []historyLimit
	if age := watch.Retention.MaxAge; age > 0 && age < size {
		ageSetting := inputName(r, retentionField(r.Config, watch, true), "--"+appconfig.FlagWatchMaxKeepAge)
		limits = append(limits, historyLimit{
			keeps:  age,
			reason: fmt.Sprintf("%s (%s)", ageSetting, appconfig.FormatDuration(age)),
		})
	}

	samples := watch.Retention.MaxSamples
	if keeps, limited := sampleHistory(samples, watch.Interval, size); limited && !source.EventDriven {
		samplesSetting := inputName(r, retentionField(r.Config, watch, false), "--"+appconfig.FlagWatchMaxKeepSamples)
		limits = append(limits, historyLimit{
			keeps: keeps,
			reason: fmt.Sprintf("the %d samples that %s keeps at one sample every %s",
				samples, samplesSetting, appconfig.FormatDuration(watch.Interval)),
		})
	}

	if len(limits) == 0 {
		return appconfig.ResolutionWarning{}, false
	}

	tightest := slices.MinFunc(limits, func(a, b historyLimit) int { return cmp.Compare(a.keeps, b.keeps) })
	windowSetting := inputName(r, window.Field, "--"+window.Flag)

	return appconfig.ResolutionWarning{
		Message: fmt.Sprintf("%s: the %s window is longer than %s, so %s can count only the last %s",
			windowSetting, appconfig.FormatDuration(size), tightest.reason, window.Counter,
			appconfig.FormatDuration(tightest.keeps)),
		Attrs: []any{slog.String("metric", window.Counter)},
	}, true
}

// sampleHistory returns how far back samples taken every interval reach, and
// whether that is less than the window. N samples span N-1 intervals. It
// compares counts before multiplying, so a huge sample limit or interval cannot
// overflow the duration.
func sampleHistory(samples int64, interval, window time.Duration) (time.Duration, bool) {
	if samples <= 0 || interval <= 0 || samples-1 > int64(window/interval) {
		return 0, false
	}

	keeps := time.Duration(samples-1) * interval

	return keeps, keeps < window
}

// retentionField is the setting field that supplied the retention age (or the
// sample limit) of the watch: the watch group's own when it set one, otherwise
// the global setting.
func retentionField(config *appconfig.Config, watch FieldWatch, age bool) string {
	if watch.Group < 0 {
		return globalRetentionField(age)
	}

	group := config.WatchGroups[watch.Group]
	switch {
	case age && group.Retention.MaxAge != nil:
		return appconfig.WatchGroupMaxAgeField(watch.Group)
	case !age && group.Retention.MaxSamples != nil:
		return appconfig.WatchGroupMaxSamplesField(watch.Group)
	}

	return globalRetentionField(age)
}

// globalRetentionField is the setting field of the global retention age or sample limit.
func globalRetentionField(age bool) string {
	if age {
		return appconfig.FieldWatchMaxAge
	}

	return appconfig.FieldWatchMaxSamples
}

// inputName names the input that supplied a setting the way a user writes it,
// or fallback when no input set it.
func inputName(r *appconfig.Resolved, field, fallback string) string {
	if name := r.OriginOf(field).Input(); name != "" {
		return name
	}

	return fallback
}
