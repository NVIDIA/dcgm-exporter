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
	"errors"
	"fmt"
	"os"
	"time"

	helm "github.com/mittwald/go-helm-client"
	helmValues "github.com/mittwald/go-helm-client/values"
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

// Install deploys the helm chart
func (c *HelmClient) Install(ctx context.Context, chartOpts HelmChartOptions, valuesOptions ...HelmChartValueOption) (string, error) {
	values := helmValues.Options{}

	for _, valueOption := range valuesOptions {
		valueOption(&values)
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

	return res.Name, err
}

func (c *HelmClient) Uninstall(releaseName string) error {
	return c.client.UninstallReleaseByName(releaseName)
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
