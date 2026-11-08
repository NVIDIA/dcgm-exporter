//go:build container

/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

package container

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// validateImage performs the startup, metrics, health, and log checks on an available image.
func validateImage(ctx context.Context, image string) {
	// Start container with dynamic port allocation
	By("Starting container")
	port, err := getFreePort()
	Expect(err).NotTo(HaveOccurred(), "Should find available port")
	containerID, err := startContainer(ctx, image, port)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(cleanupContainer, ctx, containerID)

	// Wait for container to be ready
	By(fmt.Sprintf("Waiting for container %s to be running", containerID[:12]))
	Eventually(ctx, func(ctx context.Context) bool {
		return containerIsRunning(ctx, containerID)
	}).WithTimeout(startupTimeout).WithPolling(time.Second).Should(BeTrue(),
		"Container should start within timeout")

	// Run all validations
	validateMetricsEndpoint(ctx, port)
	validateGPUInventory(ctx, port)
	validateHealthEndpoint(ctx, port)
	validateContainerLogs(ctx, containerID)

	By("✓ Validation successful")

	By(fmt.Sprintf("Container %s validation complete", containerID[:12]))
}

// validateMetricsEndpoint verifies the /metrics endpoint returns valid Prometheus data.
func validateMetricsEndpoint(ctx context.Context, port int) {
	By("Verifying metrics endpoint returns valid Prometheus data")

	data, err := readDefaultCounterCSV()
	Expect(err).NotTo(HaveOccurred(), "Should read the shipped default counters CSV")
	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(data))
	Expect(err).NotTo(HaveOccurred(), "Should parse the shipped default counters CSV")
	support, err := metriccontract.ParseFieldSupport(os.Getenv(metriccontract.FieldSupportEnv), rows)
	Expect(err).NotTo(HaveOccurred(), "DCGM field support evidence should name enabled shipped default counters")

	var (
		attemptCount int
		maxAttempts  = int(metricsTimeout.Seconds())
	)

	Eventually(ctx, func(ctx context.Context) error {
		attemptCount++

		body, err := scrapeStartupMetrics(port, attemptCount, maxAttempts)
		if err != nil {
			return err
		}
		if err := validateStartupMetricComments(body, attemptCount, maxAttempts); err != nil {
			return err
		}

		families, err := metriccontract.ParseText(body)
		if err != nil {
			return fmt.Errorf("parse metrics: %w", err)
		}
		if err := metriccontract.ValidateDCGMSamples(families); err != nil {
			return err
		}
		if err := metriccontract.ValidateContracts(families, metriccontract.BaselineGPUFamilies()); err != nil {
			return err
		}
		return metriccontract.ValidateDefaultCounterRows(families, rows, metriccontract.DefaultCounterOptions{
			FieldSupport: support,
			ExactCSV:     true,
			SkipWriter:   GinkgoWriter,
		})
	}).WithTimeout(metricsTimeout).WithPolling(time.Second).Should(Succeed(),
		"Metrics endpoint should return the baseline GPU families and shipped default counter contract")
}

// scrapeStartupMetrics fetches one readiness scrape and reports periodic HTTP failures.
func scrapeStartupMetrics(port, attemptCount, maxAttempts int) ([]byte, error) {
	resp, err := (&http.Client{Timeout: httpClientTimeout}).Get(localHTTPURL(port, "metrics"))
	if err != nil {
		if attemptCount%10 == 0 {
			fmt.Printf("  [Attempt %d/%d] Connection failed: %v\n",
				attemptCount, maxAttempts, err)
		}
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if attemptCount%10 == 0 {
			fmt.Printf("  [Attempt %d/%d] Bad status: %d\n",
				attemptCount, maxAttempts, resp.StatusCode)
		}
		return nil, fmt.Errorf("status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read failed: %w", err)
	}
	return body, nil
}

// validateStartupMetricComments checks required comments and reports initialization progress.
func validateStartupMetricComments(body []byte, attemptCount, maxAttempts int) error {
	metricsBody := string(body)

	type validationCheck struct {
		name      string
		predicate func(string) bool
		errorMsg  string
	}

	checks := [...]validationCheck{
		{
			name:      "HELP comments",
			predicate: func(s string) bool { return strings.Contains(s, "# HELP") },
			errorMsg:  "missing HELP comments (DCGM may still be initializing)",
		},
		{
			name:      "TYPE comments",
			predicate: func(s string) bool { return strings.Contains(s, "# TYPE") },
			errorMsg:  "missing TYPE comments",
		},
		{
			name: "dcgm metric comments",
			predicate: func(s string) bool {
				return strings.Contains(s, "# HELP DCGM_FI_") &&
					strings.Contains(s, "# TYPE DCGM_FI_")
			},
			errorMsg: "no DCGM_FI_ metric comments found yet",
		},
	}

	for _, check := range checks {
		if !check.predicate(metricsBody) {
			if attemptCount%10 == 0 {
				fmt.Printf("  [Attempt %d/%d] Missing %s (body: %d bytes)...\n",
					attemptCount, maxAttempts, check.name, len(metricsBody))
			}
			return fmt.Errorf("%s", check.errorMsg)
		}
	}
	return nil
}

// readDefaultCounterCSV finds the default counter CSV from source or package mode.
func readDefaultCounterCSV() ([]byte, error) {
	candidates := []string{
		filepath.Clean(filepath.Join("etc", "default-counters.csv")),
		filepath.Clean(filepath.Join("..", "..", "etc", "default-counters.csv")),
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err == nil {
			return data, nil
		}
	}
	return nil, fmt.Errorf("could not find default counters CSV; checked %s", strings.Join(candidates, ", "))
}

// validateHealthEndpoint verifies the /health endpoint returns 200 OK.
func validateHealthEndpoint(ctx context.Context, port int) {
	By("Verifying health endpoint")

	Eventually(ctx, func(ctx context.Context) error {
		resp, err := (&http.Client{Timeout: httpClientTimeout}).Get(localHTTPURL(port, "health"))
		if err != nil {
			return fmt.Errorf("health check failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected status: %d", resp.StatusCode)
		}
		return nil
	}).WithTimeout(startupTimeout).WithPolling(time.Second).Should(Succeed(),
		"Health endpoint should return 200")
}

// validateDistrolessLshw verifies the distroless image still contains a usable lshw binary.
func validateDistrolessLshw(ctx context.Context, image string) {
	By("Verifying distroless image includes /usr/bin/lshw")
	stdout, err := runLshwJSON(ctx, image)
	Expect(err).NotTo(HaveOccurred())
	output := strings.TrimSpace(stdout)
	Expect(output).NotTo(BeEmpty(), "lshw should emit JSON output")
	Expect(json.Valid([]byte(output))).To(BeTrue(),
		"lshw -json output should be valid JSON")
}

// validateContainerLogs checks for critical errors in container logs.
func validateContainerLogs(ctx context.Context, containerID string) {
	By("Checking logs for critical errors")
	logs, err := getContainerLogs(ctx, containerID)
	Expect(err).NotTo(HaveOccurred())
	Expect(logs).NotTo(ContainSubstring("panic:"),
		"Container logs should not contain panics")
	Expect(logs).NotTo(ContainSubstring("Could not exec '/usr/bin/lshw'"),
		"Container logs should not report missing lshw")
}

var _ = Describe("Docker Image Tests", Serial, Label("imageStartup"), func() {
	It("should validate [distroless] image", func(ctx context.Context) {
		image := requireExporterImage(ctx)
		validateImage(ctx, image)
		validateDistrolessLshw(ctx, image)
	})
})
