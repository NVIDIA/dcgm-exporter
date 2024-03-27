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
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/NVIDIA/dcgm-exporter/tests/e2e/internal/framework"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

// VerifyHelmConfigurationWhenHttpBasicAuthEnabled tests helm chart when Http basic authentication is enabled
var VerifyHelmConfigurationWhenHttpBasicAuthEnabled = func(kubeClient *framework.KubeClient,
	helmClient *framework.HelmClient,
	testRunLabels map[string]string,
) bool {
	return Context("and HTTP basic auth is enabled", Label("basicAuth"), func() {
		var (
			helmReleaseName string
			dcgmExpPod      *corev1.Pod
		)

		AfterAll(func(ctx context.Context) {
			shouldUninstallHelmChart(helmClient, helmReleaseName)
		})

		userName := "alice"
		userPassword := "Pa$$w0rd"

		It("should install dcgm-exporter helm chart", func(ctx context.Context) {
			By(fmt.Sprintf("Helm chart installation: %q chart started.",
				testContext.chart))

			values := getDefaultHelmValues()

			var jsonValues []string

			type basicAuth struct {
				Users map[string]string `json:"users"`
			}

			basicAuthValue := basicAuth{
				Users: map[string]string{
					userName: userPassword,
				},
			}
			basicAuthValueJson, err := json.Marshal(basicAuthValue)
			Expect(err).ShouldNot(HaveOccurred())

			jsonValues = append(jsonValues, fmt.Sprintf("basicAuth=%s", string(basicAuthValueJson)))

			helmReleaseName, err = helmClient.Install(ctx, framework.HelmChartOptions{
				CleanupOnFail: true,
				GenerateName:  true,
				Timeout:       5 * time.Minute,
				Wait:          true,
				DryRun:        false,
			}, framework.WithValues(values...), framework.WithJSONValues(jsonValues...))
			Expect(err).ShouldNot(HaveOccurred(), "Helm chart installation: %q chart failed with error err: %v", testContext.chart, err)

			By(fmt.Sprintf("Helm chart installation: %q completed.",
				testContext.chart))
			By(fmt.Sprintf("Helm chart installation: new %q release name.",
				helmReleaseName))
		})

		It("should create dcgm-exporter pod", func(ctx context.Context) {
			dcgmExpPod = shouldCheckIfPodCreated(ctx, kubeClient, dcgmExporterPodLabels)
		})

		It("should ensure that the dcgm-exporter pod is ready", func(ctx context.Context) {
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
		})

		It("should check that the metric endpoint protected by basic HTTP auth", func(ctx context.Context) {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			kubeClient.ErrWriter = GinkgoWriter
			kubeClient.OutWriter = GinkgoWriter
			localPort, err := kubeClient.PortForward(ctx, dcgmExpPod.Namespace, dcgmExpPod.Name, 9400)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(localPort).Should(BeNumerically(">", 0))
			httpClient := &http.Client{
				Timeout: 5 * time.Second,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true,
					},
				},
			}

			By("Ensure that HTTP request returns 401 error when no credentials provided")
			resp, err := httpClient.Get(fmt.Sprintf("http://localhost:%d/metrics", localPort))
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(401))
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("Unauthorized"))

			By("Ensure that HTTP request returns 200 error")
			req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://localhost:%d/metrics", localPort), http.NoBody)
			Expect(err).ShouldNot(HaveOccurred())
			req.SetBasicAuth(userName, userPassword)
			resp, err = httpClient.Do(req)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(200))
			_, err = io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
		})
	})
}
