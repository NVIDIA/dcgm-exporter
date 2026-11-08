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
	"log/slog"
	"os"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/transformation"
)

// resolveConfig is shared by daemon startup and inspection. Local metric file and
// web config file reads belong to admission at the caller, after configuration resolution.
//
// It does not stop at the first problem: every layer is applied and every
// problem found is returned together as one joined error. The resolved value
// is returned even then, so inspection can say where each problem came from.
//
// checkLocalMetrics selects whether the local metric source is read here, once,
// for the override check and the count window warnings. Startup reads it; the
// config commands do not, because config validate and config explain read the
// local files and run those checks themselves.
//
// The notice that Kubernetes wins over container labels is not added here. It
// depends on the caller: the config commands and contextToConfig add it with
// addContainerIgnoredWarning, and startup adds it in resolveEnrichment, which
// knows whether Kubernetes won.
func resolveConfig(c *cli.Context, path string, tail *cli.Context, checkLocalMetrics bool) (*appconfig.Resolved, error) {
	r := &appconfig.Resolved{Config: appconfig.DefaultConfig(), Origins: map[string]appconfig.SettingOrigin{}}
	problems := parsedEnvProblems(c, tail)
	fileFailed := false
	if err := loadSelectedYAML(r, c, path); err != nil {
		problems = append(problems, err)
		fileFailed = true
	}

	problems = append(problems, applyOverrideLayers(r, c, tail)...)

	if path != "" {
		r.Origins["ConfigFile"] = appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "PATH"}
	}

	recordReplacedEnv(r, c)

	// Container labels turned on by a flag or variable still need the default
	// socket when no layer named one.
	r.Config.ApplyDefaultContainerRuntimeSocket()

	// The metric definitions are nil when they are not a local file or inline
	// source, or are not readable yet; the later load reports that.
	var local *counters.CounterSet
	if checkLocalMetrics {
		local = devicewatchlistmanager.ReadLocalCounterSet(r.Config.MetricSource)
	}

	problems = append(problems, validateSettings(r, local)...)

	// A file that did not load leaves out settings that the combination checks
	// need, so they would report problems the file may not have.
	if !fileFailed {
		problems = append(problems, appconfig.ValidateCombinations(r.Config, r.Origin)...)
	}

	r.Warnings = append(r.Warnings, deprecationWarnings(c, tail)...)

	// Count window warnings need the metric definitions, which only startup read here.
	if local != nil && len(problems) == 0 {
		r.Warnings = append(r.Warnings, devicewatchlistmanager.CountWindowWarnings(r, local)...)
	}

	return r, errors.Join(problems...)
}

// applyOverrideLayers applies the root layer and then the tail layer, and
// returns the problems found in either.
func applyOverrideLayers(r *appconfig.Resolved, c, tail *cli.Context) []error {
	var problems []error

	for _, layer := range []*cli.Context{c, tail} {
		if layer == nil {
			continue
		}

		if err := applyExplicitConfigOverrides(layer, r.Config); err != nil {
			if layer == c {
				// A flag after -- replaces the environment value, so that value is not a problem.
				err = withoutReplacedSettings(err, tail)
			}

			if err != nil {
				problems = append(problems, err)
			}
		}

		recordExplicitOrigins(r, layer)
	}

	return problems
}

// withoutReplacedSettings drops the problems with settings that the tail layer
// sets again. The later layer wins, so the value it replaced cannot be an
// error. Problems with other settings stay.
func withoutReplacedSettings(err error, tail *cli.Context) error {
	if tail == nil || err == nil {
		return err
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var kept []error

		for _, inner := range joined.Unwrap() {
			if inner = withoutReplacedSettings(inner, tail); inner != nil {
				kept = append(kept, inner)
			}
		}

		return errors.Join(kept...)
	}

	var problem *appconfig.SettingError
	if errors.As(err, &problem) && tailReplacesSetting(tail, problem.Field) {
		return nil
	}

	return err
}

// tailReplacesSetting says whether the tail layer gives the setting a value.
// The metric source has two flags: an empty or unset ConfigMap selector does
// not replace anything.
func tailReplacesSetting(tail *cli.Context, field string) bool {
	if field == "MetricSource" {
		configMap := tail.String(CLIConfigMapData)

		return tail.IsSet(CLIFieldsFile) || (tail.IsSet(CLIConfigMapData) && configMap != "" && configMap != undefinedConfigMapData)
	}

	for _, setting := range appconfig.SettingKeys(appconfig.DefaultConfig(), appconfig.ConfigNames{}) {
		if setting.Field == field {
			return setting.Flag != "" && tail.IsSet(setting.Flag)
		}
	}

	return false
}

// recordReplacedEnv records, for each setting that a flag supplied, the
// environment variables of that flag that were set to a value.
func recordReplacedEnv(r *appconfig.Resolved, c *cli.Context) {
	envNames := map[string][]string{}
	for _, f := range c.App.Flags {
		if documented, ok := f.(cli.DocGenerationFlag); ok {
			envNames[f.Names()[0]] = documented.GetEnvVars()
		}
	}

	r.ReplacedEnv = map[string][]appconfig.EnvValue{}
	for _, setting := range appconfig.SettingKeys(r.Config, appconfig.ConfigNames{}) {
		if setting.Flag == "" || r.Origins[setting.Field].Kind != appconfig.OriginFlag {
			continue
		}

		for _, name := range envNames[setting.Flag] {
			if value := os.Getenv(name); value != "" {
				replaced := appconfig.EnvValue{Name: name, Value: value}
				r.ReplacedEnv[setting.Field] = append(r.ReplacedEnv[setting.Field], replaced)
			}
		}
	}
}

// parsedEnvProblems lists the environment values the argument parser could not read.
// A flag after -- replaces the environment value, so that value is not a problem.
func parsedEnvProblems(c, tail *cli.Context) []error {
	var problems []error
	if input, _ := c.App.Metadata[parserInputMetadataKey].(*parserInput); input != nil {
		for _, problem := range input.envProblems {
			if tail != nil && tail.IsSet(problem.Flag) {
				continue
			}

			problems = append(problems, problem)
		}
	}

	return problems
}

// loadSelectedYAML preserves explicit empty selectors as loader errors and
// records YAML presence before environment and argv layers override settings.
func loadSelectedYAML(r *appconfig.Resolved, c *cli.Context, path string) error {
	selected := c.String(CLIConfigFile)
	if path != "" {
		selected = path
	}

	if path != "" || c.IsSet(CLIConfigFile) {
		r.Config.ConfigFile = selected

		y, err := appconfig.LoadYAMLConfigFile(selected)
		if err != nil {
			if path == "" {
				// Say which input chose the file, so a missing default file in an
				// image names DCGM_EXPORTER_CONFIG_FILE.
				return fmt.Errorf("config file selected by %s: %w", flagOrigin(c, CLIConfigFile), err)
			}

			return err
		}

		return r.ApplyConfigFile(y, flagOrigin(c, CLIConfigFile))
	}

	return nil
}

// recordExplicitOrigins records each explicit launch input in the current layer.
func recordExplicitOrigins(r *appconfig.Resolved, c *cli.Context) {
	for _, setting := range appconfig.SettingKeys(r.Config, appconfig.ConfigNames{}) {
		// The metric source has its own rule below.
		if setting.Flag == "" || setting.Field == "MetricSource" || !c.IsSet(setting.Flag) {
			continue
		}

		r.Origins[setting.Field] = flagOrigin(c, setting.Flag)
	}

	if c.IsSet(CLIRemoteHEInfo) {
		from := r.Origins["RemoteHEInfo"].String()
		r.Origins["UseRemoteHE"] = appconfig.SettingOrigin{Kind: appconfig.OriginDerived, From: from}
	}

	// Source mode has a fixed precedence within each layer: a nonempty
	// ConfigMap selector wins the file selector; an empty selector does nothing.
	if c.IsSet(CLIFieldsFile) {
		r.Origins["MetricSource"] = flagOrigin(c, CLIFieldsFile)
	}

	configMap := c.String(CLIConfigMapData)
	if c.IsSet(CLIConfigMapData) && configMap != "" && configMap != undefinedConfigMapData {
		r.Origins["MetricSource"] = flagOrigin(c, CLIConfigMapData)
	}
}

// flagOrigin looks up the registered flag’s presence-based input source.
func flagOrigin(c *cli.Context, name string) appconfig.SettingOrigin {
	input, _ := c.App.Metadata[parserInputMetadataKey].(*parserInput)
	for _, f := range c.App.Flags {
		if f.Names()[0] == name {
			return input.origin(f.(cli.DocGenerationFlag))
		}
	}

	return appconfig.SettingOrigin{Kind: appconfig.OriginDefault}
}

// validateSettings checks each setting on its own. The watch group patterns
// come first, then the overrides against the local metric definitions when
// there are any, then the rest, so the problems always come out in that order.
func validateSettings(r *appconfig.Resolved, local *counters.CounterSet) []error {
	var problems []error
	add := func(err error) {
		if err != nil {
			problems = append(problems, &appconfig.SettingError{Err: err, Field: "WatchGroups"})
		}
	}

	add(appconfig.ValidateWatchGroupPatterns(r.Config.WatchGroups))
	add(devicewatchlistmanager.CheckOverridesInLocalSource(r.Config, local))

	return append(problems, appconfig.ValidateValues(r.Config, r.Origin)...)
}

// resolveRuntimeConfig resolves startup config and rejects all OTLP selections
// because this build has no OTLP exporter. It also reads the web config file
// and rejects profiling unless that file turns on TLS or basic auth. It does
// not check enrichment sources: that can wait, so it runs later in
// resolveEnrichment, after the caller installs the configured logger and logs
// the warnings.
func resolveRuntimeConfig(c *cli.Context) (*appconfig.Resolved, error) {
	resolved, err := resolveConfig(c, "", nil, true)
	if err != nil {
		return nil, err
	}

	if err := validateBuildOutputs(resolved.Config); err != nil {
		return nil, err
	}

	if err := appconfig.ValidateProfilingWebConfig(resolved.Config); err != nil {
		return nil, err
	}

	return resolved, nil
}

// validateBuildOutputs admits only output modes supplied by this executable.
func validateBuildOutputs(config *appconfig.Config) error {
	if config.EnableOTLP {
		return errors.New("OTLP export is not available in this build")
	}

	return nil
}

// enrichmentWait is how startup waits for a required enrichment source. It is
// not a setting; tests shorten it.
var enrichmentWait = transformation.DefaultEnrichmentWait()

// resolveEnrichment checks the workload-label sources once at startup, before
// the HTTP server starts. An auto source that is not usable adds a warning and
// stays on; a required one is waited for and fails startup if it never becomes
// usable. Reloads clone the resolved config and do not check again. It appends
// the warnings to resolved.Warnings for the caller to log.
func resolveEnrichment(ctx context.Context, r *appconfig.Resolved) error {
	resolution, err := transformation.ResolveEnrichment(ctx, r.Config, enrichmentWait)
	if err != nil {
		return describeRequiredEnrichment(r, err)
	}

	// Use the settings the checks arrived at.
	r.Config.Kubernetes = resolution.Kubernetes
	r.Config.ContainerLabels = resolution.ContainerLabels
	r.Config.KubernetesEnableDRA = resolution.KubernetesEnableDRA
	r.Config.HPCJobMappingDir = resolution.HPCJobMappingDir

	// Collect the warnings for the caller to log.
	addContainerIgnoredWarning(r)

	for _, warning := range resolution.Warnings {
		r.Warnings = append(r.Warnings, describeEnrichmentWarning(r, warning))
	}

	return nil
}

// enrichmentFieldOrigins lists, for each enrichment switch, the setting that
// turns the source on and the settings that explain why its source was
// required or unavailable.
var enrichmentFieldOrigins = map[appconfig.EnrichmentSource]enrichmentSettings{
	appconfig.EnrichmentKubernetes: {"Kubernetes", []relatedSetting{{"kubelet socket", "PodResourcesKubeletSocket"}}},
	appconfig.EnrichmentContainer:  {"ContainerLabels", []relatedSetting{{"runtime socket", "ContainerRuntimeSocket"}}},
	appconfig.EnrichmentDRA:        {"KubernetesEnableDRA", []relatedSetting{{"Kubernetes", "Kubernetes"}}},
	appconfig.EnrichmentHPCJobs:    {appconfig.HPCJobsSwitchField, []relatedSetting{{"mapping directory", "HPCJobMappingDir"}}},
}

// enrichmentSettings is the setting that turns a source on and the settings
// that explain why it was required or unavailable.
type enrichmentSettings struct {
	switchField string
	related     []relatedSetting
}

// relatedSetting is a setting that a source needs, with the label an error uses for it.
type relatedSetting struct{ label, field string }

// switchMode is the mode the config file gave a source's switch. A source that
// a flag or an environment variable turned on has none.
func switchMode(r *appconfig.Resolved, source appconfig.EnrichmentSource) appconfig.EnrichmentMode {
	modes := r.Config.EnrichmentModes
	switch source {
	case appconfig.EnrichmentKubernetes:
		return modes.Kubernetes
	case appconfig.EnrichmentContainer:
		return modes.Container
	case appconfig.EnrichmentDRA:
		return modes.DRA
	default:
		return modes.HPCJobs
	}
}

// enrichmentDetails says where a source's settings came from. The config file
// names the YAML key it wrote, or says the key is not set in the file when it
// left the switch out; a source turned on any other way names the flag, environment
// variable or file setting that did it. The related settings may come from
// anywhere.
func enrichmentDetails(r *appconfig.Resolved, source appconfig.EnrichmentSource) []string {
	origins := enrichmentFieldOrigins[source]

	var details []string

	switch {
	case switchMode(r, source) == appconfig.EnrichmentUnset:
		details = append(details, "turned on by "+r.Origin(origins.switchField))
	case omittedInFile(r, origins.switchField):
		details = append(details, source.Key()+" is not set in the file")
	default:
		details = append(details, "set by "+r.Origin(origins.switchField))
	}

	for _, related := range origins.related {
		origin := r.Origin(related.field)
		if omittedInFile(r, related.field) {
			origin = "default"
		}

		details = append(details, fmt.Sprintf("%s set by %s", related.label, origin))
	}

	return details
}

// omittedInFile says the setting came from the enrichment block as a whole,
// which is how the file records a switch it leaves out.
func omittedInFile(r *appconfig.Resolved, field string) bool {
	origin := r.OriginOf(field)

	return origin.Kind == appconfig.OriginFile && origin.Key == appconfig.EnrichmentKeyBase
}

// describeRequiredEnrichment adds where a required source's settings came from.
func describeRequiredEnrichment(r *appconfig.Resolved, err error) error {
	var required *transformation.RequiredEnrichmentError
	if !errors.As(err, &required) {
		return err
	}

	return fmt.Errorf("%w; %s", err, strings.Join(enrichmentDetails(r, required.Source), "; "))
}

// describeEnrichmentWarning adds the same detail to a skipped-source warning.
func describeEnrichmentWarning(r *appconfig.Resolved, w transformation.EnrichmentWarning) appconfig.ResolutionWarning {
	return appconfig.ResolutionWarning{Message: w.Message + "; " + strings.Join(enrichmentDetails(r, w.Source), "; ")}
}

// containerIgnoredMessage is the notice that Kubernetes wins over container labels.
const containerIgnoredMessage = "container runtime labels are ignored when kubernetes mode is enabled"

// addContainerIgnoredWarning adds the notice that Kubernetes wins over
// container labels, when it applies.
func addContainerIgnoredWarning(r *appconfig.Resolved) {
	if w, ok := containerIgnoredWarning(r); ok {
		w.Origin = r.Origins["ContainerLabels"]
		r.Warnings = append(r.Warnings, w)
	}
}

// containerIgnoredWarning reports the notice that Kubernetes wins over
// container labels. The config file's container switch gets no notice: there
// Kubernetes winning is the documented rule, not a surprise.
func containerIgnoredWarning(r *appconfig.Resolved) (appconfig.ResolutionWarning, bool) {
	config := r.Config
	if !config.ContainerLabels || !config.Kubernetes || config.EnrichmentModes.Container != appconfig.EnrichmentUnset {
		return appconfig.ResolutionWarning{}, false
	}

	return appconfig.ResolutionWarning{
		Message: containerIgnoredMessage,
		Attrs: []any{
			slog.String("flag", CLIContainerLabels), slog.String("mode", CLIKubernetes),
			slog.String("container_labels_set_by", r.Origin("ContainerLabels")),
			slog.String("kubernetes_set_by", r.Origin("Kubernetes")),
		},
	}, true
}
