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
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const localPortForwardHost = "127.0.0.1"

const (
	hpcJobMappingConfigMapName = "hpc-job-mapping"
	hpcJobMappingDir           = "/tmp/dcgm-exporter-hpc-jobs"
	hpcJobName                 = "job-e2e"

	debugDumpDirectory     = "/tmp/dcgm-exporter-debug"
	debugDumpReaderPodName = "debug-dump-reader"

	e2eDefaultCollectorsArg = "-f=/etc/dcgm-exporter/default-counters.csv"
	e2eCollectIntervalArg   = "--collect-interval=1000"
)

// exporterArguments formats exporter CLI arguments as a Helm values override.
func exporterArguments(args ...string) string {
	allArgs := append([]string{e2eDefaultCollectorsArg, e2eCollectIntervalArg}, args...)
	return fmt.Sprintf("arguments={%s}", strings.Join(allArgs, ","))
}

// hpcJobMappingJSONValues returns Helm JSON values that mount the HPC job mapping fixture.
func hpcJobMappingJSONValues() []string {
	return []string{
		`extraConfigMapVolumes=[{"name":"exporter-metrics-volume","configMap":{"name":"exporter-metrics-config-map","items":[{"key":"metrics","path":"default-counters.csv"}]}},{"name":"hpc-job-mapping","configMap":{"name":"hpc-job-mapping"}}]`,
		fmt.Sprintf(
			`extraVolumeMounts=[{"name":"exporter-metrics-volume","mountPath":"/etc/dcgm-exporter/default-counters.csv","subPath":"default-counters.csv"},{"name":"hpc-job-mapping","mountPath":%q}]`,
			hpcJobMappingDir,
		),
	}
}

// shouldInstallExporterAndReadMetrics installs exporter, optionally creates a workload, and validates metrics.
func shouldInstallExporterAndReadMetrics(
	ctx context.Context,
	values []string,
	jsonValues []string,
	workloadLabels map[string]string,
	validate func([]byte) error,
) (*corev1.Pod, []byte) {
	shouldInstallHelmChartWithJSONValues(ctx, helmClient, values, jsonValues)
	dcgmExpPod := shouldCheckIfPodCreated(ctx, kubeClient, dcgmExporterPodLabels)
	shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
	if workloadLabels != nil {
		shouldCreateWorkloadPod(ctx, kubeClient, workloadLabels)
	}

	var metricsResponse []byte
	if validate == nil {
		shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		return dcgmExpPod, metricsResponse
	}

	Eventually(func(g Gomega) {
		metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		g.Expect(metricsResponse).NotTo(BeEmpty())
		g.Expect(validate(metricsResponse)).To(Succeed())
	}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())

	return dcgmExpPod, metricsResponse
}

// shouldHTTPRequestPodPort requests the exporter metrics endpoint through a pod port-forward.
func shouldHTTPRequestPodPort(ctx context.Context, pod *corev1.Pod, useTLS bool, username string, password string) (int, []byte) {
	return shouldHTTPRequestPodPortPath(ctx, pod, useTLS, username, password, "metrics")
}

// shouldHTTPRequestPodPortPath requests an arbitrary exporter path through a pod port-forward.
func shouldHTTPRequestPodPortPath(
	ctx context.Context,
	pod *corev1.Pod,
	useTLS bool,
	username string,
	password string,
	path string,
) (int, []byte) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	kubeClient.ErrWriter = GinkgoWriter
	kubeClient.OutWriter = GinkgoWriter
	localPort, err := kubeClient.PortForward(ctx, pod.Namespace, pod.Name, dcgmExporterPort)
	Expect(err).NotTo(HaveOccurred())

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, localPortForwardURL(scheme, localPort, path), http.NoBody)
	Expect(err).NotTo(HaveOccurred())
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}

	client := &http.Client{
		Timeout: httpClientTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	return resp.StatusCode, body
}

func localPortForwardURL(scheme string, port int, path string) string {
	return fmt.Sprintf("%s://%s/%s", scheme, net.JoinHostPort(localPortForwardHost, strconv.Itoa(port)), strings.TrimPrefix(path, "/"))
}

// shouldReadServiceMetrics scrapes exporter metrics through the Helm-created Kubernetes Service.
func shouldReadServiceMetrics(ctx context.Context, helmReleaseName string) []byte {
	services, err := kubeClient.GetServicesByLabel(ctx, testContext.namespace, map[string]string{
		dcgmExporterPodNameLabel:     dcgmExporterPodNameLabelValue,
		"app.kubernetes.io/instance": helmReleaseName,
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(services).To(HaveLen(1), "expected one dcgm-exporter service for Helm release %q", helmReleaseName)

	serviceName := services[0].Name
	metricsResponse, err := kubeClient.DoServiceHTTPRequest(ctx, testContext.namespace, serviceName, dcgmExporterPort, "metrics")
	Expect(err).NotTo(HaveOccurred())
	Expect(metricsResponse).NotTo(BeEmpty())
	return metricsResponse
}

// shouldCreateHPCJobMappingConfigMap creates test GPU-to-job mappings for hpc_job labels.
func shouldCreateHPCJobMappingConfigMap(ctx context.Context) {
	_ = kubeClient.DeleteConfigMap(ctx, testContext.namespace, hpcJobMappingConfigMapName)
	data := map[string]string{}
	for gpuID := 0; gpuID < 16; gpuID++ {
		data[fmt.Sprintf("%d", gpuID)] = hpcJobName + "\n"
	}
	_, err := kubeClient.CreateConfigMap(ctx, testContext.namespace, hpcJobMappingConfigMapName, data)
	Expect(err).NotTo(HaveOccurred())
}

// shouldCreateDebugDumpReaderPod creates a helper pod that can read exporter debug dump files.
func shouldCreateDebugDumpReaderPod(ctx context.Context) *corev1.Pod {
	quantity, _ := resource.ParseQuantity("64Mi")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      debugDumpReaderPodName,
			Namespace: testContext.namespace,
			Labels: map[string]string{
				e2eRunIDLabel: runID.String(),
				"app":         debugDumpReaderPodName,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			NodeSelector:  getE2ENodeSelector(),
			Containers: []corev1.Container{
				{
					Name:    "reader",
					Image:   requiredBusyBoxImage(),
					Command: []string{"sleep", "infinity"},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceMemory: quantity},
						Limits:   corev1.ResourceList{corev1.ResourceMemory: quantity},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "debug-dumps", MountPath: "/debug-dumps"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "debug-dumps",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{Path: debugDumpDirectory},
					},
				},
			},
		},
	}

	_ = kubeClient.DeletePod(ctx, testContext.namespace, debugDumpReaderPodName)
	createdPod, err := kubeClient.CreatePodFromSpec(ctx, pod)
	Expect(err).NotTo(HaveOccurred())
	Eventually(func(ctx context.Context) bool {
		running, err := kubeClient.CheckPodStatus(ctx, createdPod.Namespace, createdPod.Name, func(namespace, podName string, status corev1.PodStatus) (bool, error) {
			return status.Phase == corev1.PodRunning, nil
		})
		if err != nil {
			return false
		}
		return running
	}).WithPolling(pollingIntervalNormal).Within(podReadinessTimeout).WithContext(ctx).Should(BeTrue())
	return createdPod
}

func requiredBusyBoxImage() string {
	Expect(testContext.busyboxImage).NotTo(BeEmpty(), "E2E_BUSYBOX_IMAGE or -busybox-image is required")
	return testContext.busyboxImage
}
