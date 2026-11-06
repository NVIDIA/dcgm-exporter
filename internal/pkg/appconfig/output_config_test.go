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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Version and outputs rules.
func TestV2OutputRules(t *testing.T) {
	const metric = "version: 2\nmetrics:\n  - {name: gpu, file: /tmp/a.csv}\n"
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"version 3", "version: 3\n", "version: must be 1 or 2"},
		{"no version", "metrics: []\n", "version: required; must be 1 or 2"},
		{"metrics without outputs", metric, "outputs: required when metrics is present"},
		{
			"two outputs", metric + "outputs:\n  a: {type: prometheus, schema: dcgm/v1}\n  b: {type: prometheus, schema: dcgm/v1}\n",
			"outputs: exactly one output is supported, got 2",
		},
		{"empty outputs", metric + "outputs: {}\n", "outputs: exactly one output is supported, got 0"},
		{"otlp output", metric + "outputs:\n  t: {type: otlp, schema: dcgm/v1}\n", `outputs.t.type: must be prometheus, got "otlp"`},
		{"missing type", metric + "outputs:\n  t: {schema: dcgm/v1}\n", "outputs.t.type: must be prometheus"},
		{"missing schema", metric + "outputs:\n  t: {type: prometheus}\n", "outputs.t.schema: is required"},
		{"blank output name", metric + "outputs:\n  ' ': {type: prometheus, schema: dcgm/v1}\n", "outputs: output name must not be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireParseError(t, tt.yaml, tt.wantErr)
		})
	}

	t.Run("any non-empty output name", func(t *testing.T) {
		y, _ := parseAndApply(t, metric+"outputs:\n  scrape-endpoint: {type: prometheus, schema: dcgm/v1}\n")
		assert.Contains(t, y.Outputs, "scrape-endpoint")
	})
}

// Schema names must be <name>/v<number>, and only dcgm/v1 is accepted.
func TestV2SchemaNames(t *testing.T) {
	const metric = "version: 2\nmetrics:\n  - {name: gpu, file: /tmp/a.csv}\noutputs:\n  prometheus:\n    type: prometheus\n    schema: "
	for _, schema := range []string{"dcgm", "dcgm/1", "dcgm/v0", "Dcgm/v1", "dcgm/v1.2", "dcgm/v01", "/v1", "dcgm/v1/x"} {
		t.Run("rejects "+schema, func(t *testing.T) {
			requireParseError(t, metric+`"`+schema+`"`+"\n", "outputs.prometheus.schema: ")
			requireParseError(t, metric+`"`+schema+`"`+"\n", "is not a valid schema name")
		})
	}
	t.Run("rejects default/v1", func(t *testing.T) {
		requireParseError(t, metric+"default/v1\n", `outputs.prometheus.schema: "default/v1" is not supported; only dcgm/v1 is available`)
	})
	t.Run("rejects dcgm/v2", func(t *testing.T) {
		requireParseError(t, metric+"dcgm/v2\n", "is not supported")
	})
	t.Run("accepts dcgm/v1", func(t *testing.T) {
		_, err := ParseYAMLConfig([]byte(metric + "dcgm/v1\n"))
		require.NoError(t, err)
	})
}
