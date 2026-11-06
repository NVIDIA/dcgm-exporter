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
package configinspect

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestSplitSection checks a setting path is split into its section and the rest.
func TestSplitSection(t *testing.T) {
	for _, tc := range []struct{ path, section, relative string }{
		{"sources.dcgm.watch.max_keep_age", "sources", "dcgm.watch.max_keep_age"},
		{"metrics[0].every", "metrics[0]", "every"},
		{"metrics[0]", "metrics[0]", ""},
		{"outputs[\"a.b\"].listen", "outputs", "[\"a.b\"].listen"},
		{"--collectors", "--collectors", ""},
		{"server", "server", ""},
	} {
		section, relative := splitSection(tc.path)
		assert.Equal(t, tc.section, section, tc.path)
		assert.Equal(t, tc.relative, relative, tc.path)
	}
}

// TestSectionRankFollowsTheVersion2File checks that section rank follows the version 2 file.
func TestSectionRankFollowsTheVersion2File(t *testing.T) {
	sections := []string{"debug_dump", "sources", "metrics[0]", "other", "outputs", "observability", "server"}
	ranks := map[string]int{}
	for _, section := range sections {
		ranks[section] = sectionRank(section)
	}

	assert.Equal(t, map[string]int{
		"metrics[0]": 0, "outputs": 1, "sources": 2, "server": 3, "observability": 4, "debug_dump": 5, "other": 6,
	}, ranks)
}

// TestValueTextWritesOneLine checks that value text writes one line.
func TestValueTextWritesOneLine(t *testing.T) {
	assert.Equal(t, `""`, valueText(""))
	assert.Equal(t, "plain", valueText("plain"))
	assert.Equal(t, `a\x1bb`, valueText("a\x1bb"))
	assert.Equal(t, "5", valueText(5))
	assert.Equal(t, "true", valueText(true))
	assert.Equal(t, `["a","b"]`, valueText([]any{"a", "b"}))
	assert.NotEmpty(t, valueText(make(chan int)), "a value that JSON cannot encode is still shown")
}

// TestSettingValueTextCountsInlineDefinitions checks that setting value text counts inline definitions.
func TestSettingValueTextCountsInlineDefinitions(t *testing.T) {
	definitions := []any{map[string]any{"name": "A"}, map[string]any{"name": "B"}}
	assert.Equal(t, "2 inline definitions", settingValueText("metrics[0].fields", definitions))
	assert.Equal(t, "2 inline definitions", settingValueText("metrics.fields", definitions))
	assert.Equal(t, `[{"name":"A"},{"name":"B"}]`, settingValueText("sources.kubernetes.additional_resource_names", definitions))
}

// TestSourceTextNamesTheKeyOnlyWhenAnotherKeySetTheValue checks that source text names the key only when
// another key set the value.
func TestSourceTextNamesTheKeyOnlyWhenAnotherKeySetTheValue(t *testing.T) {
	file := settingSource{Kind: appconfig.OriginFile, Line: 7, Path: "metrics[0].every"}
	assert.Equal(t, "file:7", sourceWithKey(file, "metrics[0].every"))
	assert.Equal(t, "file:7 (metrics[0].every)", sourceWithKey(file, "metrics[0].enrichment.kubernetes"))
	assert.Equal(t, "file", compactSource(settingSource{Kind: appconfig.OriginFile}))
	assert.Equal(t, "flag --x", compactSource(settingSource{Kind: appconfig.OriginFlag, Name: "--x"}))
	assert.Equal(t, "env X", compactSource(settingSource{Kind: appconfig.OriginEnv, Name: "X"}))
}

// TestRawSuffixAndRetentionText checks that raw suffix and retention text.
func TestRawSuffixAndRetentionText(t *testing.T) {
	assert.Empty(t, rawSuffix(""))
	assert.Equal(t, " (1500)", rawSuffix("1500"))
	assert.Equal(t, " (a\\x1bb)", rawSuffix("a\x1bb"))
	assert.Empty(t, retentionText(nil))
	assert.Equal(t, "10m, 3 samples", retentionText(&fieldRetention{MaxKeepAge: "10m", MaxKeepSamples: 3}))
	assert.Equal(t, "10m, no sample limit", retentionText(&fieldRetention{MaxKeepAge: "10m"}))
}

// TestExplainTextNamesTheFileVersionOnlyForTheDeprecatedFormat checks the
// header of the text view says nothing about the version of a current file.
func TestExplainTextNamesTheFileVersionOnlyForTheDeprecatedFormat(t *testing.T) {
	current := explainData{Path: "exporter.yaml", InputVersion: 2}
	assert.True(t, strings.HasPrefix(string(explainText(current, false)), "Config file   exporter.yaml\n"))

	deprecated := explainData{Path: "exporter.yaml", InputVersion: 1}
	assert.True(t, strings.HasPrefix(string(explainText(deprecated, false)),
		"Config file   exporter.yaml (version 1, deprecated; removed in 4.10)\n"))
}

// TestWriteWrappedListBreaksAfterACommaBeforeTheWidth checks that write wrapped list breaks after a comma
// before the width.
func TestWriteWrappedListBreaksAfterACommaBeforeTheWidth(t *testing.T) {
	var out bytes.Buffer
	words := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		words = append(words, strings.Repeat("w", 15))
	}

	writeWrappedList(&out, "\nLead: ", strings.Join(words, ", "))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	assert.Equal(t, "", lines[0], "the leading newline is kept")
	for _, line := range lines[1:] {
		assert.LessOrEqual(t, len(line), textWidth, line)
	}

	assert.Greater(t, len(lines), 2)
	assert.True(t, strings.HasPrefix(lines[2], "  w"), "continuation lines are indented")
	assert.NotContains(t, out.String(), " \n", "no line ends with a space")
}

// TestExplainTextListsFlagOnlySettingsAndEmptyLists checks that explain text lists flag only settings and
// empty lists.
func TestExplainTextListsFlagOnlySettingsAndEmptyLists(t *testing.T) {
	r := resolvedDefaults()
	r.Config.UseOldNamespace = true
	r.Origins["UseOldNamespace"] = appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--use-old-namespace"}
	r.Config.MetricSource = appconfig.MetricSource{Kind: appconfig.MetricSourceConfigMap, ConfigMap: appconfig.ConfigMapMetricSource{Namespace: "n", Name: "c"}}
	data := buildExplainData(r, localInputs{}, false)
	text := string(explainText(data, false))
	assert.Contains(t, text, "Config file   none (startup defaults)")
	assert.Contains(t, text, "Flag-only settings (no config file key)")
	assert.Contains(t, text, "--use-old-namespace  true  flag --use-old-namespace")

	empty := string(explainText(buildExplainData(resolvedDefaults(), localInputs{}, false), false))
	assert.Contains(t, empty, "Settings that differ from defaults\n  none\n")
	assert.NotContains(t, empty, "Flag-only settings")
}

// TestOverrideLabelSaysWhenAnOverrideWasNotNamed checks that override label says when an override was not
// named.
func TestOverrideLabelSaysWhenAnOverrideWasNotNamed(t *testing.T) {
	assert.Equal(t, "temps", overrideLabel("temps"))
	assert.Equal(t, "metrics[0].overrides[1] (unnamed)", overrideLabel("metrics[0].overrides[1]"))
}
