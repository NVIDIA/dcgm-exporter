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

package host

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runDCGMLibraryPath(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DCGM library path integration test in short mode")
	}
	source := hostDCGMLibrary(t)
	customDir := t.TempDir()
	customLibrary := filepath.Join(customDir, "libdcgm.so.4")
	copyTestFile(t, source, customLibrary)

	port := getRandomAvailablePort(t)
	process, _ := startExporterAndWaitWithEnv(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		[]string{"LD_LIBRARY_PATH=" + customDir, "LD_DEBUG=libs"},
		"--collectors", "./testdata/default-counters.csv",
		"--address", fmt.Sprintf(":%d", port),
	)

	output := process.output.String()
	if !strings.Contains(output, customLibrary) {
		t.Fatalf("runtime loader did not report the custom DCGM library %s:\n%s", customLibrary, output)
	}
	if strings.Contains(output, "panic:") {
		t.Fatalf("dcgm-exporter panicked while loading custom DCGM library:\n%s", output)
	}
	process.terminate(t)

	emptyDir := t.TempDir()
	fallbackPort := getRandomAvailablePort(t)
	fallbackProcess, _ := startExporterAndWaitWithEnv(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", fallbackPort),
		[]string{"LD_LIBRARY_PATH=" + emptyDir},
		"--collectors", "./testdata/default-counters.csv",
		"--address", fmt.Sprintf(":%d", fallbackPort),
	)
	if strings.Contains(fallbackProcess.output.String(), "panic:") {
		t.Fatalf("dcgm-exporter panicked while falling back to the system DCGM library:\n%s", fallbackProcess.output.String())
	}
}

func hostDCGMLibrary(t testing.TB) string {
	t.Helper()
	multiarch := map[string]string{"amd64": "x86_64-linux-gnu", "arm64": "aarch64-linux-gnu"}[runtime.GOARCH]
	for _, candidate := range []string{
		filepath.Join("/usr/lib", multiarch, "libdcgm.so.4"),
		filepath.Join("/lib", multiarch, "libdcgm.so.4"),
		"/usr/lib64/libdcgm.so.4",
	} {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			return resolved
		}
	}
	t.Fatalf("compatible host libdcgm.so.4 was not found")
	return ""
}

func copyTestFile(t testing.TB, source, destination string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatalf("open %s: %v", source, err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatalf("create %s: %v", destination, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copy %s: %v", destination, err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close %s: %v", destination, err)
	}
}
