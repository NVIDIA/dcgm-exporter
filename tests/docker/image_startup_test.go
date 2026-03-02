//go:build docker

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

package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// findImageByVariant finds an image in testConfig by its variant name
func findImageByVariant(variant string) (ImageInfo, bool) {
	for _, img := range testConfig.Images {
		if img.Variant == variant {
			return img, true
		}
	}
	return ImageInfo{}, false
}

// validateImage performs complete validation of a Docker image
func validateImage(ctx context.Context, img ImageInfo) {
	// Check image availability
	By(fmt.Sprintf("Checking availability of %s", img.FullName))
	exists, err := imageExists(ctx, img.FullName)
	Expect(err).NotTo(HaveOccurred())
	if !exists {
		Skip(fmt.Sprintf("Image not found: %s. Run 'make local' to build.", img.FullName))
	}
	By("✓ Image exists")

	// Start container with dynamic port allocation
	By("Starting container")
	port, err := getFreePort()
	Expect(err).NotTo(HaveOccurred(), "Should find available port")
	containerID, err := startContainer(ctx, img.FullName, port)
	Expect(err).NotTo(HaveOccurred())

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

	// Cleanup immediately to prevent GPU contention with next test
	By(fmt.Sprintf("Cleaning up container %s", containerID[:12]))
	err = cleanupContainer(ctx, containerID)
	Expect(err).NotTo(HaveOccurred(), "Container should cleanup successfully")
}

// validateMetricsEndpoint verifies the /metrics endpoint returns valid Prometheus data
func validateMetricsEndpoint(ctx context.Context, port int) {
	By("Verifying metrics endpoint returns valid Prometheus data")

	var (
		attemptCount int
		maxAttempts  = int(metricsTimeout.Seconds())
	)

	Eventually(ctx, func(ctx context.Context) error {
		attemptCount++

		resp, err := (&http.Client{Timeout: httpClientTimeout}).Get(
			fmt.Sprintf("http://localhost:%d/metrics", port))
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
				name: "actual metrics",
				predicate: func(s string) bool {
					return strings.Contains(s, "DCGM_") ||
						strings.Contains(s, "go_") ||
						strings.Contains(s, "process_")
				},
				errorMsg: "no metrics found yet",
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
	}).WithTimeout(metricsTimeout).WithPolling(time.Second).Should(Succeed(),
		"Metrics endpoint should return valid Prometheus format with data")
}

// validateHealthEndpoint verifies the /health endpoint returns 200 OK
func validateHealthEndpoint(ctx context.Context, port int) {
	By("Verifying health endpoint")

	Eventually(ctx, func(ctx context.Context) error {
		resp, err := (&http.Client{Timeout: httpClientTimeout}).Get(
			fmt.Sprintf("http://localhost:%d/health", port))
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

// validateContainerLogs checks for critical errors in container logs
func validateContainerLogs(ctx context.Context, containerID string) {
	By("Checking logs for critical errors")
	logs, err := getContainerLogs(ctx, containerID)
	Expect(err).NotTo(HaveOccurred())
	Expect(logs).NotTo(ContainSubstring("panic:"),
		"Container logs should not contain panics")
}

var _ = Describe("Docker Image Tests", Serial, func() {
	It("should validate [ubuntu22.04] image", func(ctx context.Context) {
		img, found := findImageByVariant("ubuntu22.04")
		if !found {
			Skip("ubuntu22.04 image not configured")
		}
		validateImage(ctx, img)
	})

	It("should validate [ubi9] image", func(ctx context.Context) {
		img, found := findImageByVariant("ubi9")
		if !found {
			Skip("ubi9 image not configured")
		}
		validateImage(ctx, img)
	})

	It("should validate [distroless] image", func(ctx context.Context) {
		img, found := findImageByVariant("distroless")
		if !found {
			Skip("distroless image not configured")
		}
		validateImage(ctx, img)
	})
})
