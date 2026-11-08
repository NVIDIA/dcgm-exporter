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
	"fmt"
	"strings"
)

// originRecorder collects the YAML key behind each Config field. A nil
// recorder ignores everything, which is what a plain apply uses.
type originRecorder map[string]string

// record notes that field was set by the YAML key. It does nothing on a nil recorder.
func (r originRecorder) record(field, key string) {
	if r != nil {
		r[field] = key
	}
}

// SettingOrigins returns the YAML properties that supply runtime settings.
// Presence follows ApplyTo, including explicit false and zero values. Paths
// for overrides and watch groups stay indexed so inspection can show which
// retention values an override sets and which it inherits from sources.dcgm.watch.
func (y *YAMLConfig) SettingOrigins() map[string]string {
	origins := map[string]string{}
	if y == nil {
		return origins
	}

	if y.legacyV1 != nil {
		y.legacyV1.recordOrigins(origins)
		return origins
	}
	// Parsing already applied this file without error, so applying it again to
	// a scratch Config only records where each setting came from. A failure here
	// would be a programming error, so it must not return partial origins.
	if err := y.applyV2(&Config{}, originRecorder(origins)); err != nil {
		panic("appconfig: a parsed version 2 file no longer applies: " + err.Error())
	}

	return origins
}

// recordOrigins records version 1 paths with their released camelCase names.
func (c *yamlV1Config) recordOrigins(origins map[string]string) {
	if c.Metrics != nil {
		if c.Metrics.EnableExporterMetrics != nil {
			origins["EnableExporterMetrics"] = "metrics.enableExporterMetrics"
		}

		if c.Metrics.definesMetricSource() {
			origins["MetricSource"] = "metrics.file"
			if len(c.Metrics.Fields) > 0 {
				origins["MetricSource"] = "metrics.fields"
			}
		}
	}

	if c.Server != nil && c.Server.MaxConcurrentScrapes != nil {
		origins["MaxConcurrentScrapes"] = "server.maxConcurrentScrapes"
	}

	c.recordCollectionOrigins(origins)
}

// recordCollectionOrigins records version 1 cadence and watch-group paths.
func (c *yamlV1Config) recordCollectionOrigins(origins map[string]string) {
	collection := c.Collection
	if collection == nil {
		return
	}
	if strings.TrimSpace(collection.Interval) != "" {
		origins["CollectInterval"] = "collection.interval"
	}
	recordLegacyRetentionOrigins(origins, "WatchRetention", "collection.retention", collection.Retention)

	origins["WatchGroups"] = "collection"
	if len(collection.WatchGroups) > 0 {
		origins["WatchGroups"] = "collection.watchGroups"
	}

	for i, group := range collection.WatchGroups {
		field := fmt.Sprintf("WatchGroups[%d]", i)
		path := fmt.Sprintf("collection.watchGroups[%d]", i)
		origins[field+".Name"] = path + ".name"
		origins[field+".Interval"] = path + ".interval"
		origins[field+".Fields"] = path + ".fields"
		recordLegacyRetentionOrigins(origins, field+".Retention", path+".retention", group.Retention)
	}
}

// recordLegacyRetentionOrigins records version 1 retention presence without comparing defaults.
func recordLegacyRetentionOrigins(origins map[string]string, field, path string, retention *yamlV1Retention) {
	if retention == nil {
		return
	}
	if retention.MaxAge != nil {
		origins[field+".MaxAge"] = path + ".maxAge"
	}

	if retention.MaxSamples != nil {
		origins[field+".MaxSamples"] = path + ".maxSamples"
	}
}
