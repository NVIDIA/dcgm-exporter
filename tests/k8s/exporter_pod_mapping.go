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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// readyExporterPodOnNode selects the one ready exporter on nodeName from a
// Kubernetes Pod list. E2E scenarios call it before scraping so a workload is
// always checked against the exporter that observes the same node.
func readyExporterPodOnNode(pods []corev1.Pod, nodeName string) (corev1.Pod, error) {
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return corev1.Pod{}, fmt.Errorf("workload has no assigned node")
	}

	matching := make([]corev1.Pod, 0, 1)
	for _, pod := range pods {
		if pod.Spec.NodeName != nodeName || !podIsReady(pod) {
			continue
		}
		matching = append(matching, pod)
	}

	switch len(matching) {
	case 1:
		return matching[0], nil
	case 0:
		return corev1.Pod{}, fmt.Errorf("no ready dcgm-exporter pod runs on workload node %q", nodeName)
	default:
		return corev1.Pod{}, fmt.Errorf("found %d ready dcgm-exporter pods on workload node %q, want 1", len(matching), nodeName)
	}
}

// firstReadyExporterPod returns the first nonterminating Pod with a true Ready condition.
func firstReadyExporterPod(pods []corev1.Pod) (corev1.Pod, error) {
	for _, pod := range pods {
		if podIsReady(pod) {
			return pod, nil
		}
	}

	return corev1.Pod{}, fmt.Errorf("no ready dcgm-exporter pod found")
}

// replacementExporterPod finds a newer Pod with a different UID on the previous Pod's node.
func replacementExporterPod(pods []corev1.Pod, previousPod corev1.Pod) (corev1.Pod, error) {
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil ||
			pod.Spec.NodeName != previousPod.Spec.NodeName ||
			pod.UID == previousPod.UID ||
			!pod.CreationTimestamp.After(previousPod.CreationTimestamp.Time) {
			continue
		}
		return pod, nil
	}

	return corev1.Pod{}, fmt.Errorf("no replacement dcgm-exporter pod found on node %q", previousPod.Spec.NodeName)
}

// podIsReady excludes terminating Pods and requires a true Ready condition.
func podIsReady(pod corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
