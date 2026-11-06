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
	"fmt"
	"math"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ValidateLogFormat rejects a log format other than text or json. Startup and
// the logger setup share it, so there is one rule and one message.
func ValidateLogFormat(format string) error {
	if format != "text" && format != "json" {
		return fmt.Errorf("invalid %s parameter value: %s", FlagLogFormat, format)
	}

	return nil
}

// ValidateValues checks each setting on its own. Each problem is a
// *SettingError naming the setting at fault, and origin says where a value came
// from for the messages that name it.
//
// It does not check the watch group patterns or the overrides against the
// metric definitions. The caller runs ValidateWatchGroupPatterns, and then the
// override check if it reads the metric source, before these checks, so that
// problems come out in that order.
func ValidateValues(config *Config, origin func(field string) string) []error {
	var errs []error
	add := func(err error, field string) {
		if err != nil {
			errs = append(errs, &SettingError{Err: err, Field: field})
		}
	}

	if config.MetricSource.Kind == MetricSourceFile && strings.TrimSpace(config.MetricSource.File) == "" {
		add(fmt.Errorf("%s must not be empty", FlagFieldsFile), "MetricSource")
	}

	switch {
	case config.CollectInterval <= 0:
		add(fmt.Errorf(
			"invalid %s parameter value: %d, must be greater than 0; set by %s",
			FlagCollectInterval,
			config.CollectInterval,
			origin("CollectInterval"),
		), "CollectInterval")
	case config.CollectInterval > MaxMilliseconds:
		add(overLimitError(FlagCollectInterval, config.CollectInterval, origin("CollectInterval")), "CollectInterval")
	}

	if config.MaxConcurrentScrapes <= 0 {
		add(fmt.Errorf(
			"invalid %s parameter value: %d, must be greater than 0",
			FlagMaxConcurrentScrapes,
			config.MaxConcurrentScrapes,
		), "MaxConcurrentScrapes")
	}

	if err := config.WatchRetention.Validate(); err != nil {
		field := FieldWatchMaxAge
		// Validate checks the age first, so the age is blamed whenever it is invalid.
		if samples := config.WatchRetention.MaxSamples; config.WatchRetention.MaxAge >= 0 &&
			(samples < 0 || samples > math.MaxInt32) {
			field = FieldWatchMaxSamples
		}

		add(fmt.Errorf("invalid field watch retention: %w", err), field)
	}

	errs = append(errs, validateWatchGroupRetentions(config)...)

	if !slices.Contains(DCGMDbgLvlValues, config.DCGMLogLevel) {
		add(fmt.Errorf("invalid %s parameter value: %s", FlagDCGMLogLevel, config.DCGMLogLevel), "DCGMLogLevel")
	}

	errs = append(errs, validateKubernetesValues(config)...)
	errs = append(errs, validateHPCJobMappingLimits(config)...)
	errs = append(errs, validateDurationSettings(config, origin)...)
	for _, err := range config.DumpConfig.settingErrors() {
		errs = append(errs, err)
	}
	errs = append(errs, validateCountWindows(config, origin)...)

	add(ValidateLogFormat(config.LogFormat), "LogFormat")

	if !config.WebSystemdSocket {
		// Systemd supplies the listeners then, and the address is not used.
		add(validateListenAddress(config.Address), "Address")
	}

	return errs
}

// validateWatchGroupRetentions checks per-group overrides after they inherit
// the resolved global policy. It reports every invalid group, not only the first.
func validateWatchGroupRetentions(config *Config) []error {
	var errs []error
	for _, watchGroup := range config.WatchGroups {
		if err := watchGroup.Retention.Resolve(config.WatchRetention).Validate(); err != nil {
			errs = append(errs, &SettingError{
				Field: "WatchGroups",
				Err:   fmt.Errorf("invalid %s retention: %w", watchGroup.Describe(), err),
			})
		}
	}

	return errs
}

// validateKubernetesValues checks the Kubernetes settings that a flag or the
// file can set to a value the exporter cannot use, whether or not pod labels
// are on, so that a saved config always loads back.
func validateKubernetesValues(config *Config) []error {
	var errs []error
	switch config.KubernetesGPUIdType {
	case GPUUID, DeviceName:
	default:
		errs = append(errs, &SettingError{
			Field: "KubernetesGPUIdType",
			Err: fmt.Errorf("invalid %s parameter value: %q; use %s or %s",
				FlagKubernetesGPUIDType, config.KubernetesGPUIdType, GPUUID, DeviceName),
		})
	}

	for _, pattern := range config.KubernetesPodLabelAllowlistRegex {
		if _, err := regexp.Compile(pattern); err != nil {
			flag := FlagKubernetesPodLabelAllowlistRegex
			problem := fmt.Errorf("invalid %s parameter value %q: %w", flag, pattern, err)
			errs = append(errs, &SettingError{Field: "KubernetesPodLabelAllowlistRegex", Err: problem})
		}
	}

	if err := validatePodLabelSelection(config); err != nil {
		errs = append(errs, &SettingError{Field: "KubernetesPodLabelAllowlistRegex", Err: err})
	}

	return errs
}

// validatePodLabelSelection requires an explicit, non-empty pattern list while
// pod labels are on, so the exporter never copies every pod label by default.
// A pattern of .* still selects all labels on purpose.
func validatePodLabelSelection(config *Config) error {
	if !config.KubernetesEnablePodLabels {
		return nil
	}

	flag := FlagKubernetesPodLabelAllowlistRegex
	if len(config.KubernetesPodLabelAllowlistRegex) == 0 {
		return fmt.Errorf("%s requires %s; use .* to include all labels", FlagKubernetesEnablePodLabels, flag)
	}

	if slices.Contains(config.KubernetesPodLabelAllowlistRegex, "") {
		return fmt.Errorf("%s patterns must not be empty; use .* to include all labels", flag)
	}

	return nil
}

// validateDurationSettings checks active polling, web timeouts and the DCGM
// cleanup timeout before startup. Each error names the input that set the
// value.
func validateDurationSettings(config *Config, origin func(field string) string) []error {
	var errs []error
	check := func(flag, field string, duration time.Duration) {
		if err := validatePositiveDuration(flag, duration); err != nil {
			wrapped := fmt.Errorf("%w; set by %s", err, origin(field))
			errs = append(errs, &SettingError{Err: wrapped, Field: field})
		}
	}

	if config.EnableGPUBindUnbindWatch {
		check(FlagGPUBindUnbindPollInterval, "GPUBindUnbindPollInterval", config.GPUBindUnbindPollInterval)
	}

	check(FlagWebReadTimeout, "WebReadTimeout", config.WebReadTimeout)
	check(FlagWebWriteTimeout, "WebWriteTimeout", config.WebWriteTimeout)
	check(FlagDCGMCleanupTimeout, FieldDCGMCleanupTimeout, config.DCGMCleanupTimeout)

	return errs
}

// validatePositiveDuration rejects a nonpositive duration for a named CLI setting.
func validatePositiveDuration(flag string, duration time.Duration) error {
	if duration <= 0 {
		return fmt.Errorf("invalid %s parameter value: %q, must be greater than 0", flag, duration.String())
	}

	return nil
}

// MaxMilliseconds is the largest number of milliseconds that fits a
// time.Duration. A larger setting would wrap to a negative duration, which the
// config document cannot carry and the exporter cannot use.
const MaxMilliseconds = int(math.MaxInt64 / int64(time.Millisecond))

// overLimitError is the error for a setting in milliseconds that is too large
// for a duration, naming where the value came from.
func overLimitError(flag string, value int, source string) error {
	return fmt.Errorf(
		"invalid %s parameter value: %d, must be at most %d milliseconds; set by %s",
		flag, value, MaxMilliseconds, source)
}

// validateCountWindows rejects a counter window that is not positive or too
// large for a duration. It checks the resolved config, so it covers a value
// from a file, an environment variable or a flag, and names where the value
// came from.
func validateCountWindows(config *Config, origin func(field string) string) []error {
	var errs []error
	for _, window := range CountWindows {
		value := *window.Setting(config)
		if value > MaxMilliseconds {
			errs = append(errs, &SettingError{
				Field:   window.Field,
				Err:     overLimitError(window.Flag, value, origin(window.Field)),
				Summary: fmt.Sprintf("%d is more than %d milliseconds", value, MaxMilliseconds),
			})

			continue
		}

		if value > 0 {
			continue
		}

		errs = append(errs, &SettingError{
			Field: window.Field,
			Err: fmt.Errorf(
				"invalid %s parameter value: %d, must be greater than 0; set by %s",
				window.Flag, value, origin(window.Field)),
			Summary: fmt.Sprintf("must be greater than 0, got %d", value),
		})
	}

	return errs
}

// validateListenAddress checks the form the listener needs: host:port or :port,
// where port is a number or a service name the resolver knows, such as http.
// Whether the address can be bound is only known at startup.
func validateListenAddress(address string) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid %s parameter value: %q, expected host:port or :port", FlagAddress, address)
	}

	if number, err := strconv.Atoi(port); err == nil && number >= 0 && number <= 65535 {
		return nil
	}

	// The listener also accepts a service name such as http.
	if _, err := net.LookupPort("tcp", port); err != nil {
		const reason = "the port must be a number from 0 to 65535 or a service name"
		return fmt.Errorf("invalid %s parameter value: %q, %s", FlagAddress, address, reason)
	}

	return nil
}

// ValidateCombinations checks settings that depend on one another. They
// need settings from every source, so a caller skips them when part of the
// input could not be read.
func ValidateCombinations(config *Config, origin func(field string) string) []error {
	var errs []error
	if err := validateEmbeddedLog(config, origin); err != nil {
		errs = append(errs, err)
	}

	if config.EnablePprof && strings.TrimSpace(config.WebConfigFile) == "" {
		errs = append(errs, &SettingError{
			Field: "EnablePprof",
			Err: fmt.Errorf(
				"%s requires %s so profiling endpoints are protected by exporter-toolkit auth or TLS; %s is set by %s",
				FlagEnablePprof,
				FlagWebConfigFile,
				FlagEnablePprof,
				origin("EnablePprof"),
			),
			Summary: "profiling requires a web config file",
			Fix:     "set --" + FlagWebConfigFile + " (server.web_config_file) or turn profiling off",
		})
	}

	return errs
}

// validateEmbeddedLog rejects the embedded DCGM log together with a remote
// hostengine, whichever source set each one.
func validateEmbeddedLog(config *Config, origin func(field string) string) error {
	if config.EnableDCGMLog && config.UseRemoteHE {
		return &SettingError{
			Field: "EnableDCGMLog",
			Err: fmt.Errorf(
				"%s applies only to the embedded hostengine and cannot be used with %s; set by %s and %s",
				FlagEnableDCGMLog, FlagRemoteHEInfo, origin("EnableDCGMLog"), origin("RemoteHEInfo")),
			Summary: "the embedded DCGM log cannot be used with a remote hostengine",
			Fix:     "remove the hostengine address or turn the embedded log off",
		}
	}

	return nil
}
