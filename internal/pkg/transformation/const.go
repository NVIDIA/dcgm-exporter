/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

const (
	// Note standard resource attributes
	podAttribute       = "pod"
	namespaceAttribute = "namespace"
	containerAttribute = "container"
	vgpuAttribute      = "vgpu"

	hpcJobAttribute = "hpc_job"

	oldPodAttribute       = "pod_name"
	oldNamespaceAttribute = "pod_namespace"
	oldContainerAttribute = "container_name"
	draClaimName          = "dra_claim_name"
	draClaimNamespace     = "dra_claim_namespace"
	draDriverName         = "dra_driver_name"
	draPoolName           = "dra_pool_name"
	draDeviceName         = "dra_device_name"

	DRAGPUDriverName = "gpu.nvidia.com"
)
