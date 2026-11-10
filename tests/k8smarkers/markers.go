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

// Package k8smarkers holds the k8s suite result-marker base names shared by
// tests/k8s and internal/e2e/scenario catalog drift checks.
package k8smarkers

// ResultMarkerBaseNames maps Ginkgo scenario labels to marker bases.
// Update when adding or renaming a SuiteK8s catalog ResultName/MarkerName.
var ResultMarkerBaseNames = map[string]string{
	"basicAuth":                    "dcgm_exporter_e2e_basicauth",
	"c2c":                          "dcgm_exporter_e2e_c2c",
	"chartExistingConfig":          "dcgm_exporter_e2e_chart_existing_config",
	"chartUpgrade":                 "dcgm_exporter_e2e_chart_upgrade",
	"clockEventsCounters":          "dcgm_exporter_e2e_clockeventscounters",
	"configMapData":                "dcgm_exporter_e2e_configmapdata",
	"configMatrix":                 "dcgm_exporter_e2e_config_matrix",
	"customResourceNames":          "dcgm_exporter_e2e_custom_resource_names",
	"debugDump":                    "dcgm_exporter_e2e_debug_dump",
	"default":                      "dcgm_exporter_e2e_default",
	"defaultChartMounts":           "dcgm_exporter_e2e_default_chart_mounts",
	"dra":                          "dcgm_exporter_e2e_dra",
	"fieldUnsupported":             "dcgm_exporter_e2e_field_unsupported",
	"gpuOperatorChart":             "dcgm_exporter_e2e_gpu_operator_chart",
	"gpuOperatorDRA":               "dcgm_exporter_e2e_gpu_operator_dra",
	"gpuOperatorExporter":          "dcgm_exporter_e2e_gpu_operator_exporter",
	"gpuOperatorIPv6":              "dcgm_exporter_e2e_gpu_operator_ipv6",
	"gpuOperatorMainDRA":           "dcgm_exporter_e2e_gpu_operator_main_dra",
	"gpuOperatorMainEmbedded":      "dcgm_exporter_e2e_gpu_operator_main_embedded",
	"gpuOperatorMainRemote":        "dcgm_exporter_e2e_gpu_operator_main_remote",
	"gpuOperatorMig":               "dcgm_exporter_e2e_gpu_operator_mig",
	"gpuOperatorSharedGpu":         "dcgm_exporter_e2e_gpu_operator_shared_gpu",
	"graceCpu":                     "dcgm_exporter_e2e_grace_cpu",
	"hpcJobMapping":                "dcgm_exporter_e2e_hpc_job_mapping",
	"kubernetesGpuId":              "dcgm_exporter_e2e_kubernetes_gpu_id",
	"metricsNetworkPolicy":         "dcgm_exporter_e2e_metrics_network_policy",
	"mig":                          "dcgm_exporter_e2e_mig",
	"migCombinedDeviceSelection":   "dcgm_exporter_e2e_mig_combined_device_selection",
	"migFullGpuSelection":          "dcgm_exporter_e2e_mig_full_gpu_selection",
	"migInstanceSelection":         "dcgm_exporter_e2e_mig_instance_selection",
	"migPodAttribution":            "dcgm_exporter_e2e_mig_pod_attribution",
	"migSpecificInstanceSelection": "dcgm_exporter_e2e_mig_specific_instance_selection",
	"modelName":                    "dcgm_exporter_e2e_model_name",
	"noHostname":                   "dcgm_exporter_e2e_no_hostname",
	"nvlink":                       "dcgm_exporter_e2e_nvlink",
	"nvlinkFabric":                 "dcgm_exporter_e2e_nvlink_fabric",
	"nvlinkIntegrity":              "dcgm_exporter_e2e_nvlinkintegrity",
	"nvswitch":                     "dcgm_exporter_e2e_nvswitch",
	"nvswitchLink":                 "dcgm_exporter_e2e_nvswitchlink",
	"nvswitchThroughput":           "dcgm_exporter_e2e_nvswitchthroughput",
	"oldNamespace":                 "dcgm_exporter_e2e_oldnamespace",
	"p2pStatus":                    "dcgm_exporter_e2e_p2p_status",
	"podHardening":                 "dcgm_exporter_e2e_pod_hardening",
	"podLabelAllowlist":            "dcgm_exporter_e2e_pod_label_allowlist",
	"podLabels":                    "dcgm_exporter_e2e_pod_labels",
	"podUID":                       "dcgm_exporter_e2e_pod_uid",
	"pprof":                        "dcgm_exporter_e2e_pprof",
	"profiling":                    "dcgm_exporter_e2e_profiling",
	"remoteDcgm":                   "dcgm_exporter_e2e_remote_dcgm",
	"remoteDcgmRestart":            "dcgm_exporter_e2e_remote_dcgm_restart",
	"remoteDcgmIPv6Hostname":       "dcgm_exporter_e2e_remote_dcgm_ipv6_hostname",
	"serviceAccess":                "dcgm_exporter_e2e_service_access",
	"sharedGpu":                    "dcgm_exporter_e2e_shared_gpu",
	"tls":                          "dcgm_exporter_e2e_tls",
	"tlsBasicAuth":                 "dcgm_exporter_e2e_tls_basic_auth",
	"watchRetention":               "dcgm_exporter_e2e_watch_retention",
	"yamlConfig":                   "dcgm_exporter_e2e_yaml_config",
}
