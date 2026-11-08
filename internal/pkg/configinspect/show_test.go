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
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestShowWritesADocumentThatLoadsBackAsTheSameSettings checks that show writes a document that loads back as
// the same settings.
func TestShowWritesADocumentThatLoadsBackAsTheSameSettings(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+"    every: 2s\n"+outputsBlock+"server:\n  max_concurrent_scrapes: 7\n")
	for _, format := range []OutputFormat{FormatYAML, FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			result, err := Show(r, nil, format)
			require.NoError(t, err)
			assert.Empty(t, result.Stderr)
			assert.NoError(t, result.Failure)

			// The loader is strict: an unknown key or a wrong type in the output fails here.
			config := appconfig.DefaultConfig()
			loaded, err := appconfig.ParseYAMLConfig(result.Stdout)
			require.NoError(t, err)
			require.NoError(t, loaded.ApplyTo(config))
			assert.Equal(t, 2000, config.CollectInterval)
			assert.Equal(t, 7, config.MaxConcurrentScrapes)
			assert.Len(t, config.MetricSource.Fields, 4)
		})
	}
}

// TestShowQuotesStringsThatLookLikeSomethingElse checks that show quotes strings that look like something
// else.
func TestShowQuotesStringsThatLookLikeSomethingElse(t *testing.T) {
	r := resolvedDefaults()
	r.Config.UseRemoteHE, r.Config.RemoteHEInfo = true, "remote:5555"
	result, err := Show(r, nil, "yaml")
	require.NoError(t, err)
	assert.Contains(t, string(result.Stdout), `listen: ":9400"`)
	assert.Contains(t, string(result.Stdout), `hostengine: "remote:5555"`)
	assert.Contains(t, string(result.Stdout), "gpu_id_type: uid")
}

// TestShowWritesInvisibleCharactersAsEscapes checks that show writes invisible characters as escapes.
func TestShowWritesInvisibleCharactersAsEscapes(t *testing.T) {
	// A text direction override, a line separator, a C1 control and a tag character outside the basic plane.
	const address = "h\u202ee:1\u2028\u009b\U000E0041"
	r := resolvedDefaults()
	r.Config.UseRemoteHE, r.Config.RemoteHEInfo = true, address
	for _, format := range []OutputFormat{FormatYAML, FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			result, err := Show(r, nil, format)
			require.NoError(t, err)
			text := string(result.Stdout)
			assert.Contains(t, text, `\u202e`)
			for _, raw := range []string{"\u202e", "\u2028", "\u009b", "\U000E0041"} {
				assert.NotContains(t, text, raw)
			}

			var document appconfig.YAMLConfig
			if format == "json" {
				require.NoError(t, json.Unmarshal(result.Stdout, &document))
			} else {
				require.NoError(t, yaml.Unmarshal(result.Stdout, &document))
			}

			assert.Equal(t, address, document.Sources.DCGM.HostEngine, "the escapes read back as the same text")
		})
	}
}

// TestShowReportsAResolutionFailureWithoutADocument checks that show reports a resolution failure without a
// document.
func TestShowReportsAResolutionFailureWithoutADocument(t *testing.T) {
	problem := errors.New("bad settings")
	result, err := Show(resolvedDefaults(), problem, "yaml")
	require.NoError(t, err)
	assert.Empty(t, result.Stdout)
	assert.Equal(t, problem, result.Failure)
	assert.Equal(t, "error    bad settings\n", string(result.Stderr))
}

// TestShowWarnsAboutSettingsTheDocumentCannotHold checks that show warns about settings the document cannot
// hold.
func TestShowWarnsAboutSettingsTheDocumentCannotHold(t *testing.T) {
	r := resolvedDefaults()
	r.Config.UseOldNamespace = true
	r.Origins["UseOldNamespace"] = appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--use-old-namespace"}
	r.Config.MetricSource = appconfig.MetricSource{
		Kind: appconfig.MetricSourceConfigMap, ConfigMap: appconfig.ConfigMapMetricSource{Namespace: "ns", Name: "cm"},
	}

	r.Origins["MetricSource"] = appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--configmap-data"}
	r.Config.GPUBindUnbindPollInterval = 0

	result, err := Show(r, nil, "yaml")
	require.NoError(t, err)
	stderr := string(result.Stderr)
	assert.Contains(t, stderr, "warning  --use-old-namespace: left out of the output: the config file has no key for it")
	assert.Contains(t, stderr, "warning  --configmap-data: left out of the output: the config file cannot name a ConfigMap")
	assert.Contains(t, stderr, "written as 1s because the config file rejects a value of 0s or less")
	assert.NotContains(t, stderr, "--web-config-file", "settings at their defaults need no warning")
}

// TestShowWarnsWhenTheDocumentRenamesAnOverride checks that show warns when the document renames an override.
func TestShowWarnsWhenTheDocumentRenamesAnOverride(t *testing.T) {
	// A version 1 file can name a watch group with characters that a version 2
	// override name may not contain.
	r := resolvedDefaults()
	r.Config.WatchGroups = []appconfig.WatchGroup{{
		Name: "has space", Path: "collection.watchGroups[0]", Interval: 1000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"},
	}}

	r.Origins["WatchGroups[0].Name"] = appconfig.SettingOrigin{Kind: appconfig.OriginFile, Key: "collection.watchGroups[0].name"}
	result, err := Show(r, nil, "yaml")
	require.NoError(t, err)
	assert.Contains(t, string(result.Stderr), `watch group "has space" is written as override "has-space"`)
}

// TestShowIncludesWarningsFromResolution checks that show includes warnings from resolution.
func TestShowIncludesWarningsFromResolution(t *testing.T) {
	r := resolvedDefaults()
	r.Warnings = []appconfig.ResolutionWarning{{
		Message: "flag --configmap-data is deprecated", Origin: appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--configmap-data"},
	}}

	result, err := Show(r, nil, "json")
	require.NoError(t, err)
	assert.Equal(t, "warning  --configmap-data: flag --configmap-data is deprecated\n", string(result.Stderr))
}

// TestNeedsQuotes checks which plain strings need quotes.
func TestNeedsQuotes(t *testing.T) {
	for value, want := range map[string]bool{
		":9400": true, "localhost:5555": true, "a#b": true, "-x": true, "{a}": true, "*x": true, "": false,
		"plain": false, "a-b": false, "10s": false, "/etc/dcgm": false,
	} {
		assert.Equal(t, want, needsQuotes(value), value)
	}
}

// TestShowKeepsDurationsReadable checks that show keeps durations readable.
func TestShowKeepsDurationsReadable(t *testing.T) {
	r := resolvedDefaults()
	r.Config.WebReadTimeout = 90 * time.Second
	result, err := Show(r, nil, "yaml")
	require.NoError(t, err)
	assert.Contains(t, string(result.Stdout), "read_timeout: 1m30s")
}

// TestShowWarnsAboutASettingTheDocumentLeavesOutWhileAFeatureIsOff checks that show warns about a setting the
// document leaves out while a feature is off.
func TestShowWarnsAboutASettingTheDocumentLeavesOutWhileAFeatureIsOff(t *testing.T) {
	r := resolvedDefaults()
	r.Config.DCGMLogLevel = appconfig.DCGMDbgLvlDebug
	r.Origins["DCGMLogLevel"] = appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--dcgm-log-level"}
	result, err := Show(r, nil, "yaml")
	require.NoError(t, err)
	assert.Contains(t, string(result.Stderr),
		"warning  --dcgm-log-level: left out of the output: the key is left out while the embedded DCGM log is off")
}
