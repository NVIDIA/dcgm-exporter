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

package framework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	helm "github.com/mittwald/go-helm-client"
	helmValues "github.com/mittwald/go-helm-client/values"
	ginkgo "github.com/onsi/ginkgo/v2"
	"helm.sh/helm/v3/pkg/storage/driver"
	restclient "k8s.io/client-go/rest"
)

// HelmClientOption is a function that can be used to set the fields of the helm Client
type HelmClientOption func(client *HelmClient)

// HelmClient is the helm client, that allows to work with helm packages
type HelmClient struct {
	client           helm.Client
	chart            string
	namespace        string
	k8sRestConfig    *restclient.Config
	repositoryCache  string
	repositoryConfig string
}

// NewHelmClient creates a new helm client
func NewHelmClient(opts ...HelmClientOption) (*HelmClient, error) {
	client := &HelmClient{}
	for _, o := range opts {
		o(client)
	}

	var err error
	client.repositoryCache, err = os.MkdirTemp("", ".helmcache")
	if err != nil {
		return nil, err
	}

	client.repositoryConfig, err = os.MkdirTemp("", ".helmrepo")
	if err != nil {
		return nil, err
	}

	restConfOptions := &helm.RestConfClientOptions{
		Options: &helm.Options{
			Namespace:        client.namespace,
			RepositoryConfig: client.repositoryConfig,
			RepositoryCache:  client.repositoryCache,
			DebugLog: func(format string, v ...interface{}) {
				// suppress helm chart client debug log
			},
		},
		RestConfig: client.k8sRestConfig,
	}

	helmClient, err := helm.NewClientFromRestConf(restConfOptions)
	if err != nil {
		return nil, err
	}

	client.client = helmClient

	return client, nil
}

// HelmWithKubeConfig sets a kubeconfig value in the HelmClient struct
func HelmWithKubeConfig(kubeconfig *restclient.Config) HelmClientOption {
	return func(c *HelmClient) {
		c.k8sRestConfig = kubeconfig
	}
}

// HelmWithNamespace sets a namespace value in the HelmClient struct
func HelmWithNamespace(namespace string) HelmClientOption {
	return func(c *HelmClient) {
		c.namespace = namespace
	}
}

// HelmWithChart sets a chart value in the HelmClient struct
func HelmWithChart(chart string) HelmClientOption {
	return func(c *HelmClient) {
		c.chart = chart
	}
}

type HelmChartOptions struct {
	CleanupOnFail bool
	GenerateName  bool
	ReleaseName   string
	Timeout       time.Duration
	Wait          bool
	DryRun        bool
}

type HelmChartValueOption func(*helmValues.Options)

func WithValues(values ...string) HelmChartValueOption {
	return func(o *helmValues.Options) {
		o.Values = values
	}
}

func WithJSONValues(values ...string) HelmChartValueOption {
	return func(o *helmValues.Options) {
		o.JSONValues = values
	}
}

// addDebugArgumentIfNotPresent adds --debug to arguments if it's not already present
func addDebugArgumentIfNotPresent(values *helmValues.Options) {
	// Look for arguments in Values
	for i, value := range values.Values {
		if len(value) > 10 && value[:10] == "arguments=" {
			// Check if --debug is already present
			if !containsDebug(value) {
				fmt.Fprintln(ginkgo.GinkgoWriter, "Before modification:", value)

				// Parse the arguments format
				// Expected format: arguments={-f=/etc/dcgm-exporter/default-counters.csv}
				// We need to convert this to: arguments={-f=/etc/dcgm-exporter/default-counters.csv, --debug}

				// Extract the content between curly braces
				start := strings.Index(value, "{")
				end := strings.LastIndex(value, "}")

				if start != -1 && end != -1 && end > start {
					// Extract the arguments content
					argsContent := value[start+1 : end]

					// Add --debug to the arguments content with comma separator
					if argsContent != "" {
						argsContent += ", --debug"
					} else {
						argsContent = "--debug"
					}

					// Reconstruct the value in the original format
					values.Values[i] = "arguments={" + argsContent + "}"
					fmt.Fprintln(ginkgo.GinkgoWriter, "After modification:", values.Values[i])
				}
			}
			return
		}
	}

	// Also check for array format arguments[0]=...
	debugAdded := false
	for _, value := range values.Values {
		if strings.HasPrefix(value, "arguments[") {
			// Check if this value already contains --debug
			if containsDebug(value) {
				debugAdded = true
				continue
			}

			// Find the highest index to add --debug after it
			highestIndex := -1
			for _, v := range values.Values {
				if strings.HasPrefix(v, "arguments[") {
					// Extract index from arguments[index]=value
					parts := strings.SplitN(v, "=", 2)
					if len(parts) == 2 {
						indexPart := strings.TrimPrefix(parts[0], "arguments[")
						indexPart = strings.TrimSuffix(indexPart, "]")
						if index, err := strconv.Atoi(indexPart); err == nil && index > highestIndex {
							highestIndex = index
						}
					}
				}
			}

			if !debugAdded {
				// Add --debug as the next index
				debugIndex := highestIndex + 1
				values.Values = append(values.Values, fmt.Sprintf("arguments[%d]=--debug", debugIndex))
				debugAdded = true
				fmt.Fprintln(ginkgo.GinkgoWriter, "Added --debug as arguments[", debugIndex, "]=--debug")
			}
		}
	}

	// Fallback: if no arguments keys or indexed entries were found, add arguments[0]=--debug
	if !debugAdded {
		values.Values = append(values.Values, "arguments[0]=--debug")
		fmt.Fprintln(ginkgo.GinkgoWriter, "Added --debug as arguments[0]=--debug (fallback)")
	}
}

// containsDebug checks if the arguments string contains --debug
func containsDebug(arguments string) bool {
	return strings.Contains(arguments, "--debug")
}

// Install deploys the helm chart
func (c *HelmClient) Install(ctx context.Context, chartOpts HelmChartOptions, valuesOptions ...HelmChartValueOption) (string, error) {
	values := helmValues.Options{}

	for _, valueOption := range valuesOptions {
		valueOption(&values)
	}

	// Add --debug argument if not present
	addDebugArgumentIfNotPresent(&values)

	// Print the chart values being used
	fmt.Fprintln(ginkgo.GinkgoWriter, "Chart values being used:")
	fmt.Fprintln(ginkgo.GinkgoWriter, "  Chart:", c.chart)
	fmt.Fprintln(ginkgo.GinkgoWriter, "  Namespace:", c.namespace)
	fmt.Fprintln(ginkgo.GinkgoWriter, "  GenerateName:", chartOpts.GenerateName)
	fmt.Fprintln(ginkgo.GinkgoWriter, "  ReleaseName:", chartOpts.ReleaseName)
	fmt.Fprintln(ginkgo.GinkgoWriter, "  Wait:", chartOpts.Wait)
	fmt.Fprintln(ginkgo.GinkgoWriter, "  Timeout:", chartOpts.Timeout)
	fmt.Fprintln(ginkgo.GinkgoWriter, "  CleanupOnFail:", chartOpts.CleanupOnFail)
	fmt.Fprintln(ginkgo.GinkgoWriter, "  DryRun:", chartOpts.DryRun)

	if len(values.Values) > 0 {
		fmt.Fprintln(ginkgo.GinkgoWriter, "  Values:")
		for _, value := range values.Values {
			fmt.Fprintln(ginkgo.GinkgoWriter, "    ", value)
		}
	}

	if len(values.JSONValues) > 0 {
		fmt.Fprintln(ginkgo.GinkgoWriter, "  JSONValues:")
		for _, value := range values.JSONValues {
			fmt.Fprintln(ginkgo.GinkgoWriter, "    ", value)
		}
	}

	chartSpec := helm.ChartSpec{
		ChartName:     c.chart,
		Namespace:     c.namespace,
		GenerateName:  chartOpts.GenerateName,
		Wait:          chartOpts.Wait,
		Timeout:       chartOpts.Timeout,
		CleanupOnFail: chartOpts.CleanupOnFail,
		DryRun:        chartOpts.DryRun,
		ValuesOptions: values,
	}

	if !chartOpts.GenerateName {
		if len(chartOpts.ReleaseName) == 0 {
			return "", errors.New("release name must be provided the GenerateName chart option is unset")
		}
		chartSpec.ReleaseName = chartOpts.ReleaseName
	}

	res, err := c.client.InstallChart(ctx, &chartSpec, nil)
	if err != nil {
		return "", fmt.Errorf("error installing the chart; err: %w", err)
	}

	// Print the release values after successful installation
	if err := c.GetReleaseValues(res.Name); err != nil {
		fmt.Fprintln(ginkgo.GinkgoWriter, "Warning: Failed to get release values after installation:", err)
	}

	return res.Name, err
}

func (c *HelmClient) Uninstall(releaseName string) error {
	err := c.client.UninstallReleaseByName(releaseName)
	if err != nil {
		// Check if the error indicates the release doesn't exist
		// This makes the uninstall operation idempotent
		if errors.Is(err, driver.ErrReleaseNotFound) {
			// Release doesn't exist, which is fine for cleanup operations
			return nil
		}
		return err
	}
	return nil
}

// GetReleaseValues retrieves and prints the values stored by Helm for a specific release
func (c *HelmClient) GetReleaseValues(releaseName string) error {
	// Get the release values from Helm (allValues=true to get all values, not just user-provided ones)
	values, err := c.client.GetReleaseValues(releaseName, true)
	if err != nil {
		return fmt.Errorf("failed to get release values for %s: %w", releaseName, err)
	}

	// Print the values in a readable format
	fmt.Fprintln(ginkgo.GinkgoWriter, "Helm release values for:", releaseName)
	fmt.Fprintln(ginkgo.GinkgoWriter, "======================================")

	// Convert values to JSON for better readability
	jsonData, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		// Fallback to printing as string if JSON conversion fails
		fmt.Fprintln(ginkgo.GinkgoWriter, "Values (raw):", values)
		return nil
	}

	fmt.Fprintln(ginkgo.GinkgoWriter, string(jsonData))
	fmt.Fprintln(ginkgo.GinkgoWriter, "======================================")

	return nil
}

func (c *HelmClient) Cleanup() error {
	err := os.RemoveAll(c.repositoryCache)
	if err != nil {
		return fmt.Errorf("failed to delete directory %s; err: %w", c.repositoryCache, err)
	}

	err = os.RemoveAll(c.repositoryConfig)
	if err != nil {
		return fmt.Errorf("failed to delete directory %s; err: %w", c.repositoryConfig, err)
	}

	return err
}
