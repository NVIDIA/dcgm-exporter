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
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const yamlConfigVersion = 1

// YAMLConfig is the top-level startup configuration file schema.
type YAMLConfig struct {
	Version    int             `yaml:"version"`
	Metrics    *YAMLMetrics    `yaml:"metrics,omitempty"`
	Collection *YAMLCollection `yaml:"collection,omitempty"`
}

// YAMLMetrics configures the metric definition source for startup.
type YAMLMetrics struct {
	File   string            `yaml:"file,omitempty"`
	Fields []YAMLMetricField `yaml:"fields,omitempty"`
}

// YAMLMetricField is one inline metric definition in YAML form.
type YAMLMetricField struct {
	Name           string `yaml:"name"`
	PrometheusType string `yaml:"prometheusType"`
	Help           string `yaml:"help"`
}

// YAMLCollection configures metric collection timing.
type YAMLCollection struct {
	Interval          string           `yaml:"interval,omitempty"`
	WatchGroups       []YAMLWatchGroup `yaml:"watchGroups,omitempty"`
	parsedWatchGroups []WatchGroup
}

// YAMLWatchGroup represents a parsed per-watch-group collection entry.
type YAMLWatchGroup struct {
	Name     string   `yaml:"name"`
	Interval string   `yaml:"interval"`
	Fields   []string `yaml:"fields"`
}

// LoadYAMLConfigFile reads and parses a dcgm-exporter YAML config file.
func LoadYAMLConfigFile(path string) (*YAMLConfig, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("config file path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %q: %w", path, err)
	}

	config, err := ParseYAMLConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parse config file %q: %w", path, err)
	}
	return config, nil
}

// ParseYAMLConfig decodes and validates dcgm-exporter YAML config bytes.
func ParseYAMLConfig(data []byte) (*YAMLConfig, error) {
	root, err := decodeYAMLNode(data)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateMappingKeys(root); err != nil {
		return nil, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var config YAMLConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	if err := validateYAMLConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// decodeYAMLNode decodes one YAML document for validation before struct unmarshalling.
func decodeYAMLNode(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))

	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}

	var extra yaml.Node
	err := decoder.Decode(&extra)
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("multiple YAML documents are not supported")
		}
		return nil, err
	}

	return &root, nil
}

// rejectDuplicateMappingKeys rejects repeated keys anywhere in a YAML mapping tree.
func rejectDuplicateMappingKeys(node *yaml.Node) error {
	if node == nil {
		return nil
	}

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := rejectDuplicateMappingKeys(child); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := rejectDuplicateMappingKeys(child); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		seen := make(map[string]int)
		for i := 0; i < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			if keyNode.Kind != yaml.ScalarNode {
				return fmt.Errorf("YAML mapping keys must be scalar at line %d", keyNode.Line)
			}
			if line, ok := seen[keyNode.Value]; ok {
				return fmt.Errorf("duplicate YAML key %q at line %d; first defined at line %d", keyNode.Value, keyNode.Line, line)
			}
			seen[keyNode.Value] = keyNode.Line
			if err := rejectDuplicateMappingKeys(node.Content[i+1]); err != nil {
				return err
			}
		}
	}

	return nil
}

// ApplyTo overlays startup YAML settings onto an existing runtime Config.
func (y *YAMLConfig) ApplyTo(config *Config) error {
	if y == nil {
		return nil
	}
	if config == nil {
		return fmt.Errorf("target config is nil")
	}

	if y.Metrics != nil {
		source, err := y.Metrics.metricSource()
		if err != nil {
			return err
		}
		config.MetricSource = source
		switch source.Kind {
		case MetricSourceFile:
			config.CollectorsFile = source.File
			config.ConfigMapData = UndefinedConfigMapData
		case MetricSourceInline:
			config.ConfigMapData = UndefinedConfigMapData
		}
	}

	if y.Collection != nil && strings.TrimSpace(y.Collection.Interval) != "" {
		interval, err := parseYAMLDurationMillis(y.Collection.Interval)
		if err != nil {
			return fmt.Errorf("collection.interval: %w", err)
		}
		config.CollectInterval = interval
	}
	if y.Collection != nil && len(y.Collection.WatchGroups) > 0 {
		watchGroups, err := y.Collection.watchGroups()
		if err != nil {
			return err
		}
		config.WatchGroups = watchGroups
	}

	return nil
}

// validateYAMLConfig enforces schema version and supported YAML config features.
func validateYAMLConfig(config *YAMLConfig) error {
	if config.Version != yamlConfigVersion {
		return fmt.Errorf("version must be %d", yamlConfigVersion)
	}

	if config.Metrics != nil {
		if _, err := config.Metrics.metricSource(); err != nil {
			return err
		}
	}

	if config.Collection != nil {
		if strings.TrimSpace(config.Collection.Interval) != "" {
			if _, err := parseYAMLDurationMillis(config.Collection.Interval); err != nil {
				return fmt.Errorf("collection.interval: %w", err)
			}
		}
		if _, err := config.Collection.watchGroups(); err != nil {
			return err
		}
	}

	return nil
}

// metricSource converts YAML metric settings into the runtime metric source model.
func (m *YAMLMetrics) metricSource() (MetricSource, error) {
	if m == nil {
		return MetricSource{}, fmt.Errorf("metrics is nil")
	}

	file := strings.TrimSpace(m.File)
	sourceCount := 0
	if file != "" {
		sourceCount++
	}
	if len(m.Fields) > 0 {
		sourceCount++
	}
	if sourceCount != 1 {
		return MetricSource{}, fmt.Errorf("metrics must specify exactly one of file or fields")
	}

	if file != "" {
		return MetricSource{
			Kind: MetricSourceFile,
			File: file,
		}, nil
	}

	fields := make([]MetricField, 0, len(m.Fields))
	seenNames := make(map[string]int, len(m.Fields))
	for i, field := range m.Fields {
		name := strings.TrimSpace(field.Name)
		prometheusType := strings.TrimSpace(field.PrometheusType)
		help := strings.TrimSpace(field.Help)
		if name == "" {
			return MetricSource{}, fmt.Errorf("metrics.fields[%d].name is required", i)
		}
		if first, ok := seenNames[name]; ok {
			return MetricSource{}, fmt.Errorf("metrics.fields[%d].name duplicates metrics.fields[%d].name %q", i, first, name)
		}
		seenNames[name] = i
		if prometheusType == "" {
			return MetricSource{}, fmt.Errorf("metrics.fields[%d].prometheusType is required", i)
		}
		if help == "" {
			return MetricSource{}, fmt.Errorf("metrics.fields[%d].help is required", i)
		}
		fields = append(fields, MetricField{
			Name:           name,
			PrometheusType: prometheusType,
			Help:           help,
		})
	}
	return MetricSource{Kind: MetricSourceInline, Fields: fields}, nil
}

// watchGroups converts YAML watch group settings into the runtime watch group model.
func (c *YAMLCollection) watchGroups() ([]WatchGroup, error) {
	if c == nil || len(c.WatchGroups) == 0 {
		return nil, nil
	}
	if c.parsedWatchGroups != nil {
		return c.parsedWatchGroups, nil
	}

	watchGroups := make([]WatchGroup, 0, len(c.WatchGroups))
	seenNames := map[string]struct{}{}
	for i, group := range c.WatchGroups {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			return nil, fmt.Errorf("collection.watchGroups[%d].name is required", i)
		}
		if _, ok := seenNames[name]; ok {
			return nil, fmt.Errorf("collection.watchGroups[%d].name %q is duplicated", i, name)
		}
		seenNames[name] = struct{}{}

		interval, err := parseYAMLDurationMillis(group.Interval)
		if err != nil {
			return nil, fmt.Errorf("collection.watchGroups[%d].interval: %w", i, err)
		}
		if len(group.Fields) == 0 {
			return nil, fmt.Errorf("collection.watchGroups[%d].fields must not be empty", i)
		}

		fields := make([]string, 0, len(group.Fields))
		for j, field := range group.Fields {
			field = strings.TrimSpace(field)
			if field == "" {
				return nil, fmt.Errorf("collection.watchGroups[%d].fields[%d] is required", i, j)
			}
			fields = append(fields, field)
		}

		watchGroups = append(watchGroups, WatchGroup{
			Name:     name,
			Interval: interval,
			Fields:   fields,
		})
	}

	c.parsedWatchGroups = watchGroups
	return watchGroups, nil
}

// parseYAMLDurationMillis parses a positive whole-millisecond duration for legacy interval storage.
func parseYAMLDurationMillis(value string) (int, error) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("must be greater than 0")
	}
	if duration < time.Millisecond || duration%time.Millisecond != 0 {
		return 0, fmt.Errorf("must be expressed in whole milliseconds or larger")
	}
	return int(duration / time.Millisecond), nil
}
