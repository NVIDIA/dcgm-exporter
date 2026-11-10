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
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// hardenedExporterPod returns a running exporter pod with the chart's default
// security settings and a mounted ServiceAccount token.
func hardenedExporterPod() *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptr.To(true),
			Containers: []corev1.Container{{
				Name: exporterContainerName,
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             ptr.To(false),
					RunAsUser:                ptr.To[int64](0),
					AllowPrivilegeEscalation: ptr.To(false),
					Capabilities: &corev1.Capabilities{
						Add:  []corev1.Capability{"SYS_ADMIN"},
						Drop: []corev1.Capability{"ALL"},
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: podResourcesVolumeName, MountPath: podResourcesMountPath, ReadOnly: true},
					{Name: "kube-api-access", MountPath: kubeAPIAccessMountPath, ReadOnly: true},
				},
			}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  exporterContainerName,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				User:  &corev1.ContainerUser{Linux: &corev1.LinuxContainerUser{UID: 0}},
			}},
		},
	}
}

// TestExporterPodSecurityError checks that each relaxed setting is reported.
func TestExporterPodSecurityError(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*corev1.Pod)
		wantErr bool
	}{
		{name: "chart defaults", mutate: func(*corev1.Pod) {}},
		{
			name:   "no runtime user reported",
			mutate: func(p *corev1.Pod) { p.Status.ContainerStatuses[0].User = nil },
		},
		{
			name:    "host PID namespace",
			mutate:  func(p *corev1.Pod) { p.Spec.HostPID = true },
			wantErr: true,
		},
		{
			name:    "privileged",
			mutate:  func(p *corev1.Pod) { p.Spec.Containers[0].SecurityContext.Privileged = ptr.To(true) },
			wantErr: true,
		},
		{
			name:    "privilege escalation unset",
			mutate:  func(p *corev1.Pod) { p.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation = nil },
			wantErr: true,
		},
		{
			name:    "capabilities not dropped",
			mutate:  func(p *corev1.Pod) { p.Spec.Containers[0].SecurityContext.Capabilities.Drop = nil },
			wantErr: true,
		},
		{
			name: "extra capability",
			mutate: func(p *corev1.Pod) {
				p.Spec.Containers[0].SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_ADMIN", "NET_ADMIN"}
			},
			wantErr: true,
		},
		{
			name:    "different user",
			mutate:  func(p *corev1.Pod) { p.Spec.Containers[0].SecurityContext.RunAsUser = ptr.To[int64](1000) },
			wantErr: true,
		},
		{
			name:    "process runs as another user",
			mutate:  func(p *corev1.Pod) { p.Status.ContainerStatuses[0].User.Linux.UID = 1000 },
			wantErr: true,
		},
		{
			name:    "pod resources mounted read-write",
			mutate:  func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].ReadOnly = false },
			wantErr: true,
		},
		{
			name:    "pod resources mounted elsewhere",
			mutate:  func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].MountPath = "/pod-resources" },
			wantErr: true,
		},
		{
			name:    "container not running",
			mutate:  func(p *corev1.Pod) { p.Status.ContainerStatuses[0].State.Running = nil },
			wantErr: true,
		},
		{
			name:    "no securityContext",
			mutate:  func(p *corev1.Pod) { p.Spec.Containers[0].SecurityContext = nil },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := hardenedExporterPod()
			tt.mutate(pod)

			err := exporterPodSecurityError(pod)
			if (err != nil) != tt.wantErr {
				t.Fatalf("exporterPodSecurityError() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

// TestExporterTokenMountError checks the pod field and the mounted token must
// both match the expected state.
func TestExporterTokenMountError(t *testing.T) {
	pod := hardenedExporterPod()
	if err := exporterTokenMountError(pod, true); err != nil {
		t.Fatalf("mounted token: %v", err)
	}
	if err := exporterTokenMountError(pod, false); err == nil {
		t.Fatal("mounted token passed a no-token check")
	}

	pod.Spec.AutomountServiceAccountToken = ptr.To(false)
	if err := exporterTokenMountError(pod, false); err == nil {
		t.Fatal("token mounted despite automount false passed a no-token check")
	}

	pod.Spec.Containers[0].VolumeMounts = pod.Spec.Containers[0].VolumeMounts[:1]
	if err := exporterTokenMountError(pod, false); err != nil {
		t.Fatalf("no token: %v", err)
	}

	pod.Spec.Volumes = []corev1.Volume{{
		Name: "custom-token",
		VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}}},
		}},
	}}
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts,
		corev1.VolumeMount{Name: "custom-token", MountPath: "/token"})
	if err := exporterTokenMountError(pod, false); err == nil {
		t.Fatal("token projected at another path passed a no-token check")
	}
}

// TestExporterAccessChecks checks which reads the chart grants with and
// without the ClusterRole and DRA.
func TestExporterAccessChecks(t *testing.T) {
	tests := []struct {
		name             string
		clusterRoleBound bool
		dra              bool
		wantPods         bool
		wantSlices       bool
	}{
		{name: "no Kubernetes API feature"},
		{name: "pod labels", clusterRoleBound: true, wantPods: true},
		{name: "DRA", clusterRoleBound: true, dra: true, wantPods: true, wantSlices: true},
		{name: "DRA without chart RBAC", dra: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, check := range exporterAccessChecks("gpu", tt.clusterRoleBound, tt.dra) {
				want := expectedAccess(check.attributes, tt.wantPods, tt.wantSlices)
				if check.allowed != want {
					t.Errorf("%s: allowed = %t, want %t", check, check.allowed, want)
				}
			}
		})
	}
}

// expectedAccess is the chart's grant for one action: pod and ResourceSlice
// reads when given, and nothing else.
func expectedAccess(a authorizationv1.ResourceAttributes, pods, slices bool) bool {
	read := a.Verb == "get" || a.Verb == "list" || a.Verb == "watch"
	switch {
	case read && a.Resource == "pods":
		return pods
	case read && a.Resource == "resourceslices":
		return slices
	default:
		return false
	}
}
