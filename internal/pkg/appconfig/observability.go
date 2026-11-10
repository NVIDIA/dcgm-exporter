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
	"strings"
)

// YAMLObservability configures the exporter's own telemetry.
type YAMLObservability struct {
	Metrics *YAMLObservabilityMetrics `yaml:"metrics,omitempty" json:"metrics,omitempty"`
	Logs    *YAMLObservabilityLogs    `yaml:"logs,omitempty" json:"logs,omitempty"`
}

// YAMLObservabilityMetrics controls the exporter's own Go runtime, process and HTTP metrics.
type YAMLObservabilityMetrics struct {
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// YAMLObservabilityLogs controls the exporter's own log output.
type YAMLObservabilityLogs struct {
	Format string `yaml:"format" json:"format"`
	Level  string `yaml:"level,omitempty" json:"level,omitempty"`
}

// applyObservabilityMetrics applies observability.metrics.
func (y *YAMLConfig) applyObservabilityMetrics(config *Config, rec originRecorder) {
	if y.Observability == nil || y.Observability.Metrics == nil || y.Observability.Metrics.Enabled == nil {
		return
	}
	config.EnableExporterMetrics = *y.Observability.Metrics.Enabled
	rec.record("EnableExporterMetrics", "observability.metrics.enabled")
}

// applyLogs applies observability.logs.
func (y *YAMLConfig) applyLogs(config *Config, rec originRecorder) error {
	if y.Observability == nil || y.Observability.Logs == nil {
		return nil
	}

	logs := y.Observability.Logs
	switch format := strings.TrimSpace(logs.Format); format {
	case "": // not set
	case "text", "json":
		config.LogFormat = format
		rec.record("LogFormat", "observability.logs.format")
	default:
		return keyErrorf("observability.logs.format", "%q is not supported; use text or json", logs.Format)
	}

	switch level := strings.TrimSpace(logs.Level); level {
	case "": // not set
	case "info", "debug":
		config.Debug = level == "debug"
		rec.record("Debug", "observability.logs.level")
	default:
		return keyErrorf("observability.logs.level", "%q is not supported; use info or debug", logs.Level)
	}

	return nil
}
