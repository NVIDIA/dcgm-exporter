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

package static

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHostSuiteDoesNotImportExporterInternals keeps host integration tests black-box.
func TestHostSuiteDoesNotImportExporterInternals(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	for _, tags := range []string{"", "dcgm_uri_integration"} {
		t.Run("tags="+tags, func(t *testing.T) {
			args := []string{"list", "-deps", "-test", "-buildvcs=false"}
			if tags != "" {
				args = append(args, "-tags", tags)
			}
			args = append(args, "./tests/host")

			cmd := exec.Command("go", args...)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, out)
			}

			for _, dep := range strings.Fields(string(out)) {
				switch {
				case dep == "github.com/NVIDIA/dcgm-exporter/pkg/cmd":
					t.Fatalf("host suite must exec the product binary, not import %s", dep)
				case strings.HasPrefix(dep, "github.com/NVIDIA/dcgm-exporter/internal/"):
					t.Fatalf("host suite must not import exporter internal package %s", dep)
				case dep == "github.com/NVIDIA/go-dcgm" || strings.HasPrefix(dep, "github.com/NVIDIA/go-dcgm/"):
					t.Fatalf("host suite must not depend on go-dcgm package %s", dep)
				}
			}
		})
	}
}
