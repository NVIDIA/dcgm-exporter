/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

func runMIGXidCounters(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping native DCGM test in short mode")
	}
	require.Equal(t, "True", os.Getenv("NVML_INJECTION_MODE"), "NVML injection is required")
	fixture := strings.TrimSpace(os.Getenv("NVML_YAML_FILE"))
	require.NotEmpty(t, fixture, "NVML_YAML_FILE is required")
	// #nosec G703 -- the fixture is an explicit harness input; only its file type is inspected.
	info, err := os.Stat(fixture)
	require.NoError(t, err, "NVML injection fixture")
	require.True(t, info.Mode().IsRegular(), "NVML injection fixture must be a regular file")
	require.NotEmpty(t, strings.TrimSpace(*dcgmProbeBinaryFlag), "-dcgm-probe-binary is required")
	output := filepath.Join(t.TempDir(), "mig-xid-counters.json")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// #nosec G204 -- the native helper is an explicit harness input.
	cmd := exec.CommandContext(ctx, *dcgmProbeBinaryFlag, "field-entity-exporter", "--mode", "embedded", "--scenario", "ci-only", "--output", output)
	cmd.Env = hostExporterEnv()
	cmd.Stdout = io.Discard
	var stderr limitedMIGProbeLog
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		diagnostic, readErr := readMIGXidEvidence(output + ".failed.json")
		t.Logf("MIG XID failed-attempt evidence: %s", diagnostic)
		t.Fatalf("MIG XID native probe failed (%v, deadline: %v); evidence error: %v; stderr: %s", err, ctx.Err(), readErr, stderr.String())
	}
	evidence, err := readMIGXidEvidence(output)
	require.NoError(t, err, "read successful MIG XID report")
	var report dcgmprobe.ExporterAcceptance
	require.NoError(t, json.Unmarshal(evidence, &report))
	require.Equal(t, 2, report.Version)
	require.Equal(t, "ci-only", report.Scenario)
	require.Equal(t, "embedded", report.Mode)
	require.Equal(t, "fake", report.Topology)
	t.Logf("MIG XID successful native/HTTP evidence: %s", evidence)
}

// Reports contain a bounded HTTP response plus small native histories.
func readMIGXidEvidence(path string) ([]byte, error) {
	const limit = 8 << 20
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open MIG XID evidence: %w", err)
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(body) > limit {
		return nil, fmt.Errorf("MIG XID report exceeds %d bytes", limit)
	}
	if err != nil {
		return nil, fmt.Errorf("read MIG XID evidence: %w", err)
	}
	return body, nil
}

type limitedMIGProbeLog struct{ bytes.Buffer }

func (b *limitedMIGProbeLog) Write(p []byte) (int, error) {
	const limit = 64 << 10
	n := len(p)
	if remaining := limit - b.Len(); remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(n, remaining)])
	}
	return n, nil
}

func TestReadMIGXidEvidence(t *testing.T) {
	for _, size := range []int{128, (8 << 20) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.json")
			require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o600))
			body, err := readMIGXidEvidence(path)
			if size > 8<<20 {
				require.ErrorContains(t, err, "exceeds")
				require.Nil(t, body)
				return
			}
			require.NoError(t, err)
			require.Len(t, body, size)
		})
	}
}

func TestMIGProbeLogIsBounded(t *testing.T) {
	var log limitedMIGProbeLog
	input := bytes.Repeat([]byte("x"), 65<<10)
	for range 2 {
		n, err := log.Write(input)
		require.NoError(t, err)
		require.Equal(t, len(input), n)
		require.Equal(t, 64<<10, log.Len())
	}
}
