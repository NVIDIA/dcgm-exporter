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
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// writePodFailureDetails prints each pod's readiness and container failure state.
func writePodFailureDetails(w io.Writer, pods []corev1.Pod) {
	if len(pods) == 0 {
		fmt.Fprintln(w, "  pods: none")
		return
	}
	for _, pod := range pods {
		fmt.Fprintf(w, "  pod %s: %s\n", pod.Name, podReadinessSummary(pod.Status))
	}
}

// writeEventFailureDetails prints event identity, type, reason, count, and message.
func writeEventFailureDetails(w io.Writer, events []corev1.Event) {
	if len(events) == 0 {
		fmt.Fprintln(w, "  events: none")
		return
	}
	for _, event := range events {
		fmt.Fprintf(
			w,
			"  event %s/%s: type=%s reason=%s count=%d message=%s\n",
			event.InvolvedObject.Kind,
			event.InvolvedObject.Name,
			event.Type,
			event.Reason,
			event.Count,
			event.Message,
		)
	}
}

// exporterPodReadinessSummaryOnNode summarizes selected exporter Pods on nodeName.
func exporterPodReadinessSummaryOnNode(exporterPods []corev1.Pod, nodeName string) string {
	var summaries []string
	for _, pod := range exporterPods {
		if pod.Spec.NodeName != nodeName {
			continue
		}
		summaries = append(summaries, fmt.Sprintf("pod %s: %s", pod.Name, podReadinessSummary(pod.Status)))
	}
	if len(summaries) == 0 {
		return "no dcgm-exporter pods observed"
	}
	return strings.Join(summaries, "; ")
}

// podReadinessSummary formats pod conditions and container states for failure output.
func podReadinessSummary(status corev1.PodStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "phase=%s", status.Phase)
	for _, condition := range status.Conditions {
		fmt.Fprintf(&b, " condition[%s]=%s", condition.Type, condition.Status)
		if condition.Reason != "" {
			fmt.Fprintf(&b, " reason=%s", condition.Reason)
		}
		if condition.Message != "" {
			fmt.Fprintf(&b, " message=%s", condition.Message)
		}
	}
	for _, container := range status.ContainerStatuses {
		fmt.Fprintf(&b, " container[%s].ready=%t", container.Name, container.Ready)
		switch {
		case container.State.Waiting != nil:
			fmt.Fprintf(&b, " waiting=%s", container.State.Waiting.Reason)
			if container.State.Waiting.Message != "" {
				fmt.Fprintf(&b, " message=%s", container.State.Waiting.Message)
			}
		case container.State.Terminated != nil:
			fmt.Fprintf(&b, " terminated=%s exit=%d", container.State.Terminated.Reason, container.State.Terminated.ExitCode)
			if container.State.Terminated.Message != "" {
				fmt.Fprintf(&b, " message=%s", container.State.Terminated.Message)
			}
		case container.State.Running != nil:
			fmt.Fprint(&b, " running=true")
		}
	}
	return b.String()
}
