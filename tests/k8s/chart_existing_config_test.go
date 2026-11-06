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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	// existingConfigMapName is the ConfigMap the scenario creates before the install.
	existingConfigMapName = "e2e-exporter-config"
	// existingConfigDocument is a complete exporter config that differs from the
	// chart's defaults: it lists two fields and turns on the exporter's own
	// go_* metrics.
	existingConfigDocument = `version: 2
metrics:
  - name: gpu
    every: 5s
    fields:
      - name: DCGM_FI_DEV_GPU_TEMP_CELSIUS
        prometheus_type: gauge
        help: GPU temperature (in C).
      - name: DCGM_FI_DEV_FB_USED_BYTES
        prometheus_type: gauge
        help: Framebuffer memory used (in bytes).
outputs:
  prometheus:
    type: prometheus
    schema: dcgm/v1
observability:
  metrics:
    enabled: true
`
)

var _ = Describe("dcgm-exporter chart existing config scenarios", func() {
	Context("DCGM exporter reads a ConfigMap the user manages", Ordered, Label("chartExistingConfig"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
			Expect(kubeClient.DeleteConfigMap(ctx, testContext.namespace, existingConfigMapName)).To(Succeed())
		})

		It("should use the file in configMap.existingName under configMap.key", func(ctx context.Context) {
			Expect(kubeClient.DeleteConfigMap(ctx, testContext.namespace, existingConfigMapName)).To(Succeed())
			_, err := kubeClient.CreateConfigMap(ctx, testContext.namespace, existingConfigMapName,
				map[string]string{existingConfigKey: existingConfigDocument})
			Expect(err).NotTo(HaveOccurred())

			shouldInstallHelmChart(ctx, helmClient, []string{
				"arguments={}",
				"configMap.existingName=" + existingConfigMapName,
				"configMap.key=" + existingConfigKey,
			})
			exporterPod := shouldFindExporterPod(ctx, kubeClient, dcgmExporterPodLabels)
			shouldCheckIfPodIsReady(ctx, kubeClient, exporterPod.Namespace, exporterPod.Name)
			Expect(existingConfigPodError(exporterPod, existingConfigKey)).To(Succeed())

			shouldEventuallyReadMetrics(ctx, exporterPod, existingConfigMetricsError, 2*time.Minute)
		})
	})
})
