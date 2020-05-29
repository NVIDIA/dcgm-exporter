#! /bin/bash -x
# Copyright (c) 2019, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

testing::metrics::setup() {
	:
}

testing::metrics::cleanup() {
	kubectl delete -f tests/gpu-pod.yaml
}

testing::metrics::utilization::increase() {
	# For a short while we might have multiple values returned
	# In this case it seems like the first item is the oldest
	val="$(query::prom "DCGM_FI_DEV_GPU_UTIL" | jq -r '.[-1].value[1]')"
	[ "$val" -ge 0 ] || return 1
}

testing::metrics::ensure::kube::labels() {
	val="$(query::prom "DCGM_FI_DEV_GPU_UTIL")"
	UUID="$(echo "${val}" | jq -r '.[0].metric.UUID')"
	gpu="$(echo "${val}" | jq -r '.[0].metric.gpu')"
	pod="$(echo "${val}" | jq -r '.[0].metric.exported_pod')"
	namespace="$(echo "${val}" | jq -r '.[0].metric.exported_namespace')"

	[ "$UUID" != "" ] || return 1
	[ "$gpu" != "" ] || return 1

	[ "$pod" = "nbody-pod" ] || return 1
	[ "$namespace" = "default" ] || return 1
}

testing::metrics::main() {
	# Prometheus can take a while to pickup the exporter
	with_retry 30 10s query::prom "DCGM_FI_DEV_MEMORY_TEMP"

	kubectl create -f tests/gpu-pod.yaml
	with_retry 30 10s query::pod::phase "nbody-pod" "Running"

	with_retry 10 10s testing::metrics::utilization::increase
	with_retry 10 10s testing::metrics::ensure::kube::labels
}
