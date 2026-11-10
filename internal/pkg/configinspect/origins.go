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
	"fmt"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// settingSource is where a setting's value, or a problem with it, came from, as
// inspection reports it. It is the reported form of an appconfig.SettingOrigin.
type settingSource struct {
	Kind   appconfig.OriginKind `json:"kind,omitempty"`
	Name   string               `json:"name,omitempty"`
	File   string               `json:"file,omitempty"`
	Line   int                  `json:"line,omitempty"`
	Column int                  `json:"column,omitempty"`
	Path   string               `json:"path,omitempty"`
}

// sourceOf reports origin, with the position of its key when it is in the config
// file. Selecting the file by PATH reports the file itself.
func sourceOf(r *appconfig.Resolved, origin appconfig.SettingOrigin) settingSource {
	switch {
	case origin.Kind == appconfig.OriginFile:
		source := settingSource{Kind: appconfig.OriginFile, File: r.Config.ConfigFile, Path: origin.Key}
		source.Line, source.Column, _ = r.YAML.Locate(origin.Key)
		return source
	case origin.Kind == appconfig.OriginFlag && origin.Name == "PATH":
		return settingSource{Kind: appconfig.OriginFile, File: r.Config.ConfigFile}
	case origin.Kind == appconfig.OriginDefault || origin.Kind == appconfig.OriginDerived:
		return settingSource{Kind: origin.Kind}
	default:
		return settingSource{Kind: origin.Kind, Name: origin.Name}
	}
}

// forProblem drops the default and derived kinds, because a problem with them
// has no input to point at.
func (s settingSource) forProblem() settingSource {
	if s.Kind == appconfig.OriginDefault || s.Kind == appconfig.OriginDerived {
		return settingSource{}
	}

	return s
}

// describe is the one-line form of a source: "flag --x", "env X" or "file f:3".
func (s settingSource) describe() string {
	switch s.Kind {
	case appconfig.OriginFlag:
		return "flag " + s.Name
	case appconfig.OriginEnv:
		return "env " + s.Name
	case appconfig.OriginFile:
		if s.Line > 0 {
			return fmt.Sprintf("file %s:%d", s.File, s.Line)
		}

		return "file " + s.File
	default:
		return string(s.Kind)
	}
}
