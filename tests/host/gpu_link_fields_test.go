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
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

const gpuLinkHostengineAddressEnv = "E2E_DCGM_HOSTENGINE_ADDRESS"

var gpuLinkFieldsLabel = regexp.MustCompile(`\bgpuLinkFields\b`)

// runGPULinkFields checks fields that direct DCGM could read from one active
// GPU link. Live values change, so the contract checks field presence, labels,
// type, and finite values rather than comparing readings taken at different times.
func runGPULinkFields(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping GPU-backed link test in short mode")
	}
	suiteConfig, _ := GinkgoConfiguration()
	if !gpuLinkFieldsLabel.MatchString(suiteConfig.LabelFilter) {
		t.Skip("GPU-link field contract requires explicit gpuLinkFields label selection")
	}
	address := strings.TrimSpace(os.Getenv(gpuLinkHostengineAddressEnv))
	require.NotEmpty(t, address, "%s must name the private DCGM hostengine shared by exporter and probe", gpuLinkHostengineAddressEnv)
	require.NotEmpty(t, strings.TrimSpace(*dcgmProbeBinaryFlag), "-dcgm-probe-binary is required")
	require.NotEmpty(t, strings.TrimSpace(*dcgmFieldsFileFlag), "-dcgm-fields-file is required")
	contract := probeGPULinkFields(t, address)

	var collectors bytes.Buffer
	writer := csv.NewWriter(&collectors)
	for _, field := range contract.Fields {
		require.NoError(t, writer.Write([]string{field.Name, "gauge", "Direct DCGM GPU-link field."}))
	}
	writer.Flush()
	require.NoError(t, writer.Error())
	collectorsPath := filepath.Join(t.TempDir(), "gpu-link-fields.csv")
	require.NoError(t, os.WriteFile(collectorsPath, collectors.Bytes(), 0o600))

	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
	process, _ := startExporterAndWaitForFamilies(t, metricsURL, nil, nil,
		"--collectors", collectorsPath,
		"--address", fmt.Sprintf(":%d", port),
		"--remote-hostengine-info", address,
		"--devices", fmt.Sprintf("g:%d", contract.GPU),
		"--switch-devices", "g",
		"--collect-interval", "1000",
	)
	defer process.terminate(t)
	require.NoError(t, waitForGPULinkFields(metricsURL, process, contract))
	t.Logf("GPU link contract: GPU=%d UUID=%s physical link=%d available fields=%d",
		contract.GPU, contract.GPUUUID, contract.Link, len(contract.Fields))
}

// probeGPULinkFields asks direct DCGM which fields have a sample on one link.
func probeGPULinkFields(t testing.TB, address string) metriccontract.GPULinkContract {
	t.Helper()
	output := filepath.Join(t.TempDir(), "gpu-link-fields.json")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// #nosec G204,G702 -- E2E inputs are passed directly to the selected probe binary, not to a shell.
	cmd := exec.CommandContext(ctx, *dcgmProbeBinaryFlag, "gpu-link-fields", "--address", address,
		"--field-names", *dcgmFieldsFileFlag, "--output", output)
	cmd.Env = hostExporterEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && probeMessage(stderr.String()) == noActiveGPULinkMessage {
		t.Skip(noActiveGPULinkMessage + ": no NVLink is up on this host")
	}
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		require.FailNow(t, gpuLinkProbeTimeoutMessage(stderr.String()))
	}
	require.NoErrorf(t, err, "direct GPU-link probe failed (%s)", safeProbeFailureSummary(stderr.String()))
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	var contract metriccontract.GPULinkContract
	require.NoError(t, json.Unmarshal(data, &contract))
	require.NoError(t, contract.Validate())
	return contract
}

// gpuLinkProbeTimeoutMessage names the last probe step the probe started, taken
// from the final "stage: <name>" line on its stderr.
func gpuLinkProbeTimeoutMessage(stderr string) string {
	stage := ""
	for _, line := range strings.Split(stderr, "\n") {
		if name, found := strings.CutPrefix(strings.TrimSpace(line), "stage: "); found && name != "" {
			stage = name
		}
	}
	if stage == "" {
		return "GPU-link probe did not finish within 60s before its first DCGM call"
	}
	return "DCGM did not respond within 60s during " + stage + "; restart the DCGM hostengine before running again"
}

// waitForGPULinkFields waits for the exporter to publish every available
// direct-DCGM field with the original GPU UUID and physical link index.
func waitForGPULinkFields(metricsURL string, process *hostExporterProcess, contract metriccontract.GPULinkContract) error {
	labels := map[string]string{
		"gpu":      strconv.FormatUint(uint64(contract.GPU), 10),
		"gpu_uuid": contract.GPUUUID,
		"nvlink":   strconv.FormatUint(uint64(contract.Link), 10),
	}
	checks := make([]metriccontract.FamilyContract, 0, len(contract.Fields))
	names := make([]string, 0, len(contract.Fields))
	for _, field := range contract.Fields {
		checks = append(checks, metriccontract.FamilyContract{
			Name: field.Name, Type: dto.MetricType_GAUGE, CheckType: true,
			Labels: []string{"gpu", "gpu_uuid", "nvlink"}, Finite: true,
		})
		names = append(names, field.Name)
	}
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := process.running(); err != nil {
			return err
		}
		lastErr = checkGPULinkFields(metricsURL, checks, names, labels)
		if lastErr == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for GPU-link field contract: %w", lastErr)
}

// checkGPULinkFields validates one exporter scrape against the direct-DCGM
// field and physical-link identity contract.
func checkGPULinkFields(metricsURL string, checks []metriccontract.FamilyContract, names []string, labels map[string]string) error {
	body, status, err := fetchRaw(metricsURL)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("metrics endpoint returned HTTP %d", status)
	}
	families, err := metriccontract.ParseText([]byte(body))
	if err != nil {
		return err
	}
	if err := metriccontract.ValidateContracts(families, checks); err != nil {
		return err
	}
	return metriccontract.ValidateExactlyOneLabelMatch(families, names, labels)
}

// TestGPULinkProbeTimeoutMessageNamesLastStage checks the timeout text for stage lines and for none.
func TestGPULinkProbeTimeoutMessageNamesLastStage(t *testing.T) {
	const noStage = "GPU-link probe did not finish within 60s before its first DCGM call"
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{name: "last stage wins", stderr: "stage: DCGM initialization\nstage: entity discovery\nstage: field availability discovery\n", want: "DCGM did not respond within 60s during field availability discovery; restart the DCGM hostengine before running again"},
		{name: "hang at first step", stderr: "stage: DCGM initialization\n", want: "DCGM did not respond within 60s during DCGM initialization; restart the DCGM hostengine before running again"},
		{name: "other output ignored", stderr: "stage: entity discovery\nCacheManager noise\n", want: "DCGM did not respond within 60s during entity discovery; restart the DCGM hostengine before running again"},
		{name: "no stage", stderr: "noise\n", want: noStage},
		{name: "empty", want: noStage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, gpuLinkProbeTimeoutMessage(test.stderr))
		})
	}
}
