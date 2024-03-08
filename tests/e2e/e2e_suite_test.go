//go:build e2e

/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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
package e2e

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"testing"

	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"

	"github.com/NVIDIA/dcgm-exporter/tests/e2e/internal/framework"
)

const (
	podLabel       = "pod"
	namespaceLabel = "namespace"
	containerLabel = "container"

	dcgmExporterPort = 9400

	dcgmExporterPodNameLabel      = "app.kubernetes.io/name"
	dcgmExporterPodNameLabelValue = "dcgm-exporter"

	workloadPodName       = "cuda-vector-add"
	workloadContainerName = "cuda-vector-add"
	workloadImage         = "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda11.7.1-ubuntu20.04"
)

var expectedLabels = []string{podLabel, namespaceLabel, containerLabel}

type suiteConfig struct {
	namespace       string
	kubeconfig      string
	chart           string
	imageRepository string
	imageTag        string
}

type Suite struct {
	suite.Suite
	suiteConfig
	ctx             context.Context
	k8SClient       *framework.KubeClient
	k8sConfig       *restclient.Config
	helmClient      *framework.HelmClient
	helmReleaseName string
	setupDone       bool
	labels          map[string]string
	workloadPod     *corev1.Pod
	dcgmExpPod      *corev1.Pod
}

func NewSuite() *Suite {
	if suiteCfg.kubeconfig == "" {
		log.Warning("kubeconfig parameter is empty. Defaulting to ~/.kube/config")
	}

	if len(suiteCfg.chart) == 0 {
		log.Error("chart parameter is empty")
		flag.Usage()
		os.Exit(1)
	}

	var err error

	log.Info(suiteCfg.kubeconfig)

	suiteCfg.kubeconfig, err = framework.ResolvePath(suiteCfg.kubeconfig)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := os.Stat(suiteCfg.kubeconfig); os.IsNotExist(err) {
		log.Fatalf("kubeconfig file does not exist: %s", suiteCfg.kubeconfig)
	}

	// Init out-of-cluster K8S client
	k8sConfig, err := clientcmd.BuildConfigFromFlags("", suiteCfg.kubeconfig)
	if err != nil {
		log.Fatalf("unable to load kubeconfig from %s; err: %s", suiteCfg.kubeconfig, err)
	}

	k8sClientset, err := clientset.NewForConfig(k8sConfig)
	if err != nil {
		log.Fatalf("cannot create k8s client: %s", err)
	}

	helmClient, err := framework.NewHelmClient(
		framework.HelmWithNamespace(suiteCfg.namespace),
		framework.HelmWithKubeConfig(k8sConfig),
		framework.HelmWithChart(suiteCfg.chart),
	)
	if err != nil {
		log.Fatalf("cannot create helm client: %s", err)
	}

	return &Suite{
		ctx:         context.Background(),
		k8SClient:   framework.NewKubeClient(k8sClientset),
		k8sConfig:   k8sConfig,
		helmClient:  helmClient,
		suiteConfig: suiteCfg,
	}
}

func (s *Suite) SetupSuite() {
	defer func() {
		// This is only run if SetupSuite fails or panics, otherwise TearDownSuite will call it
		if !s.setupDone {
			s.TearDownSuite()
		}
	}()
	s.T().Log("Starting E2E test setup...")

	s.labels = map[string]string{
		"e2eRunID": runID.String(),
	}

	s.T().Logf("Creating namespace: %s...", s.namespace)

	_, err := s.k8SClient.CreateNamespace(s.ctx, s.namespace, s.labels)
	s.Require().NoError(err, "Failed to create namespace")

	s.T().Logf("Namespace: %q created", s.namespace)

	s.T().Logf("Installing the helm chart: %s", s.chart)

	values := []string{
		fmt.Sprintf("serviceMonitor.enabled=%v", false),
	}

	if s.imageRepository != "" {
		values = append(values, fmt.Sprintf("image.repository=%s", s.imageRepository))
	}
	if s.imageTag != "" {
		values = append(values, fmt.Sprintf("image.tag=%s", s.imageTag))
	}

	s.helmReleaseName, err = s.helmClient.Install(s.ctx, values, framework.HelmChartOptions{
		CleanupOnFail: true,
		GenerateName:  true,
		Timeout:       5 * time.Minute,
		Wait:          true,
		DryRun:        false,
	})

	s.Require().NoError(err, "Cannot install helm chart: %s", s.chart)

	s.T().Logf("Helm chart: %q installed", s.chart)

	s.T().Log("Test setup ready.")

	s.setupDone = true
}

func (s *Suite) TearDownSuite() {

	var err error

	s.T().Log("Starting tear down E2E test setup...")

	if s.workloadPod != nil {
		s.T().Logf("Starting delete of the workload pod: %s...", s.workloadPod.Name)

		err = s.k8SClient.DeletePod(s.ctx, s.namespace, s.workloadPod.Name)
		s.Assert().NoErrorf(err, "Failed to delete pod: %s", s.workloadPod.Name)

		if err == nil {
			s.T().Logf("Workload pod: %s is deleted.", s.workloadPod.Name)
		}
	}

	s.T().Logf("Starting uninstall of the helm chart: %s...", s.chart)

	err = s.helmClient.Uninstall(s.helmReleaseName)
	s.Assert().NoError(err, "Failed to uninstall helm chart")

	if err == nil {
		s.T().Logf("The helm chart: %s is uninstalled", s.chart)
	}

	err = s.helmClient.Cleanup()

	s.Assert().NoError(err, "Failed to clean up directories used by helm client")

	s.T().Logf("Deleting namespace: %s...", s.namespace)
	err = s.k8SClient.DeleteNamespace(s.ctx, s.namespace)
	s.Assert().NoError(err, "Cannot delete namespace")
	if err == nil {
		s.T().Logf("Namespace: %q deleted", s.namespace)
	}

	s.T().Log("Test setup teardown completed.")
}

func (s *Suite) TestDCGMExporter() {
	s.DCGMExporterPrechecks()

	s.T().Run("Create workload pod", func(t *testing.T) {
		t.Log("Creating a workload pod...")

		var err error

		s.workloadPod, err = s.k8SClient.CreatePod(s.ctx,
			s.namespace,
			s.labels,
			workloadPodName,
			workloadContainerName,
			workloadImage,
		)

		require.NoError(t, err, "Cannot create workload pod")

		require.Eventuallyf(t, func() bool {
			isReady, err := s.k8SClient.CheckPodCondition(s.ctx, s.namespace, s.workloadPod.Name, corev1.PodScheduled)
			assert.NoErrorf(t, err, "Cannot get pod status: %v", err)
			return isReady
		}, 15*time.Minute, 5*time.Second, "Failed to create pod: %s", s.workloadPod.Name)

		t.Log("The workload was created.")
	})

	s.T().Run("Verify metrics", func(t *testing.T) {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			metrics, err := s.k8SClient.DoHttpRequest(s.ctx,
				s.namespace,
				s.dcgmExpPod.Name,
				dcgmExporterPort,
				"metrics")
			assert.NoError(c, err, "Failed to make request to the /metrics endpoint")
			assert.NotEmpty(c, metrics)

			var parser expfmt.TextParser
			metricFamilies, err := parser.TextToMetricFamilies(bytes.NewReader(metrics))
			require.NoError(c, err)
			assert.Greater(c, len(metricFamilies), 0)

			for _, metricFamily := range metricFamilies {
				assert.NotNil(c, metricFamily)

				metrics := metricFamily.GetMetric()
				assert.NotNil(c, metrics)

				// Each metric must have namespace, pod and container labels
				for _, metric := range metrics {
					var actualLabels []string
					for _, label := range metric.Label {
						labelName := ptr.Deref(label.Name, "")
						if slices.Contains(expectedLabels, labelName) {
							actualLabels = append(actualLabels, labelName)
							assert.NotNil(c, label.Value)
							assert.NotEmptyf(c,
								ptr.Deref(label.Value, ""),
								"The %s metric contains a label named %q label with empty value.",
								ptr.Deref(metricFamily.Name, ""),
								labelName,
							)
						}
					}
					assert.Equalf(c, len(expectedLabels), len(actualLabels),
						"Metric %s doesn't contains expected labels: %v, actual labels: %v",
						ptr.Deref(metricFamily.Name, ""), expectedLabels, metric.Label)
				}
			}
		}, 60*time.Second, 30*time.Second)
	})
}

func (s *Suite) DCGMExporterPrechecks() {
	s.T().Run("Checking pre-requisite: dcgm-exporter is up and running",
		func(t *testing.T) {
			t.Log("Checking the dcgm-exporter pod....")
			t.Log("It can take up to the 15 minutes.")
			labelMap := map[string]string{dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue}

			var pod *corev1.Pod

			require.Eventuallyf(t, func() bool {
				pods, err := s.k8SClient.GetPodsByLabel(s.ctx, s.namespace, labelMap)
				if err != nil {
					log.Warnf("Error retrieving pods: %v", err)
					return false
				}

				require.Lenf(t, pods, 1, "Expected a one pod only")
				pod = &pods[0]

				return true
			}, 15*time.Minute, 5*time.Second, "The pod was not created")

			require.NotNil(t, pod, "Nil value is not expected after pod created")

			require.Eventuallyf(t, func() bool {
				isReady, err := s.k8SClient.CheckPodCondition(s.ctx, s.namespace, pod.Name, corev1.PodReady)
				assert.NoErrorf(t, err, "Cannot get pod status: %v", err)

				s.dcgmExpPod = pod

				return isReady
			}, 15*time.Minute, 5*time.Second, "The %s pod is not running", pod.Name)

			t.Log("The dcgm-exporter pod is running")
		},
	)
}
