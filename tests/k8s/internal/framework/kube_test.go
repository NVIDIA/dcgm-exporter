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

package framework

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

// TestGetPodLogsLimitsResponseBytes checks the cap for full and tailed logs.
func TestGetPodLogsLimitsResponseBytes(t *testing.T) {
	t.Parallel()

	tailLines := int64(100)
	testCases := []struct {
		name          string
		tailLines     *int64
		wantTailLines string
	}{
		{name: "full logs", wantTailLines: ""},
		{name: "tailed logs", tailLines: &tailLines, wantTailLines: "100"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			requests := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests <- request.URL.RequestURI()
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write(bytes.Repeat([]byte{'x'}, maxLogBytes+64))
			}))
			defer server.Close()

			client, err := NewKubeClient(&rest.Config{
				Host:          server.URL,
				ContentConfig: rest.ContentConfig{ContentType: "application/json"},
			})
			require.NoError(t, err)
			logs, err := client.GetPodLogs(context.Background(), "e2e-test", "exporter", "dcgm-exporter", testCase.tailLines)
			require.NoError(t, err)
			const warning = "\n\n[WARNING: Logs truncated due to size limit (10MB)]"
			require.Len(t, logs, maxLogBytes+len(warning))
			require.True(t, strings.HasPrefix(logs, strings.Repeat("x", maxLogBytes)))
			require.True(t, strings.HasSuffix(logs, warning))
			requestURI := <-requests
			path, query, hasQuery := strings.Cut(requestURI, "?")
			require.True(t, hasQuery)
			require.Equal(t, "/api/v1/namespaces/e2e-test/pods/exporter/log", path)
			queryValues, err := url.ParseQuery(query)
			require.NoError(t, err)
			require.Equal(t, testCase.wantTailLines, queryValues.Get("tailLines"))
			require.Len(t, requests, 0)
		})
	}
}

// TestDeleteNamespaceUsesConfiguredName verifies cleanup deletes only the
// checked namespace object, using its UID as a precondition.
func TestDeleteNamespaceUsesConfiguredName(t *testing.T) {
	var options metav1.DeleteOptions
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			t.Errorf("method = %s, want %s", request.Method, http.MethodDelete)
		}
		if request.URL.Path != "/api/v1/namespaces/e2e-test" {
			t.Errorf("path = %s, want namespace delete endpoint", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&options); err != nil {
			t.Errorf("decode delete options: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success"}`))
	}))
	defer server.Close()

	client, err := NewKubeClient(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatalf("NewKubeClient() error = %v", err)
	}
	if err := client.DeleteNamespace(context.Background(), "e2e-test", types.UID("test-uid")); err != nil {
		t.Fatalf("DeleteNamespace() error = %v", err)
	}
	if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != types.UID("test-uid") {
		t.Fatalf("delete precondition = %#v, want checked namespace UID", options.Preconditions)
	}
}

// TestDeleteNamespaceIgnoresNotFound verifies repeated cleanup succeeds after
// another cleanup has already removed the configured environment.
func TestDeleteNamespaceIgnoresNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			t.Errorf("method = %s, want %s", request.Method, http.MethodDelete)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`))
	}))
	defer server.Close()

	client, err := NewKubeClient(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatalf("NewKubeClient() error = %v", err)
	}
	if err := client.DeleteNamespace(context.Background(), "e2e-test", types.UID("test-uid")); err != nil {
		t.Fatalf("DeleteNamespace() error = %v, want nil for an absent namespace", err)
	}
}

// TestCreatePodTurnsOffTheCUDARequirement verifies the sleeping workload pod
// asks the NVIDIA runtime to skip the image's CUDA version check, so the pod
// starts on a driver older than the image's CUDA version.
func TestCreatePodTurnsOffTheCUDARequirement(t *testing.T) {
	var pod corev1.Pod
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&pod); err != nil {
			t.Errorf("decode pod: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&pod)
	}))
	defer server.Close()

	client, err := NewKubeClient(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	require.NoError(t, err)
	_, err = client.CreatePod(context.Background(), "e2e-test", nil, "workload", "cuda", "registry.example/cuda:dev",
		"nvidia", nil, NVIDIAGPUResourceName)
	require.NoError(t, err)

	require.Len(t, pod.Spec.Containers, 1)
	require.Contains(t, pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "NVIDIA_DISABLE_REQUIRE", Value: "true"})
}
