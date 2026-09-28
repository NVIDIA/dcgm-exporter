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

package static_test

import (
	"os"
	"strings"
	"testing"
)

// TestDockerfileGuardsRuntimeDCGMSymbolChecks verifies runtime image stages keep strict DCGM symbol checks.
func TestDockerfileGuardsRuntimeDCGMSymbolChecks(t *testing.T) {
	contents, err := os.ReadFile(repoPath(t, "docker", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(contents)

	runtimeUbuntu := dockerfileStage(t, dockerfile, "runtime-ubuntu")
	if !strings.Contains(runtimeUbuntu, `SHELL ["/bin/bash", "-o", "pipefail", "-c"]`) {
		t.Fatal("runtime-ubuntu stage must enable pipefail before the dcgmConnect_v3 symbol check")
	}

	runtimeDistrolessHelper := dockerfileStage(t, dockerfile, "runtime-distroless-helper")
	if !strings.Contains(runtimeDistrolessHelper, `SHELL ["/bin/bash", "-o", "pipefail", "-c"]`) {
		t.Fatal("runtime-distroless-helper stage must enable pipefail before the dcgmConnect_v3 symbol check")
	}

	// Keep these checks static: they guard against removing or weakening the
	// Dockerfile symbol checks while image builds validate real packages.
	for _, symbol := range []string{"dcgmConnect_v3", "dcgmGetCpuHierarchy_v2"} {
		symbolCheck := `nm -D --defined-only /usr/lib/*-linux-gnu/libdcgm.so.4 | grep -w ` + symbol
		if got := strings.Count(dockerfile, symbolCheck); got != 2 {
			t.Fatalf("expected exactly 2 strict %s symbol checks, got %d", symbol, got)
		}

		looseSymbolCheck := `nm -D /usr/lib/*-linux-gnu/libdcgm.so.4 | grep -w ` + symbol
		if strings.Contains(dockerfile, looseSymbolCheck) {
			t.Fatalf("%s symbol checks must use nm --defined-only", symbol)
		}
	}
}

// TestDockerfilePinsRuntimeDCGMPackages verifies runtime image stages install the repo-pinned DCGM version.
func TestDockerfilePinsRuntimeDCGMPackages(t *testing.T) {
	contents, err := os.ReadFile(repoPath(t, "docker", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(contents)

	for _, stageName := range []string{"runtime-ubuntu", "runtime-distroless-helper"} {
		stage := dockerfileStage(t, dockerfile, stageName)
		if !strings.Contains(stage, "ARG DCGM_VERSION") {
			t.Fatalf("%s stage must accept DCGM_VERSION", stageName)
		}
		if !strings.Contains(stage, `DCGM_PACKAGE_VERSION="1:${DCGM_VERSION}-1"`) {
			t.Fatalf("%s stage must derive a pinned DCGM Debian package version", stageName)
		}
		if !strings.Contains(stage, `"datacenter-gpu-manager-4-core=${DCGM_PACKAGE_VERSION}"`) {
			t.Fatalf("%s stage must pin datacenter-gpu-manager-4-core", stageName)
		}
		if !strings.Contains(stage, `"datacenter-gpu-manager-4-proprietary=${DCGM_PACKAGE_VERSION}"`) {
			t.Fatalf("%s stage must pin datacenter-gpu-manager-4-proprietary", stageName)
		}
	}
}

func TestPackageDockerfileEnforcesOfflineCompileWhenModulesAreCached(t *testing.T) {
	contents, err := os.ReadFile(repoPath(t, "docker", "package.Dockerfile"))
	if err != nil {
		t.Fatalf("read package Dockerfile: %v", err)
	}
	dockerfile := string(contents)

	compileStep := dockerfileRunContaining(t, dockerfile, "make stage-package-payload")
	for _, snippet := range []string{
		`if [ "${GOPROXY_ENABLED:-}" = "true" ] && [ -d "/go/pkg/mod" ] && [ "$(ls -A /go/pkg/mod 2>/dev/null)" ]; then`,
		`export GOPROXY=off GOSUMDB=off GONOSUMDB='*';`,
		`make stage-package-payload PACKAGE_PAYLOAD_ROOT=/package-payload`,
	} {
		if !strings.Contains(compileStep, snippet) {
			t.Fatalf("package compile step must contain %q", snippet)
		}
	}
}

// dockerfileStage extracts one named Dockerfile stage for static assertions.
func dockerfileStage(t *testing.T, dockerfile string, stageName string) string {
	t.Helper()
	startMarker := " AS " + stageName + "\n"
	start := strings.Index(dockerfile, startMarker)
	if start == -1 {
		t.Fatalf("Dockerfile stage %q not found", stageName)
	}
	start = strings.LastIndex(dockerfile[:start], "FROM ")
	if start == -1 {
		t.Fatalf("Dockerfile stage %q has no FROM", stageName)
	}

	next := strings.Index(dockerfile[start+len("FROM "):], "\nFROM ")
	if next == -1 {
		return dockerfile[start:]
	}
	return dockerfile[start : start+len("FROM ")+next]
}

func dockerfileRunContaining(t *testing.T, dockerfile string, marker string) string {
	t.Helper()
	markerIndex := strings.Index(dockerfile, marker)
	if markerIndex == -1 {
		t.Fatalf("Dockerfile RUN marker %q not found", marker)
	}

	start := strings.LastIndex(dockerfile[:markerIndex], "\nRUN ")
	if start == -1 {
		t.Fatalf("Dockerfile marker %q is not inside a RUN step", marker)
	}
	start++

	rest := dockerfile[markerIndex:]
	end := len(rest)
	for _, boundary := range []string{
		"\nFROM ", "\nRUN ", "\nCMD ", "\nENTRYPOINT ", "\nCOPY ", "\nADD ", "\nENV ", "\nARG ",
		"\nLABEL ", "\nWORKDIR ", "\nEXPOSE ", "\nUSER ", "\nVOLUME ", "\nSTOPSIGNAL ", "\nHEALTHCHECK ", "\nSHELL ", "\nONBUILD ",
	} {
		if i := strings.Index(rest, boundary); i >= 0 && i < end {
			end = i
		}
	}
	return dockerfile[start : markerIndex+end]
}
