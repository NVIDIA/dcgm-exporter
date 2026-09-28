//go:build e2e

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
	"os/exec"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

const gpuOperatorNamespace = "gpu-operator"

var _ = Describe("GPU Operator integration", Ordered, func() {
	Context("GPU Operator chart resources are installed", Ordered, Label("gpuOperatorChart"), func() {
		It("should expose a ClusterPolicy", func(ctx context.Context) {
			output := shouldRunKubectl(ctx, "get", "clusterpolicy", "-o", "name")
			Expect(output).To(ContainSubstring("clusterpolicy"))
		})
	})

	Context("GPU Operator exporter is running", Ordered, Label("gpuOperatorExporter"), func() {
		It("should run GPU Operator managed pods", func(ctx context.Context) {
			output := shouldRunKubectl(ctx, "-n", gpuOperatorNamespace, "get", "pods", "-o", "wide")
			Expect(output).To(SatisfyAny(
				ContainSubstring("dcgm"),
				ContainSubstring("gpu-operator"),
				ContainSubstring("nvidia"),
			))
		})

		It("should run the dcgm-exporter image under test", func(ctx context.Context) {
			expectedImage, err := gpuOperatorExpectedExporterImage(testContext.gpuOperatorImage, testContext.exporterImage)
			Expect(err).NotTo(HaveOccurred())

			images := shouldFindGPUOperatorExporterImages(ctx)
			Expect(images).To(ContainElement(expectedImage))
		})
	})

	Context("GPU Operator shared GPU integration", Ordered, Label("gpuOperatorSharedGpu"), func() {
		It("should advertise shared GPU or time-slicing labels", func(ctx context.Context) {
			output := shouldRunKubectl(ctx, "get", "nodes", "-o", "yaml")
			Expect(strings.ToLower(output)).To(SatisfyAny(
				ContainSubstring("replicas"),
				ContainSubstring("shared"),
				ContainSubstring("timeslicing"),
			))
		})
	})

	Context("GPU Operator MIG integration", Ordered, Label("gpuOperatorMig"), func() {
		It("should advertise MIG resources or MIG labels", func(ctx context.Context) {
			output := shouldRunKubectl(ctx, "get", "nodes", "-o", "yaml")
			Expect(strings.ToLower(output)).To(ContainSubstring("mig"))
		})
	})

	Context("GPU Operator DRA integration", Ordered, Label("gpuOperatorDRA"), func() {
		It("should expose DRA resources", func(ctx context.Context) {
			output := shouldRunKubectl(ctx, "get", "deviceclass,resourceslices", "-A", "-o", "name")
			Expect(output).To(SatisfyAny(
				ContainSubstring("deviceclass"),
				ContainSubstring("resourceslice"),
			))
		})
	})

	Context("GPU Operator IPv6 exporter integration", Ordered, Label("gpuOperatorIPv6"), func() {
		It("should expose operator-managed exporter metrics over IPv6 service networking", func(ctx context.Context) {
			service := shouldFindGPUOperatorExporterService(ctx)
			ipv6 := shouldFindIPv6ClusterIP(service)
			port := shouldFindExporterServicePort(service)
			probePod := shouldCreateIPv6ProbePod(ctx)
			defer shouldDeleteProbePod(ctx, probePod)

			metrics := shouldScrapeIPv6ServiceFromPod(ctx, probePod, ipv6, port)
			Expect(defaultMetricsContractError(metrics, false)).To(Succeed())
		})
	})
})

func TestGPUOperatorExpectedExporterImage(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		exporter string
		want     string
	}{
		{name: "explicit", explicit: "override/image:tag", exporter: "repo/image:tag", want: "override/image:tag"},
		{name: "exporter", exporter: "repo/image:tag", want: "repo/image:tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gpuOperatorExpectedExporterImage(tt.explicit, tt.exporter)
			if err != nil {
				t.Fatalf("gpuOperatorExpectedExporterImage() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("gpuOperatorExpectedExporterImage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func gpuOperatorExpectedExporterImage(explicit, exporter string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if exporter == "" {
		return "", fmt.Errorf("GPU Operator image check requires -exporter-image")
	}
	return exporter, nil
}

// shouldFindGPUOperatorExporterService finds the operator-managed dcgm-exporter Service.
func shouldFindGPUOperatorExporterService(ctx context.Context) corev1.Service {
	output := shouldRunKubectl(ctx, "-n", gpuOperatorNamespace, "get", "svc", "-o", "json")
	var services corev1.ServiceList
	Expect(json.Unmarshal([]byte(output), &services)).To(Succeed())

	var fallback corev1.Service
	for _, service := range services.Items {
		if !strings.Contains(service.Name, "dcgm") || !strings.Contains(service.Name, "exporter") {
			continue
		}
		if shouldFindExporterServicePort(service) != 0 {
			if serviceHasIPv6ClusterIP(service) {
				return service
			}
			fallback = service
		}
	}
	if fallback.Name != "" {
		return fallback
	}

	Fail(fmt.Sprintf("no GPU Operator dcgm-exporter service exposing port %d found in %s", dcgmExporterPort, gpuOperatorNamespace))
	return corev1.Service{}
}

// shouldFindGPUOperatorExporterImages returns images from operator-managed exporter pods.
func shouldFindGPUOperatorExporterImages(ctx context.Context) []string {
	output := shouldRunKubectl(ctx, "-n", gpuOperatorNamespace, "get", "pods", "-o", "json")
	var pods corev1.PodList
	Expect(json.Unmarshal([]byte(output), &pods)).To(Succeed())

	var images []string
	for _, pod := range pods.Items {
		if !strings.Contains(pod.Name, "dcgm") || !strings.Contains(pod.Name, "exporter") {
			continue
		}
		for _, container := range pod.Spec.Containers {
			images = append(images, container.Image)
		}
	}

	Expect(images).NotTo(BeEmpty(), "no GPU Operator dcgm-exporter pod images found")
	return images
}

// shouldFindExporterServicePort finds the metrics port on an exporter Service.
func shouldFindExporterServicePort(service corev1.Service) int32 {
	for _, port := range service.Spec.Ports {
		if uint(port.Port) == dcgmExporterPort {
			return port.Port
		}
	}
	return 0
}

// shouldFindIPv6ClusterIP finds an IPv6 ClusterIP on an exporter Service.
func shouldFindIPv6ClusterIP(service corev1.Service) string {
	for index, family := range service.Spec.IPFamilies {
		if family == corev1.IPv6Protocol && index < len(service.Spec.ClusterIPs) {
			return service.Spec.ClusterIPs[index]
		}
	}
	for _, ip := range service.Spec.ClusterIPs {
		if strings.Contains(ip, ":") {
			return ip
		}
	}

	Fail(fmt.Sprintf("GPU Operator exporter service %s/%s has no IPv6 ClusterIP; families=%v clusterIPs=%v",
		service.Namespace, service.Name, service.Spec.IPFamilies, service.Spec.ClusterIPs))
	return ""
}

func serviceHasIPv6ClusterIP(service corev1.Service) bool {
	for _, ip := range service.Spec.ClusterIPs {
		if strings.Contains(ip, ":") {
			return true
		}
	}
	return false
}

// shouldCreateIPv6ProbePod starts a temporary pod for in-cluster IPv6 scraping.
func shouldCreateIPv6ProbePod(ctx context.Context) string {
	name := "gpu-operator-ipv6-probe"
	shouldDeleteProbePod(ctx, name)
	shouldRunKubectl(
		ctx,
		"-n", testContext.namespace,
		"run", name,
		"--image="+requiredBusyBoxImage(),
		"--restart=Never",
		"--command",
		"--", "sh", "-c", "sleep 3600",
	)
	shouldRunKubectl(ctx, "-n", testContext.namespace, "wait", "--for=condition=Ready", "pod/"+name, "--timeout=90s")
	return name
}

// shouldDeleteProbePod removes the temporary IPv6 probe pod if present.
func shouldDeleteProbePod(ctx context.Context, name string) {
	_ = shouldRunKubectlAllowFailure(ctx, "-n", testContext.namespace, "delete", "pod", name, "--ignore-not-found=true", "--wait=false")
}

// shouldScrapeIPv6ServiceFromPod scrapes exporter metrics through an IPv6 ClusterIP.
func shouldScrapeIPv6ServiceFromPod(ctx context.Context, podName, ipv6 string, port int32) []byte {
	command := fmt.Sprintf("wget -qO- -T 10 'http://[%s]:%d/metrics'", ipv6, port)
	var output string
	Eventually(func(g Gomega) {
		var err error
		output, err = runKubectlAllowOutput(ctx, "-n", testContext.namespace, "exec", podName, "--", "sh", "-c", command)
		g.Expect(err).NotTo(HaveOccurred(), output)
		g.Expect(output).NotTo(BeEmpty())
		g.Expect(defaultMetricsContractError([]byte(output), false)).To(Succeed())
	}).WithPolling(pollingIntervalSlow).Within(metricsWaitTimeout).Should(Succeed())
	return []byte(output)
}

// shouldRunKubectl runs kubectl and fails the spec if the command exits non-zero.
func shouldRunKubectl(ctx context.Context, args ...string) string {
	allArgs := append([]string{"--kubeconfig", testContext.kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", allArgs...)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("kubectl %s failed: %s", strings.Join(args, " "), string(out)))
	return string(out)
}

// shouldRunKubectlAllowFailure runs kubectl and returns only the command error.
func shouldRunKubectlAllowFailure(ctx context.Context, args ...string) error {
	_, err := runKubectlAllowOutput(ctx, args...)
	return err
}

// runKubectlAllowOutput runs kubectl and returns combined output with the command error.
func runKubectlAllowOutput(ctx context.Context, args ...string) (string, error) {
	allArgs := append([]string{"--kubeconfig", testContext.kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", allArgs...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// shouldRunKubectlWithInput runs kubectl with stdin and fails the spec on command errors.
func shouldRunKubectlWithInput(ctx context.Context, input string, args ...string) string {
	allArgs := append([]string{"--kubeconfig", testContext.kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", allArgs...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("kubectl %s failed: %s", strings.Join(args, " "), string(out)))
	return string(out)
}
