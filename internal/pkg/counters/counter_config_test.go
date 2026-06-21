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

package counters

import (
	"context"
	"errors"
	stdos "os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestEmptyConfigMap(t *testing.T) {
	// ConfigMap matches criteria but is empty
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap1",
			Namespace: "default",
		},
		Data: map[string]string{"metrics": ""},
	})

	c := appconfig.Config{
		ConfigMapData: "default:configmap1",
	}
	records, err := readConfigMap(context.Background(), clientset, &c)
	require.Error(t, err, "Should have returned an error")
	require.Empty(t, records, "Should have no records")
}

func TestValidConfigMap(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap1",
			Namespace: "default",
		},
		Data: map[string]string{"metrics": "DCGM_FI_DEV_GPU_TEMP, gauge, temperature"},
	})

	c := appconfig.Config{
		ConfigMapData: "default:configmap1",
	}
	records, err := readConfigMap(context.Background(), clientset, &c)
	require.NoError(t, err, "Should have succeeded")
	require.Len(t, records, 1, "Should have 1 record")
}

func TestInvalidConfigMapData(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap1",
			Namespace: "default",
		},
		Data: map[string]string{"bad": "DCGM_FI_DEV_GPU_TEMP, gauge, temperature"},
	})

	c := appconfig.Config{
		ConfigMapData: "default:configmap1",
	}
	records, err := readConfigMap(context.Background(), clientset, &c)
	require.Error(t, err, "Should have returned an error")
	require.Empty(t, records, "Should have no records")
}

func TestInvalidConfigMapName(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap",
			Namespace: "default",
		},
	})

	c := appconfig.Config{
		ConfigMapData: "default:configmap1",
	}
	records, err := readConfigMap(context.Background(), clientset, &c)
	require.Error(t, err, "Should have returned an error")
	require.Empty(t, records, "Should have no records")
}

func TestInvalidConfigMapNamespace(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap",
			Namespace: "c1",
		},
	})

	c := appconfig.Config{
		ConfigMapData: "default:configmap1",
	}
	records, err := readConfigMap(context.Background(), clientset, &c)
	require.Error(t, err, "Should have returned an error")
	require.Empty(t, records, "Should have no records")
}

// overrideKubeClient swaps the package-level getKubeClient for the duration of a
// test and returns a function that restores the original.
func overrideKubeClient(fn func() (kubernetes.Interface, error)) func() {
	prev := getKubeClient
	getKubeClient = fn
	return func() { getKubeClient = prev }
}

// writeCountersFile writes content to a temp file and returns its path.
func writeCountersFile(t *testing.T, content string) string {
	t.Helper()
	tmpFile, err := stdos.CreateTemp(t.TempDir(), "counters-*.csv")
	require.NoError(t, err)
	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpFile.Close())
	return tmpFile.Name()
}

// When a ConfigMap is configured but no Kubernetes client can be created (e.g. the
// ServiceAccount token is not mounted), GetCounterSet must fall back to the metrics
// file instead of crashing the process. Regression test for issue #624.
func TestGetCounterSetFallsBackToFileWhenKubeClientUnavailable(t *testing.T) {
	defer overrideKubeClient(func() (kubernetes.Interface, error) {
		return nil, errors.New("open /var/run/secrets/kubernetes.io/serviceaccount/token: no such file or directory")
	})()

	c := appconfig.Config{
		ConfigMapData:  "default:configmap1",
		CollectorsFile: writeCountersFile(t, "DCGM_FI_DEV_GPU_TEMP, gauge, temperature\n"),
	}

	cc, err := GetCounterSet(context.Background(), &c)
	require.NoError(t, err)
	require.Len(t, cc.DCGMCounters, 1)
}

// When the ConfigMap cannot be read (e.g. it does not exist), GetCounterSet must
// also fall back to the metrics file rather than crash.
func TestGetCounterSetFallsBackToFileWhenConfigMapMissing(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	defer overrideKubeClient(func() (kubernetes.Interface, error) {
		return clientset, nil
	})()

	c := appconfig.Config{
		ConfigMapData:  "default:configmap1",
		CollectorsFile: writeCountersFile(t, "DCGM_FI_DEV_GPU_TEMP, gauge, temperature\n"),
	}

	cc, err := GetCounterSet(context.Background(), &c)
	require.NoError(t, err)
	require.Len(t, cc.DCGMCounters, 1)
}

// When the ConfigMap is available, GetCounterSet must use it and not the metrics file.
func TestGetCounterSetUsesConfigMapWhenAvailable(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap1",
			Namespace: "default",
		},
		Data: map[string]string{"metrics": "DCGM_FI_DEV_GPU_TEMP, gauge, temperature"},
	})
	defer overrideKubeClient(func() (kubernetes.Interface, error) {
		return clientset, nil
	})()

	c := appconfig.Config{
		ConfigMapData:  "default:configmap1",
		CollectorsFile: writeCountersFile(t, "DCGM_FI_DEV_SM_CLOCK, gauge, sm clock\nDCGM_FI_DEV_MEM_CLOCK, gauge, mem clock\n"),
	}

	cc, err := GetCounterSet(context.Background(), &c)
	require.NoError(t, err)
	// One counter from the ConfigMap, proving the file (which has two) was not used.
	require.Len(t, cc.DCGMCounters, 1)
}

func TestExtractCounters(t *testing.T) {
	tests := []struct {
		name  string
		field string
		valid bool
	}{
		{
			name:  "Valid Input DCGM_FI_DEV_GPU_TEMP",
			field: "DCGM_FI_DEV_GPU_TEMP, gauge, temperature\n",
			valid: true,
		},
		{
			name:  "Invalid Input DCGM_EXP_XID_ERRORS_COUNTXXX",
			field: "DCGM_EXP_XID_ERRORS_COUNTXXX, gauge, temperature\n",
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractCountersHelper(t, tt.field, tt.valid)
		})
	}
}

func extractCountersHelper(t *testing.T, input string, valid bool) {
	tmpFile, err := stdos.CreateTemp(stdos.TempDir(), "prefix-")
	if err != nil {
		t.Fatalf("Cannot create temporary file: %v", err)
	}

	defer func() { _ = stdos.Remove(tmpFile.Name()) }()

	text := []byte(input)
	if _, err = tmpFile.Write(text); err != nil {
		t.Fatalf("Failed to write to temporary file: %v", err)
	}

	t.Logf("Using file: %s", tmpFile.Name())

	if err := tmpFile.Close(); err != nil {
		t.Fatalf("Cannot close temp file: %v", err)
	}

	c := appconfig.Config{
		ConfigMapData:  undefinedConfigMapData,
		CollectorsFile: tmpFile.Name(),
	}
	cc, err := GetCounterSet(context.Background(), &c)
	if valid {
		assert.NoError(t, err, "Expected no error.")
		assert.Equal(t, 1, len(cc.DCGMCounters), "Expected 1 record counters.")
	} else {
		assert.Error(t, err, "Expected error.")
		assert.Nil(t, cc, "Expected no counters.")
	}
}
