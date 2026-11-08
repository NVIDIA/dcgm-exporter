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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// readmeWholeValues are values the README documents as one row, not key by key.
var readmeWholeValues = map[string]bool{
	"config":          true,
	"securityContext": true,
	"resources":       true,
}

// TestChartReadmeListsEveryValue checks that the README parameter tables have a
// row for every value in values.yaml, so a new value cannot ship undocumented.
func TestChartReadmeListsEveryValue(t *testing.T) {
	var values map[string]interface{}
	data := readRepositoryFile(t, "deployment", "values.yaml")
	require.NoError(t, yaml.Unmarshal([]byte(data), &values))

	readme := readRepositoryFile(t, "deployment", "README.md")
	for _, key := range valueKeys("", values) {
		assert.Contains(t, readme, fmt.Sprintf("| `%s` |", key), "README has no parameter row for %s", key)
	}
}

// valueKeys returns the dotted path of every value below prefix. A non-empty
// map is followed, except the maps in readmeWholeValues; anything else is a value.
func valueKeys(prefix string, node map[string]interface{}) []string {
	var keys []string
	for name, child := range node {
		path := strings.TrimPrefix(prefix+"."+name, ".")

		nested, isMap := child.(map[string]interface{})
		if isMap && len(nested) > 0 && !readmeWholeValues[path] {
			keys = append(keys, valueKeys(path, nested)...)
			continue
		}

		keys = append(keys, path)
	}

	return keys
}
