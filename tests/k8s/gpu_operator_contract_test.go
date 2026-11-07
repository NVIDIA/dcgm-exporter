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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const (
	// gpuOperatorReadyState is the ClusterPolicy status.state once every
	// operand the policy enables is ready.
	gpuOperatorReadyState = "ready"

	// Node labels that GPU feature discovery publishes for each GPU node.
	gpuCountNodeLabel    = "nvidia.com/gpu.count"
	gpuReplicasNodeLabel = "nvidia.com/gpu.replicas"

	// gpuResourceName is the extended resource time-slicing shares, because
	// the harness config keeps renameByDefault false.
	gpuResourceName corev1.ResourceName = "nvidia.com/gpu"

	// nvidiaDRADriver is the DRA driver name, which is also the DeviceClass
	// the harness DRA workloads request.
	nvidiaDRADriver = "gpu.nvidia.com"
)

// gpuOperatorClusterPolicy holds the ClusterPolicy fields the GPU Operator
// scenarios read.
type gpuOperatorClusterPolicy struct {
	Metadata metav1.ObjectMeta `json:"metadata"`
	Spec     struct {
		DevicePlugin struct {
			Config struct {
				Name    string `json:"name"`
				Default string `json:"default"`
			} `json:"config"`
		} `json:"devicePlugin"`
		DCGM         gpuOperatorDCGMSpec         `json:"dcgm"`
		DCGMExporter gpuOperatorDCGMExporterSpec `json:"dcgmExporter"`
	} `json:"spec"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
}

// gpuOperatorDCGMSpec holds the standalone DCGM fields that ClusterPolicy and
// GPUCluster share.
type gpuOperatorDCGMSpec struct {
	Enabled *bool `json:"enabled"`
}

// gpuOperatorDCGMExporterSpec holds the exporter fields that ClusterPolicy and
// GPUCluster share.
type gpuOperatorDCGMExporterSpec struct {
	Config struct {
		Name string `json:"name"`
	} `json:"config"`
	EnablePodLabels        *bool    `json:"enablePodLabels"`
	EnablePodUID           *bool    `json:"enablePodUID"`
	PodLabelAllowlistRegex []string `json:"podLabelAllowlistRegex"`
}

// gpuOperatorClusterPolicyList is `kubectl get clusterpolicy -o json`.
type gpuOperatorClusterPolicyList struct {
	Items []gpuOperatorClusterPolicy `json:"items"`
}

// parseGPUOperatorClusterPolicies decodes a ClusterPolicy list and requires at
// least one policy.
func parseGPUOperatorClusterPolicies(data []byte) ([]gpuOperatorClusterPolicy, error) {
	var list gpuOperatorClusterPolicyList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("decode ClusterPolicy list: %w", err)
	}

	if len(list.Items) == 0 {
		return nil, errors.New("no ClusterPolicy found")
	}
	return list.Items, nil
}

// clusterPolicyReadyError checks that every ClusterPolicy reports
// status.state "ready".
func clusterPolicyReadyError(data []byte) error {
	policies, err := parseGPUOperatorClusterPolicies(data)
	if err != nil {
		return err
	}

	var errs []error
	for _, policy := range policies {
		if policy.Status.State != gpuOperatorReadyState {
			errs = append(errs, fmt.Errorf("ClusterPolicy %q status.state = %q, want %q",
				policy.Metadata.Name, policy.Status.State, gpuOperatorReadyState))
		}
	}
	return errors.Join(errs...)
}

// isGPUOperatorExporterName matches the operator-managed exporter DaemonSet,
// its pods and its Service by name. Every GPU Operator helper uses this rule.
func isGPUOperatorExporterName(name string) bool {
	return strings.Contains(name, "dcgm") && strings.Contains(name, "exporter")
}

// gpuOperatorExporterRolloutError checks the operator-managed exporter
// DaemonSet: it schedules at least one pod, every scheduled pod is updated and
// ready, its pod template runs expectedImage, and each live pod it owns is
// running, ready and running expectedImage. Succeeded and Failed pods, such as
// an evicted pod that was replaced, are not part of the rollout. daemonSets and pods are `kubectl get -o json` lists from the
// GPU Operator namespace.
func gpuOperatorExporterRolloutError(daemonSets, pods []byte, expectedImage string) error {
	var daemonSetList appsv1.DaemonSetList
	if err := json.Unmarshal(daemonSets, &daemonSetList); err != nil {
		return fmt.Errorf("decode DaemonSet list: %w", err)
	}

	var podList corev1.PodList
	if err := json.Unmarshal(pods, &podList); err != nil {
		return fmt.Errorf("decode pod list: %w", err)
	}

	var errs []error
	found := false
	for _, daemonSet := range daemonSetList.Items {
		if !isGPUOperatorExporterName(daemonSet.Name) {
			continue
		}
		found = true
		errs = append(errs, exporterDaemonSetErrors(daemonSet, podList.Items, expectedImage)...)
	}

	if !found {
		return errors.New("no GPU Operator dcgm-exporter DaemonSet found")
	}
	return errors.Join(errs...)
}

// exporterDaemonSetErrors checks one exporter DaemonSet's status counts, pod
// template image, and owned pods, and returns one error per problem.
func exporterDaemonSetErrors(daemonSet appsv1.DaemonSet, pods []corev1.Pod, expectedImage string) []error {
	var errs []error
	status := daemonSet.Status
	if status.DesiredNumberScheduled == 0 {
		errs = append(errs, fmt.Errorf("DaemonSet %q status.desiredNumberScheduled = 0, want at least 1", daemonSet.Name))
	}
	if status.NumberReady != status.DesiredNumberScheduled {
		errs = append(errs, fmt.Errorf("DaemonSet %q status.numberReady = %d, want desiredNumberScheduled %d",
			daemonSet.Name, status.NumberReady, status.DesiredNumberScheduled))
	}
	if status.UpdatedNumberScheduled != status.DesiredNumberScheduled {
		errs = append(errs, fmt.Errorf("DaemonSet %q status.updatedNumberScheduled = %d, want desiredNumberScheduled %d",
			daemonSet.Name, status.UpdatedNumberScheduled, status.DesiredNumberScheduled))
	}
	if images := containerImages(daemonSet.Spec.Template.Spec.Containers); !slices.Contains(images, expectedImage) {
		errs = append(errs, fmt.Errorf("DaemonSet %q pod template images = %v, want %q", daemonSet.Name, images, expectedImage))
	}

	// Each live owned pod must be running, ready and on the image. Terminated
	// pods linger until garbage collection and are skipped.
	owned := 0
	for _, pod := range pods {
		if !ownedByDaemonSet(pod, daemonSet.Name) || podTerminated(pod) {
			continue
		}
		owned++
		if pod.Status.Phase != corev1.PodRunning {
			errs = append(errs, fmt.Errorf("DaemonSet %q pod %q phase = %q, want %q",
				daemonSet.Name, pod.Name, pod.Status.Phase, corev1.PodRunning))
		} else if !podReady(pod) {
			errs = append(errs, fmt.Errorf("DaemonSet %q pod %q is running but its %s condition is not True",
				daemonSet.Name, pod.Name, corev1.PodReady))
		}
		if images := containerImages(pod.Spec.Containers); !slices.Contains(images, expectedImage) {
			errs = append(errs, fmt.Errorf("DaemonSet %q pod %q images = %v, want %q", daemonSet.Name, pod.Name, images, expectedImage))
		}
	}

	if int32(owned) != status.DesiredNumberScheduled {
		errs = append(errs, fmt.Errorf("DaemonSet %q owns %d pods, want desiredNumberScheduled %d",
			daemonSet.Name, owned, status.DesiredNumberScheduled))
	}
	return errs
}

// podTerminated reports whether a pod finished (Succeeded or Failed) and so
// is not part of a current rollout.
func podTerminated(pod corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

// podReady reports whether the pod's Ready condition is True.
func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// containerImages lists the image of each container in order.
func containerImages(containers []corev1.Container) []string {
	images := make([]string, 0, len(containers))
	for _, container := range containers {
		images = append(images, container.Image)
	}
	return images
}

// ownedByDaemonSet reports whether a pod's owner references name the DaemonSet.
func ownedByDaemonSet(pod corev1.Pod, name string) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" && owner.Name == name {
			return true
		}
	}
	return false
}

// clusterPolicyDevicePluginConfig returns the device plugin ConfigMap name and
// its default key from the first ClusterPolicy's spec.devicePlugin.config.
// Shared GPU scenarios need both to read the configured replica count.
func clusterPolicyDevicePluginConfig(data []byte) (name, key string, err error) {
	policies, err := parseGPUOperatorClusterPolicies(data)
	if err != nil {
		return "", "", err
	}

	config := policies[0].Spec.DevicePlugin.Config
	if config.Name == "" || config.Default == "" {
		return "", "", fmt.Errorf("ClusterPolicy %q spec.devicePlugin.config name = %q, default = %q; both are needed to find the shared GPU config",
			policies[0].Metadata.Name, config.Name, config.Default)
	}
	return config.Name, config.Default, nil
}

// devicePluginSharingConfig holds the time-slicing part of a device plugin
// config file.
type devicePluginSharingConfig struct {
	Sharing struct {
		TimeSlicing struct {
			Resources []struct {
				Name     string `json:"name"`
				Replicas int    `json:"replicas"`
			} `json:"resources"`
		} `json:"timeSlicing"`
	} `json:"sharing"`
}

// timeSlicingGPUReplicas returns the nvidia.com/gpu time-slicing replica count
// from the device plugin config stored under key in a ConfigMap JSON object.
func timeSlicingGPUReplicas(configMap []byte, key string) (int, error) {
	var object corev1.ConfigMap
	if err := json.Unmarshal(configMap, &object); err != nil {
		return 0, fmt.Errorf("decode ConfigMap: %w", err)
	}

	text, ok := object.Data[key]
	if !ok {
		return 0, fmt.Errorf("ConfigMap %q has no key %q", object.Name, key)
	}

	var config devicePluginSharingConfig
	if err := yaml.Unmarshal([]byte(text), &config); err != nil {
		return 0, fmt.Errorf("ConfigMap %q key %q: decode device plugin config: %w", object.Name, key, err)
	}

	for _, resource := range config.Sharing.TimeSlicing.Resources {
		if resource.Name != string(gpuResourceName) {
			continue
		}
		if resource.Replicas < 1 {
			return 0, fmt.Errorf("ConfigMap %q key %q: %s replicas = %d, want at least 1", object.Name, key, gpuResourceName, resource.Replicas)
		}
		return resource.Replicas, nil
	}
	return 0, fmt.Errorf("ConfigMap %q key %q has no sharing.timeSlicing resource %q", object.Name, key, gpuResourceName)
}

// sharedGPUNodesError checks every GPU node (one with the gpu.count label):
// its gpu.replicas label equals replicas, and its nvidia.com/gpu capacity is
// gpu.count times replicas. At least one GPU node must exist.
func sharedGPUNodesError(nodes []byte, replicas int) error {
	var list corev1.NodeList
	if err := json.Unmarshal(nodes, &list); err != nil {
		return fmt.Errorf("decode node list: %w", err)
	}

	var errs []error
	gpuNodes := 0
	for _, node := range list.Items {
		countLabel, isGPUNode := node.Labels[gpuCountNodeLabel]
		if !isGPUNode {
			continue
		}
		gpuNodes++

		count, err := strconv.Atoi(countLabel)
		if err != nil {
			errs = append(errs, fmt.Errorf("node %q label %s = %q, want an integer", node.Name, gpuCountNodeLabel, countLabel))
			continue
		}

		if got := node.Labels[gpuReplicasNodeLabel]; got != strconv.Itoa(replicas) {
			errs = append(errs, fmt.Errorf("node %q label %s = %q, want %d from the device plugin config",
				node.Name, gpuReplicasNodeLabel, got, replicas))
		}

		want := int64(count * replicas)
		capacity := node.Status.Capacity[gpuResourceName]
		if capacity.Value() != want {
			errs = append(errs, fmt.Errorf("node %q capacity %s = %s, want %d (%s %d x %d replicas)",
				node.Name, gpuResourceName, capacity.String(), want, gpuCountNodeLabel, count, replicas))
		}
	}

	if gpuNodes == 0 {
		return fmt.Errorf("no node has the %s label", gpuCountNodeLabel)
	}
	return errors.Join(errs...)
}

// draResourcesError checks the NVIDIA DRA driver's published objects: the
// gpu.nvidia.com DeviceClass exists, and at least one gpu.nvidia.com
// ResourceSlice names an existing node in spec.nodeName and lists a device
// whose type attribute is "gpu". Inputs are `kubectl get -o json` lists.
func draResourcesError(deviceClasses, resourceSlices, nodes []byte) error {
	var classList resourcev1.DeviceClassList
	if err := json.Unmarshal(deviceClasses, &classList); err != nil {
		return fmt.Errorf("decode DeviceClass list: %w", err)
	}

	var sliceList resourcev1.ResourceSliceList
	if err := json.Unmarshal(resourceSlices, &sliceList); err != nil {
		return fmt.Errorf("decode ResourceSlice list: %w", err)
	}

	var nodeList corev1.NodeList
	if err := json.Unmarshal(nodes, &nodeList); err != nil {
		return fmt.Errorf("decode node list: %w", err)
	}

	var errs []error
	if !slices.ContainsFunc(classList.Items, func(class resourcev1.DeviceClass) bool { return class.Name == nvidiaDRADriver }) {
		errs = append(errs, fmt.Errorf("no DeviceClass named %q", nvidiaDRADriver))
	}

	// Find a GPU ResourceSlice published for a node in this cluster.
	nodeNames := map[string]bool{}
	for _, node := range nodeList.Items {
		nodeNames[node.Name] = true
	}
	var seen []string
	found := false
	for _, slice := range sliceList.Items {
		if slice.Spec.Driver != nvidiaDRADriver {
			continue
		}

		nodeName := ""
		if slice.Spec.NodeName != nil {
			nodeName = *slice.Spec.NodeName
		}
		gpus := countDRAGPUDevices(slice.Spec.Devices)
		seen = append(seen, fmt.Sprintf("%s(node=%q, gpus=%d)", slice.Name, nodeName, gpus))
		if nodeNames[nodeName] && gpus > 0 {
			found = true
		}
	}

	if !found {
		errs = append(errs, fmt.Errorf("no %s ResourceSlice names a cluster node and lists a GPU device; slices: %v",
			nvidiaDRADriver, seen))
	}
	return errors.Join(errs...)
}

// countDRAGPUDevices counts devices whose type attribute, plain or
// domain-qualified, is the string "gpu".
func countDRAGPUDevices(devices []resourcev1.Device) int {
	count := 0
	for _, device := range devices {
		for name, value := range device.Attributes {
			isType := name == "type" || strings.HasSuffix(string(name), "/type")
			if isType && value.StringValue != nil && *value.StringValue == "gpu" {
				count++
				break
			}
		}
	}
	return count
}

// boolField formats an optional boolean field for an error message.
func boolField(value *bool) string {
	if value == nil {
		return "unset"
	}
	return strconv.FormatBool(*value)
}

// clusterPolicyEmbeddedExporterError checks the first ClusterPolicy's
// spec.dcgmExporter: config.name is metricsConfig, enablePodLabels and
// enablePodUID are true, and podLabelAllowlistRegex includes allowRegex.
func clusterPolicyEmbeddedExporterError(data []byte, metricsConfig, allowRegex string) error {
	policies, err := parseGPUOperatorClusterPolicies(data)
	if err != nil {
		return err
	}

	policy := policies[0]
	exporter := policy.Spec.DCGMExporter
	var errs []error
	if exporter.Config.Name != metricsConfig {
		errs = append(errs, fmt.Errorf("ClusterPolicy %q spec.dcgmExporter.config.name = %q, want %q",
			policy.Metadata.Name, exporter.Config.Name, metricsConfig))
	}
	if exporter.EnablePodLabels == nil || !*exporter.EnablePodLabels {
		errs = append(errs, fmt.Errorf("ClusterPolicy %q spec.dcgmExporter.enablePodLabels = %s, want true",
			policy.Metadata.Name, boolField(exporter.EnablePodLabels)))
	}
	if exporter.EnablePodUID == nil || !*exporter.EnablePodUID {
		errs = append(errs, fmt.Errorf("ClusterPolicy %q spec.dcgmExporter.enablePodUID = %s, want true",
			policy.Metadata.Name, boolField(exporter.EnablePodUID)))
	}
	if !slices.Contains(exporter.PodLabelAllowlistRegex, allowRegex) {
		errs = append(errs, fmt.Errorf("ClusterPolicy %q spec.dcgmExporter.podLabelAllowlistRegex = %v, want it to include %q",
			policy.Metadata.Name, exporter.PodLabelAllowlistRegex, allowRegex))
	}
	return errors.Join(errs...)
}

// clusterPolicyRemoteDCGMError checks the first ClusterPolicy: spec.dcgm.enabled
// is true, so the exporter uses the standalone hostengine, and
// spec.dcgmExporter.config.name is metricsConfig.
func clusterPolicyRemoteDCGMError(data []byte, metricsConfig string) error {
	policies, err := parseGPUOperatorClusterPolicies(data)
	if err != nil {
		return err
	}

	policy := policies[0]
	var errs []error
	if policy.Spec.DCGM.Enabled == nil || !*policy.Spec.DCGM.Enabled {
		errs = append(errs, fmt.Errorf("ClusterPolicy %q spec.dcgm.enabled = %s, want true",
			policy.Metadata.Name, boolField(policy.Spec.DCGM.Enabled)))
	}
	if name := policy.Spec.DCGMExporter.Config.Name; name != metricsConfig {
		errs = append(errs, fmt.Errorf("ClusterPolicy %q spec.dcgmExporter.config.name = %q, want %q",
			policy.Metadata.Name, name, metricsConfig))
	}
	return errors.Join(errs...)
}

// gpuClusterReadyError checks `kubectl get gpucluster -o json`: at least one
// GPUCluster exists, and each has spec.dcgm.enabled true and status.state
// "ready".
func gpuClusterReadyError(data []byte) error {
	var list struct {
		Items []struct {
			Metadata metav1.ObjectMeta `json:"metadata"`
			Spec     struct {
				DCGM gpuOperatorDCGMSpec `json:"dcgm"`
			} `json:"spec"`
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("decode GPUCluster list: %w", err)
	}

	if len(list.Items) == 0 {
		return errors.New("no GPUCluster found")
	}

	var errs []error
	for _, cluster := range list.Items {
		if cluster.Spec.DCGM.Enabled == nil || !*cluster.Spec.DCGM.Enabled {
			errs = append(errs, fmt.Errorf("GPUCluster %q spec.dcgm.enabled = %s, want true",
				cluster.Metadata.Name, boolField(cluster.Spec.DCGM.Enabled)))
		}
		if cluster.Status.State != gpuOperatorReadyState {
			errs = append(errs, fmt.Errorf("GPUCluster %q status.state = %q, want %q",
				cluster.Metadata.Name, cluster.Status.State, gpuOperatorReadyState))
		}
	}
	return errors.Join(errs...)
}

// exporterContainerEnvError checks that a container of the exporter pod sets
// the environment variable name. An empty want accepts any non-empty value;
// otherwise the value must equal want or end with "-" followed by want, so a
// release-prefixed service name still matches and a longer name does not.
func exporterContainerEnvError(pod corev1.Pod, name, want string) error {
	var seen []string
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			if env.Name != name {
				continue
			}

			if env.Value != "" && (want == "" || (env.Value == want || strings.HasSuffix(env.Value, "-"+want))) {
				return nil
			}
			seen = append(seen, fmt.Sprintf("%s=%q", container.Name, env.Value))
		}
	}

	if want == "" {
		want = "any non-empty value"
	} else {
		want = strconv.Quote(want)
	}
	return fmt.Errorf("exporter pod %q env %s: containers %v, want %s", pod.Name, name, seen, want)
}
