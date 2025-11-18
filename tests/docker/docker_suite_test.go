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
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	// Default configuration for local images
	defaultRegistry = "nvidia"
	defaultVersion  = "4.4.2-4.7.0"

	// Test configuration
	testPort          = 9400
	startupTimeout    = 45 * time.Second  // Increased to handle GPU initialization delays
	metricsTimeout    = 120 * time.Second // Increased for DCGM first collection cycle (30s) + processing
	httpClientTimeout = 45 * time.Second  // HTTP client timeout - must exceed DCGM collection interval (30s)
)

var testConfig TestConfig

type TestConfig struct {
	Images   []ImageInfo
	TestPort int
}

type ImageInfo struct {
	FullName string
	Variant  string
}

func TestDockerImages(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Docker Image Test Suite")
}

var _ = BeforeSuite(func(ctx context.Context) {
	var images []ImageInfo

	// Get image configuration from environment (or use defaults)
	registry := getEnvOrDefault("REGISTRY", defaultRegistry)
	version := getEnvOrDefault("VERSION", defaultVersion)

	// Get specific images for each variant (or build default from registry/version)
	imageUbuntu := getEnvOrDefault("IMAGE_UBUNTU",
		fmt.Sprintf("%s/dcgm-exporter:%s-ubuntu22.04", registry, version))
	imageUbi := getEnvOrDefault("IMAGE_UBI",
		fmt.Sprintf("%s/dcgm-exporter:%s-ubi9", registry, version))
	imageDistroless := getEnvOrDefault("IMAGE_DISTROLESS",
		fmt.Sprintf("%s/dcgm-exporter:%s-distroless", registry, version))

	// Add images that are configured
	if imageUbuntu != "" {
		images = append(images, ImageInfo{
			FullName: imageUbuntu,
			Variant:  "ubuntu22.04",
		})
	}
	if imageUbi != "" {
		images = append(images, ImageInfo{
			FullName: imageUbi,
			Variant:  "ubi9",
		})
	}
	if imageDistroless != "" {
		images = append(images, ImageInfo{
			FullName: imageDistroless,
			Variant:  "distroless",
		})
	}

	testConfig = TestConfig{
		Images:   images,
		TestPort: testPort,
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

var _ = AfterSuite(func(ctx context.Context) {
	By("Final cleanup of test containers")
	cleanupTestContainers(ctx)
})

func getEnvOrDefault(key, defaultValue string) string {
	val := os.Getenv(key)
	if val != "" {
		return val
	}
	return defaultValue
}
