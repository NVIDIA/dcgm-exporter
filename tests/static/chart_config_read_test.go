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

package static_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// requireConfigReads parses a rendered config with the exporter's YAML reader and
// applies it to a default runtime config. This covers the key, type, value and
// combination checks of the file itself. It does not run the checks `config
// validate` makes against the metric file, such as overlapping or unmatched
// `every` overrides: those need the counters package, which links DCGM through
// cgo, and the static suite is built without cgo. The host suite runs the
// binary's `config validate` on such files.
func requireConfigReads(t *testing.T, data string, wantVersion int) {
	t.Helper()
	parsed, err := appconfig.ParseYAMLConfig([]byte(data))
	require.NoError(t, err, "the config does not parse:\n%s", data)
	require.Equal(t, wantVersion, parsed.Version)
	require.NoError(t, parsed.ApplyTo(&appconfig.Config{}), "the config does not apply:\n%s", data)
}

// readmeConfigExamples returns the values of every YAML example in
// deployment/README.md that sets config to an exporter configuration map. The
// 4.8.4 values shown in the upgrade section set config.data as text and are not
// examples of the current options.
func readmeConfigExamples(t *testing.T) []map[string]interface{} {
	t.Helper()
	readme, err := os.ReadFile(filepath.Join(repoPath(t), "deployment", "README.md"))
	require.NoError(t, err)
	var examples []map[string]interface{}
	for _, block := range regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(string(readme), -1) {
		var values map[string]interface{}
		if err := yaml.Unmarshal([]byte(block[1]), &values); err != nil {
			if strings.Contains(block[1], "config:") {
				// A values example that cannot be read must not pass by being skipped.
				require.NoError(t, err, "the values example does not parse:\n%s", block[1])
			}
			continue
		}
		config, ok := values["config"].(map[string]interface{})
		if !ok {
			continue
		}
		if _, hasVersion := config["version"]; hasVersion {
			examples = append(examples, values)
		}
	}
	return examples
}

// configReadCase is one set of chart values whose rendered config is read.
type configReadCase struct {
	name   string
	values map[string]interface{}
}

// TestChartConfigMapsRead renders the ConfigMap the chart builds for the default
// config and for each config example in the chart README, and reads each rendered
// config with the exporter's config reader. See requireConfigReads for what is and
// is not covered.
func TestChartConfigMapsRead(t *testing.T) {
	readmeExamples := readmeConfigExamples(t)
	require.NotEmpty(t, readmeExamples, "deployment/README.md has no config example")
	testCases := []configReadCase{{name: "chart default config"}}
	for i, example := range readmeExamples {
		testCases = append(testCases, configReadCase{name: fmt.Sprintf("README config example %d", i+1), values: example})
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resources := renderChart(t, tc.values)

			configMap := requireResourceName(t, resources, "ConfigMap", "dcgm-exporter-config")
			data, found := configMap.Data["config.yaml"]
			require.True(t, found, "the ConfigMap has no config.yaml key, the chart's default key")
			requireConfigReads(t, data, 2)
		})
	}
}
