//go:build e2e

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

package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

const dcgmInjectionLocalPort = "15555"

const (
	dcgmFieldXIDErrors               = "230"
	dcgmFieldNVLinkCRCFlitErrorTotal = "409"
)

var _ = Describe("DCGM failure injection", Ordered, func() {
	Context("DCGM exporter emits injected XID values", Ordered, Label("failureXid"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should report an injected XID metric", func(ctx context.Context) {
			shouldInstallRemoteDCGMExporterAndInject(ctx, "xid", func(metrics []byte) error {
				return metricFamilyHasPositiveSample(metrics, "DCGM_FI_DEV_XID_ERRORS")
			})
		})
	})

	Context("DCGM exporter emits injected XID counters", Ordered, Label("failureXidCounters"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should report XID count and total metrics after injection", func(ctx context.Context) {
			shouldInstallRemoteDCGMExporterAndInject(ctx, "xid", func(metrics []byte) error {
				return errors.Join(
					metricFamilyHasPositiveSample(metrics, "DCGM_EXP_XID_ERRORS_COUNT"),
					metricFamilyHasPositiveSample(metrics, "DCGM_EXP_XID_ERRORS_TOTAL"),
				)
			})
		})
	})

	Context("DCGM exporter emits GPU health status for injected failures", Ordered, Label("failureGpuHealth"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should report GPU health samples while failure counters are enabled", func(ctx context.Context) {
			shouldInstallRemoteDCGMExporterAndInject(ctx, "xid", func(metrics []byte) error {
				return metricsHaveFamilyName(metrics, "DCGM_EXP_GPU_HEALTH_STATUS")
			})
		})
	})

	Context("DCGM exporter emits injected NVLink failures", Ordered, Label("failureNvlinkHealth"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should report injected NVLink error counters", func(ctx context.Context) {
			var dumpOnce sync.Once
			shouldInstallRemoteDCGMExporterAndInject(ctx, "nvlink", func(metrics []byte) error {
				err := metricFamilyHasPositiveSample(metrics, "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL")
				if err != nil {
					dumpOnce.Do(func() {
						dumpFailureNvlinkDiagnostics(ctx)
					})
				}
				return err
			})
		})
	})
})

func TestParseDCGMGPUIDs(t *testing.T) {
	output := `#Entity         NFELT
ID
GPU 0           1
GPU 7           N/A
GPU 7           N/A
`
	got := parseDCGMGPUIDs(output)
	want := []string{"0", "7"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("parseDCGMGPUIDs() = %v, want %v", got, want)
	}
}

// shouldInstallRemoteDCGMExporterAndInject installs exporter against standalone DCGM and waits for injected metrics.
func shouldInstallRemoteDCGMExporterAndInject(ctx context.Context, injectionScenario string, validate func([]byte) error) {
	Expect(testContext.remoteDcgm).NotTo(BeEmpty(), "failure injection requires -remote-dcgm")
	injected := false
	_, _ = shouldInstallExporterAndReadMetrics(ctx,
		[]string{exporterArguments(fmt.Sprintf("-r=%s", testContext.remoteDcgm))},
		failureInjectionMetricJSONValues(),
		nil,
		func(metrics []byte) error {
			if !injected {
				if err := runFieldInjection(ctx, injectionScenario); err != nil {
					return err
				}
				injected = true
				return fmt.Errorf("waiting for exporter to scrape injected %s field values", injectionScenario)
			}
			return validate(metrics)
		})
}

// failureInjectionMetricJSONValues returns the custom metric set needed by injection scenarios.
func failureInjectionMetricJSONValues() []string {
	return []string{customMetricsJSONValue(
		"DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).",
		"DCGM_FI_DEV_XID_ERRORS, gauge, XID errors.",
		"DCGM_EXP_XID_ERRORS_COUNT, counter, reported XIDs during last window.",
		"DCGM_EXP_XID_ERRORS_TOTAL, counter, cumulative XID errors observed since exporter start.",
		"DCGM_EXP_GPU_HEALTH_STATUS, gauge, DCGM reported health status.",
		"DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL, counter, Total NVLink flow-control CRC errors.",
	)}
}

// runFieldInjection uses dcgmi from the configured DCGM image to inject one field value.
func runFieldInjection(ctx context.Context, scenario string) error {
	if testContext.dcgmImage == "" {
		return fmt.Errorf("failure injection requires -dcgm-image")
	}
	ns := testContext.dcgmNS
	if ns == "" {
		ns = testContext.namespace + "-dcgm"
	}
	name := testContext.dcgmName
	if name == "" {
		name = "dcgm"
	}
	port := testContext.dcgmPort
	if port == "" {
		port = "5555"
	}

	pfCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var pfOutput bytes.Buffer
	portForward := exec.CommandContext(
		pfCtx,
		"kubectl",
		"--kubeconfig", testContext.kubeconfig,
		"-n", ns,
		"port-forward",
		"svc/"+name,
		dcgmInjectionLocalPort+":"+port,
	)
	portForward.Stdout = &pfOutput
	portForward.Stderr = &pfOutput
	if err := portForward.Start(); err != nil {
		return fmt.Errorf("start kubectl port-forward: %w", err)
	}
	defer func() {
		cancel()
		_ = portForward.Wait()
	}()

	if err := waitForTCP(ctx, net.JoinHostPort("127.0.0.1", dcgmInjectionLocalPort)); err != nil {
		return fmt.Errorf("wait for standalone DCGM port-forward: %w; output: %s", err, pfOutput.String())
	}

	fieldID, value, err := injectionField(scenario)
	if err != nil {
		return err
	}
	gpuIDs, err := discoverDCGMGPUIDs(ctx, fieldID)
	if err != nil {
		return err
	}
	fmt.Fprintf(GinkgoWriter, "DCGM field injection target GPU IDs: %v\n", gpuIDs)
	for _, gpuID := range gpuIDs {
		if err := injectDCGMField(ctx, fieldID, value, gpuID); err != nil {
			return err
		}
	}

	var readbackOutput bytes.Buffer
	readback := dcgmiCommand(
		ctx,
		"dmon",
		"-e", fieldID,
		"-c", "1",
		"--host", net.JoinHostPort("127.0.0.1", dcgmInjectionLocalPort),
	)
	readback.Stdout = &readbackOutput
	readback.Stderr = &readbackOutput
	if err := readback.Run(); err != nil {
		fmt.Fprintf(GinkgoWriter, "WARN: DCGM field %s readback failed after successful injection: %v; output:\n%s\n", fieldID, err, readbackOutput.String())
		return nil
	}
	fmt.Fprintf(GinkgoWriter, "DCGM field %s readback output:\n%s\n", fieldID, readbackOutput.String())
	return nil
}

func discoverDCGMGPUIDs(ctx context.Context, fieldID string) ([]string, error) {
	var output bytes.Buffer
	readback := dcgmiCommand(
		ctx,
		"dmon",
		"-e", fieldID,
		"-c", "1",
		"--host", net.JoinHostPort("127.0.0.1", dcgmInjectionLocalPort),
	)
	readback.Stdout = &output
	readback.Stderr = &output
	if err := readback.Run(); err != nil {
		return nil, fmt.Errorf("discover DCGM GPUs for field %s: %w; output: %s", fieldID, err, output.String())
	}
	gpuIDs := parseDCGMGPUIDs(output.String())
	if len(gpuIDs) == 0 {
		return nil, fmt.Errorf("discover DCGM GPUs for field %s: no GPU rows in output: %s", fieldID, output.String())
	}
	return gpuIDs, nil
}

func parseDCGMGPUIDs(output string) []string {
	re := regexp.MustCompile(`(?m)^\s*GPU\s+([0-9]+)\s+`)
	matches := re.FindAllStringSubmatch(output, -1)
	gpuIDs := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) < 2 || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		gpuIDs = append(gpuIDs, match[1])
	}
	return gpuIDs
}

func injectDCGMField(ctx context.Context, fieldID, value, gpuID string) error {
	var injectionOutput bytes.Buffer
	inject := dcgmiCommand(
		ctx,
		"test",
		"--inject",
		"--host", net.JoinHostPort("127.0.0.1", dcgmInjectionLocalPort),
		"--gpuid", gpuID,
		"--field", fieldID,
		"--value", value,
	)
	inject.Stdout = &injectionOutput
	inject.Stderr = &injectionOutput
	if err := inject.Run(); err != nil {
		return fmt.Errorf("run DCGM field injector for GPU %s: %w; output: %s", gpuID, err, injectionOutput.String())
	}
	fmt.Fprintf(GinkgoWriter, "DCGM field injection output for GPU %s:\n%s\n", gpuID, injectionOutput.String())
	return nil
}

func dumpFailureNvlinkDiagnostics(ctx context.Context) {
	for _, command := range [][]string{
		{"-n", testContext.namespace, "get", "pod", "-l", "app.kubernetes.io/name=dcgm-exporter", "-o", "wide"},
		{"-n", testContext.namespace, "get", "pod", "-l", "app.kubernetes.io/name=dcgm-exporter", "-o", "yaml"},
		{"-n", testContext.namespace, "get", "configmap", "exporter-metrics-config-map", "-o", "yaml"},
		{"-n", testContext.namespace, "logs", "-l", "app.kubernetes.io/name=dcgm-exporter", "--tail=200"},
	} {
		output, err := runKubectlAllowOutput(ctx, command...)
		if err != nil {
			fmt.Fprintf(GinkgoWriter, "WARN: kubectl %v failed: %v\n%s\n", command, err, output)
			continue
		}
		fmt.Fprintf(GinkgoWriter, "kubectl %v output:\n%s\n", command, output)
	}
}

func dcgmiCommand(ctx context.Context, args ...string) *exec.Cmd {
	dockerArgs := []string{
		"run",
		"--rm",
		"--network", "host",
		"--entrypoint", "/usr/bin/dcgmi",
		testContext.dcgmImage,
	}
	dockerArgs = append(dockerArgs, args...)
	return exec.CommandContext(ctx, "docker", dockerArgs...)
}

// injectionField maps a failure-injection scenario to the DCGM field and value to inject.
func injectionField(scenario string) (fieldID string, value string, err error) {
	switch scenario {
	case "xid":
		return dcgmFieldXIDErrors, "79", nil
	case "nvlink":
		return dcgmFieldNVLinkCRCFlitErrorTotal, "1", nil
	default:
		return "", "", fmt.Errorf("unsupported DCGM injection scenario %q", scenario)
	}
}

// waitForTCP waits until a local TCP port-forward accepts connections.
func waitForTCP(ctx context.Context, address string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for %s", address)
}

// metricFamilyHasPositiveSample verifies a metric family contains at least one positive sample.
func metricFamilyHasPositiveSample(metrics []byte, familyName string) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}
	family := families[familyName]
	if family == nil || len(family.GetMetric()) == 0 {
		return fmt.Errorf("metric family %q is missing or empty; response: %s", familyName, metricsResponseSnippet(metrics))
	}
	for _, metric := range family.GetMetric() {
		switch {
		case metric.Gauge != nil && metric.Gauge.GetValue() > 0:
			return nil
		case metric.Counter != nil && metric.Counter.GetValue() > 0:
			return nil
		case metric.Untyped != nil && metric.Untyped.GetValue() > 0:
			return nil
		}
	}
	return fmt.Errorf("metric family %q did not contain a positive sample; response: %s", familyName, metricsResponseSnippet(metrics))
}
