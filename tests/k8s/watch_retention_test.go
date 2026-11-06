//go:build k8s

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
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

const (
	retentionGlobalFieldName   = "DCGM_FI_DEV_SM_CLOCK_HERTZ"
	retentionOverrideFieldName = "DCGM_FI_DEV_GPU_TEMP_CELSIUS"
	retentionInterval          = 500 * time.Millisecond
)

// registerWatchRetentionSpec registers the spec that installs the chart with a
// YAML config and checks DCGM applies the global and per-group cache retention.
func registerWatchRetentionSpec() {
	It("should enforce global and per-watch-group cache retention", Label("watchRetention"), func(ctx context.Context) {
		Expect(testContext.remoteDcgm).NotTo(BeEmpty(), "watch retention scenario requires -remote-dcgm")

		Expect(testContext.dcgmProbeBinary).NotTo(BeEmpty(), "watch retention scenario requires -dcgm-probe-binary")

		// The override bounds the temperature field to two samples; the global
		// flags set the retention for everything else.
		yamlConfig := strings.TrimSpace(`
version: 2
metrics:
  - name: gpu
    every: 500ms
    fields:
      - name: DCGM_FI_DEV_GPU_TEMP_CELSIUS
        prometheus_type: gauge
        help: GPU temperature (in C).
      - name: DCGM_FI_DEV_SM_CLOCK_HERTZ
        prometheus_type: gauge
        help: SM clock frequency in hertz.
    overrides:
      - name: bounded-temperature
        match: [DCGM_FI_DEV_GPU_TEMP_CELSIUS]
        every: 500ms
        sources:
          dcgm:
            watch:
              max_keep_samples: 2
outputs:
  prometheus:
    type: prometheus
    schema: dcgm/v1
`)

		_, _ = shouldInstallExporterAndReadMetrics(
			ctx,
			[]string{
				fmt.Sprintf(
					"arguments={-r=%s,--watch-max-keep-age=0s,--watch-max-keep-samples=%d}",
					testContext.remoteDcgm,
					retentionGlobalMaxSamples,
				),
			},
			[]string{"config=" + configJSON(yamlConfig)},
			nil,
			retentionMetricsError,
		)

		// Query the cache counts through a port-forward to the remote hostengine.
		portForwardCtx, cancelPortForward := context.WithCancel(ctx)
		defer cancelPortForward()
		hostengine := shouldPortForwardRemoteDCGM(portForwardCtx)
		outputDir := GinkgoT().TempDir()

		// Wait until the cache counts reach their bounds.
		Eventually(func(g Gomega) {
			report, err := readCacheCounts(ctx, outputDir, hostengine)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(retentionCacheCountsError(report)).To(Succeed())
		}).WithPolling(time.Second).Within(time.Minute).Should(Succeed())

		// DCGM converts maxKeepSamples to an age window with scheduling slack
		// instead of enforcing a literal count quota. Keep querying across
		// multiple update intervals to prove both effective bounds remain stable.
		Consistently(func() error {
			report, err := readCacheCounts(ctx, outputDir, hostengine)
			if err != nil {
				return err
			}

			return retentionCacheCountsError(report)
		}).WithPolling(retentionInterval).Within(3 * time.Second).Should(Succeed())
	})
}

// retentionMetricsError requires both globally collected and watch-group fields in the scrape.
func retentionMetricsError(metrics []byte) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}
	for _, name := range []string{retentionOverrideFieldName, retentionGlobalFieldName} {
		family := families[name]
		if family == nil || len(family.GetMetric()) == 0 {
			return fmt.Errorf("metric family %q is missing or empty", name)
		}
	}
	return nil
}

// shouldPortForwardRemoteDCGM forwards the selected standalone DCGM pod and returns its local address.
func shouldPortForwardRemoteDCGM(ctx context.Context) string {
	namespace := remoteDcgmNamespace()
	name := remoteDcgmName()
	labels := map[string]string{
		"app.kubernetes.io/name":     "dcgm",
		"app.kubernetes.io/instance": name,
	}
	var podName string
	Eventually(func(g Gomega) {
		pods, err := kubeClient.GetPodsByLabel(ctx, namespace, labels)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(pods).To(HaveLen(1))
		podName = pods[0].Name
	}).WithPolling(time.Second).Within(time.Minute).Should(Succeed())
	shouldCheckIfPodIsReady(ctx, kubeClient, namespace, podName)

	targetPort := 5555
	if testContext.dcgmPort != "" {
		parsed, err := strconv.Atoi(testContext.dcgmPort)
		Expect(err).NotTo(HaveOccurred())
		targetPort = parsed
	}
	kubeClient.OutWriter = GinkgoWriter
	kubeClient.ErrWriter = GinkgoWriter
	localPort, err := kubeClient.PortForward(ctx, namespace, podName, targetPort)
	Expect(err).NotTo(HaveOccurred())
	return net.JoinHostPort(localPortForwardHost, strconv.Itoa(localPort))
}

// readCacheCounts runs dcgm-probe and decodes its cache-count report from a temporary file.
func readCacheCounts(
	ctx context.Context,
	outputDir string,
	hostengine string,
	fieldIDs ...int,
) (dcgmprobe.CacheCounts, error) {
	if len(fieldIDs) == 0 {
		fieldIDs = []int{retentionOverrideFieldID, retentionGlobalFieldID}
	}
	output, err := os.CreateTemp(outputDir, "dcgm-cache-counts-*.json")
	if err != nil {
		return dcgmprobe.CacheCounts{}, fmt.Errorf("reserve cache-count output: %w", err)
	}
	outputPath := output.Name()
	if err := output.Close(); err != nil {
		return dcgmprobe.CacheCounts{}, fmt.Errorf("close reserved cache-count output: %w", err)
	}
	if err := os.Remove(outputPath); err != nil {
		return dcgmprobe.CacheCounts{}, fmt.Errorf("prepare cache-count output: %w", err)
	}
	defer func() {
		_ = os.Remove(outputPath)
	}()

	args := []string{"cache-counts", "--hostengine", hostengine, "--output", outputPath}
	for _, fieldID := range fieldIDs {
		args = append(args, "--field-id", strconv.Itoa(fieldID))
	}
	queryCtx, cancelQuery := context.WithTimeout(ctx, 15*time.Second)
	defer cancelQuery()
	// #nosec G204 -- the binary path is an explicit harness input resolved before the suite starts.
	command := exec.CommandContext(queryCtx, testContext.dcgmProbeBinary, args...)
	combined, err := command.CombinedOutput()
	if err != nil {
		return dcgmprobe.CacheCounts{}, fmt.Errorf("query DCGM cache: %w; output: %s", err, strings.TrimSpace(string(combined)))
	}

	file, err := os.Open(outputPath)
	if err != nil {
		return dcgmprobe.CacheCounts{}, fmt.Errorf("open cache-count report: %w", err)
	}
	defer file.Close()
	var report dcgmprobe.CacheCounts
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return dcgmprobe.CacheCounts{}, fmt.Errorf("decode cache-count report: %w", err)
	}
	return report, nil
}
