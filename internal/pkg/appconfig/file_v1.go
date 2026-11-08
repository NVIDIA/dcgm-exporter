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
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlV1Config is the version 1 schema: the 4.8.4 version 1 keys without the
// sources block, which only version 2 supports. It keeps the camelCase names
// those releases accepted.
type yamlV1Config struct {
	Version    int               `yaml:"version"`
	Metrics    *yamlV1Metrics    `yaml:"metrics,omitempty"`
	Collection *yamlV1Collection `yaml:"collection,omitempty"`
	Server     *yamlV1Server     `yaml:"server,omitempty"`
}

type yamlV1Server struct {
	MaxConcurrentScrapes *int `yaml:"maxConcurrentScrapes,omitempty"`
}

type yamlV1Metrics struct {
	File   string              `yaml:"file,omitempty"`
	Fields []yamlV1MetricField `yaml:"fields,omitempty"`
	// EnableExporterMetrics enables Go runtime, process, and HTTP handler metrics.
	EnableExporterMetrics *bool `yaml:"enableExporterMetrics,omitempty"`
}

type yamlV1MetricField struct {
	Name           string `yaml:"name"`
	PrometheusType string `yaml:"prometheusType"`
	Help           string `yaml:"help"`
}

type yamlV1Collection struct {
	Interval    string             `yaml:"interval,omitempty"`
	Retention   *yamlV1Retention   `yaml:"retention,omitempty"`
	WatchGroups []yamlV1WatchGroup `yaml:"watchGroups,omitempty"`
}

type yamlV1Retention struct {
	MaxAge     *string `yaml:"maxAge,omitempty"`
	MaxSamples *int64  `yaml:"maxSamples,omitempty"`
}

type yamlV1WatchGroup struct {
	Name      string           `yaml:"name"`
	Interval  string           `yaml:"interval"`
	Fields    []string         `yaml:"fields"`
	Retention *yamlV1Retention `yaml:"retention,omitempty"`
}

// parseYAMLV1Config reads a version 1 file and rejects any key that version 1 did not define.
// The result keeps the version 1 form until it is applied to a Config.
func parseYAMLV1Config(root *yaml.Node, data []byte) (*YAMLConfig, error) {
	if err := rejectUnknownKeys(root, reflect.TypeOf(yamlV1Config{})); err != nil {
		return nil, fmt.Errorf("%w; version 1 does not support this key; newer settings need version 2", err)
	}

	var legacy yamlV1Config
	if err := decodeYAMLConfig(data, &legacy); err != nil {
		return nil, err
	}

	if err := legacy.validate(); err != nil {
		return nil, err
	}

	return &YAMLConfig{Version: legacy.Version, legacyV1: &legacy}, nil
}

// validate checks every section of a version 1 file and returns the first problem found.
func (c *yamlV1Config) validate() error {
	if err := c.Server.validate(); err != nil {
		return err
	}

	if c.Metrics != nil {
		if c.Metrics.definesMetricSource() {
			if _, err := c.Metrics.metricSource(); err != nil {
				return err
			}
		} else if c.Metrics.EnableExporterMetrics == nil {
			return keyErrorSpacef("metrics", "must specify file, fields, or enableExporterMetrics")
		}
	}

	if c.Collection == nil {
		return nil
	}

	if strings.TrimSpace(c.Collection.Interval) != "" {
		if _, err := parseYAMLDurationMillis(c.Collection.Interval); err != nil {
			return keyWrap("collection.interval", err)
		}
	}

	if _, err := c.Collection.watchGroups(); err != nil {
		return err
	}

	if _, err := c.Collection.Retention.watchRetentionOverride("collection.retention"); err != nil {
		return err
	}

	return nil
}

// validate rejects a max concurrent scrapes value that is not positive.
func (s *yamlV1Server) validate() error {
	if s != nil && s.MaxConcurrentScrapes != nil && *s.MaxConcurrentScrapes <= 0 {
		return keyErrorSpacef("server.maxConcurrentScrapes", "must be greater than 0")
	}

	return nil
}

// applyTo copies the settings present in the file onto config and leaves the rest as they were.
func (c *yamlV1Config) applyTo(config *Config) error {
	if c.Metrics != nil {
		if c.Metrics.EnableExporterMetrics != nil {
			config.EnableExporterMetrics = *c.Metrics.EnableExporterMetrics
		}

		if c.Metrics.definesMetricSource() {
			source, err := c.Metrics.metricSource()
			if err != nil {
				return err
			}
			config.MetricSource = source
		}
	}

	if c.Server != nil && c.Server.MaxConcurrentScrapes != nil {
		config.MaxConcurrentScrapes = *c.Server.MaxConcurrentScrapes
	}

	return c.applyCollection(config)
}

// applyCollection copies the collection interval, retention and watch groups onto config.
func (c *yamlV1Config) applyCollection(config *Config) error {
	if c.Collection == nil {
		return nil
	}

	if strings.TrimSpace(c.Collection.Interval) != "" {
		interval, err := parseYAMLDurationMillis(c.Collection.Interval)
		if err != nil {
			return keyWrap("collection.interval", err)
		}

		config.CollectInterval = interval
	}

	retention, err := c.Collection.Retention.watchRetentionOverride("collection.retention")
	if err != nil {
		return err
	}

	config.WatchRetention = retention.Resolve(config.WatchRetention)

	watchGroups, err := c.Collection.watchGroups()
	if err != nil {
		return err
	}

	config.WatchGroups = watchGroups
	return nil
}

// watchGroups converts the file's watch groups to the runtime form and rejects repeated names.
func (c *yamlV1Collection) watchGroups() ([]WatchGroup, error) {
	if c == nil || len(c.WatchGroups) == 0 {
		return nil, nil
	}

	watchGroups := make([]WatchGroup, 0, len(c.WatchGroups))
	seenNames := map[string]struct{}{}

	for i, group := range c.WatchGroups {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			return nil, keyErrorSpacef(fmt.Sprintf("collection.watchGroups[%d].name", i), msgRequired)
		}

		// A group's own content is checked before its name is compared with the
		// earlier ones, so a duplicate that is also invalid reports the content problem.
		watchGroup, err := group.watchGroup(name, i)
		if err != nil {
			return nil, err
		}

		if _, ok := seenNames[name]; ok {
			return nil, keyErrorSpacef(fmt.Sprintf("collection.watchGroups[%d].name", i), "%q is duplicated", name)
		}

		seenNames[name] = struct{}{}

		watchGroups = append(watchGroups, watchGroup)
	}

	return watchGroups, nil
}

// watchGroup converts one version 1 watch group whose name was already checked.
func (g yamlV1WatchGroup) watchGroup(name string, index int) (WatchGroup, error) {
	interval, err := parseYAMLDurationMillis(g.Interval)
	if err != nil {
		return WatchGroup{}, keyWrap(fmt.Sprintf("collection.watchGroups[%d].interval", index), err)
	}

	if len(g.Fields) == 0 {
		return WatchGroup{}, keyErrorSpacef(fmt.Sprintf("collection.watchGroups[%d].fields", index), "must not be empty")
	}

	fields := make([]string, 0, len(g.Fields))
	for j, field := range g.Fields {
		field = strings.TrimSpace(field)
		if field == "" {
			return WatchGroup{}, keyErrorSpacef(fmt.Sprintf("collection.watchGroups[%d].fields[%d]", index, j), msgRequired)
		}

		fields = append(fields, field)
	}

	retentionPath := fmt.Sprintf("collection.watchGroups[%d].retention", index)
	retention, err := g.Retention.watchRetentionOverride(retentionPath)
	if err != nil {
		return WatchGroup{}, err
	}

	if err := rejectZeroRetention(retention, retentionPath, "maxAge", "maxSamples"); err != nil {
		return WatchGroup{}, err
	}

	return WatchGroup{Name: name, Interval: interval, Fields: fields, Retention: retention}, nil
}

// watchRetentionOverride parses the retention block, and a missing block sets nothing.
// path names the block in error text.
func (r *yamlV1Retention) watchRetentionOverride(path string) (WatchRetentionOverride, error) {
	if r == nil {
		return WatchRetentionOverride{}, nil
	}
	return parseRetention(r.MaxAge, r.MaxSamples, path, "maxAge", "maxSamples")
}

// definesMetricSource reports whether this YAML block selects a file or inline fields.
func (m *yamlV1Metrics) definesMetricSource() bool {
	return m != nil && (strings.TrimSpace(m.File) != "" || len(m.Fields) > 0)
}

// metricSource converts the version 1 metrics block into the runtime metric source model.
func (m *yamlV1Metrics) metricSource() (MetricSource, error) {
	file := strings.TrimSpace(m.File)

	// Exactly one of file or fields may be set.
	sourceCount := 0
	if file != "" {
		sourceCount++
	}

	if len(m.Fields) > 0 {
		sourceCount++
	}

	if sourceCount != 1 {
		return MetricSource{}, keyErrorSpacef("metrics", "must specify exactly one of file or fields")
	}

	if file != "" {
		return MetricSource{Kind: MetricSourceFile, File: file}, nil
	}

	rows := make([]yamlMetricRow, len(m.Fields))
	for i, field := range m.Fields {
		rows[i] = yamlMetricRow(field)
	}

	fields, err := inlineMetricFields(rows, "metrics.fields", "prometheusType")
	if err != nil {
		return MetricSource{}, err
	}

	return MetricSource{Kind: MetricSourceInline, Fields: fields}, nil
}
