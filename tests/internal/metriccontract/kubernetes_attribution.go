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

package metriccontract

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	dto "github.com/prometheus/client_model/go"
)

// Kubernetes attribution label names, as the exporter writes them without
// --use-old-namespace, and the labels that name a sample's MIG instance and
// NVLink. podLabelPrefix starts a pod label renamed to avoid a collision.
const (
	podLabel           = "pod"
	namespaceLabel     = "namespace"
	containerLabel     = "container"
	podUIDLabel        = "pod_uid"
	podLabelPrefix     = "pod_label_"
	migInstanceIDLabel = "GPU_I_ID"
	nvlinkLabel        = "nvlink"
)

// podAttributionLabels are every label Kubernetes attribution adds to a
// sample, apart from pod labels starting with podLabelPrefix. A sample of no
// GPU must carry none of them.
var podAttributionLabels = []string{containerLabel, namespaceLabel, podLabel, podUIDLabel}

// MIG instance labels. The exporter keys MIG instance samples by
// "<gpu>-<gpu instance>", which never matches a whole-GPU kubelet device ID.
var migInstanceLabels = []string{migInstanceIDLabel, "GPU_I_PROFILE"}

// PodAttribution is the workload identity Kubernetes attribution must put on
// the samples of the one GPU given to the pod.
type PodAttribution struct {
	// GPUUUID is the UUID nvidia-smi reports inside the pod.
	GPUUUID   string
	Pod       string
	Namespace string
	Container string
	// PodUID is the pod's metadata.uid. Empty skips the pod_uid check.
	PodUID string
	// ForbidPodUID requires the pod's GPU samples to carry no pod_uid, as
	// when pod UID collection is off.
	ForbidPodUID bool
}

// validate rejects an expectation without a GPU or pod identity. Fields are
// checked in a fixed order so the error is the same on every run.
func (a PodAttribution) validate() error {
	for _, field := range []struct{ name, value string }{
		{name: "GPU UUID", value: a.GPUUUID},
		{name: "pod", value: a.Pod},
		{name: "namespace", value: a.Namespace},
		{name: "container", value: a.Container},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("pod attribution expectation is missing the %s", field.name)
		}
	}
	return nil
}

// ValidatePodAttribution checks that Kubernetes attribution names exactly the
// pod's GPU: every whole-GPU DCGM_FI_ sample of that GPU carries the pod,
// namespace and container (and the pod_uid rule), no sample of another GPU
// (GPU NVLink samples included) names the pod, and no sample without a GPU
// UUID (NVSwitch, NVSwitch link, CPU and CPU core) carries pod labels.
func ValidatePodAttribution(families map[string]*dto.MetricFamily, want PodAttribution) error {
	if err := want.validate(); err != nil {
		return err
	}

	samples := attributionSamplesByUUID(families)
	own := wholeGPUFieldSamples(samples[want.GPUUUID])
	if len(own) == 0 {
		return fmt.Errorf("the exporter reports no whole-GPU DCGM_FI_ sample for the pod's GPU %s", want.GPUUUID)
	}

	// Each whole-GPU sample of the pod's GPU must name the pod.
	var problems []string
	for _, sample := range own {
		if wrong := wrongPodLabels(sample.labels, want); wrong != "" {
			problems = append(problems, fmt.Sprintf("GPU %s %s: %s", want.GPUUUID, sample.family, wrong))
		}
	}

	problems = append(problems, gpusNamingPod(samples, want.Pod, samplesOfGPU(want.GPUUUID))...)
	problems = append(problems, nonGPUSamplesWithPodLabels(families)...)
	return attributionError(want, problems)
}

// ValidatePodNotAttributed checks that the exporter reports the pod's GPU,
// that no sample of any GPU (GPU NVLink samples included) names the pod, and
// that no sample without a GPU UUID (NVSwitch, NVSwitch link, CPU and CPU
// core) carries pod labels. It is the expected outcome when the exporter
// cannot match the kubelet's device IDs to its GPUs.
func ValidatePodNotAttributed(families map[string]*dto.MetricFamily, want PodAttribution) error {
	if err := want.validate(); err != nil {
		return err
	}

	samples := attributionSamplesByUUID(families)
	if len(wholeGPUFieldSamples(samples[want.GPUUUID])) == 0 {
		return fmt.Errorf("the exporter reports no whole-GPU DCGM_FI_ sample for the pod's GPU %s", want.GPUUUID)
	}

	problems := gpusNamingPod(samples, want.Pod, nil)
	problems = append(problems, nonGPUSamplesWithPodLabels(families)...)
	return attributionError(want, problems)
}

// MIGPodAttribution is the workload identity Kubernetes attribution must put
// on the samples of the one MIG instance given to the pod. GPUUUID is the
// instance's parent GPU.
type MIGPodAttribution struct {
	PodAttribution
	// GPUInstanceID is the GPU_I_ID of the pod's MIG instance.
	GPUInstanceID string
}

// validate rejects an expectation without a GPU, MIG instance or pod identity.
func (a MIGPodAttribution) validate() error {
	if err := a.PodAttribution.validate(); err != nil {
		return err
	}

	if strings.TrimSpace(a.GPUInstanceID) == "" {
		return errors.New("pod attribution expectation is missing the GPU instance ID")
	}
	return nil
}

// ValidateMIGPodAttribution checks that Kubernetes attribution names exactly
// the pod's MIG instance: every DCGM_FI_ sample of that instance (matched by
// parent GPU UUID and GPU_I_ID) carries the pod, namespace and container (and
// the pod_uid rule), no other GPU, GPU NVLink or MIG instance sample names the
// pod, and no sample without a GPU UUID (NVSwitch, NVSwitch link, CPU and CPU
// core) carries pod labels.
func ValidateMIGPodAttribution(families map[string]*dto.MetricFamily, want MIGPodAttribution) error {
	if err := want.validate(); err != nil {
		return err
	}

	samples := attributionSamplesByUUID(families)
	own := migInstanceFieldSamples(samples[want.GPUUUID], want.GPUInstanceID)
	if len(own) == 0 {
		return fmt.Errorf("the exporter reports no DCGM_FI_ sample for the pod's MIG instance %s on GPU %s",
			want.GPUInstanceID, want.GPUUUID)
	}

	// Each sample of the pod's MIG instance must name the pod.
	var problems []string
	for _, sample := range own {
		if wrong := wrongPodLabels(sample.labels, want.PodAttribution); wrong != "" {
			problems = append(problems, fmt.Sprintf("GPU %s instance %s %s: %s",
				want.GPUUUID, want.GPUInstanceID, sample.family, wrong))
		}
	}

	// GPU NVLink samples have no GPU_I_ID, so they are never the instance's own.
	ofInstance := func(uuid string, sample gpuSample) bool {
		return uuid == want.GPUUUID && sample.labels[migInstanceIDLabel] == want.GPUInstanceID
	}
	problems = append(problems, gpusNamingPod(samples, want.Pod, ofInstance)...)
	problems = append(problems, nonGPUSamplesWithPodLabels(families)...)
	return attributionError(want.PodAttribution, problems)
}

// migInstanceFieldSamples keeps the DCGM field samples of one MIG instance.
func migInstanceFieldSamples(samples []gpuSample, gpuInstanceID string) []gpuSample {
	var kept []gpuSample
	for _, sample := range samples {
		if strings.HasPrefix(sample.family, "DCGM_FI_") && sample.labels[migInstanceIDLabel] == gpuInstanceID {
			kept = append(kept, sample)
		}
	}
	return kept
}

// samplesOfGPU returns a sample filter that matches every sample of the GPU uuid.
func samplesOfGPU(uuid string) func(string, gpuSample) bool {
	return func(sampleUUID string, _ gpuSample) bool {
		return sampleUUID == uuid
	}
}

// attributionSamplesByUUID groups every sample that names a GPU by that GPU's
// UUID, including GPU NVLink samples, which name it only in gpu_uuid.
func attributionSamplesByUUID(families map[string]*dto.MetricFamily) map[string][]gpuSample {
	samples := map[string][]gpuSample{}
	for _, name := range slices.Sorted(maps.Keys(families)) {
		for _, metric := range families[name].GetMetric() {
			labels := metricLabels(metric)
			uuid := sampleGPUUUID(labels)
			if uuid == "" {
				continue
			}

			samples[uuid] = append(samples[uuid], gpuSample{family: name, labels: labels})
		}
	}
	return samples
}

// nonGPUSamplesWithPodLabels lists, one entry per family, the samples without
// a GPU UUID label (NVSwitch, NVSwitch link, CPU and CPU core samples) that carry any
// pod attribution label, whichever pod it names. Each entry shows the first
// such sample and its pod labels.
func nonGPUSamplesWithPodLabels(families map[string]*dto.MetricFamily) []string {
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(families)) {
		count, first := 0, ""
		for _, metric := range families[name].GetMetric() {
			labels := metricLabels(metric)
			if sampleGPUUUID(labels) != "" {
				continue
			}

			podLabels, entityLabels := splitPodAttributionLabels(labels)
			if len(podLabels) == 0 {
				continue
			}

			count++
			if first == "" {
				first = fmt.Sprintf("{%s}: %s", strings.Join(entityLabels, ","), strings.Join(podLabels, ", "))
			}
		}

		if count > 0 {
			problems = append(problems, fmt.Sprintf("%s: %d sample(s) without a GPU UUID carry pod labels, first %s",
				name, count, first))
		}
	}
	return problems
}

// splitPodAttributionLabels returns a sample's pod attribution labels and its
// other labels, each as sorted name="value" text.
func splitPodAttributionLabels(labels map[string]string) (podLabels, otherLabels []string) {
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		text := fmt.Sprintf("%s=%q", name, labels[name])
		if slices.Contains(podAttributionLabels, name) || strings.HasPrefix(name, podLabelPrefix) {
			podLabels = append(podLabels, text)
			continue
		}
		otherLabels = append(otherLabels, text)
	}
	return podLabels, otherLabels
}

// wholeGPUFieldSamples keeps the DCGM field samples of the whole GPU, leaving
// out exporter-owned families, MIG instance samples and GPU NVLink samples.
func wholeGPUFieldSamples(samples []gpuSample) []gpuSample {
	var kept []gpuSample
	for _, sample := range samples {
		if strings.HasPrefix(sample.family, "DCGM_FI_") && !isMIGInstanceSample(sample.labels) &&
			sample.labels[nvlinkLabel] == "" {
			kept = append(kept, sample)
		}
	}
	return kept
}

// isMIGInstanceSample reports whether the labels name a MIG instance.
func isMIGInstanceSample(labels map[string]string) bool {
	for _, name := range migInstanceLabels {
		if labels[name] != "" {
			return true
		}
	}
	return false
}

// wrongPodLabels describes each pod label the sample lacks or carries with
// another value, or returns "" when all match.
func wrongPodLabels(labels map[string]string, want PodAttribution) string {
	expected := []struct{ name, value string }{
		{name: containerLabel, value: want.Container},
		{name: namespaceLabel, value: want.Namespace},
		{name: podLabel, value: want.Pod},
	}
	if want.PodUID != "" {
		expected = append(expected, struct{ name, value string }{name: podUIDLabel, value: want.PodUID})
	}

	var wrong []string
	for _, label := range expected {
		if labels[label.name] != label.value {
			wrong = append(wrong, fmt.Sprintf("%s=%q, want %q", label.name, labels[label.name], label.value))
		}
	}

	if _, present := labels[podUIDLabel]; want.ForbidPodUID && present {
		wrong = append(wrong, fmt.Sprintf("%s=%q, want none", podUIDLabel, labels[podUIDLabel]))
	}
	return strings.Join(wrong, ", ")
}

// gpusNamingPod lists, one entry per GPU, the families in which a sample has a
// pod label equal to pod, in any namespace. Samples for which allowed returns
// true may name the pod and are skipped; a nil allowed skips none.
func gpusNamingPod(
	samples map[string][]gpuSample, pod string, allowed func(uuid string, sample gpuSample) bool,
) []string {
	var problems []string
	for _, uuid := range slices.Sorted(maps.Keys(samples)) {
		families := map[string]bool{}
		for _, sample := range samples[uuid] {
			if allowed != nil && allowed(uuid, sample) {
				continue
			}
			if sample.labels[podLabel] == pod {
				families[sample.family] = true
			}
		}
		if len(families) == 0 {
			continue
		}

		familyList := strings.Join(slices.Sorted(maps.Keys(families)), ", ")
		problems = append(problems, fmt.Sprintf("GPU %s names the pod in %s", uuid, familyList))
	}
	return problems
}

// attributionError joins the problems into one error that names the pod, or
// returns nil when there are none.
func attributionError(want PodAttribution, problems []string) error {
	if len(problems) == 0 {
		return nil
	}

	slices.Sort(problems)
	return fmt.Errorf("kubernetes attribution of pod %s/%s is wrong in %d place(s):\n%s",
		want.Namespace, want.Pod, len(problems), strings.Join(problems, "\n"))
}
