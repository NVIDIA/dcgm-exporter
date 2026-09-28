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

package kubeclient

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestGetKubeClientReturnsInClusterConfigErrorOutsideCluster(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	client, err := GetKubeClient()

	assert.Nil(t, client)
	assert.Error(t, err)
}

func restoreKubeClientSeams(t *testing.T) {
	t.Helper()
	prevInClusterConfig := inClusterConfigFunc
	prevNewClient := newKubernetesClientFunc
	t.Cleanup(func() {
		inClusterConfigFunc = prevInClusterConfig
		newKubernetesClientFunc = prevNewClient
	})
}

func TestGetKubeClientWithInjectedConfig(t *testing.T) {
	restoreKubeClientSeams(t)
	wantClient := fake.NewClientset()
	inClusterConfigFunc = func() (*rest.Config, error) {
		return &rest.Config{Host: "https://kubernetes.default.svc"}, nil
	}
	newKubernetesClientFunc = func(*rest.Config) (kubernetes.Interface, error) {
		return wantClient, nil
	}

	client, err := GetKubeClient()

	require.NoError(t, err)
	assert.Same(t, wantClient, client)
}

func TestGetKubeClientReturnsClientConstructionError(t *testing.T) {
	restoreKubeClientSeams(t)
	inClusterConfigFunc = func() (*rest.Config, error) {
		return &rest.Config{Host: "https://kubernetes.default.svc"}, nil
	}
	newKubernetesClientFunc = func(*rest.Config) (kubernetes.Interface, error) {
		return nil, errors.New("client construction failed")
	}

	client, err := GetKubeClient()

	assert.Nil(t, client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client construction failed")
}
