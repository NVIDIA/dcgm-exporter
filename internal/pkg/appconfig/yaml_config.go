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
	"math"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	yamlConfigVersion       = 2
	legacyYAMLConfigVersion = 1
)

// YAMLConfig is the top-level startup configuration file schema.
type YAMLConfig struct {
	Version     int                `yaml:"version"`
	Metrics     *YAMLMetrics       `yaml:"metrics,omitempty"`
	Collections []CollectionConfig `yaml:"collections,omitempty"`
	Sources     *YAMLSources       `yaml:"sources,omitempty"`
	Server      *YAMLServer        `yaml:"server,omitempty"`

	legacyV1 *yamlV1Config
}

// YAMLServer configures the exporter HTTP server.
type YAMLServer struct {
	MaxConcurrentScrapes *int `yaml:"maxConcurrentScrapes,omitempty"`
}

// YAMLMetrics configures the metric definition source for startup.
type YAMLMetrics struct {
	File   string            `yaml:"file,omitempty"`
	Fields []YAMLMetricField `yaml:"fields,omitempty"`
	// EnableExporterMetrics enables Go runtime, process, and HTTP handler metrics.
	EnableExporterMetrics *bool `yaml:"enableExporterMetrics,omitempty"`
}

// YAMLMetricField is one inline metric definition in YAML form.
type YAMLMetricField struct {
	Name           string `yaml:"name"`
	PrometheusType string `yaml:"prometheusType"`
	Help           string `yaml:"help"`
}

// CollectionConfig configures one named DCGM field collection. Include values
// retain dcgm-exporter's existing glob-pattern semantics; they are not
// nv-exporter catalog SignalIds.
type CollectionConfig struct {
	Name    string                  `yaml:"name"`
	Every   string                  `yaml:"every"`
	Metrics CollectionMetricsConfig `yaml:"metrics"`
	Sources *YAMLCollectionSources  `yaml:"sources,omitempty"`
}

// CollectionMetricsConfig selects DCGM metric fields for one collection.
type CollectionMetricsConfig struct {
	Include []string `yaml:"include"`
}

// YAMLSources is the supported subset of nv-exporter source configuration.
type YAMLSources struct {
	DCGM *YAMLDCGMSource `yaml:"dcgm,omitempty"`
}

// YAMLDCGMSource configures exporter-wide DCGM source behavior.
type YAMLDCGMSource struct {
	Watch            *YAMLDCGMWatch        `yaml:"watch,omitempty"`
	DetectBindUnbind *YAMLDetectBindUnbind `yaml:"detectBindUnbind,omitempty"`
}

// YAMLDetectBindUnbind configures physical GPU lifecycle detection.
type YAMLDetectBindUnbind struct {
	Enabled      *bool  `yaml:"enabled,omitempty"`
	PollInterval string `yaml:"pollInterval,omitempty"`
}

// YAMLCollectionSources configures source settings that can vary by collection.
// Exporter-wide DCGM lifecycle settings intentionally do not belong here.
type YAMLCollectionSources struct {
	DCGM *YAMLCollectionDCGMSource `yaml:"dcgm,omitempty"`
}

// YAMLCollectionDCGMSource configures collection-local DCGM behavior.
type YAMLCollectionDCGMSource struct {
	Watch *YAMLDCGMWatch `yaml:"watch,omitempty"`
}

// YAMLDCGMWatch is the presence-aware YAML representation of a field-watch retention policy.
// Its pointer fields distinguish omitted properties from explicit zero values until application to Config.
type YAMLDCGMWatch struct {
	MaxKeepAge     *string `yaml:"maxKeepAge,omitempty"`
	MaxKeepSamples *int64  `yaml:"maxKeepSamples,omitempty"`
}

// yamlV1Config is the version 1 schema. It is deliberately separate from
// YAMLConfig so each version rejects keys from the other schema.
type yamlV1Config struct {
	Version    int               `yaml:"version"`
	Sources    *yamlV1Sources    `yaml:"sources,omitempty"`
	Metrics    *YAMLMetrics      `yaml:"metrics,omitempty"`
	Collection *yamlV1Collection `yaml:"collection,omitempty"`
	Server     *YAMLServer       `yaml:"server,omitempty"`
}

type yamlV1Sources struct {
	DCGM *yamlV1DCGMSource `yaml:"dcgm,omitempty"`
}

type yamlV1DCGMSource struct {
	DetectBindUnbind *YAMLDetectBindUnbind `yaml:"detectBindUnbind,omitempty"`
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

	var header struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return nil, err
	}

	switch header.Version {
	case legacyYAMLConfigVersion:
		return parseYAMLV1Config(data)
	case yamlConfigVersion:
		return parseYAMLV2Config(data)
	default:
		return nil, fmt.Errorf("version must be %d or %d", legacyYAMLConfigVersion, yamlConfigVersion)
	}
}

func parseYAMLV2Config(data []byte) (*YAMLConfig, error) {
	var config YAMLConfig
	if err := decodeYAMLConfig(data, &config); err != nil {
		return nil, err
	}
	if err := validateYAMLConfig(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

func parseYAMLV1Config(data []byte) (*YAMLConfig, error) {
	var legacy yamlV1Config
	if err := decodeYAMLConfig(data, &legacy); err != nil {
		return nil, err
	}
	if err := legacy.validate(); err != nil {
		return nil, err
	}
	config := &YAMLConfig{
		Version:  legacy.Version,
		Metrics:  legacy.Metrics,
		Server:   legacy.Server,
		legacyV1: &legacy,
	}
	if legacy.Sources != nil && legacy.Sources.DCGM != nil {
		config.Sources = &YAMLSources{DCGM: &YAMLDCGMSource{
			DetectBindUnbind: legacy.Sources.DCGM.DetectBindUnbind,
		}}
	}
	return config, nil
}

func decodeYAMLConfig(data []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(target)
}

// MarshalYAML preserves a version 1 document's legacy shape while serializing
// version 2 documents with their canonical schema.
func (y YAMLConfig) MarshalYAML() (interface{}, error) {
	if y.legacyV1 != nil {
		return y.legacyV1, nil
	}

	return struct {
		Version     int                `yaml:"version"`
		Metrics     *YAMLMetrics       `yaml:"metrics,omitempty"`
		Collections []CollectionConfig `yaml:"collections,omitempty"`
		Sources     *YAMLSources       `yaml:"sources,omitempty"`
		Server      *YAMLServer        `yaml:"server,omitempty"`
	}{
		Version:     y.Version,
		Metrics:     y.Metrics,
		Collections: y.Collections,
		Sources:     y.Sources,
		Server:      y.Server,
	}, nil
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

	if y.Sources != nil && y.Sources.DCGM != nil && y.Sources.DCGM.DetectBindUnbind != nil {
		detection := y.Sources.DCGM.DetectBindUnbind
		if detection.Enabled != nil {
			config.EnableGPUBindUnbindWatch = *detection.Enabled
		}
		if strings.TrimSpace(detection.PollInterval) != "" {
			pollInterval, err := time.ParseDuration(strings.TrimSpace(detection.PollInterval))
			if err != nil {
				return fmt.Errorf("sources.dcgm.detectBindUnbind.pollInterval: %w", err)
			}
			config.GPUBindUnbindPollInterval = pollInterval
		}
	}

	if y.Metrics != nil {
		if y.Metrics.EnableExporterMetrics != nil {
			config.EnableExporterMetrics = *y.Metrics.EnableExporterMetrics
		}
		if y.Metrics.definesMetricSource() {
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
	}

	if y.Server != nil && y.Server.MaxConcurrentScrapes != nil {
		config.MaxConcurrentScrapes = *y.Server.MaxConcurrentScrapes
	}
	if y.legacyV1 != nil {
		return y.legacyV1.applyCollection(config)
	}

	if y.Sources != nil {
		retention, err := y.Sources.watchRetentionOverride("sources.dcgm.watch")
		if err != nil {
			return err
		}
		config.WatchRetention = retention.Resolve(config.WatchRetention)
	}

	if y.Collections != nil {
		plan, err := y.compileWatchPlan()
		if err != nil {
			return err
		}
		if plan.defaultCollection != nil {
			config.CollectInterval = plan.defaultCollection.Interval
			config.WatchRetention = plan.defaultCollection.Retention.Resolve(config.WatchRetention)
		}
		config.WatchGroups = plan.watchGroups
	}

	return nil
}

// validateYAMLConfig enforces schema version and supported YAML config features.
func validateYAMLConfig(config *YAMLConfig) error {
	if config.Version != yamlConfigVersion {
		return fmt.Errorf("version must be %d", yamlConfigVersion)
	}
	if err := validateYAMLSettings(config.Metrics, config.Server); err != nil {
		return err
	}
	if config.Sources != nil && config.Sources.DCGM != nil {
		if err := validateYAMLDetectBindUnbind(config.Sources.DCGM.DetectBindUnbind); err != nil {
			return err
		}
	}
	if config.Sources != nil {
		if _, err := config.Sources.watchRetentionOverride("sources.dcgm.watch"); err != nil {
			return err
		}
	}
	if config.Collections != nil {
		if len(config.Collections) == 0 {
			return fmt.Errorf("collections must contain at least one collection")
		}
		if _, err := config.compileWatchPlan(); err != nil {
			return err
		}
	}
	return nil
}

func validateYAMLSettings(metrics *YAMLMetrics, server *YAMLServer) error {
	if server != nil && server.MaxConcurrentScrapes != nil && *server.MaxConcurrentScrapes <= 0 {
		return fmt.Errorf("server.maxConcurrentScrapes must be greater than 0")
	}

	if metrics != nil {
		if metrics.definesMetricSource() {
			if _, err := metrics.metricSource(); err != nil {
				return err
			}
		} else if metrics.EnableExporterMetrics == nil {
			return fmt.Errorf("metrics must specify file, fields, or enableExporterMetrics")
		}
	}
	return nil
}

func validateYAMLDetectBindUnbind(config *YAMLDetectBindUnbind) error {
	if config == nil {
		return nil
	}
	pollInterval := strings.TrimSpace(config.PollInterval)
	if pollInterval == "" {
		return nil
	}
	duration, err := time.ParseDuration(pollInterval)
	if err != nil {
		return fmt.Errorf("sources.dcgm.detectBindUnbind.pollInterval: %w", err)
	}
	if duration <= 0 {
		return fmt.Errorf("sources.dcgm.detectBindUnbind.pollInterval must be greater than 0")
	}
	return nil
}

func (c *yamlV1Config) validate() error {
	if c.Version != legacyYAMLConfigVersion {
		return fmt.Errorf("version must be %d", legacyYAMLConfigVersion)
	}
	if err := validateYAMLSettings(c.Metrics, c.Server); err != nil {
		return err
	}
	if c.Sources != nil && c.Sources.DCGM != nil {
		if err := validateYAMLDetectBindUnbind(c.Sources.DCGM.DetectBindUnbind); err != nil {
			return err
		}
	}
	if c.Collection == nil {
		return nil
	}
	if strings.TrimSpace(c.Collection.Interval) != "" {
		if _, err := parseYAMLDurationMillis(c.Collection.Interval); err != nil {
			return fmt.Errorf("collection.interval: %w", err)
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

func (c *yamlV1Config) applyCollection(config *Config) error {
	if c.Collection == nil {
		return nil
	}
	if strings.TrimSpace(c.Collection.Interval) != "" {
		interval, err := parseYAMLDurationMillis(c.Collection.Interval)
		if err != nil {
			return fmt.Errorf("collection.interval: %w", err)
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

func (c *yamlV1Collection) watchGroups() ([]WatchGroup, error) {
	if c == nil || len(c.WatchGroups) == 0 {
		return nil, nil
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

		retention, err := group.Retention.watchRetentionOverride(
			fmt.Sprintf("collection.watchGroups[%d].retention", i),
		)
		if err != nil {
			return nil, err
		}
		if retention.MaxAge != nil && retention.MaxSamples != nil &&
			*retention.MaxAge == 0 && *retention.MaxSamples == 0 {
			return nil, fmt.Errorf(
				"collection.watchGroups[%d].retention: maxAge and maxSamples cannot both be zero",
				i,
			)
		}

		watchGroups = append(watchGroups, WatchGroup{
			Name:      name,
			Interval:  interval,
			Fields:    fields,
			Retention: retention,
		})
	}
	return watchGroups, nil
}

// definesMetricSource reports whether this YAML block selects a file or inline fields.
func (m *YAMLMetrics) definesMetricSource() bool {
	return m != nil && (strings.TrimSpace(m.File) != "" || len(m.Fields) > 0)
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

type yamlWatchPlan struct {
	defaultCollection *WatchGroup
	watchGroups       []WatchGroup
}

// compileWatchPlan converts canonical collections into the runtime watch plan.
// An all-fields collection supplies the default cadence and retention; narrower
// collections become per-field overrides.
func (c *YAMLConfig) compileWatchPlan() (yamlWatchPlan, error) {
	plan := yamlWatchPlan{
		watchGroups: make([]WatchGroup, 0, len(c.Collections)),
	}
	seenNames := map[string]struct{}{}
	for i, collection := range c.Collections {
		path := fmt.Sprintf("collections[%d]", i)
		name := strings.TrimSpace(collection.Name)
		if name == "" {
			return yamlWatchPlan{}, fmt.Errorf("%s.name is required", path)
		}
		if _, ok := seenNames[name]; ok {
			return yamlWatchPlan{}, fmt.Errorf("%s.name %q is duplicated", path, name)
		}
		seenNames[name] = struct{}{}

		interval, err := parseYAMLDurationMillis(collection.Every)
		if err != nil {
			return yamlWatchPlan{}, fmt.Errorf("%s.every: %w", path, err)
		}
		if len(collection.Metrics.Include) == 0 {
			return yamlWatchPlan{}, fmt.Errorf("%s.metrics.include must not be empty", path)
		}

		fields := make([]string, 0, len(collection.Metrics.Include))
		for j, field := range collection.Metrics.Include {
			field = strings.TrimSpace(field)
			if field == "" {
				return yamlWatchPlan{}, fmt.Errorf("%s.metrics.include[%d] is required", path, j)
			}
			fields = append(fields, field)
		}

		retention, err := collection.Sources.watchRetentionOverride(path + ".sources.dcgm.watch")
		if err != nil {
			return yamlWatchPlan{}, err
		}
		if retention.MaxAge != nil && retention.MaxSamples != nil &&
			*retention.MaxAge == 0 && *retention.MaxSamples == 0 {
			return yamlWatchPlan{}, fmt.Errorf(
				"%s.sources.dcgm.watch: maxKeepAge and maxKeepSamples cannot both be zero",
				path,
			)
		}

		watchGroup := WatchGroup{
			Name:      name,
			Interval:  interval,
			Fields:    fields,
			Retention: retention,
		}
		if isDefaultCollection(fields) {
			if plan.defaultCollection != nil {
				return yamlWatchPlan{}, fmt.Errorf("%s.metrics.include duplicates the all-fields collection %q", path, plan.defaultCollection.Name)
			}
			plan.defaultCollection = &watchGroup
			continue
		}
		plan.watchGroups = append(plan.watchGroups, watchGroup)
	}

	return plan, nil
}

func isDefaultCollection(fields []string) bool {
	return len(fields) == 1 && fields[0] == "*"
}

// watchRetentionOverride parses and validates the YAML values while preserving property presence.
func (s *YAMLSources) watchRetentionOverride(path string) (WatchRetentionOverride, error) {
	if s == nil || s.DCGM == nil || s.DCGM.Watch == nil {
		return WatchRetentionOverride{}, nil
	}

	return watchRetentionOverride(s.DCGM.Watch, path)
}

func (s *YAMLCollectionSources) watchRetentionOverride(path string) (WatchRetentionOverride, error) {
	if s == nil || s.DCGM == nil || s.DCGM.Watch == nil {
		return WatchRetentionOverride{}, nil
	}

	return watchRetentionOverride(s.DCGM.Watch, path)
}

func watchRetentionOverride(r *YAMLDCGMWatch, path string) (WatchRetentionOverride, error) {
	var retention WatchRetentionOverride
	if r.MaxKeepAge != nil {
		maxAge, err := time.ParseDuration(strings.TrimSpace(*r.MaxKeepAge))
		if err != nil {
			return WatchRetentionOverride{}, fmt.Errorf("%s.maxKeepAge: %w", path, err)
		}
		if maxAge < 0 {
			return WatchRetentionOverride{}, fmt.Errorf("%s.maxKeepAge must not be negative", path)
		}
		retention.MaxAge = &maxAge
	}
	if r.MaxKeepSamples != nil {
		if *r.MaxKeepSamples < 0 {
			return WatchRetentionOverride{}, fmt.Errorf("%s.maxKeepSamples must not be negative", path)
		}
		if *r.MaxKeepSamples > math.MaxInt32 {
			return WatchRetentionOverride{}, fmt.Errorf(
				"%s.maxKeepSamples must not exceed %d",
				path,
				math.MaxInt32,
			)
		}
		maxSamples := *r.MaxKeepSamples
		retention.MaxSamples = &maxSamples
	}

	return retention, nil
}

func (r *yamlV1Retention) watchRetentionOverride(path string) (WatchRetentionOverride, error) {
	if r == nil {
		return WatchRetentionOverride{}, nil
	}

	var retention WatchRetentionOverride
	if r.MaxAge != nil {
		maxAge, err := time.ParseDuration(strings.TrimSpace(*r.MaxAge))
		if err != nil {
			return WatchRetentionOverride{}, fmt.Errorf("%s.maxAge: %w", path, err)
		}
		if maxAge < 0 {
			return WatchRetentionOverride{}, fmt.Errorf("%s.maxAge must not be negative", path)
		}
		retention.MaxAge = &maxAge
	}
	if r.MaxSamples != nil {
		if *r.MaxSamples < 0 {
			return WatchRetentionOverride{}, fmt.Errorf("%s.maxSamples must not be negative", path)
		}
		if *r.MaxSamples > math.MaxInt32 {
			return WatchRetentionOverride{}, fmt.Errorf(
				"%s.maxSamples must not exceed %d",
				path,
				math.MaxInt32,
			)
		}
		maxSamples := *r.MaxSamples
		retention.MaxSamples = &maxSamples
	}

	return retention, nil
}

// parseYAMLDurationMillis parses a positive whole-millisecond duration for runtime storage.
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
