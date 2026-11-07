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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/resultmarker"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	// Default configuration for local images
	defaultRegistry        = "nvidia"
	fallbackDefaultVersion = "4.8.2"

	// Test configuration
	testPort          = 9400
	startupTimeout    = 45 * time.Second  // Increased to handle GPU initialization delays
	metricsTimeout    = 120 * time.Second // Increased for DCGM first collection cycle (30s) + processing
	httpClientTimeout = 45 * time.Second  // HTTP client timeout - must exceed DCGM collection interval (30s)
)

var testConfig TestConfig

// TestConfig holds the images and port the specs use.
type TestConfig struct {
	// ExporterImage is the distroless exporter image; empty when it is not configured.
	ExporterImage string
	// DCGMImage runs nv-hostengine for the remote DCGM specs; empty when it is not configured.
	DCGMImage string
	TestPort  int
}

// TestDockerImages runs the Ginkgo suite for container image validation.
func TestDockerImages(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Docker Image Test Suite")
}

var _ = resultmarker.RegisterSuite(
	func() bool { return resultmarker.EnabledFromEnv("E2E_SUITE_RESULT_MARKERS", "E2E_RESULT_MARKERS") },
	"container",
)

var _ = BeforeSuite(func(ctx context.Context) {
	registry := getEnvOrDefault("REGISTRY", defaultRegistry)
	version := getEnvOrDefault("VERSION", defaultImageVersion())

	testConfig = TestConfig{
		ExporterImage: getEnvOrDefault("EXPORTER_DISTROLESS_IMAGE",
			fmt.Sprintf("%s/dcgm-exporter:%s", registry, version)),
		DCGMImage: os.Getenv("DCGM_IMAGE"),
		TestPort:  testPort,
	}

	By(fmt.Sprintf("Testing exporter image %q", testConfig.ExporterImage))

	By("Validating Docker is available")
	available := dockerAvailable()
	Expect(available).To(BeTrue(), "Docker must be available to run tests")

	By("Cleaning up any leftover test containers")
	removeLeftoverTestContainers(ctx)
})

// requireExporterImage returns the configured exporter image once it is available to Docker. When
// the image is not configured or not found, the spec fails if E2E_REQUIRE_CONTAINER_IMAGES=1 and
// is skipped otherwise.
func requireExporterImage(ctx context.Context) string {
	image := testConfig.ExporterImage
	required := os.Getenv("E2E_REQUIRE_CONTAINER_IMAGES") == "1"
	if image == "" {
		if required {
			Fail("required exporter image is not configured")
		}
		Skip("Exporter image is not configured. Run 'task build:image:local' to build.")
	}

	exists, err := imageExists(ctx, image)
	Expect(err).NotTo(HaveOccurred())
	if !exists {
		if required {
			Fail(fmt.Sprintf("required image not found: %s", image))
		}
		Skip(fmt.Sprintf("Image not found: %s. Run 'task build:image:local' to build.", image))
	}

	return image
}

// defaultImageVersion returns EXPORTER_VERSION from hack/versions.env, or the local fallback when it is not set.
func defaultImageVersion() string {
	if exporter := versionValue("EXPORTER_VERSION"); exporter != "" {
		return exporter
	}
	return fallbackDefaultVersion
}

// versionValue reads a named pin from hack/versions.env in source or package mode.
func versionValue(key string) string {
	for _, candidate := range []string{
		filepath.Clean(filepath.Join("hack", "versions.env")),
		filepath.Clean(filepath.Join("..", "..", "hack", "versions.env")),
	} {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		prefix := key + "="
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
	}
	return ""
}

var _ = AfterSuite(func(ctx context.Context) {
	By("Final cleanup of test containers")
	cleanupTestContainers(ctx)
})

// getEnvOrDefault returns an environment variable value or the provided fallback when unset.
func getEnvOrDefault(key, defaultValue string) string {
	val, ok := os.LookupEnv(key)
	if ok {
		return val
	}
	return defaultValue
}
