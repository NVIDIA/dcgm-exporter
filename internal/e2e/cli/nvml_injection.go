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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

func prepareNVMLInjection(opts *config.Tests, requireFiles bool) error {
	if opts.DCGMNVMLInjectionYAML == "" {
		return nil
	}
	absolute, err := filepath.Abs(opts.DCGMNVMLInjectionYAML)
	if err != nil {
		return fmt.Errorf("resolve --dcgm-nvml-injection-yaml: %w", err)
	}
	opts.DCGMNVMLInjectionYAML = absolute
	if !requireFiles {
		return nil
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return fmt.Errorf("read --dcgm-nvml-injection-yaml: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("--dcgm-nvml-injection-yaml must name a regular file")
	}
	return nil
}

func dcgmFieldsSource(ctx context.Context, root string, runner e2eexec.Runner) (string, error) {
	packaged := filepath.Join(root, "etc", "go-dcgm-const-fields.go")
	if info, err := os.Stat(packaged); err == nil && info.Mode().IsRegular() {
		return packaged, nil
	}
	result := runner.Run(ctx, e2eexec.Command{
		Name: "go",
		Args: []string{"list", "-m", "-f", "{{.Dir}}", "github.com/NVIDIA/go-dcgm"},
		Dir:  root,
	})
	if result.ExitCode != 0 {
		return "", fmt.Errorf("locate go-dcgm field definitions: %s%s", result.Stdout, result.Stderr)
	}
	path := filepath.Join(strings.TrimSpace(string(result.Stdout)), "pkg", "dcgm", "const_fields.go")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read go-dcgm field definitions: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("go-dcgm field definitions must be a regular file: %s", path)
	}
	return path, nil
}
