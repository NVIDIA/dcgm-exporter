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
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settingErrorOf finds the problem about one setting among the errors.
func settingErrorOf(t *testing.T, errs []error, field string) *SettingError {
	t.Helper()
	for _, err := range errs {
		var setting *SettingError
		require.ErrorAs(t, err, &setting)
		if setting.Field == field {
			return setting
		}
	}

	return nil
}

// originName is an origin lookup that names its field, so a test can tell which
// setting an error text came from.
func originName(field string) string { return "origin of " + field }

func TestValidateValuesAcceptsTheDefaults(t *testing.T) {
	assert.Empty(t, ValidateValues(DefaultConfig(), originName))
}

func TestValidateValuesNamesTheSettingAtFault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*Config)
		field   string
		message string
	}{
		{"empty metric file", func(c *Config) { c.MetricSource.File = "  " }, "MetricSource", "collectors must not be empty"},
		{"collect interval", func(c *Config) { c.CollectInterval = 0 }, "CollectInterval", "invalid collect-interval parameter value: 0, must be greater than 0; set by origin of CollectInterval"},
		{"scrapes", func(c *Config) { c.MaxConcurrentScrapes = -1 }, "MaxConcurrentScrapes", "invalid max-concurrent-scrapes parameter value: -1"},
		{"retention age", func(c *Config) { c.WatchRetention.MaxAge = -time.Second }, "WatchRetention.MaxAge", "invalid field watch retention"},
		{"retention samples", func(c *Config) { c.WatchRetention.MaxSamples = -1 }, "WatchRetention.MaxSamples", "invalid field watch retention"},
		{"group retention", func(c *Config) {
			age := -time.Second
			c.WatchGroups = []WatchGroup{{Name: "g", Fields: []string{"A"}, Retention: WatchRetentionOverride{MaxAge: &age}}}
		}, "WatchGroups", "invalid watch group \"g\" retention"},
		{"log level", func(c *Config) { c.DCGMLogLevel = "LOUD" }, "DCGMLogLevel", "invalid dcgm-log-level parameter value: LOUD"},
		{"gpu id type", func(c *Config) { c.KubernetesGPUIdType = "mac" }, "KubernetesGPUIdType", "use uid or device-name"},
		{"allowlist regex", func(c *Config) { c.KubernetesPodLabelAllowlistRegex = []string{"("} }, "KubernetesPodLabelAllowlistRegex", "invalid kubernetes-pod-label-allowlist-regex parameter value \"(\""},
		{"pod labels without allowlist", func(c *Config) { c.KubernetesEnablePodLabels = true }, "KubernetesPodLabelAllowlistRegex", "kubernetes-enable-pod-labels requires kubernetes-pod-label-allowlist-regex"},
		{"empty allowlist pattern", func(c *Config) {
			c.KubernetesEnablePodLabels, c.KubernetesPodLabelAllowlistRegex = true, []string{"^app$", ""}
		}, "KubernetesPodLabelAllowlistRegex", "kubernetes-pod-label-allowlist-regex patterns must not be empty"},
		{"read timeout", func(c *Config) { c.WebReadTimeout = 0 }, "WebReadTimeout", "invalid web-read-timeout parameter value: \"0s\", must be greater than 0; set by origin of WebReadTimeout"},
		{"write timeout", func(c *Config) { c.WebWriteTimeout = -time.Second }, "WebWriteTimeout", "invalid web-write-timeout parameter value: \"-1s\", must be greater than 0; set by origin of WebWriteTimeout"},
		{"poll interval", func(c *Config) {
			c.EnableGPUBindUnbindWatch, c.GPUBindUnbindPollInterval = true, 0
		}, "GPUBindUnbindPollInterval", "invalid gpu-bind-unbind-poll-interval parameter value: \"0s\", must be greater than 0; set by origin of GPUBindUnbindPollInterval"},
		{"xid window", func(c *Config) { c.XIDCountWindowSize = 0 }, "XIDCountWindowSize", "set by origin of XIDCountWindowSize"},
		{"clock window", func(c *Config) { c.ClockEventsCountWindowSize = -5 }, "ClockEventsCountWindowSize", "invalid clock-events-count-window-size parameter value: -5"},
		{"HPC file bytes", func(c *Config) { c.HPCJobMappingMaxFileBytes = 0 }, "HPCJobMappingMaxFileBytes", "invalid hpc-job-mapping-max-file-bytes parameter value: 0, must be greater than 0"},
		{"HPC job ID bytes", func(c *Config) { c.HPCJobMappingMaxJobIDBytes = -1 }, "HPCJobMappingMaxJobIDBytes", "invalid hpc-job-mapping-max-job-id-bytes parameter value: -1"},
		{"HPC jobs per file", func(c *Config) { c.HPCJobMappingMaxJobsPerFile = math.MaxInt64 }, "HPCJobMappingMaxJobsPerFile", "less than 9223372036854775807"},
		{"log format", func(c *Config) { c.LogFormat = "xml" }, "LogFormat", "invalid log-format parameter value: xml"},
		{"address", func(c *Config) { c.Address = "nowhere" }, "Address", "expected host:port or :port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			tc.change(config)
			problem := settingErrorOf(t, ValidateValues(config, originName), tc.field)
			require.NotNil(t, problem, "no problem reported for %s", tc.field)
			assert.Contains(t, problem.Error(), tc.message)
		})
	}
}

func TestValidateValuesSkipsChecksThatDoNotApply(t *testing.T) {
	config := DefaultConfig()
	// The poll interval only counts while the watch is on, and the address
	// only while the exporter listens itself.
	config.GPUBindUnbindPollInterval = 0
	config.Address = "nowhere"
	config.WebSystemdSocket = true
	assert.Empty(t, ValidateValues(config, originName))
}

func TestValidateValuesAcceptsExplicitPodLabelSelection(t *testing.T) {
	for _, patterns := range [][]string{{".*"}, {"^app$", "^tier$"}} {
		config := DefaultConfig()
		config.KubernetesEnablePodLabels, config.KubernetesPodLabelAllowlistRegex = true, patterns
		assert.Empty(t, ValidateValues(config, originName), "%v", patterns)
	}

	// An empty pattern does not matter while pod labels are off.
	config := DefaultConfig()
	config.KubernetesPodLabelAllowlistRegex = []string{""}
	assert.Empty(t, ValidateValues(config, originName))
}

func TestValidateValuesReportsProblemsInOrder(t *testing.T) {
	config := DefaultConfig()
	config.MetricSource.File = ""
	config.CollectInterval = 0
	config.LogFormat = "xml"
	var fields []string
	for _, err := range ValidateValues(config, originName) {
		var setting *SettingError
		require.ErrorAs(t, err, &setting)
		fields = append(fields, setting.Field)
	}

	assert.Equal(t, []string{"MetricSource", "CollectInterval", "LogFormat"}, fields)
}

func TestValidateValuesReportsEveryInvalidWatchGroupRetention(t *testing.T) {
	age := -time.Second
	config := DefaultConfig()
	config.WatchGroups = []WatchGroup{
		{Name: "first", Fields: []string{"A"}, Retention: WatchRetentionOverride{MaxAge: &age}},
		{Name: "ok", Fields: []string{"B"}},
		{Name: "third", Fields: []string{"C"}, Retention: WatchRetentionOverride{MaxAge: &age}},
	}

	errs := ValidateValues(config, originName)
	require.Len(t, errs, 2)
	assert.ErrorContains(t, errs[0], `invalid watch group "first" retention`)
	assert.ErrorContains(t, errs[1], `invalid watch group "third" retention`)
}

func TestValidateValuesWatchRetentionBlamesTheAgeFirst(t *testing.T) {
	config := DefaultConfig()
	config.WatchRetention = WatchRetention{MaxAge: -time.Second, MaxSamples: -1}
	assert.NotNil(t, settingErrorOf(t, ValidateValues(config, originName), "WatchRetention.MaxAge"))
	assert.Nil(t, settingErrorOf(t, ValidateValues(config, originName), "WatchRetention.MaxSamples"))
}

func TestValidateCombinationsNamesWhereEachSettingCameFrom(t *testing.T) {
	config := DefaultConfig()
	assert.Empty(t, ValidateCombinations(config, originName))

	config.EnableDCGMLog, config.UseRemoteHE = true, true
	config.EnablePprof = true
	errs := ValidateCombinations(config, originName)
	require.Len(t, errs, 2)

	embedded := settingErrorOf(t, errs, "EnableDCGMLog")
	require.NotNil(t, embedded)
	assert.Contains(t, embedded.Error(), "set by origin of EnableDCGMLog and origin of RemoteHEInfo")
	assert.NotEmpty(t, embedded.Summary)
	assert.NotEmpty(t, embedded.Fix)

	pprof := settingErrorOf(t, errs, "EnablePprof")
	require.NotNil(t, pprof)
	assert.Contains(t, pprof.Error(), "enable-pprof requires web-config-file")
	assert.Contains(t, pprof.Error(), "enable-pprof is set by origin of EnablePprof")
	assert.Contains(t, pprof.Fix, "--web-config-file")

	config.WebConfigFile = "/etc/web.yml"
	assert.Len(t, ValidateCombinations(config, originName), 1)
}

func TestValidateLogFormat(t *testing.T) {
	assert.NoError(t, ValidateLogFormat("text"))
	assert.NoError(t, ValidateLogFormat("json"))
	assert.EqualError(t, ValidateLogFormat("yaml"), "invalid log-format parameter value: yaml")
}

func TestValidateListenAddress(t *testing.T) {
	for address, valid := range map[string]bool{
		":9400": true, "localhost:9400": true, "[::1]:9400": true, ":0": true, ":http": true,
		"9400": false, "host": false, ":70000": false, ":-1": false, ":no-such-service-name": false, "": false,
	} {
		err := validateListenAddress(address)
		if valid {
			assert.NoError(t, err, address)
		} else {
			assert.Error(t, err, address)
		}
	}
}

func TestSettingErrorAndEnvValueErrorKeepTheirCause(t *testing.T) {
	cause := errors.New("cause")
	setting := &SettingError{Err: cause, Field: "Address"}
	assert.EqualError(t, setting, "cause")
	assert.ErrorIs(t, setting, cause)

	env := &EnvValueError{Env: "X", Flag: "y", Summary: "bad", Err: cause}
	assert.EqualError(t, env, "cause")
	assert.ErrorIs(t, env, cause)
}
