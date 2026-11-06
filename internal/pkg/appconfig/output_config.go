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
	"regexp"
	"strings"
)

const prometheusSchema = "dcgm/v1"

// schemaNamePattern is the shared spelling of every schema name: <name>/v<number>.
var schemaNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*/v[1-9][0-9]*$`)

// YAMLOutput is one output. This build has one kind, the Prometheus endpoint.
type YAMLOutput struct {
	Type   string `yaml:"type" json:"type"`
	Schema string `yaml:"schema" json:"schema"`
	Listen string `yaml:"listen" json:"listen"`
}

// validateOutputs checks the outputs block. A file with metrics must name
// exactly one output; a file without metrics may leave outputs out.
func (y *YAMLConfig) validateOutputs() error {
	if y.Outputs == nil {
		if len(y.Metrics) > 0 {
			return &keyError{Path: "outputs", Msg: "required when metrics is present", At: "metrics"}
		}

		return nil
	}

	if len(y.Outputs) != 1 {
		return keyErrorf("outputs", "exactly one output is supported, got %d", len(y.Outputs))
	}

	for name, output := range y.Outputs {
		if strings.TrimSpace(name) == "" {
			return keyErrorf("outputs", "output name must not be empty")
		}

		if err := output.validate(joinKey("outputs", name)); err != nil {
			return err
		}
	}

	return nil
}

// validate checks that the output is a Prometheus endpoint with the supported schema.
// outputPath names the output in error text.
func (o YAMLOutput) validate(outputPath string) error {
	if o.Type != "prometheus" {
		return keyErrorf(outputPath+".type", "must be prometheus, got %q", o.Type)
	}

	schemaPath := outputPath + ".schema"
	if o.Schema == "" {
		return keyErrorf(schemaPath, "is required; use %s", prometheusSchema)
	}

	if !schemaNamePattern.MatchString(o.Schema) {
		return keyErrorf(schemaPath, "%q is not a valid schema name; use <name>/v<number>, for example %s",
			o.Schema, prometheusSchema)
	}

	if o.Schema != prometheusSchema {
		return keyErrorf(schemaPath, "%q is not supported; only %s is available", o.Schema, prometheusSchema)
	}

	return nil
}

// applyListen sets the listen address of the output. validateOutputs has
// already checked that there is exactly one output.
func (y *YAMLConfig) applyListen(config *Config, rec originRecorder) {
	for name, output := range y.Outputs {
		if listen := strings.TrimSpace(output.Listen); listen != "" {
			config.Address = listen
			rec.record("Address", joinKey("outputs", name)+".listen")
		}
	}
}
