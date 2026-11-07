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
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const (
	hpcJobMappingConfigMapName = "hpc-job-mapping"
	hpcJobMappingDir           = "/tmp/dcgm-exporter-hpc-jobs"
	hpcJobName                 = "job-e2e"

	debugDumpReaderPodName  = "debug-dump-reader"
	debugDumpCleanupTimeout = 30 * time.Second
)

// exporterArguments formats the default exporter arguments plus args as a Helm values override.
func exporterArguments(args ...string) string {
	return "arguments=" + helmArgumentList(append(defaultExporterArguments(), args...))
}

// hpcJobMappingJSONValues copies the projected inputs into regular mapping files.
func hpcJobMappingJSONValues() []string {
	return []string{
		`extraConfigMapVolumes=[{"name":"hpc-job-input","configMap":{"name":"hpc-job-mapping"}},{"name":"hpc-job-mapping","emptyDir":{}}]`,
		fmt.Sprintf(`extraVolumeMounts=[{"name":"hpc-job-mapping","mountPath":%q}]`, hpcJobMappingDir),
		fmt.Sprintf(`initContainers=[{"name":"copy-hpc-jobs","image":%q,"command":["sh","-ec","cp -L /input/[0-9]* /jobs/"],"securityContext":{"runAsUser":0,"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}},"volumeMounts":[{"name":"hpc-job-input","mountPath":"/input","readOnly":true},{"name":"hpc-job-mapping","mountPath":"/jobs"}]}]`, requiredBusyBoxImage()),
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
	var dcgmExpPod *corev1.Pod
	if workloadLabels != nil {
		workload := shouldCreateWorkloadPod(ctx, kubeClient, workloadLabels)
		dcgmExpPod = shouldFindReadyExporterPodForWorkload(ctx, kubeClient, workload)
	} else {
		dcgmExpPod = shouldFindExporterPod(ctx, kubeClient, dcgmExporterPodLabels)
		shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
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

// shouldReinstallExporterAndReadMetrics removes the exporter of the previous
// step, installs one with values and jsonValues, and waits until validate
// accepts a scrape of it.
func shouldReinstallExporterAndReadMetrics(ctx context.Context, values, jsonValues []string, validate func([]byte) error) {
	cleanupTestContext(ctx, kubeClient, helmClient)
	_, _ = shouldInstallExporterAndReadMetrics(ctx, values, jsonValues, testRunLabels, validate)
}

// shouldHTTPRequestPodPort requests the exporter metrics endpoint through a pod port-forward.
func shouldHTTPRequestPodPort(ctx context.Context, pod *corev1.Pod, useTLS bool, username string, password string) (int, []byte) {
	return shouldHTTPRequestPodPortPath(ctx, pod, useTLS, username, password, "metrics")
}

// shouldHTTPRequestPodPortPath requests an arbitrary exporter path through a
// pod port-forward and fails the spec on any transport error.
func shouldHTTPRequestPodPortPath(
	ctx context.Context,
	pod *corev1.Pod,
	useTLS bool,
	username string,
	password string,
	path string,
) (int, []byte) {
	status, body, err := requestPodPortPath(ctx, pod, useTLS, username, password, path)
	Expect(err).NotTo(HaveOccurred())
	return status, body
}

// requestPodPortPath requests an exporter path through a pod port-forward and
// returns the status and body, or the error that stopped the request, so a
// retrying caller can try again.
func requestPodPortPath(
	ctx context.Context,
	pod *corev1.Pod,
	useTLS bool,
	username string,
	password string,
	path string,
) (int, []byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	kubeClient.ErrWriter = GinkgoWriter
	kubeClient.OutWriter = GinkgoWriter
	localPort, err := kubeClient.PortForward(ctx, pod.Namespace, pod.Name, dcgmExporterPort)
	if err != nil {
		return 0, nil, fmt.Errorf("port-forward to pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, localPortForwardURL(scheme, localPort, path), http.NoBody)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
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
	if err != nil {
		return 0, nil, fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read response of %s: %w", path, err)
	}
	return resp.StatusCode, body, nil
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

// shouldRequireExactlyOneE2ENode returns the only node selected for an isolated host-path test.
func shouldRequireExactlyOneE2ENode(ctx context.Context) string {
	nodeSelector := getE2ENodeSelector()
	Expect(nodeSelector).NotTo(BeEmpty(),
		"debug-dump tests require -node-selector-key and -node-selector-value")

	nodes, err := kubeClient.GetNodesByLabel(ctx, nodeSelector)
	Expect(err).NotTo(HaveOccurred())

	nodeName, err := singleNodeName(nodes)
	Expect(err).NotTo(HaveOccurred())
	return nodeName
}

// shouldCreateDebugDumpReaderPod creates a helper pod pinned to the debug-dump node.
func shouldCreateDebugDumpReaderPod(ctx context.Context, hostPath, nodeName string) *corev1.Pod {
	quantity, _ := resource.ParseQuantity("64Mi")
	hostPathType := corev1.HostPathDirectoryOrCreate
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
			NodeName:      nodeName,
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
						{Name: "debug-dumps", MountPath: debugDumpReaderMount},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "debug-dumps",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: hostPath,
							Type: &hostPathType,
						},
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

// debugDumpFiles lists matching debug dumps for one validated test-run directory.
func debugDumpFiles(ctx context.Context, readerPod *corev1.Pod, debugDumpDirectory string, pattern string) ([]string, error) {
	readerDirectory, err := debugDumpReaderDirectory(debugDumpDirectory, runID.String())
	if err != nil {
		return nil, err
	}

	stdout, stderr, err := kubeClient.ExecutePodCommand(ctx,
		readerPod.Namespace,
		readerPod.Name,
		"reader",
		[]string{"find", readerDirectory, "-type", "f", "-name", pattern, "-print"},
	)
	if err != nil {
		return nil, fmt.Errorf("list debug dumps: %w: %s", err, stderr)
	}
	return strings.Fields(stdout), nil
}

// shouldPrepareDebugDumpDirectory creates an empty directory owned by this test run.
func shouldPrepareDebugDumpDirectory(ctx context.Context, readerPod *corev1.Pod, debugDumpDirectory string) {
	readerDirectory, err := debugDumpReaderDirectory(debugDumpDirectory, runID.String())
	Expect(err).NotTo(HaveOccurred())

	_, stderr, err := kubeClient.ExecutePodCommand(ctx,
		readerPod.Namespace,
		readerPod.Name,
		"reader",
		[]string{"mkdir", "-p", readerDirectory},
	)
	Expect(err).NotTo(HaveOccurred(), stderr)

	files, err := debugDumpFiles(ctx, readerPod, debugDumpDirectory, "*")
	Expect(err).NotTo(HaveOccurred())
	Expect(files).To(BeEmpty(), "debug dump directory must start empty")
}

// shouldRemoveDebugDumpDirectory deletes only the run-owned debug dump directory.
func shouldRemoveDebugDumpDirectory(ctx context.Context, readerPod *corev1.Pod, debugDumpDirectory string) {
	readerDirectory, err := debugDumpReaderDirectory(debugDumpDirectory, runID.String())
	Expect(err).NotTo(HaveOccurred())

	_, stderr, err := kubeClient.ExecutePodCommand(ctx,
		readerPod.Namespace,
		readerPod.Name,
		"reader",
		[]string{"rm", "-rf", readerDirectory},
	)
	Expect(err).NotTo(HaveOccurred(), stderr)

	_, stderr, err = kubeClient.ExecutePodCommand(ctx,
		readerPod.Namespace,
		readerPod.Name,
		"reader",
		[]string{"test", "!", "-e", readerDirectory},
	)
	Expect(err).NotTo(HaveOccurred(), stderr)
}

// requiredBusyBoxImage returns the configured probe image or fails with its setup requirement.
func requiredBusyBoxImage() string {
	Expect(testContext.busyboxImage).NotTo(BeEmpty(), "E2E_BUSYBOX_IMAGE or -busybox-image is required")
	return testContext.busyboxImage
}

// configJSON converts an exporter configuration document to the JSON that --set-json
// takes for the chart's `config` value.
func configJSON(document string) string {
	converted, err := yaml.YAMLToJSON([]byte(document))
	Expect(err).NotTo(HaveOccurred())
	return string(converted)
}
