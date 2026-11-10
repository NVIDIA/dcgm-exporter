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
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// msgRequired is the error text for a key that must be set.
	msgRequired = "is required"

	yamlConfigVersion       = 2
	legacyYAMLConfigVersion = 1

	// LegacyConfigVersionWarning is reported once for every version 1 file.
	LegacyConfigVersionWarning = "config version 1 is deprecated and will be removed in 4.10; " +
		"see the version 2 migration table in the 4.9.0 documentation"
)

// YAMLConfig is the startup configuration file. The exported fields hold the
// current format. A file in the older format (version 1) keeps its released
// shape in legacyV1.
//
// The same types also hold the full config that config show and config explain
// print (NewFullConfig). The json tags repeat the yaml keys for that output, and
// a field that must be written even when empty has no omitempty.
type YAMLConfig struct {
	Version       int                   `yaml:"version" json:"version"`
	Metrics       []YAMLMetricEntry     `yaml:"metrics,omitempty" json:"metrics,omitempty"`
	Outputs       map[string]YAMLOutput `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	Sources       *YAMLSources          `yaml:"sources,omitempty" json:"sources,omitempty"`
	Server        *YAMLServer           `yaml:"server,omitempty" json:"server,omitempty"`
	Observability *YAMLObservability    `yaml:"observability,omitempty" json:"observability,omitempty"`
	DebugDump     *YAMLDebugDump        `yaml:"debug_dump,omitempty" json:"debug_dump,omitempty"`

	legacyV1 *yamlV1Config
	root     *yaml.Node
}

// ReadError is a config file that could not be read.
type ReadError struct {
	Path string
	Err  error
}

// Error names the file and the system error, for startup logs.
func (e *ReadError) Error() string { return fmt.Sprintf("read config file %q: %v", e.Path, e.Err) }

// Unwrap returns the system error, so errors.Is can tell a missing file from a permission failure.
func (e *ReadError) Unwrap() error { return e.Err }

// LoadYAMLConfigFile reads and parses a dcgm-exporter YAML config file.
func LoadYAMLConfigFile(path string) (*YAMLConfig, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("config file path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &ReadError{Path: path, Err: err}
	}

	config, err := ParseYAMLConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parse config file %q: %w", path, err)
	}

	config.resolveRelativePaths(filepath.Dir(path))
	return config, nil
}

// ParseYAMLConfig decodes and validates dcgm-exporter YAML config bytes. A
// problem in the file comes back as a *ConfigError that says where it is.
func ParseYAMLConfig(data []byte) (*YAMLConfig, error) {
	root, err := decodeYAMLNode(data)
	if err != nil {
		return nil, locateConfigError(nil, err)
	}

	config, err := parseYAMLConfig(root, data)
	if err != nil {
		return nil, locateConfigError(root, err)
	}

	config.root = root
	return config, nil
}

// parseYAMLConfig reads the decoded file as the version its header names.
func parseYAMLConfig(root *yaml.Node, data []byte) (*YAMLConfig, error) {
	if err := rejectDuplicateMappingKeys(root); err != nil {
		return nil, err
	}

	// Read the version alone first. The full decode that follows depends on it,
	// and a bad version should be reported as that, not as an unknown key.
	var header struct {
		Version *int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return nil, err
	}

	if header.Version == nil {
		return nil, missingVersionError(root)
	}

	switch *header.Version {
	case legacyYAMLConfigVersion:
		return parseYAMLV1Config(root, data)
	case yamlConfigVersion:
		return parseYAMLV2Config(root, data)
	default:
		return nil, keyErrorf("version", "must be %d or %d", legacyYAMLConfigVersion, yamlConfigVersion)
	}
}

// missingVersionError reports a document whose version has no value. A key
// that is in the file without a value is reported at that key; when the key is
// absent there is nothing to point at, so the error is at the start of the file.
func missingVersionError(root *yaml.Node) error {
	allowed := fmt.Sprintf("must be %d or %d", legacyYAMLConfigVersion, yamlConfigVersion)
	if line, column, found := locatePath(root, "version", true); found {
		return &keyError{Path: "version", Msg: allowed, Line: line, Column: column}
	}

	return &keyError{Path: "version", Msg: "required; " + allowed, Line: 1, Column: 1}
}

// resolveRelativePaths makes a relative metric file path or web config file
// path start from the config file's directory. A version 1 file keeps its
// released behavior: the path is used as written, relative to the working
// directory.
func (y *YAMLConfig) resolveRelativePaths(dir string) {
	if y.legacyV1 != nil {
		return
	}

	for i := range y.Metrics {
		file := strings.TrimSpace(y.Metrics[i].File)
		if file != "" && !filepath.IsAbs(file) {
			y.Metrics[i].File = filepath.Join(dir, file)
		}
	}

	if y.Server != nil {
		file := strings.TrimSpace(y.Server.WebConfigFile)
		if file != "" && !filepath.IsAbs(file) {
			y.Server.WebConfigFile = filepath.Join(dir, file)
		}
	}
}

// Warnings lists notices the caller should show to the user, each with the
// key it is about. A version 1 file always produces the deprecation notice.
func (y *YAMLConfig) Warnings() []ResolutionWarning {
	if y == nil || y.legacyV1 == nil {
		return nil
	}

	return []ResolutionWarning{{
		Message: LegacyConfigVersionWarning, Origin: SettingOrigin{Kind: OriginFile, Key: "version"},
	}}
}

// ApplyTo overlays startup YAML settings onto an existing runtime Config.
func (y *YAMLConfig) ApplyTo(config *Config) error {
	if y == nil {
		return nil
	}
	if config == nil {
		return fmt.Errorf("target config is nil")
	}
	if y.legacyV1 != nil {
		return y.legacyV1.applyTo(config)
	}

	// Apply to a copy, so a file that fails part way leaves the config as it was.
	next := config.Clone()
	if err := y.applyV2(next, nil); err != nil {
		return err
	}
	*config = *next
	return nil
}

// MarshalYAML keeps a version 1 document in its released shape and writes
// every other document with the current keys.
func (y YAMLConfig) MarshalYAML() (interface{}, error) {
	if y.legacyV1 != nil {
		return y.legacyV1, nil
	}

	type plain YAMLConfig
	return plain(y), nil
}

// decodeYAMLConfig decodes one YAML document into target and rejects keys the target does not define.
func decodeYAMLConfig(data []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(target)
}

// decodeYAMLNode decodes one YAML document for validation before struct unmarshalling.
func decodeYAMLNode(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))

	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the file contains no YAML document")
		}
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
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			if err := rejectDuplicateMappingKeys(child); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		return rejectDuplicateMappingEntries(node)
	}

	return nil
}

// rejectDuplicateMappingEntries rejects non-scalar and repeated keys in one
// mapping, then checks each value.
func rejectDuplicateMappingEntries(node *yaml.Node) error {
	seen := make(map[string]int)

	// Content alternates key, value, key, value.
	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		if keyNode.Kind != yaml.ScalarNode {
			return fmt.Errorf("YAML mapping keys must be scalar at line %d", keyNode.Line)
		}

		if line, ok := seen[keyNode.Value]; ok {
			return &keyError{
				Msg:  fmt.Sprintf("duplicate YAML key %q at line %d; first defined on line %d", keyNode.Value, keyNode.Line, line),
				Line: keyNode.Line, Column: keyNode.Column,
			}
		}

		seen[keyNode.Value] = keyNode.Line

		if err := rejectDuplicateMappingKeys(node.Content[i+1]); err != nil {
			return err
		}
	}

	return nil
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

// parsePositiveDuration parses a duration that must be greater than zero.
func parsePositiveDuration(value, keyPath string) (time.Duration, error) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, keyWrap(keyPath, err)
	}

	if duration <= 0 {
		return 0, keyErrorf(keyPath, "must be greater than 0")
	}

	return duration, nil
}

// yamlMetricRow is one inline metric row, shared by both file versions.
type yamlMetricRow struct {
	Name           string
	PrometheusType string
	Help           string
}

// inlineMetricFields checks inline rows and converts them to runtime fields.
// path and typeKey name the YAML location so each version reports its own keys.
func inlineMetricFields(rows []yamlMetricRow, path, typeKey string) ([]MetricField, error) {
	fields := make([]MetricField, 0, len(rows))
	seenNames := make(map[string]int, len(rows))

	for i, row := range rows {
		name := strings.TrimSpace(row.Name)
		prometheusType := strings.TrimSpace(row.PrometheusType)
		help := strings.TrimSpace(row.Help)

		if name == "" {
			return nil, keyErrorSpacef(fmt.Sprintf("%s[%d].name", path, i), msgRequired)
		}

		if first, ok := seenNames[name]; ok {
			return nil, keyErrorSpacef(fmt.Sprintf("%s[%d].name", path, i), "duplicates %s[%d].name %q", path, first, name)
		}

		seenNames[name] = i

		if prometheusType == "" {
			return nil, keyErrorSpacef(fmt.Sprintf("%s[%d].%s", path, i, typeKey), msgRequired)
		}

		if help == "" {
			return nil, keyErrorSpacef(fmt.Sprintf("%s[%d].help", path, i), msgRequired)
		}

		fields = append(fields, MetricField{Name: name, PrometheusType: prometheusType, Help: help})
	}

	return fields, nil
}

// parseRetention parses retention values and keeps which ones were set.
// ageKey and samplesKey are the key names used in error text.
func parseRetention(age *string, samples *int64, path, ageKey, samplesKey string) (WatchRetentionOverride, error) {
	var retention WatchRetentionOverride

	if age != nil {
		maxAge, err := time.ParseDuration(strings.TrimSpace(*age))
		if err != nil {
			return WatchRetentionOverride{}, keyWrap(path+"."+ageKey, err)
		}

		if maxAge < 0 {
			return WatchRetentionOverride{}, keyErrorSpacef(path+"."+ageKey, "must not be negative")
		}

		retention.MaxAge = &maxAge
	}

	if samples != nil {
		if *samples < 0 {
			return WatchRetentionOverride{}, keyErrorSpacef(path+"."+samplesKey, "must not be negative")
		}

		if *samples > math.MaxInt32 {
			return WatchRetentionOverride{}, keyErrorSpacef(path+"."+samplesKey, "must not exceed %d", math.MaxInt32)
		}

		maxSamples := *samples
		retention.MaxSamples = &maxSamples
	}

	return retention, nil
}

// rejectZeroRetention fails when a watch group sets both retention values to zero.
func rejectZeroRetention(retention WatchRetentionOverride, path, ageKey, samplesKey string) error {
	if retention.MaxAge != nil && retention.MaxSamples != nil &&
		*retention.MaxAge == 0 && *retention.MaxSamples == 0 {
		return keyErrorf(path, "%s and %s cannot both be zero", ageKey, samplesKey)
	}

	return nil
}
