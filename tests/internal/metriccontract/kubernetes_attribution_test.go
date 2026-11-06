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
	"fmt"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// podLabels is the pod, namespace, container and pod_uid label text of the
// expected workload.
const podLabels = `pod="cuda-workload",namespace="e2e",container="cuda-workload",pod_uid="uid-1"`

// podLabelsWithoutUID is podLabels without pod_uid, as when pod UID
// collection is off.
const podLabelsWithoutUID = `pod="cuda-workload",namespace="e2e",container="cuda-workload"`

// Non-GPU samples shaped like the exporter's output: a Grace CPU, one of its
// cores, an NVSwitch and one of its links. None has a GPU UUID label.
const (
	cpuSampleLabels    = `cpu="0",cpu_serial="0x1",hostname="node-1"`
	coreSampleLabels   = `cpucore="5",cpu="0",hostname="node-1"`
	switchSampleLabels = `nvswitch="nvswitch0",hostname="node-1"`
	linkSampleLabels   = `nvlink="3",nvswitch="nvswitch0",hostname="node-1"`
)

// attributionPod is the workload identity the attribution fixtures expect.
var attributionPod = PodAttribution{
	GPUUUID:   "GPU-1",
	Pod:       "cuda-workload",
	Namespace: "e2e",
	Container: "cuda-workload",
	PodUID:    "uid-1",
}

// attributionSample is one fixture sample: a family, a GPU and extra labels.
// An empty uuid renders a sample without a GPU UUID label, as the exporter
// writes NVSwitch, NVSwitch link, CPU and CPU core samples.
type attributionSample struct {
	family string
	uuid   string
	labels string
}

// attributionScrape renders the samples as Prometheus text and parses it.
func attributionScrape(t *testing.T, samples ...attributionSample) map[string]*dto.MetricFamily {
	t.Helper()

	var text strings.Builder
	typed := map[string]bool{}
	for _, sample := range samples {
		if !typed[sample.family] {
			fmt.Fprintf(&text, "# TYPE %s gauge\n", sample.family)
			typed[sample.family] = true
		}
		labels := sample.labels
		if sample.uuid != "" {
			labels = strings.Trim(fmt.Sprintf("UUID=%q,%s", sample.uuid, sample.labels), ",")
		}
		fmt.Fprintf(&text, "%s{%s} 1\n", sample.family, labels)
	}

	families, err := ParseText([]byte(text.String()))
	require.NoError(t, err)
	return families
}

// requireAttributionResult checks that err is nil when wantErr is empty and
// otherwise contains every part of wantErr.
func requireAttributionResult(t *testing.T, err error, wantErr []string) {
	t.Helper()

	if len(wantErr) == 0 {
		require.NoError(t, err)
		return
	}

	require.Error(t, err)
	for _, part := range wantErr {
		require.Contains(t, err.Error(), part)
	}
}

// TestValidatePodAttribution covers exact attribution, MIG instance samples,
// the pod_uid rules, other GPUs that name the pod, and incomplete
// expectations.
func TestValidatePodAttribution(t *testing.T) {
	withoutPodUID := attributionPod
	withoutPodUID.PodUID = ""
	forbidPodUID := withoutPodUID
	forbidPodUID.ForbidPodUID = true

	tests := []struct {
		name    string
		want    PodAttribution
		samples []attributionSample
		wantErr []string
	}{
		{
			name: "only the pod's GPU names the pod",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-0"},
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_FI_DEV_POWER_USAGE", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-2", labels: `pod="other",namespace="e2e",container="c"`},
			},
		},
		{
			name: "samples outside DCGM field families need no pod",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_EXP_XID_ERRORS_COUNT", uuid: "GPU-1"},
			},
		},
		{
			name: "MIG instance samples of the pod's GPU need no pod",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_FI_PROF_GR_ENGINE_ACTIVE", uuid: "GPU-1", labels: `GPU_I_ID="3",GPU_I_PROFILE="1g.10gb"`},
			},
		},
		{
			name: "non-GPU samples without pod labels",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_FI_DEV_CPU_UTIL_TOTAL", labels: cpuSampleLabels},
				{family: "DCGM_FI_DEV_CPU_UTIL_TOTAL", labels: coreSampleLabels},
				{family: "DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT", labels: switchSampleLabels},
				{family: "DCGM_FI_DEV_NVSWITCH_LINK_THROUGHPUT_TX", labels: linkSampleLabels},
			},
		},
		{
			name: "the pod's GPU's NVLink sample names the pod through its gpu_uuid label",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL", labels: `nvlink="0",gpu="1",gpu_uuid="GPU-1",` + podLabels},
			},
		},
		{
			name: "another GPU's NVLink sample names the pod",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL", labels: `nvlink="0",gpu="0",gpu_uuid="GPU-0",` + podLabels},
			},
			wantErr: []string{"wrong in 1 place(s)", "GPU GPU-0 names the pod in DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL"},
		},
		{
			name: "an old-namespace sample with a lowercase uuid label is a GPU sample",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_FI_DEV_POWER_USAGE", labels: `uuid="GPU-1",` + podLabels},
			},
		},
		{
			name: "CPU and CPU core samples carry the pod's labels",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_FI_DEV_CPU_UTIL_TOTAL", labels: cpuSampleLabels + "," + podLabels},
				{family: "DCGM_FI_DEV_CPU_UTIL_TOTAL", labels: coreSampleLabels + "," + podLabels},
			},
			wantErr: []string{
				"wrong in 1 place(s)",
				`DCGM_FI_DEV_CPU_UTIL_TOTAL: 2 sample(s) without a GPU UUID carry pod labels, first {cpu="0",cpu_serial="0x1",hostname="node-1"}: container="cuda-workload", namespace="e2e", pod="cuda-workload", pod_uid="uid-1"`,
			},
		},
		{
			name: "NVSwitch and link samples carry another pod's labels",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT", labels: switchSampleLabels + `,pod="other",namespace="e2e",container="c"`},
				{family: "DCGM_FI_DEV_NVSWITCH_LINK_THROUGHPUT_TX", labels: linkSampleLabels + `,pod_label_team="gpu"`},
			},
			wantErr: []string{
				"wrong in 2 place(s)",
				`DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT: 1 sample(s) without a GPU UUID carry pod labels, first {hostname="node-1",nvswitch="nvswitch0"}: container="c", namespace="e2e", pod="other"`,
				`DCGM_FI_DEV_NVSWITCH_LINK_THROUGHPUT_TX: 1 sample(s) without a GPU UUID carry pod labels, first {hostname="node-1",nvlink="3",nvswitch="nvswitch0"}: pod_label_team="gpu"`,
			},
		},
		{
			name: "pod_uid is not checked without a pod UID",
			want: withoutPodUID,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabelsWithoutUID + `,pod_uid="uid-9"`},
			},
		},
		{
			name: "pod_uid is absent when it is forbidden",
			want: forbidPodUID,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabelsWithoutUID},
			},
		},
		{
			name: "pod_uid is present when it is forbidden",
			want: forbidPodUID,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
			},
			wantErr: []string{`GPU GPU-1 DCGM_FI_DEV_GPU_TEMP: pod_uid="uid-1", want none`},
		},
		{
			name: "an empty pod_uid label is present when it is forbidden",
			want: forbidPodUID,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabelsWithoutUID + `,pod_uid=""`},
			},
			wantErr: []string{`GPU GPU-1 DCGM_FI_DEV_GPU_TEMP: pod_uid="", want none`},
		},
		{
			name: "a sample of the pod's GPU lacks the pod",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
				{family: "DCGM_FI_DEV_POWER_USAGE", uuid: "GPU-1"},
			},
			wantErr: []string{
				"wrong in 1 place(s)",
				`GPU GPU-1 DCGM_FI_DEV_POWER_USAGE: container="", want "cuda-workload", namespace="", want "e2e", pod="", want "cuda-workload", pod_uid="", want "uid-1"`,
			},
		},
		{
			name: "another GPU also names the pod",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-0", labels: podLabels},
				{family: "DCGM_FI_DEV_POWER_USAGE", uuid: "GPU-0", labels: podLabels},
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
			},
			wantErr: []string{"GPU GPU-0 names the pod in DCGM_FI_DEV_GPU_TEMP, DCGM_FI_DEV_POWER_USAGE"},
		},
		{
			name: "another GPU names a pod of the same name in another namespace",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-0", labels: `pod="cuda-workload",namespace="other",container="cuda-workload"`},
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
			},
			wantErr: []string{"GPU GPU-0 names the pod in DCGM_FI_DEV_GPU_TEMP"},
		},
		{
			name: "pod_uid differs from metadata.uid",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: `pod="cuda-workload",namespace="e2e",container="cuda-workload",pod_uid="uid-2"`},
			},
			wantErr: []string{`GPU GPU-1 DCGM_FI_DEV_GPU_TEMP: pod_uid="uid-2", want "uid-1"`},
		},
		{
			name: "the pod's GPU is not reported",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-0"},
			},
			wantErr: []string{"the exporter reports no whole-GPU DCGM_FI_ sample for the pod's GPU GPU-1"},
		},
		{
			name: "the pod's GPU has only MIG instance samples",
			want: attributionPod,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: `GPU_I_ID="3"`},
			},
			wantErr: []string{"the exporter reports no whole-GPU DCGM_FI_ sample for the pod's GPU GPU-1"},
		},
		{
			name:    "expectation without a GPU",
			want:    PodAttribution{Pod: "p", Namespace: "n", Container: "c"},
			samples: []attributionSample{{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1"}},
			wantErr: []string{"pod attribution expectation is missing the GPU UUID"},
		},
		{
			name:    "expectation without a pod",
			want:    PodAttribution{GPUUUID: "GPU-1", Namespace: "n", Container: "c"},
			samples: []attributionSample{{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1"}},
			wantErr: []string{"pod attribution expectation is missing the pod"},
		},
		{
			name:    "expectation without a namespace",
			want:    PodAttribution{GPUUUID: "GPU-1", Pod: "p", Container: "c"},
			samples: []attributionSample{{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1"}},
			wantErr: []string{"pod attribution expectation is missing the namespace"},
		},
		{
			name:    "expectation without a container",
			want:    PodAttribution{GPUUUID: "GPU-1", Pod: "p", Namespace: "n"},
			samples: []attributionSample{{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1"}},
			wantErr: []string{"pod attribution expectation is missing the container"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePodAttribution(attributionScrape(t, tc.samples...), tc.want)
			requireAttributionResult(t, err, tc.wantErr)
		})
	}
}

// TestValidatePodNotAttributed covers the outcome when the exporter cannot
// match the kubelet's device IDs: the GPU is reported and no sample names the pod.
func TestValidatePodNotAttributed(t *testing.T) {
	tests := []struct {
		name    string
		samples []attributionSample
		wantErr []string
	}{
		{
			name: "no GPU names the pod",
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-0", labels: `pod="other",namespace="e2e",container="c"`},
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1"},
			},
		},
		{
			name: "the pod's GPU names the pod",
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1", labels: podLabels},
			},
			wantErr: []string{"GPU GPU-1 names the pod in DCGM_FI_DEV_GPU_TEMP"},
		},
		{
			name: "non-GPU samples without pod labels",
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1"},
				{family: "DCGM_FI_DEV_CPU_UTIL_TOTAL", labels: cpuSampleLabels},
				{family: "DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT", labels: switchSampleLabels},
			},
		},
		{
			name: "a GPU NVLink sample names the pod",
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1"},
				{family: "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL", labels: `nvlink="0",gpu="0",gpu_uuid="GPU-0",` + podLabels},
			},
			wantErr: []string{"GPU GPU-0 names the pod in DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL"},
		},
		{
			name: "a CPU sample carries the pod's labels",
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1"},
				{family: "DCGM_FI_DEV_CPU_UTIL_TOTAL", labels: cpuSampleLabels + "," + podLabelsWithoutUID},
			},
			wantErr: []string{
				`DCGM_FI_DEV_CPU_UTIL_TOTAL: 1 sample(s) without a GPU UUID carry pod labels, first {cpu="0",cpu_serial="0x1",hostname="node-1"}: container="cuda-workload", namespace="e2e", pod="cuda-workload"`,
			},
		},
		{
			name: "another GPU names the pod",
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-0", labels: podLabels},
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-1"},
			},
			wantErr: []string{"GPU GPU-0 names the pod in DCGM_FI_DEV_GPU_TEMP"},
		},
		{
			name: "the pod's GPU is not reported",
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_GPU_TEMP", uuid: "GPU-0"},
			},
			wantErr: []string{"the exporter reports no whole-GPU DCGM_FI_ sample for the pod's GPU GPU-1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePodNotAttributed(attributionScrape(t, tc.samples...), attributionPod)
			requireAttributionResult(t, err, tc.wantErr)
		})
	}
}

// MIG instance labels shaped like the exporter's output for two instances of
// GPU-1.
const (
	podInstanceLabels   = `gpu="1",GPU_I_PROFILE="1g.23gb",GPU_I_ID="3"`
	otherInstanceLabels = `gpu="1",GPU_I_PROFILE="1g.23gb",GPU_I_ID="5"`
)

// TestValidateMIGPodAttribution covers a pod on one MIG instance: the
// instance's samples, other instances and the parent GPU, non-GPU samples, and
// incomplete expectations.
func TestValidateMIGPodAttribution(t *testing.T) {
	want := MIGPodAttribution{PodAttribution: attributionPod, GPUInstanceID: "3"}

	tests := []struct {
		name    string
		want    MIGPodAttribution
		samples []attributionSample
		wantErr []string
	}{
		{
			name: "only the pod's MIG instance names the pod",
			want: want,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_FB_USED", uuid: "GPU-1", labels: podInstanceLabels + "," + podLabels},
				{family: "DCGM_FI_PROF_GR_ENGINE_ACTIVE", uuid: "GPU-1", labels: podInstanceLabels + "," + podLabels},
				{family: "DCGM_FI_DEV_FB_USED", uuid: "GPU-1", labels: otherInstanceLabels},
				{family: "DCGM_FI_DEV_POWER_USAGE", uuid: "GPU-1", labels: `gpu="1"`},
				{family: "DCGM_FI_DEV_CPU_UTIL_TOTAL", labels: cpuSampleLabels},
				{family: "DCGM_FI_DEV_CPU_UTIL_TOTAL", labels: coreSampleLabels},
			},
		},
		{
			name: "CPU samples carry the MIG pod's labels",
			want: want,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_FB_USED", uuid: "GPU-1", labels: podInstanceLabels + "," + podLabels},
				{family: "DCGM_FI_DEV_CPU_UTIL_TOTAL", labels: cpuSampleLabels + "," + podLabels},
				{family: "DCGM_FI_DEV_CPU_UTIL_TOTAL", labels: coreSampleLabels + "," + podLabels},
			},
			wantErr: []string{
				"wrong in 1 place(s)",
				`DCGM_FI_DEV_CPU_UTIL_TOTAL: 2 sample(s) without a GPU UUID carry pod labels`,
			},
		},
		{
			name: "a sample of the pod's MIG instance lacks the pod",
			want: want,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_FB_USED", uuid: "GPU-1", labels: podInstanceLabels + "," + podLabels},
				{family: "DCGM_FI_PROF_GR_ENGINE_ACTIVE", uuid: "GPU-1", labels: podInstanceLabels},
			},
			wantErr: []string{
				`GPU GPU-1 instance 3 DCGM_FI_PROF_GR_ENGINE_ACTIVE: container="", want "cuda-workload"`,
			},
		},
		{
			name: "another MIG instance of the same GPU names the pod",
			want: want,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_FB_USED", uuid: "GPU-1", labels: podInstanceLabels + "," + podLabels},
				{family: "DCGM_FI_DEV_FB_FREE", uuid: "GPU-1", labels: otherInstanceLabels + "," + podLabels},
			},
			wantErr: []string{"GPU GPU-1 names the pod in DCGM_FI_DEV_FB_FREE"},
		},
		{
			name: "the parent GPU's whole-GPU sample names the pod",
			want: want,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_FB_USED", uuid: "GPU-1", labels: podInstanceLabels + "," + podLabels},
				{family: "DCGM_FI_DEV_POWER_USAGE", uuid: "GPU-1", labels: `gpu="1",` + podLabels},
			},
			wantErr: []string{"GPU GPU-1 names the pod in DCGM_FI_DEV_POWER_USAGE"},
		},
		{
			name: "the parent GPU's NVLink sample names the pod",
			want: want,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_FB_USED", uuid: "GPU-1", labels: podInstanceLabels + "," + podLabels},
				{family: "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL", labels: `nvlink="0",gpu="1",gpu_uuid="GPU-1",` + podLabels},
			},
			wantErr: []string{"GPU GPU-1 names the pod in DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL"},
		},
		{
			name: "the pod's MIG instance is not reported",
			want: want,
			samples: []attributionSample{
				{family: "DCGM_FI_DEV_FB_USED", uuid: "GPU-1", labels: otherInstanceLabels},
			},
			wantErr: []string{"the exporter reports no DCGM_FI_ sample for the pod's MIG instance 3 on GPU GPU-1"},
		},
		{
			name:    "expectation without a GPU instance",
			want:    MIGPodAttribution{PodAttribution: attributionPod},
			samples: []attributionSample{{family: "DCGM_FI_DEV_FB_USED", uuid: "GPU-1", labels: podInstanceLabels}},
			wantErr: []string{"pod attribution expectation is missing the GPU instance ID"},
		},
		{
			name:    "expectation without a pod",
			want:    MIGPodAttribution{PodAttribution: PodAttribution{GPUUUID: "GPU-1", Namespace: "n", Container: "c"}, GPUInstanceID: "3"},
			samples: []attributionSample{{family: "DCGM_FI_DEV_FB_USED", uuid: "GPU-1", labels: podInstanceLabels}},
			wantErr: []string{"pod attribution expectation is missing the pod"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMIGPodAttribution(attributionScrape(t, tc.samples...), tc.want)
			requireAttributionResult(t, err, tc.wantErr)
		})
	}
}
