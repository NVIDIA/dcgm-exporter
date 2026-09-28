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

package exportercontract

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const baselineMetrics = `
# HELP DCGM_FI_DEV_SM_CLOCK SM clock.
# TYPE DCGM_FI_DEV_SM_CLOCK gauge
DCGM_FI_DEV_SM_CLOCK{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 210
# HELP DCGM_FI_DEV_MEM_CLOCK Memory clock.
# TYPE DCGM_FI_DEV_MEM_CLOCK gauge
DCGM_FI_DEV_MEM_CLOCK{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 405
# HELP DCGM_FI_DEV_MEMORY_TEMP Memory temperature.
# TYPE DCGM_FI_DEV_MEMORY_TEMP gauge
DCGM_FI_DEV_MEMORY_TEMP{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature.
# TYPE DCGM_FI_DEV_GPU_TEMP gauge
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 41
# HELP DCGM_FI_DEV_POWER_USAGE Power usage.
# TYPE DCGM_FI_DEV_POWER_USAGE gauge
DCGM_FI_DEV_POWER_USAGE{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 77
# HELP DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION Total energy.
# TYPE DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION counter
DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 196764132
# HELP DCGM_FI_DEV_PCIE_REPLAY_COUNTER PCIe replay counter.
# TYPE DCGM_FI_DEV_PCIE_REPLAY_COUNTER counter
DCGM_FI_DEV_PCIE_REPLAY_COUNTER{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_GPU_UTIL GPU util.
# TYPE DCGM_FI_DEV_GPU_UTIL gauge
DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_MEM_COPY_UTIL Memory copy util.
# TYPE DCGM_FI_DEV_MEM_COPY_UTIL gauge
DCGM_FI_DEV_MEM_COPY_UTIL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 37
# HELP DCGM_FI_DEV_ENC_UTIL Encoder util.
# TYPE DCGM_FI_DEV_ENC_UTIL gauge
DCGM_FI_DEV_ENC_UTIL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_DEC_UTIL Decoder util.
# TYPE DCGM_FI_DEV_DEC_UTIL gauge
DCGM_FI_DEV_DEC_UTIL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_FB_FREE FB free.
# TYPE DCGM_FI_DEV_FB_FREE gauge
DCGM_FI_DEV_FB_FREE{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 6604
# HELP DCGM_FI_DEV_FB_USED FB used.
# TYPE DCGM_FI_DEV_FB_USED gauge
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 1024
# HELP DCGM_FI_DEV_VGPU_LICENSE_STATUS vGPU license status.
# TYPE DCGM_FI_DEV_VGPU_LICENSE_STATUS gauge
DCGM_FI_DEV_VGPU_LICENSE_STATUS{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
`

const customMetrics = `
# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature.
# TYPE DCGM_FI_DEV_GPU_TEMP gauge
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload",DCGM_FI_DRIVER_VERSION="580.105.08"} 41
# HELP DCGM_FI_DEV_FB_USED FB used.
# TYPE DCGM_FI_DEV_FB_USED gauge
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload",DCGM_FI_DRIVER_VERSION="580.105.08"} 1024
`

func TestBaselineMetricsAcceptsDefaultExporterMetrics(t *testing.T) {
	require.NoError(t, BaselineMetrics([]byte(baselineMetrics), BaselineOptions{
		RequireHostname:         true,
		RequireKubernetesLabels: true,
		KubernetesLabels:        kubernetesLabels(),
	}))
}

func TestNoHostnameMetricsRejectsHostnameLabels(t *testing.T) {
	require.Error(t, NoHostnameMetrics([]byte(baselineMetrics)))
}

func TestCustomMetricsRequiresReplacementSet(t *testing.T) {
	require.NoError(t, CustomMetrics([]byte(customMetrics), kubernetesLabels()))
	require.Error(t, CustomMetrics([]byte(baselineMetrics), kubernetesLabels()))
}

func kubernetesLabels() map[string]string {
	return map[string]string{
		"pod":       "cuda-workload",
		"namespace": "dcgm-exporter",
		"container": "cuda-workload",
	}
}
