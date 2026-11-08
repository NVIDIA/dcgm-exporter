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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/NVIDIA/dcgm-exporter/tests/k8s/internal/framework"
)

const failureDiagnosticsTimeout = 15 * time.Second

var _ = JustAfterEach(func(ctx context.Context) {
	if !CurrentSpecReport().Failed() || kubeClient == nil || testContext.namespace == "" {
		return
	}
	diagnosticsCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failureDiagnosticsTimeout)
	defer cancel()
	writeKubernetesFailureDetails(diagnosticsCtx, GinkgoWriter, kubeClient, testContext.namespace)
})

// writeKubernetesFailureDetails prints pod state and events before exporter crash logs.
func writeKubernetesFailureDetails(ctx context.Context, w io.Writer, client *framework.KubeClient, namespace string) {
	fmt.Fprintf(w, "Kubernetes failure details for namespace %s:\n", namespace)
	pods, podsErr := client.GetPodsByLabel(ctx, namespace, nil)
	if podsErr != nil {
		fmt.Fprintf(w, "  pods: unavailable: %v\n", podsErr)
	} else {
		writePodFailureDetails(w, pods)
	}

	events, err := client.GetEvents(ctx, namespace)
	if err != nil {
		fmt.Fprintf(w, "  events: unavailable: %v\n", err)
	} else {
		writeEventFailureDetails(w, events)
	}

	if podsErr == nil {
		writeExporterCrashLogs(ctx, w, client, namespace, pods)
	}
}

// writeExporterCrashLogs adds recent logs for crashing dcgm-exporter containers.
func writeExporterCrashLogs(
	ctx context.Context,
	w io.Writer,
	client *framework.KubeClient,
	namespace string,
	pods []corev1.Pod,
) {
	for _, pod := range pods {
		if pod.Labels[dcgmExporterPodNameLabel] != dcgmExporterPodNameLabelValue {
			continue
		}
		for _, container := range pod.Status.ContainerStatuses {
			if container.State.Waiting == nil || container.State.Waiting.Reason != "CrashLoopBackOff" {
				continue
			}

			tailLines := int64(100)
			logs, err := client.GetPodLogs(ctx, namespace, pod.Name, container.Name, &tailLines)
			if err != nil {
				fmt.Fprintf(w, "  pod %s container %s logs: unavailable: %v\n", pod.Name, container.Name, err)
				continue
			}
			fmt.Fprintf(w, "  pod %s container %s last 100 lines:\n%s\n", pod.Name, container.Name, logs)
		}
	}
}

// TestWriteKubernetesFailureDetailsFetchesOnlyExporterCrashLogs verifies scoped log requests.
func TestWriteKubernetesFailureDetailsFetchesOnlyExporterCrashLogs(t *testing.T) {
	crashingExporter := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "exporter-crashing",
			Labels: map[string]string{dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "exporter",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		}}},
	}
	nonExporter := crashingExporter.DeepCopy()
	nonExporter.Name = "helper-crashing"
	nonExporter.Labels = map[string]string{"app": "helper"}
	healthyExporter := crashingExporter.DeepCopy()
	healthyExporter.Name = "exporter-pending"
	healthyExporter.Status.ContainerStatuses[0].State.Waiting.Reason = "ImagePullBackOff"

	logRequests := make(chan string, 3)
	requestOrder := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/namespaces/e2e-test/pods":
			_ = json.NewEncoder(w).Encode(corev1.PodList{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"},
				Items:    []corev1.Pod{crashingExporter, *nonExporter, *healthyExporter},
			})
		case "/api/v1/namespaces/e2e-test/pods/exporter-crashing/log":
			requestOrder <- "logs"
			logRequests <- request.URL.RawQuery
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "panic: exporter failed")
		case "/api/v1/namespaces/e2e-test/events":
			requestOrder <- "events"
			_ = json.NewEncoder(w).Encode(corev1.EventList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "EventList"}})
		default:
			t.Errorf("unexpected Kubernetes request: %s %s", request.Method, request.URL.String())
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	client, err := framework.NewKubeClient(&rest.Config{
		Host:          server.URL,
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
	})
	if err != nil {
		t.Fatalf("NewKubeClient() error = %v", err)
	}
	var out strings.Builder
	writeKubernetesFailureDetails(context.Background(), &out, client, "e2e-test")
	if len(requestOrder) != 2 {
		t.Fatalf("diagnostic request count = %d, want events and crashing-container logs", len(requestOrder))
	}
	firstRequest, secondRequest := <-requestOrder, <-requestOrder
	if firstRequest != "events" || secondRequest != "logs" {
		t.Fatalf("diagnostic request order = %q, %q; want events before logs", firstRequest, secondRequest)
	}
	if !strings.Contains(out.String(), "panic: exporter failed") {
		t.Fatalf("failure details omit exporter logs:\n%s", out.String())
	}
	var query string
	select {
	case query = <-logRequests:
	default:
		t.Fatal("no log request for the crashing exporter container")
	}
	if !strings.Contains(query, "container=exporter") || !strings.Contains(query, "tailLines=100") {
		t.Fatalf("log query = %q, want container=exporter and tailLines=100", query)
	}
	select {
	case extra := <-logRequests:
		t.Fatalf("unexpected extra log request: %q", extra)
	default:
	}
}
