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
	fallbackDefaultVersion = "4.5.3-4.8.2"

	// Test configuration
	testPort          = 9400
	startupTimeout    = 45 * time.Second  // Increased to handle GPU initialization delays
	metricsTimeout    = 120 * time.Second // Increased for DCGM first collection cycle (30s) + processing
	httpClientTimeout = 45 * time.Second  // HTTP client timeout - must exceed DCGM collection interval (30s)
)

var testConfig TestConfig

type TestConfig struct {
	Images    []ImageInfo
	DCGMImage string
	TestPort  int
}

type ImageInfo struct {
	FullName string
	Variant  string
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
	var images []ImageInfo

	// Get image configuration from environment (or use defaults)
	registry := getEnvOrDefault("REGISTRY", defaultRegistry)
	version := getEnvOrDefault("VERSION", defaultImageVersion())

	// Get specific images for each variant (or build default from registry/version)
	imageUbuntu := getEnvOrDefault("EXPORTER_UBUNTU_IMAGE",
		fmt.Sprintf("%s/dcgm-exporter:%s-ubuntu26.04", registry, version))
	imageDistroless := getEnvOrDefault("EXPORTER_DISTROLESS_IMAGE",
		fmt.Sprintf("%s/dcgm-exporter:%s-distroless", registry, version))
	dcgmImage := getEnvOrDefault("DCGM_IMAGE", imageUbuntu)

	// Add images that are configured
	if imageUbuntu != "" {
		images = append(images, ImageInfo{
			FullName: imageUbuntu,
			Variant:  "ubuntu26.04",
		})
	}
	if imageDistroless != "" {
		images = append(images, ImageInfo{
			FullName: imageDistroless,
			Variant:  "distroless",
		})
	}

	testConfig = TestConfig{
		Images:    images,
		DCGMImage: dcgmImage,
		TestPort:  testPort,
	}

	By(fmt.Sprintf("Testing %d image(s)", len(images)))
	for _, img := range images {
		By(fmt.Sprintf("  - %s [%s]", img.FullName, img.Variant))
	}

	By("Validating Docker is available")
	available := dockerAvailable()
	Expect(available).To(BeTrue(), "Docker must be available to run tests")

	By("Cleaning up any leftover test containers")
	cleanupTestContainers(ctx)
})

func defaultImageVersion() string {
	dcgm := versionValue("DCGM_VERSION")
	exporter := versionValue("EXPORTER_VERSION")
	if dcgm != "" && exporter != "" {
		return dcgm + "-" + exporter
	}
	return fallbackDefaultVersion
}

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
