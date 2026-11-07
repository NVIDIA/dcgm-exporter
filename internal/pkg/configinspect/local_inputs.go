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
	"encoding/csv"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/prometheus/exporter-toolkit/web"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

// notChecked is a startup check that inspection cannot run offline. The id is
// stable; the label is the text shown to people.
type notChecked struct{ id, label string }

var (
	runtimeNotChecked = []notChecked{
		{"gpus", "GPUs and MIG instances"},
		{"dcgm", "DCGM connection"},
		{"field_support", "field support"},
		{"kubelet_socket", "kubelet and container runtime sockets"},
		{"workload_labels", "Kubernetes, container and HPC label sources"},
		{"listener", "listen address"},
		{"directories", "HPC mapping and debug dump directories"},
	}
	configMapNotChecked = notChecked{"configmap_content", "ConfigMap metric definitions"}
)

// fileReasonFormat joins a file path and the reason it cannot be used.
const fileReasonFormat = "%s: %s"

// notCheckedFor lists what inspection cannot check for this configuration.
func notCheckedFor(config *appconfig.Config) []notChecked {
	list := append([]notChecked{}, runtimeNotChecked...)
	if config.MetricSource.Kind == appconfig.MetricSourceConfigMap {
		list = append(list, configMapNotChecked)
	}

	return list
}

// notCheckedIDs is the list of stable ids, for JSON.
func notCheckedIDs(list []notChecked) []string {
	ids := make([]string, 0, len(list))
	for _, item := range list {
		ids = append(ids, item.id)
	}

	return ids
}

// localInputs is what validate and explain read from local files.
type localInputs struct {
	// counters is nil when the metric definitions were not read.
	counters *counters.CounterSet
	// checked describes each input that was read and found valid.
	checked []string
	// problems are the inputs that were read and found invalid.
	problems []error
}

// readLocalInputs reads the local files the configuration names: the metric
// definitions and the web config file, which must turn on TLS or basic auth
// when profiling is on. It does not touch DCGM, a listener, the
// Kubernetes API or any other service. checkOverrides also checks the
// overrides against the metric definitions; config explain leaves that to
// config validate and startup.
func readLocalInputs(config *appconfig.Config, checkOverrides bool) localInputs {
	var inputs localInputs
	inputs.readMetricSource(config, checkOverrides)
	if config.WebConfigFile != "" {
		if err := validateInspectionWebConfig(config.WebConfigFile); err != nil {
			problem := &appconfig.SettingError{Err: err, Field: "WebConfigFile"}
			var pathError *os.PathError
			if errors.As(err, &pathError) {
				problem.Summary = webConfigReadSummary(config.WebConfigFile, err)
			}

			inputs.problems = append(inputs.problems, problem)
		} else if err := appconfig.ValidateProfilingWebConfig(config); err != nil {
			inputs.problems = append(inputs.problems, err)
		} else {
			inputs.checked = append(inputs.checked, "web config file: "+config.WebConfigFile)
		}
	}

	return inputs
}

// readMetricSource reads and checks the metric definitions and, when
// checkOverrides is set, the overrides that select fields from them.
func (in *localInputs) readMetricSource(config *appconfig.Config, checkOverrides bool) {
	// A ConfigMap needs the Kubernetes API, so its content is not read.
	records, lines, err := counters.ReadLocalRecords(config.MetricSource)
	if errors.Is(err, counters.ErrSourceNotLocal) {
		return
	}

	isFile := config.MetricSource.Kind == appconfig.MetricSourceFile
	if err != nil {
		in.problems = append(in.problems, &appconfig.SettingError{
			Field: "MetricSource", Err: fmt.Errorf("read metric file %q: %w", config.MetricSource.File, err),
			Summary: fmt.Sprintf(fileReasonFormat, EscapeText(config.MetricSource.File), metricReadReason(err)),
		})

		return
	}

	// Check the records, then the overrides that select fields from them. A
	// bad record of a file is named by its line; lines is nil for inline records.
	set, err := counters.ValidateRecords(records)
	if err != nil {
		problem := &appconfig.SettingError{
			Field: "MetricSource", Err: fmt.Errorf("validate metric definitions: %w", counters.AtLines(err, lines)),
		}
		if isFile {
			// A bare line number reads as a line of the config file.
			problem.Summary = fmt.Sprintf(fileReasonFormat, EscapeText(config.MetricSource.File), problem.Err.Error())
		}

		in.problems = append(in.problems, problem)

		return
	}

	in.counters = set
	if isFile {
		in.checked = append(in.checked, "metric file: "+config.MetricSource.File)
	} else {
		in.checked = append(in.checked, "inline metric definitions")
	}

	if checkOverrides {
		in.checkOverrides(config, set)
	}
}

// checkOverrides checks the overrides against the metric definitions with the
// same check startup makes, and words each kind of problem for the report.
func (in *localInputs) checkOverrides(config *appconfig.Config, set *counters.CounterSet) {
	err := devicewatchlistmanager.CheckOverrides(config, set)
	var window *devicewatchlistmanager.WindowOverrideError
	var unmatched *devicewatchlistmanager.UnmatchedWatchGroupError
	switch {
	case err == nil:
	case errors.As(err, &window):
		in.problems = append(in.problems, &appconfig.SettingError{
			Field: "WatchGroups", Err: fmt.Errorf("validate overrides: %w", err),
		})
	case errors.As(err, &unmatched) && unmatched.Group.Path != "":
		// An override is located at its own entry in the config file.
		problem := &appconfig.SettingError{Field: "WatchGroups", Err: err, Path: unmatched.Group.Path}
		in.problems = append(in.problems, problem)
	default:
		in.problems = append(in.problems, &appconfig.SettingError{
			Field: "WatchGroups", Err: err,
		})
	}
}

// metricReadReason says why a metric file could not be read. A malformed row
// gets its line and the parser's reason; a system failure is named only by its
// category, because the system error text carries paths.
func metricReadReason(err error) string {
	var parseErr *csv.ParseError
	switch {
	case errors.As(err, &parseErr):
		return fmt.Sprintf("line %d: %v", parseErr.StartLine, parseErr.Err)
	case errors.Is(err, fs.ErrNotExist):
		return "cannot read metric file: file does not exist"
	case errors.Is(err, fs.ErrPermission):
		return "cannot read metric file: permission denied"
	default:
		return "cannot read metric file"
	}
}

// validateInspectionWebConfig names the web config file and the category of a read
// failure, and hides the parser's details, which may quote authentication or TLS
// credentials.
func validateInspectionWebConfig(path string) error {
	if err := web.Validate(path); err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			return fmt.Errorf("validate web config file %q: %w", path, pathError)
		}

		return fmt.Errorf(
			"invalid web config file %q: check exporter-toolkit TLS, authentication and HTTP settings "+
				"(details suppressed to protect credentials)", path,
		)
	}

	return nil
}

// webConfigReadSummary is the text shown for a web config file that could not be read.
func webConfigReadSummary(path string, err error) string {
	reason := strings.Replace(safeReadReason(err), "configuration file", "web config file", 1)
	return fmt.Sprintf(fileReasonFormat, EscapeText(path), reason)
}
