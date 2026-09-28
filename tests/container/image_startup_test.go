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
	dto "github.com/prometheus/client_model/go"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// findImageByVariant finds an image in testConfig by its variant name.
func findImageByVariant(variant string) (ImageInfo, bool) {
	for _, img := range testConfig.Images {
		if img.Variant == variant {
			return img, true
		}
	}
	return ImageInfo{}, false
}

// validateImage performs the image availability, startup, metrics, health, and log checks.
func validateImage(ctx context.Context, img ImageInfo) {
	// Check image availability
	By(fmt.Sprintf("Checking availability of %s", img.FullName))
	exists, err := imageExists(ctx, img.FullName)
	Expect(err).NotTo(HaveOccurred())
	if !exists {
		if os.Getenv("E2E_REQUIRE_CONTAINER_IMAGES") == "1" {
			Fail(fmt.Sprintf("required image not found: %s", img.FullName))
		}
		Skip(fmt.Sprintf("Image not found: %s. Run 'make local' to build.", img.FullName))
	}
	By("✓ Image exists")

	// Start container with dynamic port allocation
	By("Starting container")
	port, err := getFreePort()
	Expect(err).NotTo(HaveOccurred(), "Should find available port")
	containerID, err := startContainer(ctx, img.FullName, port)
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
	validateHealthEndpoint(ctx, port)
	validateContainerLogs(ctx, containerID)

	By("✓ Validation successful")

	By(fmt.Sprintf("Container %s validation complete", containerID[:12]))
}

// validateMetricsEndpoint verifies the /metrics endpoint returns valid Prometheus data.
func validateMetricsEndpoint(ctx context.Context, port int) {
	By("Verifying metrics endpoint returns valid Prometheus data")

	var (
		attemptCount int
		maxAttempts  = int(metricsTimeout.Seconds())
	)

	Eventually(ctx, func(ctx context.Context) error {
		attemptCount++

		resp, err := (&http.Client{Timeout: httpClientTimeout}).Get(localHTTPURL(port, "metrics"))
		if err != nil {
			if attemptCount%10 == 0 {
				fmt.Printf("  [Attempt %d/%d] Connection failed: %v\n",
					attemptCount, maxAttempts, err)
			}
			return fmt.Errorf("connection failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			if attemptCount%10 == 0 {
				fmt.Printf("  [Attempt %d/%d] Bad status: %d\n",
					attemptCount, maxAttempts, resp.StatusCode)
			}
			return fmt.Errorf("status: %d", resp.StatusCode)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read failed: %w", err)
		}

		metricsBody := string(body)

		// Validate Prometheus format - check all required components
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

		families, err := metriccontract.ParseText(body)
		if err != nil {
			return fmt.Errorf("parse metrics: %w", err)
		}
		if err := metriccontract.ValidateAtLeastOneDCGMGPUFamily(families); err != nil {
			return err
		}
		if err := validateDefaultCounterRows(families); err != nil {
			return err
		}

		return nil
	}).WithTimeout(metricsTimeout).WithPolling(time.Second).Should(Succeed(),
		"Metrics endpoint should return valid Prometheus format with real DCGM GPU data")
}

// validateDefaultCounterRows checks the default CSV rows against emitted container metrics.
func validateDefaultCounterRows(families map[string]*dto.MetricFamily) error {
	data, err := readDefaultCounterCSV()
	if err != nil {
		return err
	}
	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("read default counters: %w", err)
	}
	return metriccontract.ValidateDefaultCounterRows(
		families,
		rows,
		metriccontract.DefaultCounterOptions{SkipWriter: GinkgoWriter},
	)
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
func validateDistrolessLshw(ctx context.Context, img ImageInfo) {
	By("Verifying distroless image includes /usr/bin/lshw")
	stdout, err := runLshwJSON(ctx, img.FullName)
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
	It("should validate [ubuntu26.04] image", func(ctx context.Context) {
		img, found := findImageByVariant("ubuntu26.04")
		if !found {
			Skip("ubuntu26.04 image not configured")
		}
		validateImage(ctx, img)
	})

	It("should validate [distroless] image", func(ctx context.Context) {
		img, found := findImageByVariant("distroless")
		if !found {
			Skip("distroless image not configured")
		}
		validateDistrolessLshw(ctx, img)
		validateImage(ctx, img)
	})
})
