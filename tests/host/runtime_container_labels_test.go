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
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const runtimeLabelContainerName = "dcgm-e2e-runtime-label"

// runStartWithRuntimeContainerLabels verifies container-label CLI wiring against a Docker-compatible socket.
func runStartWithRuntimeContainerLabels(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	socketPath := startFakeDockerRuntime(t)
	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
	process, _ := startExporterAndWait(
		t,
		metricsURL,
		"--collectors", "./testdata/default-counters.csv",
		"--address", fmt.Sprintf(":%d", port),
		"--container-labels",
		"--container-runtime-socket", socketPath,
	)

	require.Eventually(t, func() bool {
		if err := process.running(); err != nil {
			t.Log(err)
			return false
		}
		metricsResp, statusCode, err := fetchRaw(metricsURL)
		return err == nil && statusCode == http.StatusOK && gpuMetricContainsLabelValue(t, metricsResp, "container", runtimeLabelContainerName)
	}, 60*time.Second, 500*time.Millisecond, "expected a GPU metric with container=%q", runtimeLabelContainerName)
}

// startFakeDockerRuntime serves the runtime API endpoints used by container label discovery.
func startFakeDockerRuntime(t testing.TB) string {
	t.Helper()

	const containerID = "0123456789abcdef"
	socketPath := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"Id":%q,"Names":["/%s"]}]`, containerID, runtimeLabelContainerName)
	})
	mux.HandleFunc("/containers/"+containerID+"/json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"Id":%q,"Name":"/%s","HostConfig":{"DeviceRequests":[{"Driver":"nvidia","Count":-1,"Capabilities":[["gpu"]]}]}}`, containerID, runtimeLabelContainerName)
	})

	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			done <- err
		}
		close(done)
	}()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = listener.Close()
		require.NoError(t, <-done)
	})
	return socketPath
}

// gpuMetricContainsLabelValue reports whether a GPU metric family has one label value.
func gpuMetricContainsLabelValue(t testing.TB, metricsResp string, labelName string, labelValue string) bool {
	t.Helper()

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	for name, family := range families {
		if !strings.HasPrefix(name, "DCGM_FI_DEV_") {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == labelName && label.GetValue() == labelValue {
					return true
				}
			}
		}
	}
	return false
}
