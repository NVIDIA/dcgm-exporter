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
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func TestChartArgumentsRenderAsStrings(t *testing.T) {
	testCases := []struct {
		name      string
		arguments []string
	}{
		{
			name: "default arguments",
		},
		{
			name:      "empty argument list",
			arguments: []string{},
		},
		{
			name:      "separate numeric argument",
			arguments: []string{"--collect-interval", "1000"},
		},
		{
			name:      "argument with equals sign",
			arguments: []string{"--collect-interval=1000"},
		},
		{
			name:      "YAML scalar strings",
			arguments: []string{"true", "false", "null", "~", "0123", "0x10", "1e3", "yes", "no", "on", "off"},
		},
		{
			name: "empty strings and special characters",
			arguments: []string{
				"",
				" leading and trailing spaces ",
				"metrics: default",
				"path # comment",
				`"quoted"`,
				`C:\metrics\counters.csv`,
				"line one\nline two",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]interface{}{}
			if tc.arguments != nil {
				values["arguments"] = tc.arguments
			}
			release, err := runHelmInstall(t, values)
			require.NoError(t, err)

			manifest := requireManifestKind(t, release.Manifest, "DaemonSet")
			manifestJSON, err := yaml.YAMLToJSON(manifest)
			require.NoError(t, err)
			var daemonSet unstructured.Unstructured
			require.NoError(t, daemonSet.UnmarshalJSON(manifestJSON))
			containers, found, err := unstructured.NestedSlice(daemonSet.Object, "spec", "template", "spec", "containers")
			require.NoError(t, err)
			require.True(t, found)
			require.Len(t, containers, 1)
			exporter := containers[0].(map[string]interface{})
			require.Equal(t, "exporter", exporter["name"])

			// Check raw types; yaml.Unmarshal into []string can hide numeric or Boolean arguments.
			arguments, found, err := unstructured.NestedStringSlice(exporter, "args")
			require.NoError(t, err)
			if len(tc.arguments) == 0 {
				require.False(t, found, "empty arguments must omit the container args field")
				return
			}
			require.True(t, found)
			require.Equal(t, tc.arguments, arguments)
		})
	}
}
