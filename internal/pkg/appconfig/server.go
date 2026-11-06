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
	"strings"
)

// YAMLServer configures the exporter HTTP server.
type YAMLServer struct {
	ReadTimeout          string `yaml:"read_timeout,omitempty" json:"read_timeout,omitempty"`
	WriteTimeout         string `yaml:"write_timeout,omitempty" json:"write_timeout,omitempty"`
	MaxConcurrentScrapes *int   `yaml:"max_concurrent_scrapes,omitempty" json:"max_concurrent_scrapes,omitempty"`
	SystemdSocket        *bool  `yaml:"systemd_socket,omitempty" json:"systemd_socket,omitempty"`
	WebConfigFile        string `yaml:"web_config_file" json:"web_config_file"`
	Profiling            *bool  `yaml:"profiling,omitempty" json:"profiling,omitempty"`
}

// applyServerSettings applies the server block.
func (y *YAMLConfig) applyServerSettings(config *Config, rec originRecorder) error {
	server := y.Server
	if server == nil {
		return nil
	}

	if server.MaxConcurrentScrapes != nil {
		if *server.MaxConcurrentScrapes <= 0 {
			return keyErrorf("server.max_concurrent_scrapes", "must be greater than 0")
		}

		config.MaxConcurrentScrapes = *server.MaxConcurrentScrapes
		rec.record("MaxConcurrentScrapes", "server.max_concurrent_scrapes")
	}

	if readTimeout := strings.TrimSpace(server.ReadTimeout); readTimeout != "" {
		timeout, err := parsePositiveDuration(readTimeout, "server.read_timeout")
		if err != nil {
			return err
		}

		config.WebReadTimeout = timeout
		rec.record("WebReadTimeout", "server.read_timeout")
	}

	if writeTimeout := strings.TrimSpace(server.WriteTimeout); writeTimeout != "" {
		timeout, err := parsePositiveDuration(writeTimeout, "server.write_timeout")
		if err != nil {
			return err
		}

		config.WebWriteTimeout = timeout
		rec.record("WebWriteTimeout", "server.write_timeout")
	}

	if server.SystemdSocket != nil {
		config.WebSystemdSocket = *server.SystemdSocket
		rec.record("WebSystemdSocket", "server.systemd_socket")
	}

	if file := strings.TrimSpace(server.WebConfigFile); file != "" {
		config.WebConfigFile = file
		rec.record("WebConfigFile", "server.web_config_file")
	}

	if server.Profiling != nil {
		config.EnablePprof = *server.Profiling
		rec.record("EnablePprof", "server.profiling")
	}

	return nil
}
