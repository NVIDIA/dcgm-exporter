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
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// FieldWatch is the watch the exporter installs for one field: the watch group
// that took it, how often DCGM samples it and how much history DCGM keeps.
type FieldWatch struct {
	// Group is the index of the watch group that took the field, or -1 when
	// no group did and the field is watched with the default group.
	Group int
	// Interval is how often DCGM samples the field.
	Interval time.Duration
	// Retention is how much history DCGM keeps of the field.
	Retention appconfig.WatchRetention
}

// fieldWatchPolicy decides which watch each field gets. The first watch group
// that matches a field takes it; any other field gets the default interval and
// the global retention.
type fieldWatchPolicy struct {
	groups          []appconfig.WatchGroup
	matchers        []appconfig.FieldMatcher
	global          appconfig.WatchRetention
	defaultInterval time.Duration
}

// newFieldWatchPolicy prepares the policy for the watch groups. It fails when a
// group's match patterns cannot be read.
func newFieldWatchPolicy(
	groups []appconfig.WatchGroup, retention appconfig.WatchRetention, defaultInterval time.Duration,
) (fieldWatchPolicy, error) {
	matchers, err := appconfig.WatchGroupMatchers(groups)
	if err != nil {
		return fieldWatchPolicy{}, err
	}

	return fieldWatchPolicy{
		groups:          groups,
		matchers:        matchers,
		global:          effectiveRetention(retention),
		defaultInterval: defaultInterval,
	}, nil
}

// of returns the watch of the field named name. A field with no name is never
// matched by a group.
func (p fieldWatchPolicy) of(name string) FieldWatch {
	if name != "" {
		for i, group := range p.groups {
			if MatchesField(p.matchers[i], name) {
				return FieldWatch{
					Group:     i,
					Interval:  time.Duration(group.Interval) * time.Millisecond,
					Retention: group.Retention.Resolve(p.global),
				}
			}
		}
	}

	return FieldWatch{Group: -1, Interval: p.defaultInterval, Retention: p.global}
}

// WatchOfField returns the watch the exporter installs for the field named
// name under config. When the watch group patterns cannot be read it returns
// the default watch for every field, together with the error.
func WatchOfField(config *appconfig.Config, name string) (FieldWatch, error) {
	defaultInterval := time.Duration(config.CollectInterval) * time.Millisecond

	policy, err := newFieldWatchPolicy(config.WatchGroups, config.WatchRetention, defaultInterval)
	if err != nil {
		policy = fieldWatchPolicy{global: effectiveRetention(config.WatchRetention), defaultInterval: defaultInterval}
	}

	return policy.of(name), err
}

// effectiveRetention returns the configured global policy, or the default
// policy for a zero-value Config.
func effectiveRetention(retention appconfig.WatchRetention) appconfig.WatchRetention {
	if retention.MaxAge == 0 && retention.MaxSamples == 0 {
		return appconfig.DefaultWatchRetention()
	}

	return retention
}

// MatchesField matches patterns against both XID names and resolves exact DCGM aliases.
func MatchesField(matcher appconfig.FieldMatcher, name string) bool {
	if matcher.Matches(name) {
		return true
	}

	fieldID, known := dcgm.GetFieldID(name)
	if !known {
		return false
	}

	if fieldID == dcgm.DCGM_FI_DEV_XID_ERROR &&
		(matcher.Matches("DCGM_FI_DEV_XID_ERROR") || matcher.Matches("DCGM_FI_DEV_XID_ERRORS")) {
		return true
	}

	for _, pattern := range matcher {
		if selectedID, ok := dcgm.GetFieldID(pattern.String()); ok && selectedID == fieldID {
			return true
		}
	}

	return false
}
