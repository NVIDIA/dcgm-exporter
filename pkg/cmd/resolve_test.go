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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/transformation"
)

const shippedDefaultConfig = "../../etc/config.yaml"

// The file installed in images and packages gives the same settings as running
// with no config file and no flags.
func TestShippedDefaultConfigMatchesNoConfigDefaults(t *testing.T) {
	bare, err := runResolvedConfig(t, nil)
	require.NoError(t, err)
	shipped, err := runResolvedConfig(t, map[string]string{"DCGM_EXPORTER_CONFIG_FILE": shippedDefaultConfig})
	require.NoError(t, err)

	want := *bare.Config
	got := *shipped.Config
	assert.Equal(t, shippedDefaultConfig, got.ConfigFile)
	got.ConfigFile = want.ConfigFile
	assert.Equal(t, want, got)
	assert.Empty(t, shipped.Warnings)
	assert.Equal(t, "environment: DCGM_EXPORTER_CONFIG_FILE", shipped.Origin("ConfigFile"))
}

var (
	// commentedKeyLine matches a commented setting such as `# every: 30s` or
	// `#   - name: x`. Prose starts with a capital letter or a plain word, so it does not match.
	commentedKeyLine = regexp.MustCompile(`^(\s*)# (\s*)(- )?[a-z_]+:( |$)`)

	// exampleBlockHeader finds the start of a commented block.
	exampleBlockHeader = regexp.MustCompile(`^\s*# \s*([a-z_]+):`)

	// exampleBlockKeys are the commented blocks that show a feature, not a default.
	exampleBlockKeys = map[string]bool{"fields": true, "overrides": true, "embedded_log": true}
)

// exampleBlockOwners names, for each line, the example block it belongs to, or "" for
// a line outside every example block. A block starts at its commented header and holds
// the commented lines indented deeper than the header.
func exampleBlockOwners(lines []string) []string {
	owners := make([]string, len(lines))
	current, headerIndent := "", 0

	for i, line := range lines {
		match := commentedKeyLine.FindStringSubmatch(line)
		if match == nil {
			current = ""
			continue
		}

		// A line indented no deeper than the header ends the block.
		indent := len(match[2])
		if current != "" && indent <= headerIndent {
			current = ""
		}

		// A header of an example block starts the next block.
		if header := exampleBlockHeader.FindStringSubmatch(line); current == "" && header != nil && exampleBlockKeys[header[1]] {
			current, headerIndent = header[1], indent
		}

		owners[i] = current
	}

	return owners
}

// uncommentShippedConfig deletes the "# " at the comment column of commented setting
// lines in the shipped file. With no blocks it uncomments the defaults and leaves the
// example blocks commented. With blocks it uncomments only those example blocks, and
// comments out the metrics file when `fields` replaces it. The list of flags with no key
// at the end is prose and stays as it is.
func uncommentShippedConfig(t *testing.T, blocks ...string) string {
	t.Helper()
	data, err := os.ReadFile(shippedDefaultConfig)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	owners := exampleBlockOwners(lines)

	// Everything from the flag list on is prose.
	flagList := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(line, "# Flags with no key") {
			flagList = i
			break
		}
	}

	wanted := map[string]bool{}
	for _, block := range blocks {
		wanted[block] = true
	}

	for i, line := range lines {
		match := commentedKeyLine.FindStringSubmatch(line)

		switch {
		case i >= flagList:
		case match == nil:
			if wanted["fields"] && strings.HasPrefix(line, "    file:") {
				lines[i] = "    # " + strings.TrimPrefix(line, "    ")
			}
		case (owners[i] == "" && len(blocks) == 0) || wanted[owners[i]]:
			lines[i] = match[1] + line[len(match[1])+2:]
		}
	}

	return strings.Join(lines, "\n")
}

// withoutEmptySlices makes an empty list and no list compare equal; the file writes
// `[]` where the no-file settings leave the list unset.
func withoutEmptySlices(config appconfig.Config) appconfig.Config {
	if len(config.KubernetesPodLabelAllowlistRegex) == 0 {
		config.KubernetesPodLabelAllowlistRegex = nil
	}
	if len(config.NvidiaResourceNames) == 0 {
		config.NvidiaResourceNames = nil
	}
	return config
}

// Every commented default in the shipped file is a real key with the value the exporter
// uses without a file, so uncommenting all of them changes nothing.
func TestShippedDefaultConfigCommentedDefaultsMatchNoConfigDefaults(t *testing.T) {
	document := uncommentShippedConfig(t)
	for _, block := range []string{"fields:", "overrides:", "embedded_log:"} {
		require.Contains(t, document, "# "+block, "example blocks stay commented")
		require.NotContains(t, document, "\n    "+block, "example blocks stay commented")
	}

	require.Contains(t, document, "\n  kubernetes:\n", "defaults are uncommented")

	bare, err := runResolvedConfig(t, nil)
	require.NoError(t, err)
	uncommented, err := runResolvedConfig(t, map[string]string{"DCGM_EXPORTER_CONFIG_FILE": writeConfigFile(t, document)})
	require.NoError(t, err)

	want := withoutEmptySlices(*bare.Config)
	got := withoutEmptySlices(*uncommented.Config)
	got.ConfigFile = want.ConfigFile
	assert.Equal(t, want, got)
	assert.Empty(t, uncommented.Warnings)
}

// The commented examples for fields, overrides and the embedded DCGM log load when
// uncommented. `fields` replaces the metrics file, so it is loaded on its own.
func TestShippedDefaultConfigCommentedExamplesLoad(t *testing.T) {
	tests := []struct {
		name   string
		blocks []string
	}{
		{name: "inline fields", blocks: []string{"fields"}},
		{name: "overrides and embedded log", blocks: []string{"overrides", "embedded_log"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := uncommentShippedConfig(t, tt.blocks...)

			for _, block := range tt.blocks {
				require.Contains(t, document, "\n    "+block+":")
			}

			// The overrides name fields that are in the repository's counters file.
			countersFile, err := filepath.Abs("../../etc/default-counters.csv")
			require.NoError(t, err)
			document = strings.ReplaceAll(document, "/etc/dcgm-exporter/default-counters.csv", countersFile)

			resolved, err := runResolvedConfig(t, map[string]string{"DCGM_EXPORTER_CONFIG_FILE": writeConfigFile(t, document)})
			require.NoError(t, err)
			assert.Empty(t, resolved.Warnings)
		})
	}
}

// A flag still replaces the default file's value for the same setting.
func TestFlagsOverrideShippedDefaultConfig(t *testing.T) {
	env := map[string]string{"DCGM_EXPORTER_CONFIG_FILE": shippedDefaultConfig}
	resolved, err := runResolvedConfig(t, env, "-f", "/custom/counters.csv", "-c", "10000", "-k")
	require.NoError(t, err)
	assert.Equal(t, "/custom/counters.csv", resolved.Config.MetricSource.File)
	assert.Equal(t, 10000, resolved.Config.CollectInterval)
	assert.True(t, resolved.Config.Kubernetes)
}

// An image that sets DCGM_EXPORTER_CONFIG_FILE fails to start when the file is
// missing, for example after a volume is mounted over /etc/dcgm-exporter. The
// error names the file and the variable.
func TestMissingDefaultConfigFileNamesFileAndVariable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.yaml")
	_, err := runResolvedConfig(t, map[string]string{"DCGM_EXPORTER_CONFIG_FILE": missing})
	require.Error(t, err)
	assert.ErrorContains(t, err, missing)
	assert.ErrorContains(t, err, "DCGM_EXPORTER_CONFIG_FILE")
}

// The chart's default config file, with the environment variable the chart sets,
// gives the settings the exporter has with no file, except that the file turns
// Kubernetes labels on as auto and reads the metrics list the chart mounts.
func TestChartDefaultConfigWithChartEnvironment(t *testing.T) {
	data, err := os.ReadFile("../../deployment/values.yaml")
	require.NoError(t, err)
	var values struct {
		Config map[string]interface{} `yaml:"config"`
	}

	require.NoError(t, yaml.Unmarshal(data, &values))
	document, err := yaml.Marshal(values.Config)
	require.NoError(t, err)
	chartFile := writeConfigFile(t, string(document))

	chartEnv := map[string]string{"DCGM_EXPORTER_LISTEN": ":9400"}
	bare, err := runResolvedConfig(t, chartEnv)
	require.NoError(t, err)
	withFile := map[string]string{"DCGM_EXPORTER_CONFIG_FILE": chartFile}
	for name, value := range chartEnv {
		withFile[name] = value
	}

	chart, err := runResolvedConfig(t, withFile)
	require.NoError(t, err)

	assert.True(t, chart.Config.Kubernetes)
	assert.Equal(t, appconfig.EnrichmentAuto, chart.Config.EnrichmentModes.Kubernetes)
	assert.Equal(t, "/etc/dcgm-exporter/chart/metrics/default-counters.csv", chart.Config.MetricSource.File)

	want := *bare.Config
	got := *chart.Config
	got.ConfigFile = want.ConfigFile
	got.Kubernetes, got.EnrichmentModes.Kubernetes = want.Kubernetes, want.EnrichmentModes.Kubernetes
	got.MetricSource.File = want.MetricSource.File
	assert.Equal(t, want, got)
}

// TestResolveConfigReportsEveryProblemTogether checks resolution does not stop
// at the first problem, so inspection can report them all.
func TestResolveConfigReportsEveryProblemTogether(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	resolved, err := resolveFile(t, "version: 2\nserver: {read_timeout: 0s}\n",
		"--web-write-timeout", "soon", "--devices", "g:", "--xid-count-window-size", "0")

	require.Error(t, err)
	require.NotNil(t, resolved, "the partly resolved config is returned for inspection")
	problems := joinedErrors(err)
	require.Len(t, problems, 4, "%v", err)
	assert.ErrorContains(t, err, "server.read_timeout: must be greater than 0")
	assert.ErrorContains(t, err, "parse web-write-timeout")
	assert.ErrorContains(t, err, "invalid xid-count-window-size parameter value: 0")
}

// TestResolveConfigSkipsCombinationChecksWhenTheFileDidNotLoad checks a file
// that failed to load does not cause problems about settings it might have held.
func TestResolveConfigSkipsCombinationChecksWhenTheFileDidNotLoad(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	_, err := resolveFile(t, "version: 2\nserver: {read_timeout: 0s}\n", "--enable-pprof")
	require.Error(t, err)
	assert.ErrorContains(t, err, "server.read_timeout", "the file is rejected while it loads")
	assert.Len(t, joinedErrors(err), 1, "pprof needs a web config file that the broken file may have set: %v", err)

	_, err = resolveFile(t, "version: 2\n", "--enable-pprof")
	assert.ErrorContains(t, err, "enable-pprof requires web-config-file")
}

// TestResolveConfigKeepsTheFileLayerForInspection checks the file's own result
// and origins are kept apart from what the environment and flags changed.
func TestResolveConfigKeepsTheFileLayerForInspection(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	resolved, err := resolveFile(t, "version: 2\nserver: {max_concurrent_scrapes: 3}\n", "--max-concurrent-scrapes", "5")
	require.NoError(t, err)
	assert.Equal(t, 5, resolved.Config.MaxConcurrentScrapes)
	require.NotNil(t, resolved.FileConfig)
	assert.Equal(t, 3, resolved.FileConfig.MaxConcurrentScrapes)
	assert.Equal(t, appconfig.SettingOrigin{Kind: appconfig.OriginFile, Key: "server.max_concurrent_scrapes"}, resolved.FileOrigins["MaxConcurrentScrapes"])
	assert.Equal(t, appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--max-concurrent-scrapes"}, resolved.Origins["MaxConcurrentScrapes"])

	resolved, err = resolveFile(t, "version: 2\n")
	require.NoError(t, err)
	assert.NotNil(t, resolved.FileConfig, "a file with no settings still has a file layer")
	assert.Empty(t, resolved.FileOrigins["MaxConcurrentScrapes"])
}

// joinedErrors splits errors joined with errors.Join, at any depth, into the
// individual problems.
func joinedErrors(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var all []error
		for _, inner := range joined.Unwrap() {
			all = append(all, joinedErrors(inner)...)
		}

		return all
	}

	return []error{err}
}

// Tests that reach a required source that is not there would otherwise wait
// the full production grace period.
func init() {
	enrichmentWait = transformation.EnrichmentWait{
		Grace: 150 * time.Millisecond, Poll: 10 * time.Millisecond, LogEvery: time.Minute,
	}
}

// resolveStartupConfig runs the startup steps in order: the config, then the
// enrichment sources.
func resolveStartupConfig(c *cli.Context) (*appconfig.Resolved, error) {
	resolved, err := resolveRuntimeConfig(c)
	if err != nil {
		return nil, err
	}

	if err := resolveEnrichment(context.Background(), resolved); err != nil {
		return nil, err
	}

	return resolved, nil
}

// writeEnrichmentConfig writes a config file with the given enrichment block
// and returns its path.
func writeEnrichmentConfig(t *testing.T, enrichment string) string {
	t.Helper()
	return writeEnrichmentDocument(t, enrichment, "")
}

// writeEnrichmentDocument writes a config file with one metric entry, the
// given enrichment block and extra top-level YAML such as a sources block.
func writeEnrichmentDocument(t *testing.T, enrichment, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	doc := "version: 2\nmetrics:\n  - name: gpu\n    file: /etc/dcgm-exporter/default-counters.csv\n" +
		"    enrichment: " + enrichment + "\n" +
		"outputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n" + extra
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	return path
}

// A flag that sets a source replaces the file's choice and keeps the
// best-effort meaning: it is never required and never checked at startup.
func TestEnrichmentFlagOverridesFileMode(t *testing.T) {
	file := writeEnrichmentConfig(t, "{kubernetes: true, container: false, dra: false, hpc_jobs: false}")
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, file))

	resolved, err := resolveConfig(ctx, "", nil, true)
	require.NoError(t, err)
	assert.True(t, resolved.Config.Kubernetes)
	assert.Equal(t, appconfig.EnrichmentRequired, resolved.Config.EnrichmentModes.Kubernetes)
	assert.Equal(t, "YAML: metrics[0].enrichment.kubernetes", resolved.Origin("Kubernetes"))

	require.NoError(t, ctx.Set(CLIKubernetes, "true"))
	resolved, err = resolveConfig(ctx, "", nil, true)
	require.NoError(t, err)
	assert.True(t, resolved.Config.Kubernetes)
	assert.Equal(t, appconfig.EnrichmentUnset, resolved.Config.EnrichmentModes.Kubernetes)
	assert.NotContains(t, resolved.Origin("Kubernetes"), "YAML")

	require.NoError(t, ctx.Set(CLIKubernetes, "false"))
	resolved, err = resolveConfig(ctx, "", nil, true)
	require.NoError(t, err)
	assert.False(t, resolved.Config.Kubernetes)
	assert.Equal(t, appconfig.EnrichmentUnset, resolved.Config.EnrichmentModes.Kubernetes)
}

// A required source that is missing stops startup after the wait; an auto one
// only warns and stays on.
func TestRuntimeConfigEnrichmentRequiredAndAuto(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile,
		writeEnrichmentConfig(t, "{kubernetes: true, container: false, dra: false, hpc_jobs: false}")))

	require.NoError(t, ctx.Set(CLIPodResourcesKubeletSocket, missing))
	_, err := resolveStartupConfig(ctx)
	require.ErrorContains(t, err, "enrichment.kubernetes is true but Kubernetes is unavailable")
	require.ErrorContains(t, err, "set by YAML: metrics[0].enrichment.kubernetes")

	// An auto container source that is not usable stays on with a warning, so
	// the runtime can use it when it appears, whatever is wrong with the path.
	notASocket := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notASocket, nil, 0o600))
	for _, socket := range []string{missing, notASocket} {
		ctx = newTestCLIContext(t)
		require.NoError(t, ctx.Set(CLIConfigFile,
			writeEnrichmentConfig(t, "{kubernetes: false, container: auto, dra: false, hpc_jobs: false}")))

		require.NoError(t, ctx.Set(CLIContainerRuntimeSocket, socket))
		resolved, err := resolveStartupConfig(ctx)
		require.NoError(t, err)
		assert.True(t, resolved.Config.ContainerLabels)
		require.Len(t, resolved.Warnings, 1)
		assert.Contains(t, resolved.Warnings[0].Message, "stays on")
		assert.Contains(t, resolved.Warnings[0].Message, "set by YAML: metrics[0].enrichment.container")
		assert.Contains(t, resolved.Warnings[0].Message, "runtime socket set by")
	}
}

// A switch the file leaves out is reported as a key not set in the file, and
// its related settings as set by default.
func TestEnrichmentWarningCallsAnOmittedSwitchTheDefault(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, writeEnrichmentConfig(t, "{kubernetes: false, dra: false, hpc_jobs: false}")))
	require.NoError(t, ctx.Set(CLIContainerRuntimeSocket, missing))
	resolved, err := resolveStartupConfig(ctx)
	require.NoError(t, err)
	require.Len(t, resolved.Warnings, 1)
	message := resolved.Warnings[0].Message
	assert.Contains(t, message, "metrics[0].enrichment.container is not set in the file")
	assert.Contains(t, message, "runtime socket set by default")
	assert.NotContains(t, message, "(auto)")
	assert.NotContains(t, message, "set by YAML")
}

// Setting a source by flag drops the file's mode for that source and no other.
// Environment variables are bound to the same flags, so they take this path too.
func TestEnrichmentFlagsClearOnlyTheirOwnMode(t *testing.T) {
	file := writeEnrichmentDocument(t, "{kubernetes: true, container: auto, dra: auto, hpc_jobs: true}",
		"sources:\n  dra: {mode: kubernetes}\n")

	fileModes := appconfig.EnrichmentModes{
		Kubernetes: appconfig.EnrichmentRequired, Container: appconfig.EnrichmentAuto,
		DRA: appconfig.EnrichmentAuto, HPCJobs: appconfig.EnrichmentRequired,
	}

	tests := []struct {
		flag, value string
		cleared     func(*appconfig.EnrichmentModes)
	}{
		{CLIKubernetes, "true", func(m *appconfig.EnrichmentModes) { m.Kubernetes = appconfig.EnrichmentUnset }},
		{CLIContainerLabels, "true", func(m *appconfig.EnrichmentModes) { m.Container = appconfig.EnrichmentUnset }},
		{CLIKubernetesEnableDRA, "true", func(m *appconfig.EnrichmentModes) { m.DRA = appconfig.EnrichmentUnset }},
		{CLIHPCJobMappingDir, "/jobs", func(m *appconfig.EnrichmentModes) { m.HPCJobs = appconfig.EnrichmentUnset }},
		// Source paths do not change how a switch is treated.
		{CLIContainerRuntimeSocket, "/run/other.sock", func(*appconfig.EnrichmentModes) {}},
	}

	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIConfigFile, file))
			require.NoError(t, ctx.Set(tt.flag, tt.value))
			resolved, err := resolveConfig(ctx, "", nil, true)
			require.NoError(t, err)
			want := fileModes
			tt.cleared(&want)
			assert.Equal(t, want, resolved.Config.EnrichmentModes)
		})
	}
}

// Every required source that cannot be used names its switch and the related
// settings with where each came from.
func TestRequiredEnrichmentFailuresNameTheirSource(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	allOff := map[string]string{"kubernetes": "false", "container": "false", "dra": "false", "hpc_jobs": "false"}
	tests := []struct {
		name    string
		on      map[string]string
		sources string
		flags   map[string]string
		want    []string
	}{
		{
			name: "kubernetes", on: map[string]string{"kubernetes": "true"},
			want: []string{"set by YAML: metrics[0].enrichment.kubernetes", "kubelet socket set by"},
		},
		{
			name: "container", on: map[string]string{"container": "true"},
			sources: "sources:\n  container: {runtime_socket: " + missing + "}\n",
			want: []string{
				"set by YAML: metrics[0].enrichment.container",
				"runtime socket set by YAML: sources.container.runtime_socket",
			},
		},
		{
			name: "dra", on: map[string]string{"dra": "true", "kubernetes": "auto"},
			sources: "sources:\n  dra: {mode: kubernetes}\n",
			want:    []string{"set by YAML: metrics[0].enrichment.dra", "Kubernetes set by "},
		},
		{
			name: "hpc_jobs", on: map[string]string{"hpc_jobs": "true"},
			sources: "sources:\n  hpc_jobs: {mapping_dir: " + missing + "}\n",
			want: []string{
				"set by YAML: metrics[0].enrichment.hpc_jobs",
				"mapping directory set by YAML: sources.hpc_jobs.mapping_dir",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switches := maps.Clone(allOff)
			maps.Copy(switches, tt.on)
			enrichment := fmt.Sprintf("{kubernetes: %s, container: %s, dra: %s, hpc_jobs: %s}",
				switches["kubernetes"], switches["container"], switches["dra"], switches["hpc_jobs"])
			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIConfigFile, writeEnrichmentDocument(t, enrichment, tt.sources)))
			require.NoError(t, ctx.Set(CLIPodResourcesKubeletSocket, missing))
			for flag, value := range tt.flags {
				require.NoError(t, ctx.Set(flag, value))
			}
			_, err := resolveStartupConfig(ctx)
			require.Error(t, err)
			for _, want := range tt.want {
				assert.ErrorContains(t, err, want)
			}
		})
	}
}

// Kubernetes wins over container labels without a warning when the config
// file asked for both; two flags that conflict still get the warning.
func TestContainerLabelsWarningFollowsKubernetesWins(t *testing.T) {
	const ignored = "container runtime labels are ignored when kubernetes mode is enabled"

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile, writeEnrichmentConfig(t, "{kubernetes: auto, container: auto}")))
	resolved, err := resolveConfig(ctx, "", nil, true)
	require.NoError(t, err)
	addContainerIgnoredWarning(resolved)
	assert.NotContains(t, warningMessages(resolved), ignored)

	ctx = newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIKubernetes, "true"))
	require.NoError(t, ctx.Set(CLIContainerLabels, "true"))
	require.NoError(t, ctx.Set(CLIContainerRuntimeSocket, "/run/runtime.sock"))
	resolved, err = resolveConfig(ctx, "", nil, true)
	require.NoError(t, err)
	addContainerIgnoredWarning(resolved)
	assert.Contains(t, warningMessages(resolved), ignored)
}

// warningMessages lists the text of the warnings collected so far.
func warningMessages(r *appconfig.Resolved) []string {
	messages := make([]string, 0, len(r.Warnings))
	for _, w := range r.Warnings {
		messages = append(messages, w.Message)
	}

	return messages
}

// Kubernetes wins over container labels only if its kubelet socket is usable at
// startup. When it is not and container labels are on with a socket, this
// process uses container labels and Kubernetes stays off. Without a usable container
// socket there is nothing to fall back to, so Kubernetes stays on.
func TestKubernetesAutoFallsBackToContainerLabelsOnlyWhenTheyCanRun(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIConfigFile,
		writeEnrichmentConfig(t, "{kubernetes: auto, container: false, dra: false, hpc_jobs: false}")))

	require.NoError(t, ctx.Set(CLIPodResourcesKubeletSocket, missing))
	require.NoError(t, ctx.Set(CLIContainerLabels, "true"))

	// A runtime socket nothing listens on is no fallback.
	require.NoError(t, ctx.Set(CLIContainerRuntimeSocket, missing))
	resolved, err := resolveStartupConfig(ctx)
	require.NoError(t, err)
	assert.True(t, resolved.Config.Kubernetes)

	// A runtime socket that answers the container list is a fallback.
	runtimeSocket := filepath.Join(t.TempDir(), "r.sock")
	listener, err := net.Listen("unix", runtimeSocket)
	require.NoError(t, err)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Answer only the bounded container list the startup check makes.
		if r.Method != http.MethodGet || r.URL.Path != "/containers/json" || r.URL.RawQuery != "limit=1" {
			http.NotFound(w, r)

			return
		}

		// A check cut off at its limit hangs up mid-reply; that write error is expected.
		_, _ = w.Write([]byte("[]"))
	})
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: handler}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve runtime API: %v", err)
		}
	}()
	t.Cleanup(func() { server.Close() })
	require.NoError(t, ctx.Set(CLIContainerRuntimeSocket, runtimeSocket))
	resolved, err = resolveStartupConfig(ctx)
	require.NoError(t, err)
	assert.True(t, resolved.Config.ContainerLabels)
	assert.False(t, resolved.Config.Kubernetes)
	assert.Contains(t, strings.Join(warningMessages(resolved), "\n"), "Kubernetes stays off until the exporter restarts")
	for _, w := range resolved.Warnings {
		assert.NotEqual(t, containerIgnoredMessage, w.Message)
	}
}

// runResolvedConfig runs the real app with the given environment and arguments
// and returns what startup resolved, without starting the exporter.
func runResolvedConfig(t *testing.T, env map[string]string, args ...string) (*appconfig.Resolved, error) {
	t.Helper()
	unsetFlagEnvVars(t, NewApp("test").Flags)
	for name, value := range env {
		t.Setenv(name, value)
	}

	var resolved *appconfig.Resolved
	var resolveErr error
	app := NewApp("test")
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	app.Action = func(c *cli.Context) error {
		resolved, resolveErr = resolveStartupConfig(c)
		return nil
	}

	require.NoError(t, app.Run(append([]string{"dcgm-exporter"}, args...)))
	return resolved, resolveErr
}

// An environment variable or a real command-line flag replaces the file's mode
// for its own source. The origin names where the setting came from.
func TestEnrichmentEnvAndArgvThroughTheRealApp(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	file := writeEnrichmentDocument(t, "{kubernetes: auto, container: false, dra: auto, hpc_jobs: auto}",
		"sources:\n  dra: {mode: kubernetes}\n")

	tests := []struct {
		name       string
		env        map[string]string
		args       []string
		field      string
		wantOrigin string
		check      func(*testing.T, *appconfig.Config)
	}{
		{
			name: "env Kubernetes", env: map[string]string{"DCGM_EXPORTER_KUBERNETES": "true"},
			field: "Kubernetes", wantOrigin: "environment: DCGM_EXPORTER_KUBERNETES",
			check: func(t *testing.T, c *appconfig.Config) {
				// Not switched off although the socket is missing: nothing else could supply labels.
				assert.True(t, c.Kubernetes)
				assert.Equal(t, appconfig.EnrichmentUnset, c.EnrichmentModes.Kubernetes)
			},
		},
		{
			name: "argv container labels", args: []string{"--container-labels=true", "--container-runtime-socket=" + missing},
			field: "ContainerLabels", wantOrigin: "argv: --container-labels",
			check: func(t *testing.T, c *appconfig.Config) {
				assert.Equal(t, appconfig.EnrichmentUnset, c.EnrichmentModes.Container)
			},
		},
		{
			name: "env DRA", env: map[string]string{"KUBERNETES_ENABLE_DRA": "true"},
			field: "KubernetesEnableDRA", wantOrigin: "environment: KUBERNETES_ENABLE_DRA",
			check: func(t *testing.T, c *appconfig.Config) {
				assert.True(t, c.KubernetesEnableDRA)
				assert.Equal(t, appconfig.EnrichmentUnset, c.EnrichmentModes.DRA)
			},
		},
		{
			name: "env HPC directory", env: map[string]string{"DCGM_HPC_JOB_MAPPING_DIR": missing},
			field: "HPCJobMappingDir", wantOrigin: "environment: DCGM_HPC_JOB_MAPPING_DIR",
			check: func(t *testing.T, c *appconfig.Config) {
				assert.Equal(t, missing, c.HPCJobMappingDir)
				assert.Equal(t, appconfig.EnrichmentUnset, c.EnrichmentModes.HPCJobs)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--config-file=" + file, "--pod-resources-kubelet-socket=" + missing}, tt.args...)
			resolved, err := runResolvedConfig(t, tt.env, args...)
			require.NoError(t, err)
			assert.Equal(t, tt.wantOrigin, resolved.Origin(tt.field))
			tt.check(t, resolved.Config)
		})
	}
}

// Required-source errors give the exact origin of each related setting when a
// real command line sets it. The stubbed context in the other tests does not
// record argv origins, so this drives the real app.
func TestRequiredEnrichmentErrorsNameArgvOriginsThroughTheRealApp(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	kubernetesFile := writeEnrichmentConfig(t, "{kubernetes: true, container: false, dra: false, hpc_jobs: false}")
	containerFile := writeEnrichmentDocument(t, "{kubernetes: false, container: true, dra: false, hpc_jobs: false}",
		"sources:\n  container: {runtime_socket: "+missing+"}\n")

	_, err := runResolvedConfig(t, nil, "--config-file="+kubernetesFile, "--pod-resources-kubelet-socket="+missing)
	require.ErrorContains(t, err, "set by YAML: metrics[0].enrichment.kubernetes")
	assert.ErrorContains(t, err, "kubelet socket set by argv: --pod-resources-kubelet-socket")

	// A flag that turns Kubernetes on is auto, so a required container source displaces it.
	resolved, err := runResolvedConfig(t, nil, "--config-file="+containerFile, "--kubernetes",
		"--pod-resources-kubelet-socket="+missing)
	require.ErrorContains(t, err, "enrichment.container is true but the container runtime is unavailable")
	assert.Nil(t, resolved)
}

// Required Kubernetes enrichment with a kubelet socket that is not there fails
// startup after the short test wait, and the error names the missing socket path.
func TestRequiredKubernetesEnrichmentNamesTheMissingKubeletSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "kubelet.sock")
	file := writeEnrichmentConfig(t, "{kubernetes: true, container: false, dra: false, hpc_jobs: false}")

	resolved, err := runResolvedConfig(t, nil, "--config-file="+file, "--pod-resources-kubelet-socket="+socket)

	assert.Nil(t, resolved)
	require.ErrorContains(t, err, "metrics[0].enrichment.kubernetes is true but Kubernetes is unavailable")
	assert.ErrorContains(t, err, socket)
	assert.ErrorContains(t, err, "still unavailable after waiting "+enrichmentWait.Grace.String())
}

// runStartupConfigAndLogger runs the production startup step through the real
// app and restores the logger it installs.
func runStartupConfigAndLogger(t *testing.T, ctx context.Context, args ...string) (*appconfig.Config, error) {
	t.Helper()
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	unsetFlagEnvVars(t, NewApp("test").Flags)
	var config *appconfig.Config
	var resolveErr error
	app := NewApp("test")
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	app.Action = func(c *cli.Context) error {
		var resolved *appconfig.Resolved
		resolved, resolveErr = resolveConfigAndLogger(ctx, c)
		if resolved != nil {
			config = resolved.Config
		}

		return nil
	}
	require.NoError(t, app.Run(append([]string{"dcgm-exporter"}, args...)))
	return config, resolveErr
}

// Startup waits for a required source before anything is served, and goes on
// as soon as the source is there.
func TestStartupWaitsForARequiredSourceThatAppears(t *testing.T) {
	previous := enrichmentWait
	t.Cleanup(func() { enrichmentWait = previous })
	enrichmentWait = transformation.EnrichmentWait{Grace: 5 * time.Second, Poll: 5 * time.Millisecond, LogEvery: time.Second}

	dir := filepath.Join(t.TempDir(), "jobs")
	file := writeEnrichmentDocument(t, "{kubernetes: false, container: false, dra: false, hpc_jobs: true}",
		"sources:\n  hpc_jobs: {mapping_dir: "+dir+"}\n")
	const delay = 80 * time.Millisecond
	// The startup wait fails the test if the directory never appears.
	time.AfterFunc(delay, func() { _ = os.Mkdir(dir, 0o700) })

	start := time.Now()
	config, err := runStartupConfigAndLogger(t, context.Background(), "--config-file="+file)

	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), delay)
	assert.Equal(t, dir, config.HPCJobMappingDir)
}

// A required source that never appears fails startup after the grace period and
// the error names the YAML key and where the path came from.
func TestStartupFailsWhenARequiredSourceNeverAppears(t *testing.T) {
	previous := enrichmentWait
	t.Cleanup(func() { enrichmentWait = previous })
	enrichmentWait = transformation.EnrichmentWait{Grace: 100 * time.Millisecond, Poll: 5 * time.Millisecond, LogEvery: time.Second}

	dir := filepath.Join(t.TempDir(), "jobs")
	file := writeEnrichmentDocument(t, "{kubernetes: false, container: false, dra: false, hpc_jobs: true}",
		"sources:\n  hpc_jobs: {mapping_dir: "+dir+"}\n")

	config, err := runStartupConfigAndLogger(t, context.Background(), "--config-file="+file)

	assert.Nil(t, config)
	require.ErrorContains(t, err, "metrics[0].enrichment.hpc_jobs is true")
	assert.ErrorContains(t, err, dir)
	assert.ErrorContains(t, err, "still unavailable after waiting 100ms")
	assert.ErrorContains(t, err, "mapping directory set by YAML: sources.hpc_jobs.mapping_dir")
}

// An auto source, and a source a flag turned on, never fail or delay startup.
func TestStartupDoesNotWaitForAutoOrFlagSources(t *testing.T) {
	previous := enrichmentWait
	t.Cleanup(func() { enrichmentWait = previous })
	enrichmentWait = transformation.EnrichmentWait{Grace: time.Minute, Poll: time.Minute, LogEvery: time.Minute}
	missing := filepath.Join(t.TempDir(), "missing")

	auto := writeEnrichmentDocument(t, "{kubernetes: false, container: false, dra: false, hpc_jobs: auto}",
		"sources:\n  hpc_jobs: {mapping_dir: "+missing+"}\n")
	flagFile := writeEnrichmentConfig(t, "{kubernetes: false, container: false, dra: false, hpc_jobs: true}")
	for name, args := range map[string][]string{
		"file auto": {"--config-file=" + auto},
		"flag":      {"--config-file=" + flagFile, "--hpc-job-mapping-dir=" + missing},
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			config, err := runStartupConfigAndLogger(t, context.Background(), args...)
			require.NoError(t, err)
			assert.Less(t, time.Since(start), 10*time.Second)
			assert.Equal(t, missing, config.HPCJobMappingDir, "the source stays on")
		})
	}
}

// Stopping the exporter during the wait ends startup with the context error.
func TestStartupStopsWaitingWhenTheExporterIsStopped(t *testing.T) {
	previous := enrichmentWait
	t.Cleanup(func() { enrichmentWait = previous })
	enrichmentWait = transformation.EnrichmentWait{Grace: time.Minute, Poll: 5 * time.Millisecond, LogEvery: time.Minute}
	file := writeEnrichmentDocument(t, "{kubernetes: false, container: false, dra: false, hpc_jobs: true}",
		"sources:\n  hpc_jobs: {mapping_dir: "+filepath.Join(t.TempDir(), "jobs")+"}\n")

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := runStartupConfigAndLogger(t, ctx, "--config-file="+file)

	require.ErrorIs(t, err, context.Canceled)
}

// Inspection commands read the file only. They report that source availability
// is a startup check instead of waiting for a required source.
func TestConfigInspectionDoesNotWaitForEnrichmentSources(t *testing.T) {
	previous := enrichmentWait
	t.Cleanup(func() { enrichmentWait = previous })
	enrichmentWait = transformation.EnrichmentWait{Grace: time.Minute, Poll: time.Minute, LogEvery: time.Minute}
	// Validation reads the metric file, so it must exist on any host.
	dir := t.TempDir()
	counters := filepath.Join(dir, "counters.csv")
	require.NoError(t, os.WriteFile(counters, []byte("DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature.\n"), 0o600))
	file := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte("version: 2\nmetrics:\n  - name: gpu\n    file: "+counters+"\n"+
		"    enrichment: {kubernetes: true, container: false, dra: false, hpc_jobs: false}\n"+
		"outputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n"+
		"sources:\n  kubernetes: {pod_resources_socket: "+filepath.Join(dir, "missing.sock")+"}\n"), 0o600))

	for _, command := range []string{"validate", "explain"} {
		t.Run(command, func(t *testing.T) {
			start := time.Now()
			stdout, _, err := inspectionRun(t, "config", command, file)
			require.NoError(t, err)
			assert.Less(t, time.Since(start), 10*time.Second)
			assert.Contains(t, stdout, "Not checked (needs the running exporter):")
			assert.Contains(t, stdout, "Kubernetes, container and HPC label sources")
		})
	}
}

// A source that a flag turned on is named by that flag, not by a YAML key.
func TestEnrichmentWarningNamesTheFlagThatTurnedTheSourceOn(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	resolved, err := runResolvedConfig(t, nil, "--kubernetes", "--pod-resources-kubelet-socket="+missing)
	require.NoError(t, err)

	messages := strings.Join(warningMessages(resolved), "\n")
	assert.Contains(t, messages, "turned on by argv: --kubernetes")
	assert.Contains(t, messages, "kubelet socket set by argv: --pod-resources-kubelet-socket")
	assert.NotContains(t, messages, "set by YAML")
}

// Container labels switched on by a flag or variable use the default runtime
// socket when no layer names one, whatever the config file says about them. A
// socket that is named is never replaced, and labels that stay off use none.
func TestContainerLabelsFallBackToTheDefaultRuntimeSocket(t *testing.T) {
	const defaultSocket = "/var/run/docker.sock"
	off := writeEnrichmentConfig(t, "{kubernetes: false, container: false, dra: false, hpc_jobs: false}")
	offWithSocket := writeEnrichmentDocument(t, "{kubernetes: false, container: false, dra: false, hpc_jobs: false}",
		"sources:\n  container: {runtime_socket: /run/file.sock}\n")

	tests := []struct {
		name       string
		env        map[string]string
		args       []string
		wantLabels bool
		wantSocket string
	}{
		{
			name: "file off and flag on", args: []string{"--config-file=" + off, "--container-labels"},
			wantLabels: true, wantSocket: defaultSocket,
		},
		{
			name: "file off and environment on", env: map[string]string{"DCGM_EXPORTER_CONTAINER_LABELS": "true"},
			args: []string{"--config-file=" + off}, wantLabels: true, wantSocket: defaultSocket,
		},
		{
			name: "file socket and flag on", args: []string{"--config-file=" + offWithSocket, "--container-labels"},
			wantLabels: true, wantSocket: "/run/file.sock",
		},
		{
			name: "flag socket wins", args: []string{"--config-file=" + off, "--container-labels", "--container-runtime-socket=/run/flag.sock"},
			wantLabels: true, wantSocket: "/run/flag.sock",
		},
		{
			name: "flag only without a file", args: []string{"--container-labels"},
			wantLabels: true, wantSocket: defaultSocket,
		},
		{
			name: "labels off", args: []string{"--config-file=" + off},
			wantLabels: false, wantSocket: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := runResolvedConfig(t, tt.env, tt.args...)

			require.NoError(t, err)
			assert.Equal(t, tt.wantLabels, resolved.Config.ContainerLabels)
			assert.Equal(t, tt.wantSocket, resolved.Config.ContainerRuntimeSocket)
		})
	}
}

// A stop signal while startup waits for a required source is a clean stop, not
// a startup failure.
func TestStopDuringTheRequiredSourceWaitIsACleanStop(t *testing.T) {
	previous := enrichmentWait
	t.Cleanup(func() { enrichmentWait = previous })
	enrichmentWait = transformation.EnrichmentWait{Grace: time.Minute, Poll: 5 * time.Millisecond, LogEvery: time.Minute}
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	unsetFlagEnvVars(t, NewApp("test").Flags)
	file := writeEnrichmentDocument(t, "{kubernetes: false, container: false, dra: false, hpc_jobs: true}",
		"sources:\n  hpc_jobs: {mapping_dir: "+filepath.Join(t.TempDir(), "jobs")+"}\n")

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	var runErr error
	app := NewApp("test")
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	app.Action = func(c *cli.Context) error {
		runErr = runDCGMExporter(ctx, c, nil)
		return nil
	}
	require.NoError(t, app.Run([]string{"dcgm-exporter", "--config-file=" + file}))

	assert.NoError(t, runErr)
}

// TestEnrichmentFieldOriginsNameSettings checks that each setting that the
// startup messages for an enrichment source name is a field of the settings
// table, so a renamed setting cannot leave a message without its origin.
func TestEnrichmentFieldOriginsNameSettings(t *testing.T) {
	known := map[string]bool{}
	for _, setting := range appconfig.SettingKeys(appconfig.DefaultConfig(), appconfig.ConfigNames{}) {
		known[setting.Field] = true
	}

	for source, origins := range enrichmentFieldOrigins {
		assert.True(t, known[origins.switchField], "source %v: %q is not a setting field", source, origins.switchField)
		for _, related := range origins.related {
			assert.True(t, known[related.field], "source %v: %q is not a setting field", source, related.field)
		}
	}
}

// The HPC job labels turned on by a flag over a file that required them are
// named by the flag, not by the file's switch.
func TestHPCJobsWarningNamesTheFlagThatTurnedTheSourceOn(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	file := writeEnrichmentConfig(t, "{kubernetes: false, container: false, dra: false, hpc_jobs: true}")
	resolved, err := runResolvedConfig(t, nil, "--config-file", file, "--hpc-job-mapping-dir="+missing)
	require.NoError(t, err)

	messages := strings.Join(warningMessages(resolved), "\n")
	assert.Contains(t, messages, "turned on by argv: --hpc-job-mapping-dir")
	assert.NotContains(t, messages, "set by YAML")
}

// A flag after -- replaces the environment value of the same setting, so a
// value the environment gave that cannot be used is not a problem; only the
// value that wins is checked.
func TestResolveConfigIgnoresAnEnvironmentValueThatATailFlagReplaces(t *testing.T) {
	csv := inspectionCSV(t, explainCSVRows)
	tests := []struct {
		name, env, bad string
		tail           []string
	}{
		{"gpu devices", "DCGM_EXPORTER_DEVICES_STR", "bogus", []string{"--devices", "f"}},
		{"switch devices", "DCGM_EXPORTER_OTHER_DEVICES_STR", "bogus", []string{"--switch-devices", "f"}},
		{"cpu devices", "DCGM_EXPORTER_CPU_DEVICES_STR", "bogus", []string{"--cpu-devices", "f"}},
		{"web read timeout", "DCGM_EXPORTER_WEB_READ_TIMEOUT", "soon", []string{"--web-read-timeout", "5s"}},
		{"web write timeout", "DCGM_EXPORTER_WEB_WRITE_TIMEOUT", "soon", []string{"--web-write-timeout", "5s"}},
		{"poll interval", "DCGM_EXPORTER_GPU_BIND_UNBIND_POLL_INTERVAL", "soon", []string{"--gpu-bind-unbind-poll-interval", "5s"}},
		{"collect interval", "DCGM_EXPORTER_INTERVAL", "bogus", []string{"--collect-interval", "5000"}},
		{"xid window", "DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE", "bogus", []string{"--xid-count-window-size", "5000"}},
		{"max concurrent scrapes", "DCGM_EXPORTER_MAX_CONCURRENT_SCRAPES", "bogus", []string{"--max-concurrent-scrapes", "4"}},
		{"configmap source replaced by a file", "DCGM_EXPORTER_CONFIGMAP_DATA", "malformed", []string{"--collectors", csv}},
		{
			"configmap source replaced by another configmap", "DCGM_EXPORTER_CONFIGMAP_DATA", "malformed",
			[]string{"--configmap-data", "default:metrics"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			t.Setenv(tt.env, tt.bad)
			path := explainFile(t, "")

			for _, command := range []string{"show", "validate", "explain"} {
				_, stderr, err := inspectionRun(t, append([]string{"config", command, path, "--"}, tt.tail...)...)
				require.NoError(t, err, "%s: %s", command, stderr)
				assert.NotContains(t, stderr, "error ", "%s reported a problem", command)
			}

			// Without the flag, the same value is the problem, and the report names the variable.
			_, stderr, err := inspectionRun(t, "config", "validate", path)
			require.Error(t, err)
			assert.Contains(t, stderr, tt.env)
		})
	}
}

// An empty --configmap-data after -- selects nothing, so it does not replace a
// malformed DCGM_EXPORTER_CONFIGMAP_DATA, which stays a problem.
func TestResolveConfigKeepsAMalformedConfigMapWhenTheTailFlagIsEmpty(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	t.Setenv("DCGM_EXPORTER_CONFIGMAP_DATA", "malformed")
	path := explainFile(t, "")

	_, stderr, err := inspectionRun(t, "config", "validate", path, "--", "--configmap-data", "")

	require.Error(t, err)
	assert.Contains(t, stderr, "DCGM_EXPORTER_CONFIGMAP_DATA")
}

// The environment value stays listed as replaced, and a tail flag that is
// itself bad is reported against the flag.
func TestExplainListsAnEnvironmentValueThatATailFlagReplaced(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	t.Setenv("DCGM_EXPORTER_DEVICES_STR", "bogus")
	path := explainFile(t, "")

	data := explainJSON(t, nil, path, "--", "--devices", "g:1")
	gpu := settingByPath(t, data, "metrics[0].entities.gpu.gpus")
	assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--devices"}, gpu.Source)
	require.Len(t, gpu.Replaced, 1)
	assert.Equal(t, settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_DEVICES_STR"}, gpu.Replaced[0].settingSource)

	_, stderr, err := inspectionRun(t, "config", "validate", path, "--", "--devices", "nope")
	require.Error(t, err)
	assert.Contains(t, stderr, "--devices: the only valid options")
	assert.NotContains(t, stderr, "bogus")

	_, stderr, err = inspectionRun(t, "config", "validate", path, "--", "--switch-devices", "f")
	require.Error(t, err)
	assert.Contains(t, stderr, "DCGM_EXPORTER_DEVICES_STR: the only valid options", "a bad value that nothing replaces stays a problem")
}

// TestResolveConfigWarnsWhenACountWindowOutlastsTheHistory checks that startup
// resolution warns about a count window set by a flag that DCGM's history
// cannot fill, and only when the count metric is in the metric file.
func TestResolveConfigWarnsWhenACountWindowOutlastsTheHistory(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	dir := t.TempDir()
	withCount := filepath.Join(dir, "with-count.csv")
	withoutCount := filepath.Join(dir, "without-count.csv")
	require.NoError(t, os.WriteFile(withCount, []byte("DCGM_EXP_XID_ERRORS_COUNT, gauge, XID errors.\n"), 0o600))
	require.NoError(t, os.WriteFile(withoutCount, []byte("DCGM_FI_DEV_GPU_TEMP, gauge, Temperature.\n"), 0o600))

	const want = "--xid-count-window-size: the 20m window is longer than --watch-max-keep-age (10m), " +
		"so DCGM_EXP_XID_ERRORS_COUNT can count only the last 10m"
	for name, tc := range map[string]struct {
		file string
		want []string
	}{
		"count metric enabled":  {file: withCount, want: []string{want}},
		"count metric disabled": {file: withoutCount},
	} {
		t.Run(name, func(t *testing.T) {
			var got []string
			app := NewApp()
			app.Action = func(ctx *cli.Context) error {
				resolved, err := resolveConfig(ctx, "", nil, true)
				for _, warning := range resolved.Warnings {
					got = append(got, warning.Message)
				}

				return err
			}

			args := []string{"dcgm-exporter", "--collectors", tc.file, "--xid-count-window-size", "1200000"}
			require.NoError(t, app.Run(args))
			assert.Equal(t, tc.want, got)
		})
	}
}
