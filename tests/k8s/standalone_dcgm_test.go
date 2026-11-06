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
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

const restartEnergyFieldID = 1611 // DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL

var _ = Describe("dcgm-exporter standalone DCGM scenarios", func() {
	Context("DCGM exporter connects to a remote DCGM", Ordered, func() {
		AfterEach(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		registerWatchRetentionSpec()

		It("should emit the default metric contract through a standalone DCGM", Label("remoteDcgm"), func(ctx context.Context) {
			Expect(testContext.remoteDcgm).NotTo(BeEmpty(), "remote DCGM scenario requires -remote-dcgm")
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments(fmt.Sprintf("-r=%s", testContext.remoteDcgm))},
				nil,
				nil,
				func(metrics []byte) error {
					return baselineMetricContractError(metrics, false)
				})
		})

		It("should recover metrics after the standalone DCGM restarts", Label("remoteDcgmRestart"), func(ctx context.Context) {
			Expect(testContext.remoteDcgm).NotTo(BeEmpty(), "remote DCGM restart scenario requires -remote-dcgm")
			Expect(testContext.remoteDcgm).To(Equal(remoteDcgmServiceAddress()), "restart scenario must connect to the E2E-owned DCGM Service")
			Expect(testContext.dcgmProbeBinary).NotTo(BeEmpty(), "restart freshness requires -dcgm-probe-binary")
			exporterPod, initialMetrics := shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments(fmt.Sprintf("-r=%s", testContext.remoteDcgm))},
				nil,
				nil,
				func(metrics []byte) error {
					return baselineMetricContractError(metrics, false)
				})

			Expect(exporterPod.Status.ContainerStatuses).To(HaveLen(1))
			initialContainer := exporterPod.Status.ContainerStatuses[0]
			Expect(initialContainer.Name).To(Equal("exporter"))
			Expect(initialContainer.State.Running).NotTo(BeNil())
			baseline, err := restartEnergySamples(initialMetrics)
			Expect(err).NotTo(HaveOccurred())

			dcgmPod := shouldRestartRemoteDCGM(ctx)
			Expect(dcgmPod.Status.ContainerStatuses).To(HaveLen(1))
			Expect(dcgmPod.Status.ContainerStatuses[0].State.Running).NotTo(BeNil())
			// Hostengine sample times and its container start time use the same
			// node clock, avoiding a comparison against the controller's clock.
			dcgmStartedAt := dcgmPod.Status.ContainerStatuses[0].State.Running.StartedAt.Time
			portForwardCtx, cancelPortForward := context.WithCancel(ctx)
			defer cancelPortForward()
			hostengine := shouldPortForwardRemoteDCGM(portForwardCtx)
			outputDir := GinkgoT().TempDir()
			recoveryCtx, cancelRecovery := context.WithTimeout(ctx, 2*time.Minute)
			defer cancelRecovery()

			Eventually(func(g Gomega) {
				// A scrape on the old DCGM connection reports the terminal error
				// that lets Kubernetes restart the exporter container naturally.
				metrics, err := kubeClient.DoHTTPRequest(recoveryCtx,
					exporterPod.Namespace,
					exporterPod.Name,
					dcgmExporterPort,
					"metrics")
				g.Expect(err).NotTo(HaveOccurred())
				currentPod, err := kubeClient.GetPod(recoveryCtx, exporterPod.Namespace, exporterPod.Name)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(currentPod.UID).To(Equal(exporterPod.UID), "automatic recovery must preserve the exporter pod")
				g.Expect(currentPod.Status.ContainerStatuses).To(HaveLen(1))
				container := currentPod.Status.ContainerStatuses[0]
				g.Expect(container.Name).To(Equal(initialContainer.Name))
				g.Expect(container.RestartCount).To(BeNumerically(">", initialContainer.RestartCount), "Kubernetes must restart the exporter after its terminal DCGM error")
				g.Expect(container.LastTerminationState.Terminated).NotTo(BeNil())
				g.Expect(container.LastTerminationState.Terminated.ExitCode).NotTo(BeZero(), "the lost DCGM connection must terminate the exporter with an error")
				g.Expect(container.State.Running).NotTo(BeNil())
				g.Expect(container.State.Running.StartedAt.Time.After(initialContainer.State.Running.StartedAt.Time)).To(BeTrue())
				g.Expect(container.Ready).To(BeTrue())
				g.Expect(metrics).NotTo(BeEmpty())
				g.Expect(baselineMetricContractError(metrics, false)).To(Succeed())
				// Read history after the scrape so it includes the exact native
				// sample even when the energy counter advances between requests.
				report, err := readCacheCounts(recoveryCtx, outputDir, hostengine, restartEnergyFieldID)
				g.Expect(err).NotTo(HaveOccurred())
				freshnessErr := restartEnergyFreshnessError(metrics, baseline, report, dcgmStartedAt)
				if freshnessErr != nil {
					// Preserve the semantic failure if a later query reaches the
					// recovery deadline and replaces Eventually's final error.
					fmt.Fprintf(GinkgoWriter, "Post-restart freshness comparison failed: %v\n", freshnessErr)
				}
				g.Expect(freshnessErr).To(Succeed())
				By(fmt.Sprintf("Exporter pod %s kept UID %s; container restarted %d -> %d and is ready; energy samples match post-restart DCGM history", exporterPod.Name, exporterPod.UID, initialContainer.RestartCount, container.RestartCount))
			}).WithPolling(5 * time.Second).Within(2 * time.Minute).WithContext(recoveryCtx).Should(Succeed())
		})

		It("should emit metrics through an IPv6-only standalone DCGM Service hostname", Label("remoteDcgmIPv6Hostname"), func(ctx context.Context) {
			Expect(testContext.remoteDcgm).To(Equal(remoteDcgmServiceAddress()), "IPv6 hostname scenario must use the E2E-owned Service DNS name")
			Eventually(func() error {
				return remoteDcgmIPv6ServiceReady(ctx)
			}).WithPolling(pollingIntervalSlow).Within(metricsWaitTimeout).Should(Succeed())

			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments(fmt.Sprintf("-r=%s", testContext.remoteDcgm))},
				nil,
				nil,
				func(metrics []byte) error {
					return baselineMetricContractError(metrics, false)
				})
		})
	})
})

// shouldRestartRemoteDCGM replaces only the owned DCGM pod and returns its ready replacement.
func shouldRestartRemoteDCGM(ctx context.Context) *corev1.Pod {
	namespace := remoteDcgmNamespace()
	name := remoteDcgmName()
	labels := map[string]string{"app.kubernetes.io/name": "dcgm", "app.kubernetes.io/instance": name}
	pods, err := kubeClient.GetPodsByLabel(ctx, namespace, labels)
	Expect(err).NotTo(HaveOccurred())
	Expect(pods).To(HaveLen(1), "restart requires exactly one owned standalone DCGM pod")
	oldPod := pods[0]

	shouldRunKubectl(ctx, "-n", namespace, "delete", "pod", oldPod.Name, "--wait=true")
	shouldRunKubectl(ctx, "-n", namespace, "rollout", "status", "deployment/"+name, "--timeout=120s")
	var replacement *corev1.Pod
	Eventually(func(g Gomega) {
		pods, err := kubeClient.GetPodsByLabel(ctx, namespace, labels)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(pods).To(HaveLen(1))
		g.Expect(pods[0].UID).NotTo(Equal(oldPod.UID))
		replacement = &pods[0]
	}).WithPolling(time.Second).Within(time.Minute).Should(Succeed())
	shouldCheckIfPodIsReady(ctx, kubeClient, namespace, replacement.Name)
	// Refresh status after readiness so the caller sees its running state.
	replacement, err = kubeClient.GetPod(ctx, namespace, replacement.Name)
	Expect(err).NotTo(HaveOccurred())
	return replacement
}

// restartEnergySamples retains the GPU identity and energy values in one scrape.
func restartEnergySamples(metrics []byte) (map[string]float64, error) {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return nil, err
	}
	family := families["DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL"]
	if family == nil || len(family.GetMetric()) == 0 {
		return nil, fmt.Errorf("energy metric family is missing or empty")
	}
	samples := make(map[string]float64, len(family.GetMetric()))
	for _, sample := range family.GetMetric() {
		labels := make(map[string]string, len(sample.GetLabel()))
		for _, label := range sample.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		if labels["gpu"] == "" || labels["UUID"] == "" || labels["GPU_CI_ID"] != "" || sample.Counter == nil {
			return nil, fmt.Errorf("energy sample lacks GPU identity or counter value")
		}
		key := restartEnergyIdentity(labels)
		if _, exists := samples[key]; exists {
			return nil, fmt.Errorf("duplicate energy identity %s", key)
		}
		samples[key] = sample.GetCounter().GetValue()
		if math.IsNaN(samples[key]) || math.IsInf(samples[key], 0) || samples[key] < 0 || samples[key] >= 1<<53 {
			return nil, fmt.Errorf("energy sample %s is invalid or exceeds exact integer precision", key)
		}
	}
	return samples, nil
}

func restartEnergyIdentity(labels map[string]string) string {
	return fmt.Sprintf("gpu=%s UUID=%s GPU_I_ID=%s", labels["gpu"], labels["UUID"], labels["GPU_I_ID"])
}

// restartEnergyFreshnessError rejects cached output using independent native
// timestamps, values and GPU identities from the replacement hostengine.
func restartEnergyFreshnessError(metrics []byte, baseline map[string]float64, report dcgmprobe.CacheCounts, startedAt time.Time) error {
	if report.Version != dcgmprobe.CurrentCacheCountsVersion {
		return fmt.Errorf("cache report version %d, want %d", report.Version, dcgmprobe.CurrentCacheCountsVersion)
	}
	samples, err := restartEnergySamples(metrics)
	if err != nil {
		return err
	}
	if len(samples) != len(baseline) {
		return fmt.Errorf("energy identity count changed from %d to %d", len(baseline), len(samples))
	}
	for identity, value := range samples {
		previous, exists := baseline[identity]
		if !exists || value <= previous {
			return fmt.Errorf("energy sample %s = %g has not advanced from baseline %g", identity, value, previous)
		}
		matched := false
		for _, native := range report.History {
			entityKey := strconv.FormatUint(uint64(native.Entity.Group), 10) + ":" + strconv.FormatUint(uint64(native.Entity.ID), 10)
			labels := report.EntityLabels[entityKey]
			if native.FieldID == restartEnergyFieldID && native.FieldType == uint('i') && native.Status == 0 &&
				native.TimestampUsec > startedAt.UnixMicro() && native.Value >= 0 && native.Value < 1<<53 &&
				restartEnergyIdentity(labels) == identity && float64(native.Value) == value {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("energy sample %s = %g has no matching post-restart native observation", identity, value)
		}
	}
	return nil
}

// remoteDcgmNamespace returns the standalone DCGM namespace used by e2e.
func remoteDcgmNamespace() string {
	if testContext.dcgmNS != "" {
		return testContext.dcgmNS
	}
	return testContext.namespace + "-dcgm"
}

// remoteDcgmName returns the standalone DCGM deployment and service name used by e2e.
func remoteDcgmName() string {
	if testContext.dcgmName != "" {
		return testContext.dcgmName
	}
	return "dcgm"
}

// remoteDcgmServiceAddress builds the in-cluster DNS address using the configured or default port.
func remoteDcgmServiceAddress() string {
	port := testContext.dcgmPort
	if port == "" {
		port = "5555"
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local:%s", remoteDcgmName(), remoteDcgmNamespace(), port)
}

// remoteDcgmIPv6ServiceReady waits until the E2E-owned Service and its ready
// endpoints are both IPv6-only before the exporter is installed.
func remoteDcgmIPv6ServiceReady(ctx context.Context) error {
	namespace := remoteDcgmNamespace()
	name := remoteDcgmName()
	serviceOutput, err := runKubectlAllowOutput(ctx, "-n", namespace, "get", "service", name, "-o", "json")
	if err != nil {
		return fmt.Errorf("read standalone DCGM Service: %w: %s", err, strings.TrimSpace(serviceOutput))
	}
	if err := validateRemoteDcgmIPv6Service([]byte(serviceOutput)); err != nil {
		return err
	}

	endpointOutput, err := runKubectlAllowOutput(ctx, "-n", namespace, "get", "endpointslice", "-l", "kubernetes.io/service-name="+name, "-o", "json")
	if err != nil {
		return fmt.Errorf("read standalone DCGM EndpointSlices: %w: %s", err, strings.TrimSpace(endpointOutput))
	}
	return validateRemoteDcgmIPv6Endpoints([]byte(endpointOutput))
}
