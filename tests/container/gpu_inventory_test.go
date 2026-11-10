//go:build container

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

package container

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// hostInventory reads GPU data from nvidia-smi on the test host. Its identity
// read is shared by every comparison in the test process.
var hostInventory = nvidiasmi.NewInventory(nvidiasmi.LocalRunner)

// validateGPUInventory compares the container's identity labels and values
// with nvidia-smi on the test host, for the families in the shipped CSV the
// image runs with. The container shares the host UTS namespace, so its hostname
// label is the Docker host's name. Each attempt scrapes and reads the nvidia-smi
// values for those families again because the image collects every 30 seconds
// by default.
func validateGPUInventory(ctx context.Context, port int) {
	By("Comparing exporter GPU identity and values with nvidia-smi on the host")

	// A remote Docker daemon runs the exporter on GPUs this host cannot query.
	endpoint, err := dockerEndpoint(ctx)
	if err != nil {
		fmt.Printf("GPU inventory comparison skipped: cannot read the Docker endpoint: %v\n", err)
		return
	}
	if !localDockerEndpoint(endpoint) {
		fmt.Printf("GPU inventory comparison skipped: Docker endpoint %q is not a local unix socket\n", endpoint)
		return
	}

	data, err := readDefaultCounterCSV()
	Expect(err).NotTo(HaveOccurred(), "Should read the shipped default counters CSV")
	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(data))
	Expect(err).NotTo(HaveOccurred(), "Should parse the shipped default counters CSV")
	hostname, err := dockerHostName(ctx)
	Expect(err).NotTo(HaveOccurred(), "read the Docker host name")
	opts := metriccontract.GPUInventoryOptions{CSVRows: rows, ExpectedHostname: hostname, HostnameSource: "Docker host name"}

	valueFields := metriccontract.ValueQueryFields(rows)
	var lastReport metriccontract.GPUInventoryReport
	Eventually(ctx, func(ctx context.Context) error {
		gpus, err := hostInventory.Identity(ctx)
		if err != nil {
			return nvidiaSmiAttemptError(err)
		}
		readings, err := hostInventory.Readings(ctx, valueFields)
		if err != nil {
			return nvidiaSmiAttemptError(err)
		}

		body, err := scrapeStartupMetrics(port, 1, 1)
		if err != nil {
			return fmt.Errorf("scrape exporter: %w", err)
		}
		families, err := metriccontract.ParseText(body)
		if err != nil {
			return fmt.Errorf("parse metrics: %w", err)
		}

		lastReport = metriccontract.CompareGPUInventory(families, gpus, readings, opts)
		return lastReport.Err()
	}).WithTimeout(metricsTimeout).WithPolling(5*time.Second).Should(Succeed(),
		"exporter GPU identity and values should match nvidia-smi on the host")

	for _, reason := range lastReport.Skipped {
		fmt.Fprintf(GinkgoWriter, "GPU inventory comparison skipped: %s\n", reason)
	}
}

// dockerEndpoint returns the daemon address docker run uses: DOCKER_HOST when
// set, otherwise the endpoint of the current Docker context.
func dockerEndpoint(ctx context.Context) (string, error) {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return host, nil
	}

	output, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return "", fmt.Errorf("docker context inspect: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// nvidiaSmiAttemptError stops the retries when nvidia-smi is not installed and
// otherwise returns err so the comparison tries again until its timeout.
func nvidiaSmiAttemptError(err error) error {
	if nvidiasmi.IsMissing(err) {
		return StopTrying("setup: comparing exporter output with the host GPUs needs nvidia-smi on PATH").Wrap(err)
	}
	return err
}

// dockerHostName returns the name of the host the Docker daemon runs on. A
// container in the host UTS namespace reports this name, which differs from
// the test process's host name when the test itself runs in a container.
func dockerHostName(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.Name}}").Output()
	if err != nil {
		return "", fmt.Errorf("docker info: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}
