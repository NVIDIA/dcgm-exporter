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

package transformation

import (
	"log/slog"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
)

// Process enriches a scrape with pod identity, selected pod labels, and DRA
// attributes. It reads one pod-resource snapshot, then delegates each output
// policy to the virtual-GPU, dedicated-device, or DRA helper that owns it.
func (p *PodMapper) Process(metrics collector.MetricsByCounter, deviceInfo deviceinfo.Provider) error {
	deviceToPods, deviceToPod, deviceToPodsDRA, err := p.getMappings(deviceInfo)
	if err != nil {
		p.logMappingError(err)
		return nil
	}

	getMetricGroup := metricGroupGetter(deviceInfo)
	if p.Config.KubernetesVirtualGPUs {
		return p.enrichVirtualGPUMetrics(metrics, deviceToPods, deviceToPodsDRA, deviceInfo, getMetricGroup)
	} else {
		if err := p.enrichDedicatedMetrics(metrics, deviceToPod, getMetricGroup); err != nil {
			return err
		}
	}
	return p.enrichDRAMetrics(metrics, deviceToPodsDRA, getMetricGroup)
}

// logMappingError reports a failed pod-resource snapshot without failing the
// scrape. Kubernetes enrichment is best effort, so callers still render the
// unlabelled metrics when kubelet is unavailable. The failure repeats on every
// scrape, so it is logged at most once per interval.
func (p *PodMapper) logMappingError(err error) {
	// An oversized response is its own case with its own warning.
	if status.Code(err) == codes.ResourceExhausted {
		if p.oversizeWarnings.allow() {
			slog.Warn(
				"Kubelet pod-resources response exceeded gRPC receive limit; pod metric labels are missing "+
					"while the response exceeds the limit (checked every scrape, warning repeated at most every 5 minutes)",
				"limit_bytes", kubeletPodResourcesMaxRecvMsgSize,
				"error", err,
			)
		}

		return
	}

	if p.unavailableWarnings.allow() {
		slog.Warn("Failed to get pod mappings", "error", err)
	}
}

// metricGroupGetter returns the current device entity group without repeatedly
// querying the provider while metrics from one scrape are enriched.
func metricGroupGetter(deviceInfo deviceinfo.Provider) func() dcgm.Field_Entity_Group {
	metricGroup := dcgm.FE_NONE
	loadedMetricGroup := false
	return func() dcgm.Field_Entity_Group {
		if !loadedMetricGroup && deviceInfo != nil {
			metricGroup = deviceInfo.InfoType()
			loadedMetricGroup = true
		}
		return metricGroup
	}
}

// metricBelongsToGPU reports whether a metric of entity group group belongs to
// a GPU or a MIG instance, and so may name the pod that holds that device. A
// link metric belongs to a GPU only when its parent is a GPU; NVSwitch, NVSwitch
// link, CPU and CPU core metrics never name a pod, whatever their device ID.
func metricBelongsToGPU(metric collector.Metric, group dcgm.Field_Entity_Group) bool {
	switch group {
	case dcgm.FE_GPU, dcgm.FE_GPU_I, dcgm.FE_GPU_CI:
		return true
	case dcgm.FE_LINK:
		return metric.ParentType == dcgm.FE_GPU
	default:
		return false
	}
}

// enrichVirtualGPUMetrics replaces each allocated device metric with one metric
// per sharing pod and appends independent DRA allocations from the original
// metric. Unallocated device metrics and usable per-process data are preserved.
func (p *PodMapper) enrichVirtualGPUMetrics(
	metrics collector.MetricsByCounter,
	deviceToPods map[string][]PodInfo,
	deviceToPodsDRA map[string][]PodInfo,
	deviceInfo deviceinfo.Provider,
	getMetricGroup func() dcgm.Field_Entity_Group,
) error {
	if deviceToPods == nil {
		return p.enrichDRAMetrics(metrics, deviceToPodsDRA, getMetricGroup)
	}
	slog.Debug("Device to sharing pods mapping", "mapping", deviceToPods)

	gpuUUIDToDeviceID := getGPUUUIDToDeviceID(deviceInfo, p.Config.KubernetesGPUIdType)
	processCollector := &perProcessCollector{
		client:    nvmlprovider.Client(),
		pidMapper: newPIDToPodMapper(),
	}
	perProcessData := processCollector.Collect(gpuUUIDToDeviceID, deviceToPods, deviceInfo)

	for counter := range metrics {
		enriched, err := p.enrichVirtualGPUCounter(
			metrics[counter], counter, deviceToPods, deviceToPodsDRA, perProcessData, getMetricGroup,
		)
		if err != nil {
			return err
		}
		if len(enriched) > 0 {
			metrics[counter] = enriched
		}
	}
	return nil
}

// enrichVirtualGPUCounter builds one counter's sharing and per-process samples,
// preserving the original samples for devices without an allocation.
func (p *PodMapper) enrichVirtualGPUCounter(
	metrics []collector.Metric,
	counter counters.Counter,
	deviceToPods map[string][]PodInfo,
	deviceToPodsDRA map[string][]PodInfo,
	perProcessData *perProcessDataMap,
	getMetricGroup func() dcgm.Field_Entity_Group,
) ([]collector.Metric, error) {
	var enriched []collector.Metric
	for _, metric := range metrics {
		if !metricBelongsToGPU(metric, getMetricGroup()) {
			enriched = append(enriched, metric)
			continue
		}

		deviceID, err := metric.GetIDOfType(p.Config.KubernetesGPUIdType)
		if err != nil {
			return nil, err
		}
		draPods := p.draPodsForDevice(deviceToPodsDRA, deviceID)
		enriched = append(enriched, p.draMetricCopies(metric, draPods, getMetricGroup)...)

		sharedMetrics, err := p.enrichVirtualGPUDeviceMetric(
			metric,
			counter,
			deviceToPods[deviceID],
			len(draPods) > 0,
			perProcessData,
			getMetricGroup,
		)
		if err != nil {
			return nil, err
		}
		enriched = append(enriched, sharedMetrics...)
	}
	return enriched, nil
}

// draPodsForDevice returns DRA allocations when DRA enrichment is enabled.
func (p *PodMapper) draPodsForDevice(deviceToPods map[string][]PodInfo, deviceID string) []PodInfo {
	if !p.Config.KubernetesEnableDRA {
		return nil
	}
	return deviceToPods[deviceID]
}

// draMetricCopies adds one DRA-enriched copy for each allocation of metric's device.
func (p *PodMapper) draMetricCopies(
	metric collector.Metric,
	podInfos []PodInfo,
	getMetricGroup func() dcgm.Field_Entity_Group,
) []collector.Metric {
	var enriched []collector.Metric
	for _, podInfo := range podInfos {
		podMetric := cloneMetric(metric)
		p.addDRAPodAttributes(&podMetric, podInfo, getMetricGroup)
		enriched = append(enriched, podMetric)
	}
	return enriched
}

// enrichVirtualGPUDeviceMetric adds sharing and per-process samples from one device.
func (p *PodMapper) enrichVirtualGPUDeviceMetric(
	metric collector.Metric,
	counter counters.Counter,
	podInfos []PodInfo,
	hasDRAAllocation bool,
	perProcessData *perProcessDataMap,
	getMetricGroup func() dcgm.Field_Entity_Group,
) ([]collector.Metric, error) {
	if isPerProcessMetric(counter.FieldName) {
		perProcessMetrics, err := p.createPerProcessMetrics(metric, counter, metric, perProcessData, getMetricGroup)
		if err != nil {
			return nil, err
		}
		if perProcessMetrics != nil {
			return append([]collector.Metric{metric}, perProcessMetrics...), nil
		}
	}

	var enriched []collector.Metric
	for _, podInfo := range podInfos {
		podMetric := cloneMetric(metric)
		p.addPodAttributes(&podMetric, podInfo, getMetricGroup, true)
		enriched = append(enriched, podMetric)
	}
	if len(podInfos) == 0 && !hasDRAAllocation {
		enriched = append(enriched, metric)
	}
	return enriched, nil
}

// enrichDedicatedMetrics adds one pod's identity to each metric from a device
// plugin allocation. Metrics without a matching allocation remain unchanged.
func (p *PodMapper) enrichDedicatedMetrics(
	metrics collector.MetricsByCounter,
	deviceToPod map[string]PodInfo,
	getMetricGroup func() dcgm.Field_Entity_Group,
) error {
	if deviceToPod == nil {
		return nil
	}
	slog.Debug("Device to pod mapping", "mapping", deviceToPod)

	for counter := range metrics {
		for index, metric := range metrics[counter] {
			podInfo, ok, err := p.dedicatedPodFor(metric, deviceToPod, getMetricGroup())
			if err != nil {
				return err
			}
			if !ok {
				continue
			}

			if metric.Source != nil {
				metrics[counter][index] = cloneMetric(metric)
			}
			p.addPodAttributes(&metrics[counter][index], podInfo, getMetricGroup, false)
		}
	}
	return nil
}

// dedicatedPodFor returns the pod allocated the device of metric, and false
// when no pod holds it or the metric belongs to no GPU.
func (p *PodMapper) dedicatedPodFor(
	metric collector.Metric, deviceToPod map[string]PodInfo, group dcgm.Field_Entity_Group,
) (PodInfo, bool, error) {
	if !metricBelongsToGPU(metric, group) {
		return PodInfo{}, false, nil
	}

	deviceID, err := metric.GetIDOfType(p.Config.KubernetesGPUIdType)
	if err != nil {
		return PodInfo{}, false, err
	}

	podInfo, ok := deviceToPod[deviceID]
	return podInfo, ok, nil
}

// enrichDRAMetrics duplicates metrics for every DRA allocation and adds the
// claim and device attributes that distinguish one dynamic allocation from
// another. It preserves the original metric when no DRA allocation matches.
func (p *PodMapper) enrichDRAMetrics(
	metrics collector.MetricsByCounter,
	deviceToPods map[string][]PodInfo,
	getMetricGroup func() dcgm.Field_Entity_Group,
) error {
	if !p.Config.KubernetesEnableDRA || deviceToPods == nil {
		return nil
	}
	slog.Debug("Device to pod mapping for DRA", "mapping", deviceToPods)

	for counter := range metrics {
		enriched, err := p.enrichDRACounter(metrics[counter], deviceToPods, getMetricGroup)
		if err != nil {
			return err
		}
		if len(enriched) > 0 {
			metrics[counter] = enriched
		}
	}
	return nil
}

// enrichDRACounter copies one counter's samples for each matching DRA pod,
// keeping samples that have no DRA allocation unchanged.
func (p *PodMapper) enrichDRACounter(
	metrics []collector.Metric,
	deviceToPods map[string][]PodInfo,
	getMetricGroup func() dcgm.Field_Entity_Group,
) ([]collector.Metric, error) {
	var enriched []collector.Metric
	for _, metric := range metrics {
		if !metricBelongsToGPU(metric, getMetricGroup()) {
			enriched = append(enriched, metric)
			continue
		}

		deviceID, err := metric.GetIDOfType(p.Config.KubernetesGPUIdType)
		if err != nil {
			return nil, err
		}
		podInfos := deviceToPods[deviceID]
		if podInfos == nil {
			enriched = append(enriched, metric)
			continue
		}
		for _, podInfo := range podInfos {
			podMetric := cloneMetric(metric)
			p.addDRAPodAttributes(&podMetric, podInfo, getMetricGroup)
			enriched = append(enriched, podMetric)
		}
	}
	return enriched, nil
}

// addPodAttributes adds common pod identity and labels to one metric. The
// includeVGPU flag is used only by sharing allocations, where the pod mapping
// carries a virtual-GPU slot as an extra attribute.
func (p *PodMapper) addPodAttributes(
	metric *collector.Metric,
	podInfo PodInfo,
	getMetricGroup func() dcgm.Field_Entity_Group,
	includeVGPU bool,
) {
	p.addPodIdentityAttributes(metric, podInfo)
	if includeVGPU && podInfo.VGPU != "" {
		metric.Attributes[vgpuAttribute] = podInfo.VGPU
	}
	if len(podInfo.Labels) > 0 {
		copyPodLabels(metric, podInfo.Labels, getMetricGroup())
	}
	removeAttributeLabelCollisions(metric)
}

// addDRAPodAttributes adds pod identity and dynamic-resource attributes before
// copying pod labels. That order lets a pod label that collides with a DRA
// attribute receive the established pod_label_ fallback name.
func (p *PodMapper) addDRAPodAttributes(
	metric *collector.Metric,
	podInfo PodInfo,
	getMetricGroup func() dcgm.Field_Entity_Group,
) {
	p.addPodIdentityAttributes(metric, podInfo)
	addDRAAttributes(metric, podInfo.DynamicResources)
	if len(podInfo.Labels) > 0 {
		copyPodLabels(metric, podInfo.Labels, getMetricGroup())
	}
	removeAttributeLabelCollisions(metric)
}

// addDRAAttributes adds the dynamic-resource identity that is only meaningful
// for a DRA allocation. A nil allocation intentionally adds nothing.
func addDRAAttributes(metric *collector.Metric, resource *DynamicResourceInfo) {
	if resource == nil {
		return
	}
	metric.Attributes[draClaimName] = resource.ClaimName
	metric.Attributes[draClaimNamespace] = resource.ClaimNamespace
	metric.Attributes[draDriverName] = resource.DriverName
	metric.Attributes[draPoolName] = resource.PoolName
	metric.Attributes[draDeviceName] = resource.DeviceName

	if resource.MIGInfo == nil {
		return
	}
	metric.Attributes[draMigProfile] = resource.MIGInfo.Profile
	if resource.MIGInfo.MIGDeviceUUID != "" {
		metric.Attributes[draMigDeviceUUID] = resource.MIGInfo.MIGDeviceUUID
	}
}

// removeAttributeLabelCollisions makes attributes the single owner of an
// emitted label name. This preserves the renderer's existing collision rule.
func removeAttributeLabelCollisions(metric *collector.Metric) {
	for key := range metric.Attributes {
		delete(metric.Labels, key)
	}
}
