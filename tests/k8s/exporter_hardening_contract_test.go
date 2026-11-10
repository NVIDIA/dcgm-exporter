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
	"errors"
	"fmt"
	"slices"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	exporterContainerName  = "exporter"
	podResourcesVolumeName = "pod-gpu-resources"
	podResourcesMountPath  = "/var/lib/kubelet/pod-resources"
	kubeAPIAccessMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"
)

// exporterAccessCheck is one API action and whether the exporter's
// ServiceAccount may perform it.
type exporterAccessCheck struct {
	attributes authorizationv1.ResourceAttributes
	allowed    bool
}

// String names the action for failure messages.
func (c exporterAccessCheck) String() string {
	a := c.attributes
	resource := a.Resource
	if a.Group != "" {
		resource += "." + a.Group
	}
	if a.Subresource != "" {
		resource += "/" + a.Subresource
	}
	if a.Name != "" {
		resource += "/" + a.Name
	}
	scope := "cluster"
	if a.Namespace != "" {
		scope = "namespace " + a.Namespace
	}
	return fmt.Sprintf("%s %s in %s", a.Verb, resource, scope)
}

// exporterAccessChecks lists what the chart's RBAC grants and denies the
// exporter. The chart creates no namespaced Role. The
// ClusterRole adds pod reads when it is bound, and ResourceSlice reads only
// with DRA. Writes and other reads stay denied in every case. Namespaced
// resources are checked both cluster-wide and in the exporter's namespace, so
// a local RoleBinding cannot hide extra access.
func exporterAccessChecks(namespace string, clusterRoleBound, dra bool) []exporterAccessCheck {
	var checks []exporterAccessCheck
	add := func(allowed bool, verbs []string, attributes authorizationv1.ResourceAttributes) {
		for _, verb := range verbs {
			attributes.Verb = verb
			checks = append(checks, exporterAccessCheck{attributes: attributes, allowed: allowed})
		}
	}

	reads := []string{"get", "list", "watch"}
	writes := []string{"create", "update", "patch", "delete"}
	pods := authorizationv1.ResourceAttributes{Resource: "pods"}
	localPods := authorizationv1.ResourceAttributes{Namespace: namespace, Resource: "pods"}
	resourceSlices := authorizationv1.ResourceAttributes{Group: "resource.k8s.io", Resource: "resourceslices"}

	add(clusterRoleBound, reads, pods)
	add(clusterRoleBound, reads, localPods)
	add(clusterRoleBound && dra, reads, resourceSlices)

	add(false, writes, localPods)
	add(false, []string{"create"}, authorizationv1.ResourceAttributes{Namespace: namespace, Resource: "pods", Subresource: "exec"})
	add(false, writes, resourceSlices)
	add(false, append([]string{"list"}, writes...), authorizationv1.ResourceAttributes{Namespace: namespace, Resource: "configmaps"})
	add(false, []string{"get"}, authorizationv1.ResourceAttributes{Namespace: namespace, Resource: "configmaps", Name: "other"})
	add(false, append(reads, writes...), authorizationv1.ResourceAttributes{Resource: "secrets"})
	add(false, append(reads, writes...), authorizationv1.ResourceAttributes{Namespace: namespace, Resource: "secrets"})
	add(false, append(reads, writes...), authorizationv1.ResourceAttributes{Resource: "nodes"})
	add(false, writes, authorizationv1.ResourceAttributes{Namespace: namespace, Group: "apps", Resource: "daemonsets"})

	return checks
}

// exporterPodSecurityError reports where a running exporter pod differs from
// the chart's default security settings in deployment/values.yaml.
func exporterPodSecurityError(pod *corev1.Pod) error {
	var errs []error
	if pod.Spec.HostPID || pod.Spec.HostIPC || pod.Spec.HostNetwork {
		errs = append(errs, fmt.Errorf("pod shares host namespaces: hostPID=%t hostIPC=%t hostNetwork=%t",
			pod.Spec.HostPID, pod.Spec.HostIPC, pod.Spec.HostNetwork))
	}

	container := findContainer(pod.Spec.Containers, exporterContainerName)
	if container == nil {
		return errors.Join(append(errs, fmt.Errorf("pod %s has no %q container", pod.Name, exporterContainerName))...)
	}

	errs = append(errs, containerSecurityErrors(container.SecurityContext)...)
	errs = append(errs, podResourcesMountErrors(container.VolumeMounts)...)
	errs = append(errs, containerRuntimeUserErrors(pod.Status.ContainerStatuses, container.SecurityContext)...)
	return errors.Join(errs...)
}

// containerSecurityErrors checks the exporter container's securityContext.
// The chart runs as root and adds SYS_ADMIN because DCGM profiling metrics
// need both; it drops every other capability and blocks privilege escalation.
func containerSecurityErrors(sc *corev1.SecurityContext) []error {
	if sc == nil {
		return []error{errors.New("exporter container has no securityContext")}
	}

	var errs []error
	if sc.Privileged != nil && *sc.Privileged {
		errs = append(errs, errors.New("exporter container is privileged"))
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		errs = append(errs, errors.New("exporter container allows privilege escalation"))
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != 0 {
		errs = append(errs, fmt.Errorf("exporter container runAsUser = %v, want the chart default 0", sc.RunAsUser))
	}

	if sc.Capabilities == nil {
		return append(errs, errors.New("exporter container does not set capabilities"))
	}
	if !slices.Contains(sc.Capabilities.Drop, "ALL") {
		errs = append(errs, fmt.Errorf("exporter container drops %v, want ALL", sc.Capabilities.Drop))
	}
	if !slices.Equal(sc.Capabilities.Add, []corev1.Capability{"SYS_ADMIN"}) {
		errs = append(errs, fmt.Errorf("exporter container adds %v, want only SYS_ADMIN", sc.Capabilities.Add))
	}
	return errs
}

// podResourcesMountErrors checks that the kubelet pod-resources socket
// directory, the only host path the chart mounts by default, is mounted
// read-only where the exporter reads it.
func podResourcesMountErrors(mounts []corev1.VolumeMount) []error {
	for _, mount := range mounts {
		if mount.Name != podResourcesVolumeName {
			continue
		}
		var errs []error
		if mount.MountPath != podResourcesMountPath {
			errs = append(errs, fmt.Errorf("volume %q is mounted at %q, want %q", podResourcesVolumeName, mount.MountPath, podResourcesMountPath))
		}
		if !mount.ReadOnly {
			errs = append(errs, fmt.Errorf("volume %q is mounted read-write", podResourcesVolumeName))
		}
		return errs
	}
	return []error{fmt.Errorf("volume %q is not mounted", podResourcesVolumeName)}
}

// containerRuntimeUserErrors checks the exporter container is running and,
// when the kubelet reports the process user, that it matches runAsUser.
func containerRuntimeUserErrors(statuses []corev1.ContainerStatus, sc *corev1.SecurityContext) []error {
	for _, status := range statuses {
		if status.Name != exporterContainerName {
			continue
		}
		if status.State.Running == nil {
			return []error{errors.New("exporter container is not running")}
		}
		if status.User == nil || status.User.Linux == nil || sc == nil || sc.RunAsUser == nil {
			return nil
		}
		if status.User.Linux.UID != *sc.RunAsUser {
			return []error{fmt.Errorf("exporter process runs as UID %d, want %d", status.User.Linux.UID, *sc.RunAsUser)}
		}
		return nil
	}
	return []error{errors.New("exporter container has no status")}
}

// exporterTokenMountError checks whether the pod mounts a ServiceAccount
// token. The chart mounts one only when RBAC is created for a Kubernetes
// API feature such as pod labels, pod UID or DRA.
func exporterTokenMountError(pod *corev1.Pod, wantMounted bool) error {
	automount := pod.Spec.AutomountServiceAccountToken
	if automount == nil {
		return fmt.Errorf("pod automountServiceAccountToken is unset, want %t", wantMounted)
	}
	if *automount != wantMounted {
		return fmt.Errorf("pod automountServiceAccountToken = %t, want %t", *automount, wantMounted)
	}

	container := findContainer(pod.Spec.Containers, exporterContainerName)
	if container == nil {
		return fmt.Errorf("pod %s has no %q container", pod.Name, exporterContainerName)
	}
	tokenVolumes := serviceAccountTokenVolumes(pod.Spec.Volumes)
	mounted := slices.ContainsFunc(container.VolumeMounts, func(m corev1.VolumeMount) bool {
		return m.MountPath == kubeAPIAccessMountPath || tokenVolumes[m.Name]
	})
	if mounted != wantMounted {
		return fmt.Errorf("exporter container token mounted = %t, want %t", mounted, wantMounted)
	}
	return nil
}

// serviceAccountTokenVolumes returns the names of projected volumes that
// carry a ServiceAccount token, wherever they are mounted.
func serviceAccountTokenVolumes(volumes []corev1.Volume) map[string]bool {
	names := make(map[string]bool)
	for _, volume := range volumes {
		if volume.Projected == nil {
			continue
		}
		for _, source := range volume.Projected.Sources {
			if source.ServiceAccountToken != nil {
				names[volume.Name] = true
			}
		}
	}
	return names
}

// findContainer returns the named container, or nil when the pod has none.
func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}
