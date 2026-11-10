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

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

func prepareNVMLInjection(opts *config.TestOptions, requireFiles bool) error {
	return prepareYAMLPath("--dcgm-nvml-injection-yaml", &opts.DCGMNVMLInjectionYAML, requireFiles)
}

func prepareNVSDMMock(opts *config.TestOptions, requireFiles bool) error {
	return prepareYAMLPath("--dcgm-nvsdm-mock-yaml", &opts.DCGMNVSDMMockYAML, requireFiles)
}

func prepareYAMLPath(flagName string, path *string, requireFile bool) error {
	if *path == "" {
		return nil
	}
	absolute, err := filepath.Abs(*path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", flagName, err)
	}
	*path = absolute
	if !requireFile {
		return nil
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return fmt.Errorf("read %s: %w", flagName, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must name a regular file", flagName)
	}
	return nil
}
