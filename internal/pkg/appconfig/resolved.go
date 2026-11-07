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

import "maps"

// Resolved is the effective configuration and where each setting came from.
// Startup and the config commands build it the same way, so they agree on every
// value. ApplyConfigFile builds the file layer; the environment and flag layers
// are built in pkg/cmd, next to the flag definitions. The config commands report
// the result without reading flags again.
type Resolved struct {
	Config *Config
	YAML   *YAMLConfig
	// Origins names the input that supplied each setting, by setting field name.
	Origins map[string]SettingOrigin
	// FileConfig is the config as the file left it, before environment
	// variables and flags. It is nil when no file was selected.
	FileConfig *Config
	// FileOrigins is the part of Origins that the file supplied.
	FileOrigins map[string]SettingOrigin
	Warnings    []ResolutionWarning
	// ReplacedEnv lists, by setting field name, the environment variables that
	// were set to a value for a setting that a flag then replaced.
	ReplacedEnv map[string][]EnvValue
}

// EnvValue is an environment variable and the raw text it was set to.
type EnvValue struct {
	Name, Value string
}

// ApplyConfigFile applies a loaded config file to the config and records what
// the file supplied: the file, its warnings, where each setting it wrote came
// from, and the config and origins as the file left them, before environment
// variables and flags. selectedBy says which input chose the file. Startup and
// the config commands both build the file layer with it. Nothing is recorded
// when the file does not apply.
func (r *Resolved) ApplyConfigFile(y *YAMLConfig, selectedBy SettingOrigin) error {
	if err := y.ApplyTo(r.Config); err != nil {
		return err
	}

	r.YAML = y
	r.Warnings = append(r.Warnings, y.Warnings()...)

	r.Origins["ConfigFile"] = selectedBy
	for field, key := range y.SettingOrigins() {
		r.Origins[field] = SettingOrigin{Kind: OriginFile, Key: key}
	}

	// The HPC job labels switch has no field in the config. A file with a
	// metrics entry records its origin; a file without one has only the mapping
	// directory, which turned the source on, so the switch has the same origin.
	if _, found := r.Origins[HPCJobsSwitchField]; !found {
		if origin, found := r.Origins["HPCJobMappingDir"]; found {
			r.Origins[HPCJobsSwitchField] = origin
		}
	}

	// Keep the config and origins as the file left them, so a later layer that
	// replaces a value can be shown next to the value it replaced.
	r.FileConfig, r.FileOrigins = r.Config.Clone(), maps.Clone(r.Origins)

	return nil
}

// Origin returns the text that names the winning source of a setting, as used
// in startup error messages and logs.
func (r *Resolved) Origin(field string) string {
	return r.OriginOf(field).String()
}

// OriginOf returns the winning source of a setting; a setting no input
// supplied has the default origin.
func (r *Resolved) OriginOf(field string) SettingOrigin {
	if origin, found := r.Origins[field]; found {
		return origin
	}

	return SettingOrigin{Kind: OriginDefault}
}

// HPCJobsSwitchField names the HPC job labels switch among the setting rows.
// The runtime has no field for it: the switch decides HPCJobMappingDir.
const HPCJobsSwitchField = "HPCJobs"

// OriginKind names the layer that supplied a setting.
type OriginKind string

// The layers that can supply a setting.
const (
	OriginDefault OriginKind = "default"
	OriginFile    OriginKind = "file"
	OriginEnv     OriginKind = "env"
	OriginFlag    OriginKind = "flag"
	OriginDerived OriginKind = "derived"
)

// SettingOrigin says which input supplied a setting's value.
type SettingOrigin struct {
	Kind OriginKind
	// Name is the flag (with its dashes), the environment variable, or PATH.
	Name string
	// Key is the YAML path in the config file, for a file origin.
	Key string
	// From describes what a derived value was worked out from.
	From string
}

// String gives the short text used in startup error messages and logs.
func (o SettingOrigin) String() string {
	switch o.Kind {
	case OriginFile:
		return "YAML: " + o.Key
	case OriginEnv:
		return "environment: " + o.Name
	case OriginFlag:
		return "argv: " + o.Name
	case OriginDerived:
		return "derived: " + o.From
	default:
		return "default"
	}
}

// Input names the input that supplied the setting the way a user writes it: the
// YAML key, the flag with its dashes, or the environment variable. It is empty
// for a default or derived value.
func (o SettingOrigin) Input() string {
	switch o.Kind {
	case OriginFile:
		return o.Key
	case OriginEnv, OriginFlag:
		return o.Name
	default:
		return ""
	}
}

// ResolutionWarning is one resolution warning. Startup logs Message with
// Attrs; inspection reports Message and, when known, where the warning comes from.
type ResolutionWarning struct {
	Message string
	Attrs   []any
	Origin  SettingOrigin
}

// SettingError ties an error to the setting it is about, so inspection can
// say where the bad value came from. Its text is the wrapped error's text.
type SettingError struct {
	Err error
	// Field is the name origin tracking uses for the setting.
	Field string
	// Path is the key path in the config file that the problem is located at;
	// empty means the place the setting was read from.
	Path string
	// Summary is a shorter text for inspection; empty means use the error text.
	Summary string
	// Fix says how to correct the problem, when that is known.
	Fix string
}

// Error returns the wrapped error's text.
func (e *SettingError) Error() string { return e.Err.Error() }

// Unwrap returns the wrapped error.
func (e *SettingError) Unwrap() error { return e.Err }

// EnvValueError is an environment variable value that its flag cannot parse.
// The flag is applied without its environment variable, so parsing goes on and
// the caller decides how to report the problem.
type EnvValueError struct {
	Env, Flag, Summary string
	Err                error
}

// Error is the parser's own message.
func (e *EnvValueError) Error() string { return e.Err.Error() }

// Unwrap returns the parser's error.
func (e *EnvValueError) Unwrap() error { return e.Err }
