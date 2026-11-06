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
	"bytes"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestExporterPodReadinessSummaryOnNode excludes status from other nodes.
func TestExporterPodReadinessSummaryOnNode(t *testing.T) {
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "exporter-local",
				Labels: map[string]string{"app.kubernetes.io/name": "dcgm-exporter"},
			},
			Spec: corev1.PodSpec{NodeName: "gpu-node-a"},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "exporter",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "exporter-remote",
				Labels: map[string]string{"app.kubernetes.io/name": "dcgm-exporter"},
			},
			Spec:   corev1.PodSpec{NodeName: "gpu-node-b"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	}

	got := exporterPodReadinessSummaryOnNode(pods, "gpu-node-a")
	if !strings.Contains(got, "exporter-local") || !strings.Contains(got, "waiting=CrashLoopBackOff") {
		t.Fatalf("node-local summary = %q, want local exporter status", got)
	}
	if strings.Contains(got, "exporter-remote") {
		t.Fatalf("node-local summary included another node: %q", got)
	}
}

// TestWritePodFailureDetailsIncludesImagePullFailure checks pending container detail output.
func TestWritePodFailureDetailsIncludesImagePullFailure(t *testing.T) {
	pods := []corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "debug-dump-reader"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "reader",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "ErrImagePull",
					Message: "failed to pull helper image",
				}},
			}},
		},
	}}
	var out bytes.Buffer
	writePodFailureDetails(&out, pods)
	for _, want := range []string{"debug-dump-reader", "phase=Pending", "waiting=ErrImagePull", "failed to pull helper image"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("failure details missing %q:\n%s", want, out.String())
		}
	}
}

// TestWriteEventFailureDetailsIncludesReasonAndMessage checks event output.
func TestWriteEventFailureDetailsIncludesReasonAndMessage(t *testing.T) {
	events := []corev1.Event{{
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "debug-dump-reader"},
		Type:           corev1.EventTypeWarning,
		Reason:         "Failed",
		Message:        "Failed to pull image: registry lookup failed",
		Count:          3,
	}}
	var out bytes.Buffer
	writeEventFailureDetails(&out, events)
	for _, want := range []string{"Pod/debug-dump-reader", "type=Warning", "reason=Failed", "count=3", "registry lookup failed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("failure details missing %q:\n%s", want, out.String())
		}
	}
}
