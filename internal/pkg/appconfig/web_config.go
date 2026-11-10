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
	"fmt"
	"os"
	"strings"

	"github.com/prometheus/exporter-toolkit/web"
	"gopkg.in/yaml.v3"
)

// ValidateProfilingWebConfig reads the web config file and rejects profiling
// when the file turns on neither TLS nor basic auth, because the profiling
// endpoints share the metrics listener and its protection. It does nothing
// when profiling is off or no file is set; ValidateCombinations reports a
// missing file. Startup and config validate call it once the file is readable.
func ValidateProfilingWebConfig(config *Config) error {
	path := strings.TrimSpace(config.WebConfigFile)
	if !config.EnablePprof || path == "" {
		return nil
	}

	protected, err := webConfigProtected(path)
	if err != nil {
		return &SettingError{Field: "WebConfigFile", Err: err}
	}

	if protected {
		return nil
	}

	return &SettingError{
		Field: "EnablePprof",
		Err: fmt.Errorf(
			"%s requires %s %q to turn on TLS (tls_server_config) or basic auth (basic_auth_users); it turns on neither",
			FlagEnablePprof, FlagWebConfigFile, path,
		),
		Summary: "profiling requires a web config file with TLS or basic auth",
		Fix:     "add tls_server_config or basic_auth_users to the web config file, or turn profiling off",
	}
}

// webConfigProtected reports whether the exporter-toolkit web config file at
// path turns on TLS or basic auth. exporter-toolkit validates the file first;
// its parser errors are not returned because they may quote credentials.
func webConfigProtected(path string) (bool, error) {
	if err := web.Validate(path); err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			return false, fmt.Errorf("validate web config file %q: %w", path, pathError)
		}

		return false, fmt.Errorf("invalid web config file %q: check exporter-toolkit TLS, authentication and HTTP settings "+
			"(details suppressed to protect credentials)", path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read web config file %q: %w", path, err)
	}

	var parsed web.Config
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		return false, fmt.Errorf("parse web config file %q (details suppressed to protect credentials)", path)
	}

	return parsed.TLSConfig.IsEnabled() || len(parsed.Users) > 0, nil
}
