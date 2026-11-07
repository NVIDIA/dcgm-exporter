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

package host

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// configStartupErrorTimeout bounds a run that must fail while reading the config
// file or the settings, before the exporter reaches DCGM.
const configStartupErrorTimeout = 30 * time.Second

// configStartupErrorCase is one unusable setting and what the run must report.
// The expected text is the YAML path, flag or variable name together with the
// reason, so a change that drops the location fails the case.
type configStartupErrorCase struct {
	name string
	// configPath is passed as --config-file when set, and must appear in stderr.
	configPath string
	// args are extra arguments after --config-file and --address.
	args []string
	env  []string
	// wantStderr lists text that stderr must contain.
	wantStderr []string
	// yamlPathOnly says the error names the YAML path of the setting but not
	// the file. The override checks that run after the file is loaded report
	// it this way.
	yamlPathOnly bool
}

var configStartupErrorCases = []configStartupErrorCase{
	{
		name:       "rejects an unknown top-level key",
		configPath: configFixture("unknown-top-level-key.yaml"),
		wantStderr: []string{"unknownSetting: unknown key"},
	},
	{
		name:       "rejects an invalid collection interval",
		configPath: configFixture("version1-invalid-interval.yaml"),
		wantStderr: []string{"collection.interval", "invalid duration"},
	},
	{
		name:       "rejects a version 2 key in a version 1 file",
		configPath: configFixture("version1-with-version2-key.yaml"),
		wantStderr: []string{"collections: unknown key", "newer settings need version 2"},
	},
	{
		name:       "rejects an unsupported version",
		configPath: configFixture("unsupported-version.yaml"),
		wantStderr: []string{"version: must be 1 or 2"},
	},
	{
		name:       "rejects a missing file",
		configPath: configFixture("does-not-exist.yaml"),
		wantStderr: []string{"no such file"},
	},
	{
		name:       "rejects an invalid regular expression in an override match",
		configPath: configFixture("version2-match-invalid-regex.yaml"),
		wantStderr: []string{"metrics[0].overrides[0].match[0]", "invalid regular expression"},
	},
	{
		name:       "rejects a group item in an override match",
		configPath: configFixture("version2-match-group-item.yaml"),
		wantStderr: []string{"metrics[0].overrides[0].match[0]", "reserved for groups"},
	},
	{
		name:         "rejects overlapping every overrides",
		configPath:   configFixture("version2-override-overlap.yaml"),
		wantStderr:   []string{"metrics[0].overrides", "matches multiple overrides"},
		yamlPathOnly: true,
	},
	{
		name:         "rejects an every override that matches no field",
		configPath:   configFixture("version2-override-matches-nothing.yaml"),
		wantStderr:   []string{"metrics[0].overrides", "matched no configured fields"},
		yamlPathOnly: true,
	},
	{
		name:       "rejects an override name with invalid characters",
		configPath: configFixture("version2-override-invalid-name.yaml"),
		wantStderr: []string{"metrics[0].overrides[0].name", "may contain only letters"},
	},
	{
		name:       "rejects Kubernetes and container enrichment that are both required",
		configPath: configFixture("version2-enrichment-conflict.yaml"),
		wantStderr: []string{"metrics[0].enrichment", "cannot both be true"},
	},
	{
		name:         "rejects a window on a field that is not a window counter",
		configPath:   configFixture("version2-window-on-normal-field.yaml"),
		wantStderr:   []string{"metrics[0].overrides[0]", "not a window counter"},
		yamlPathOnly: true,
	},
	{
		name:       "rejects a zero XID count window flag",
		args:       []string{"-x", "0"},
		wantStderr: []string{"--xid-count-window-size", "must be greater than 0"},
	},
	{
		name:       "rejects a zero collection interval flag",
		args:       []string{"-c", "0"},
		wantStderr: []string{"--collect-interval", "must be greater than 0"},
	},
	{
		name:       "rejects an unparseable web read timeout flag",
		args:       []string{"--web-read-timeout", "abc"},
		wantStderr: []string{"web-read-timeout", "invalid duration"},
	},
	{
		name:       "rejects an unparseable collection interval variable",
		env:        []string{"DCGM_EXPORTER_INTERVAL=fast"},
		wantStderr: []string{"DCGM_EXPORTER_INTERVAL", "collect-interval"},
	},
	{
		name:       "rejects profiling without a web config file",
		args:       []string{"--enable-pprof"},
		wantStderr: []string{"enable-pprof", "web-config-file"},
	},
	{
		name:       "rejects profiling with a web config file that turns on neither TLS nor basic auth",
		args:       []string{"--enable-pprof", "--web-config-file", "./testdata/web-config-http-only.yml"},
		wantStderr: []string{"enable-pprof", "web-config-http-only.yml", "tls_server_config", "basic_auth_users"},
	},
	{
		name:       "rejects DCGM logging together with a remote hostengine",
		args:       []string{"--enable-dcgm-log", "-r", "127.0.0.1:5555"},
		wantStderr: []string{"enable-dcgm-log", "remote-hostengine-info"},
	},
}

// runConfigStartupError checks that one unusable setting stops the exporter
// with exit status 1, a message that names the YAML path, flag or variable and
// the file, and nothing on stdout. The case fails before DCGM starts, so no
// GPU or DCGM data is needed.
func runConfigStartupError(t testing.TB, tc configStartupErrorCase) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	args := []string{"--address", "127.0.0.1:0"}
	if tc.configPath != "" {
		args = append([]string{"--config-file", tc.configPath}, args...)
	}
	args = append(args, tc.args...)

	failure := runExporterExpectingStartFailure(t, configStartupErrorTimeout, tc.env, args...)
	require.Equal(t, exitInvalid, failure.ExitCode, "exit status; stderr:\n%s", failure.Stderr)
	require.Empty(t, failure.Stdout, "stdout must stay empty")
	for _, want := range tc.wantStderr {
		require.Contains(t, failure.Stderr, want, "stderr must name the problem")
	}
	if tc.configPath != "" && !tc.yamlPathOnly {
		require.Contains(t, failure.Stderr, filepath.Base(tc.configPath), "stderr must name the config file")
	}
}
