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
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const mountedCustomCountersFile = "/tmp/dcgm-exporter-custom-counters.csv"

// availableExporterImage returns the first configured exporter image available to Docker.
func availableExporterImage(ctx context.Context) ImageInfo {
	for _, img := range testConfig.Images {
		exists, err := imageExists(ctx, img.FullName)
		Expect(err).NotTo(HaveOccurred())
		if exists {
			return img
		}
	}
	if os.Getenv("E2E_REQUIRE_CONTAINER_IMAGES") == "1" {
		Fail("no configured exporter image is available")
	}
	Skip("No configured exporter image found. Run 'make local' to build.")
	return ImageInfo{}
}

// fetchMetricsText scrapes the container metrics endpoint until it returns HTTP 200.
func fetchMetricsText(ctx context.Context, port int) string {
	var body string
	Eventually(ctx, func(ctx context.Context) error {
		resp, err := (&http.Client{Timeout: httpClientTimeout}).Get(localHTTPURL(port, "metrics"))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status: %d", resp.StatusCode)
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		body = string(raw)
		return nil
	}).WithTimeout(metricsTimeout).WithPolling(time.Second).Should(Succeed())
	return body
}

// writeCustomCountersFile creates a temporary collector CSV mounted by configuration tests.
func writeCustomCountersFile() string {
	dir, err := os.MkdirTemp("", "dcgm-exporter-counters-")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "custom-counters.csv")
	data := "DCGM_FI_DEV_GPU_TEMP,gauge,GPU temperature (in C).\n"
	Expect(os.WriteFile(path, []byte(data), 0o600)).To(Succeed())
	return path
}

// expectSelectorDiagnostic verifies invalid selectors produce a useful startup diagnostic.
func expectSelectorDiagnostic(ctx context.Context, image ImageInfo, selector string, expected string) {
	port := mustFreePort()
	id := runExporterContainer(ctx, image.FullName, []string{"--net", "host"},
		"-a", fmt.Sprintf(":%d", port), "-f", countersFile, "-d", selector)

	Eventually(ctx, func(ctx context.Context) string {
		logs, _ := containerLogsCombined(ctx, id)
		return logs
	}).WithTimeout(startupTimeout).WithPolling(time.Second).Should(ContainSubstring(expected))

	if !containerIsRunning(ctx, id) {
		return
	}

	body := fetchMetricsText(ctx, port)
	Expect(body).NotTo(ContainSubstring("DCGM_FI_DEV_"),
		"invalid device selection should not expose DCGM GPU metric families")
}

var _ = Describe("dcgm-exporter container runtime configuration", Serial, Label("configuration"), func() {
	It("honors DCGM_EXPORTER_LISTEN on a non-default port", func(ctx context.Context) {
		img := availableExporterImage(ctx)
		port := mustFreePort()

		runExporterContainer(ctx, img.FullName,
			[]string{"--net", "host", "-e", fmt.Sprintf("DCGM_EXPORTER_LISTEN=:%d", port)})

		body := fetchValidMetrics(ctx, port)
		Expect(body).To(ContainSubstring("DCGM_FI_DEV_"))
	})

	It("uses a mounted custom collectors file", func(ctx context.Context) {
		img := availableExporterImage(ctx)
		port := mustFreePort()
		hostCountersFile := writeCustomCountersFile()

		runExporterContainer(ctx, img.FullName,
			[]string{"--net", "host", "-v", fmt.Sprintf("%s:%s:ro", hostCountersFile, mountedCustomCountersFile)},
			"-a", fmt.Sprintf(":%d", port), "-f", mountedCustomCountersFile)

		body := fetchValidMetrics(ctx, port)
		Expect(body).To(ContainSubstring("DCGM_FI_DEV_GPU_TEMP"))
		Expect(body).NotTo(ContainSubstring("DCGM_FI_DEV_POWER_USAGE"))
	})

	It("accepts runtime interval, validation, logging, and web timeout flags", func(ctx context.Context) {
		img := availableExporterImage(ctx)
		port := mustFreePort()

		runExporterContainer(ctx, img.FullName,
			[]string{"--net", "host"},
			"-a", fmt.Sprintf(":%d", port),
			"-f", countersFile,
			"--collect-interval=1000",
			"--disable-startup-validate",
			"--enable-dcgm-log",
			"--dcgm-log-level=INFO",
			"--web-read-timeout=3s",
			"--web-write-timeout=2m")

		body := fetchValidMetrics(ctx, port)
		Expect(body).To(ContainSubstring("DCGM_FI_DEV_"))
	})
})

var _ = Describe("dcgm-exporter pprof startup validation", Serial, Label("pprofRequiresWebConfig"), func() {
	It("rejects --enable-pprof without --web-config-file", func(ctx context.Context) {
		img := availableExporterImage(ctx)
		port := mustFreePort()

		id := runExporterContainer(ctx, img.FullName, []string{"--net", "host"},
			"-a", fmt.Sprintf(":%d", port),
			"-f", countersFile,
			"--enable-pprof")

		Eventually(ctx, func(ctx context.Context) bool {
			return containerIsRunning(ctx, id)
		}).WithTimeout(startupTimeout).WithPolling(time.Second).Should(BeFalse(),
			"exporter must not stay up when pprof is enabled without a web config")

		logs, err := containerLogsCombined(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(logs).To(ContainSubstring("enable-pprof"))
		Expect(logs).To(ContainSubstring("web-config-file"))

		resp, err := (&http.Client{Timeout: time.Second}).Get(localHTTPURL(port, "metrics"))
		if err == nil {
			defer resp.Body.Close()
			Expect(resp.StatusCode).NotTo(Equal(http.StatusOK))
		}
	})
})

var _ = Describe("dcgm-exporter invalid device selectors", Serial, Label("invalidDeviceSelectors"), func() {
	DescribeTable("logs the rejected device selector",
		func(ctx context.Context, selector string, expected string) {
			img := availableExporterImage(ctx)
			expectSelectorDiagnostic(ctx, img, selector, expected)
		},
		Entry("invalid GPU ID", "g:999999", "couldn't find requested GPU ID '999999'"),
		Entry("invalid GPU instance ID", "i:999999", "couldn't find requested GPU instance ID '999999'"),
	)
})
