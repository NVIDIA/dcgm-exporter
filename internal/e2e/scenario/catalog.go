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

// Package scenario owns the e2e scenario catalog and selector metadata.
package scenario

import (
	"fmt"
	"io"
	"strings"
)

// Suite is the execution domain for a scenario.
type Suite string

const (
	SuiteStatic    Suite = "static"
	SuiteHost      Suite = "host"
	SuiteContainer Suite = "container"
	SuiteK8s       Suite = "k8s"
)

var allSuites = []Suite{SuiteStatic, SuiteHost, SuiteContainer, SuiteK8s}

// AllSuites returns suites in the CLI execution order.
func AllSuites() []Suite {
	return append([]Suite{}, allSuites...)
}

// ValidSuite reports whether value is a known suite.
func ValidSuite(value Suite) bool {
	for _, suite := range allSuites {
		if suite == value {
			return true
		}
	}
	return false
}

// ValidSuiteSelector reports whether value is a CLI suite selector.
func ValidSuiteSelector(value string) bool {
	if value == "all" {
		return true
	}
	return ValidSuite(Suite(value))
}

// Scenario describes one e2e validation scenario.
type Scenario struct {
	Suite        Suite
	Name         string
	Description  string
	Group        string
	MarkerName   string
	Capabilities []string
	ResultName   string
	Enabled      string
	Required     string
	Remote       bool
	ExtraGates   []string
}

// Selector returns the suite/name selector used by the e2e CLI.
func (s Scenario) Selector() string {
	return string(s.Suite) + "/" + s.Name
}

// CapabilitySummary returns the display form used by --list-scenarios.
func (s Scenario) CapabilitySummary() string {
	return strings.Join(s.Capabilities, ", ")
}

// MarkerBaseName returns the parser-facing marker base for a Ginkgo scenario.
func (s Scenario) MarkerBaseName() (string, bool) {
	if s.Suite == SuiteStatic {
		return "", false
	}
	if s.MarkerName != "" {
		return s.MarkerName, true
	}
	prefix := "dcgm_exporter_e2e_"
	if s.Suite != SuiteK8s {
		prefix += string(s.Suite) + "_"
	}
	return prefix + strings.ToLower(s.Name), true
}

// HasAnyCapability reports whether a scenario is gated by any named capability.
func (s Scenario) HasAnyCapability(names ...string) bool {
	for _, candidate := range append(append([]string{}, s.Capabilities...), s.ExtraGates...) {
		for _, name := range names {
			if candidate == name {
				return true
			}
		}
	}
	return false
}

// Row returns the original pipe-delimited catalog row.
func (s Scenario) Row() string {
	remote := ""
	if s.Remote {
		remote = "remote"
	}
	return strings.Join([]string{
		string(s.Suite),
		s.Name,
		s.Description,
		s.Group,
		s.MarkerName,
		strings.Join(s.Capabilities, ","),
		s.ResultName,
		s.Enabled,
		s.Required,
		remote,
	}, "|")
}

// Find returns the catalog scenario for suite/name.
func Find(cat []Scenario, suite Suite, name string) (Scenario, bool) {
	for _, entry := range cat {
		if entry.Suite == suite && entry.Name == name {
			return entry, true
		}
	}
	return Scenario{}, false
}

// MustFind returns the catalog scenario for suite/name and panics on catalog drift.
func MustFind(cat []Scenario, suite Suite, name string) Scenario {
	entry, ok := Find(cat, suite, name)
	if !ok {
		panic(fmt.Sprintf("missing e2e scenario %s/%s", suite, name))
	}
	return entry
}

// Catalog is the static source of truth for scenario selectors and result names.
var Catalog = []Scenario{
	{Suite: SuiteStatic, Name: "chartRbacRendering", Description: "Helm chart RBAC rendering for Kubernetes attribution features", ResultName: "TestChartKubernetesRBACRenderContract", Enabled: "always", Required: "none"},
	{Suite: SuiteStatic, Name: "chartImageRendering", Description: "Helm chart image and pull-secret rendering", ResultName: "TestChartImageRenderingContract", Enabled: "always", Required: "none"},
	{Suite: SuiteStatic, Name: "chartServiceMonitorRendering", Description: "Helm chart ServiceMonitor rendering", ResultName: "TestChartServiceMonitorRenderingContract", Enabled: "always", Required: "none"},
	{Suite: SuiteStatic, Name: "chartYamlConfig", Description: "Helm chart YAML config rendering", ResultName: "TestChartYAMLConfigRenderContract,TestChartYAMLConfigCanMountExistingConfigMap,TestChartYAMLConfigExistingConfigMapRequiresName", Enabled: "always", Required: "none"},
	{Suite: SuiteStatic, Name: "dockerfileRuntimeSymbols", Description: "Dockerfile runtime DCGM symbol and package pinning checks", ResultName: "TestDockerfileGuardsRuntimeDCGMSymbolChecks,TestDockerfilePinsRuntimeDCGMPackages,TestPackageDockerfileEnforcesOfflineCompileWhenModulesAreCached", Enabled: "always", Required: "none"},
	{Suite: SuiteStatic, Name: "paths", Description: "Source and package path resolution checks", ResultName: "TestRepoPathResolvesSourceAndPackageFiles", Enabled: "always", Required: "none"},
	{Suite: SuiteStatic, Name: "packageSystemdUnit", Description: "Packaged systemd unit contract", ResultName: "TestPackageSystemdUnitContract", Enabled: "always", Required: "none"},

	{Suite: SuiteHost, Name: "startupMetrics", Description: "Direct host exporter startup and metrics scrape", MarkerName: "dcgm_exporter_e2e_host_startup_metrics", ResultName: "startupMetrics", Enabled: "always", Required: "none"},
	{Suite: SuiteHost, Name: "configFile", Description: "Direct host YAML config file behavior", MarkerName: "dcgm_exporter_e2e_host_config_file", ResultName: "configFile", Enabled: "always", Required: "none"},
	{Suite: SuiteHost, Name: "nvmlInjectionMetrics", Description: "Direct DCGM/exporter parity for every available numeric GPU field", MarkerName: "dcgm_exporter_e2e_host_nvml_injection_metrics", ResultName: "nvmlInjectionMetrics", Enabled: "nvml_injection", Required: "none"},
	{Suite: SuiteHost, Name: "watchGroups", Description: "Direct host YAML watch group behavior", MarkerName: "dcgm_exporter_e2e_host_watch_groups", ResultName: "watchGroups", Enabled: "always", Required: "none"},
	{Suite: SuiteHost, Name: "runtimeContainerLabels", Description: "Direct host runtime container labels", MarkerName: "dcgm_exporter_e2e_host_runtime_container_labels", ResultName: "runtimeContainerLabels", Enabled: "always", Required: "none"},
	{Suite: SuiteHost, Name: "hpcJobMapping", Description: "Direct host HPC/SLURM job mapping labels", MarkerName: "dcgm_exporter_e2e_host_hpc_job_mapping", ResultName: "hpcJobMapping", Enabled: "always", Required: "none"},
	{Suite: SuiteHost, Name: "startupTLS", Description: "Direct host TLS and basic auth behavior", MarkerName: "dcgm_exporter_e2e_host_startup_tls", ResultName: "startupTLS", Enabled: "always", Required: "none"},
	{Suite: SuiteHost, Name: "reload", Description: "Direct host SIGHUP and file-watcher reload behavior", MarkerName: "dcgm_exporter_e2e_host_reload", ResultName: "reload", Enabled: "always", Required: "none"},
	{Suite: SuiteHost, Name: "dcgmUri", Description: "Direct host remote DCGM URI and VSOCK behavior", MarkerName: "dcgm_exporter_e2e_host_dcgm_uri", ResultName: "dcgmUri", Enabled: "always", Required: "none"},
	{Suite: SuiteHost, Name: "ipv6Listen", Description: "Direct host IPv6 listen and scrape behavior", MarkerName: "dcgm_exporter_e2e_host_ipv6_listen", ResultName: "ipv6Listen", Enabled: "always", Required: "none"},
	{Suite: SuiteHost, Name: "gpuBindUnbindWatch", Description: "Direct host GPU bind/unbind watcher startup smoke", MarkerName: "dcgm_exporter_e2e_host_gpu_bind_unbind_watch", ResultName: "gpuBindUnbindWatch", Enabled: "always", Required: "none"},
	{Suite: SuiteHost, Name: "systemdSocket", Description: "Direct host systemd socket activation behavior", MarkerName: "dcgm_exporter_e2e_host_systemd_socket", ResultName: "systemdSocket", Enabled: "always", Required: "none"},

	{Suite: SuiteContainer, Name: "imageStartup", Description: "Container image startup, health, and default metrics", MarkerName: "dcgm_exporter_e2e_container_image_startup", ResultName: "imageStartup", Enabled: "always", Required: "none"},
	{Suite: SuiteContainer, Name: "configuration", Description: "Container runtime configuration and custom collectors", MarkerName: "dcgm_exporter_e2e_container_configuration", ResultName: "configuration", Enabled: "always", Required: "none"},
	{Suite: SuiteContainer, Name: "pprofRequiresWebConfig", Description: "Container startup rejects pprof without web config", MarkerName: "dcgm_exporter_e2e_container_pprof_requires_web_config", ResultName: "pprofRequiresWebConfig", Enabled: "always", Required: "none"},
	{Suite: SuiteContainer, Name: "invalidDeviceSelectors", Description: "Container invalid device selector diagnostics", MarkerName: "dcgm_exporter_e2e_container_invalid_device_selectors", ResultName: "invalidDeviceSelectors", Enabled: "always", Required: "none"},
	{Suite: SuiteContainer, Name: "remoteDcgmUri", Description: "Container remote DCGM URI behavior", MarkerName: "dcgm_exporter_e2e_container_remote_dcgm_uri", ResultName: "remoteDcgmUri", Enabled: "always", Required: "none"},

	{Suite: SuiteK8s, Name: "default", Description: "Default Helm deployment and default metric contract", Group: "embedded-baseline", ResultName: "default", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "configMatrix", Description: "Custom metrics replacement through Helm", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_config_matrix", ResultName: "configMatrix", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "configMapData", Description: "Kubernetes ConfigMap metric loading through --configmap-data", Group: "embedded-baseline", ResultName: "configMapData", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "yamlConfig", Description: "Kubernetes YAML config loading through Helm", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_yaml_config", ResultName: "yamlConfig", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "oldNamespace", Description: "Legacy Kubernetes attribution label names", Group: "embedded-baseline", ResultName: "oldNamespace", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "clockEventsCounters", Description: "Clock event exporter counters", Group: "embedded-baseline", ResultName: "clockEventsCounters", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "tls", Description: "TLS scrape endpoint", Group: "embedded-baseline", ResultName: "tls", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "basicAuth", Description: "Basic auth scrape endpoint", Group: "embedded-baseline", ResultName: "basicAuth", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "tlsBasicAuth", Description: "TLS and basic auth together", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_tls_basic_auth", ResultName: "tlsBasicAuth", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "serviceAccess", Description: "Kubernetes Service and direct scrape behavior", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_service_access", ResultName: "serviceAccess", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "podLabels", Description: "Kubernetes pod label attribution", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_pod_labels", ResultName: "podLabels", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "podUID", Description: "Kubernetes pod UID attribution", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_pod_uid", ResultName: "podUID", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "podLabelAllowlist", Description: "Pod label allowlist filtering", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_pod_label_allowlist", ResultName: "podLabelAllowlist", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "kubernetesGpuId", Description: "Kubernetes GPU ID mapping mode", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_kubernetes_gpu_id", ResultName: "kubernetesGpuId", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "noHostname", Description: "Hostname label suppression", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_no_hostname", ResultName: "noHostname", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "modelName", Description: "Model name normalization", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_model_name", ResultName: "modelName", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "pprof", Description: "pprof endpoint enablement", Group: "embedded-baseline", ResultName: "pprof", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "debugDump", Description: "Debug dump file output", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_debug_dump", ResultName: "debugDump", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "hpcJobMapping", Description: "HPC job mapping labels", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_hpc_job_mapping", ResultName: "hpcJobMapping", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "customResourceNames", Description: "Custom NVIDIA resource name attribution", Group: "embedded-baseline", MarkerName: "dcgm_exporter_e2e_custom_resource_names", ResultName: "customResourceNames", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "profiling", Description: "Datacenter profiling metrics", Group: "embedded-hardware", Capabilities: []string{"host:profiling"}, ResultName: "profiling", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "p2pStatus", Description: "P2P status exporter metric", Group: "embedded-hardware", MarkerName: "dcgm_exporter_e2e_p2p_status", Capabilities: []string{"host:p2p"}, ResultName: "p2pStatus", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "fieldUnsupported", Description: "Unsupported DCGM field omission behavior", Group: "embedded-hardware", MarkerName: "dcgm_exporter_e2e_field_unsupported", Capabilities: []string{"host:unsupported_field"}, ResultName: "fieldUnsupported", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "nvlink", Description: "NVLink metrics", Group: "embedded-hardware", Capabilities: []string{"host:nvlink"}, ResultName: "nvlink", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "mig", Description: "MIG metrics and attribution", Group: "embedded-mig", Capabilities: []string{"host:mig"}, ResultName: "mig", Enabled: "always", Required: "mig"},
	{Suite: SuiteK8s, Name: "migFullGpuSelection", Description: "DCGM_EXPORTER_DEVICES_STR full GPU selection in mixed MIG mode", Group: "embedded-mig", MarkerName: "dcgm_exporter_e2e_mig_full_gpu_selection", Capabilities: []string{"host:mixed_mig"}, ResultName: "migFullGpuSelection", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "migInstanceSelection", Description: "DCGM_EXPORTER_DEVICES_STR all-MIG-instance selection in mixed MIG mode", Group: "embedded-mig", MarkerName: "dcgm_exporter_e2e_mig_instance_selection", Capabilities: []string{"host:mixed_mig"}, ResultName: "migInstanceSelection", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "migSpecificInstanceSelection", Description: "DCGM_EXPORTER_DEVICES_STR specific MIG instance selection in mixed MIG mode", Group: "embedded-mig", MarkerName: "dcgm_exporter_e2e_mig_specific_instance_selection", Capabilities: []string{"host:mixed_mig", "host:mig_instance_entity"}, ResultName: "migSpecificInstanceSelection", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "migCombinedDeviceSelection", Description: "DCGM_EXPORTER_DEVICES_STR combined full-GPU and MIG-instance selection in mixed MIG mode", Group: "embedded-mig", MarkerName: "dcgm_exporter_e2e_mig_combined_device_selection", Capabilities: []string{"host:mixed_mig"}, ResultName: "migCombinedDeviceSelection", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "dra", Description: "DRA attribution", Group: "embedded-dra", Capabilities: []string{"cluster:dra"}, ResultName: "dra", Enabled: "always", Required: "dra"},
	{Suite: SuiteK8s, Name: "sharedGpu", Description: "Shared/vGPU attribution", Group: "embedded-shared-gpu", MarkerName: "dcgm_exporter_e2e_shared_gpu", Capabilities: []string{"cluster:shared_gpu"}, ResultName: "sharedGpu", Enabled: "always", Required: "shared_gpu"},
	{Suite: SuiteK8s, Name: "nvswitch", Description: "NVSwitch metrics", Group: "embedded-hardware", Capabilities: []string{"host:nvswitch"}, ResultName: "nvswitch", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "graceCpu", Description: "Grace CPU/Sysmon metrics", Group: "embedded-hardware", MarkerName: "dcgm_exporter_e2e_grace_cpu", Capabilities: []string{"host:grace_cpu"}, ResultName: "graceCpu", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "c2c", Description: "C2C metrics", Group: "embedded-hardware", Capabilities: []string{"host:c2c"}, ResultName: "c2c", Enabled: "always", Required: "none"},
	{Suite: SuiteK8s, Name: "remoteDcgm", Description: "Exporter connected to standalone DCGM", Group: "standalone-baseline", MarkerName: "dcgm_exporter_e2e_remote_dcgm", Capabilities: []string{"dcgm:remote_dcgm", "cluster:standalone_dcgm_resources"}, ResultName: "remoteDcgm", Enabled: "always", Required: "none", Remote: true},
	{Suite: SuiteK8s, Name: "remoteDcgmRestart", Description: "Exporter recovers after standalone DCGM restart", Group: "standalone-baseline", MarkerName: "dcgm_exporter_e2e_remote_dcgm_restart", Capabilities: []string{"dcgm:remote_dcgm", "cluster:standalone_dcgm_resources"}, ResultName: "remoteDcgmRestart", Enabled: "always", Required: "none", Remote: true},
	{Suite: SuiteK8s, Name: "failureXid", Description: "Injected XID values reported through exporter metrics", Group: "standalone-failure-injection", MarkerName: "dcgm_exporter_e2e_failure_xid", Capabilities: []string{"dcgm:failure_injection", "cluster:standalone_dcgm_resources"}, ResultName: "failureXid", Enabled: "failure_injection", Required: "failure_injection", Remote: true},
	{Suite: SuiteK8s, Name: "failureXidCounters", Description: "Injected XID count and total metrics", Group: "standalone-failure-injection", MarkerName: "dcgm_exporter_e2e_failure_xid_counters", Capabilities: []string{"dcgm:failure_injection", "cluster:standalone_dcgm_resources"}, ResultName: "failureXidCounters", Enabled: "failure_injection", Required: "failure_injection", Remote: true},
	{Suite: SuiteK8s, Name: "failureGpuHealth", Description: "Injected failure signals reflected in GPU health metrics", Group: "standalone-failure-injection", MarkerName: "dcgm_exporter_e2e_failure_gpu_health", Capabilities: []string{"dcgm:failure_injection", "cluster:standalone_dcgm_resources"}, ResultName: "failureGpuHealth", Enabled: "failure_injection", Required: "failure_injection", Remote: true},
	{Suite: SuiteK8s, Name: "failureNvlinkHealth", Description: "Injected NVLink failure signals reflected in NVLink metrics", Group: "standalone-failure-injection", MarkerName: "dcgm_exporter_e2e_failure_nvlink_health", Capabilities: []string{"dcgm:failure_injection_nvlink_crc", "cluster:standalone_dcgm_resources"}, ResultName: "failureNvlinkHealth", Enabled: "failure_injection", Required: "failure_injection_nvlink", Remote: true},
	{Suite: SuiteK8s, Name: "gpuOperatorChart", Description: "GPU Operator chart integration", Group: "gpu-operator-baseline", MarkerName: "dcgm_exporter_e2e_gpu_operator_chart", Capabilities: []string{"cluster:gpu_operator"}, ResultName: "gpuOperatorChart", Enabled: "gpu_operator", Required: "gpu_operator"},
	{Suite: SuiteK8s, Name: "gpuOperatorExporter", Description: "GPU Operator managed exporter integration", Group: "gpu-operator-baseline", MarkerName: "dcgm_exporter_e2e_gpu_operator_exporter", Capabilities: []string{"cluster:gpu_operator"}, ResultName: "gpuOperatorExporter", Enabled: "gpu_operator", Required: "gpu_operator"},
	{Suite: SuiteK8s, Name: "gpuOperatorSharedGpu", Description: "GPU Operator shared GPU integration", Group: "gpu-operator-shared-gpu", MarkerName: "dcgm_exporter_e2e_gpu_operator_shared_gpu", Capabilities: []string{"cluster:gpu_operator", "cluster:shared_gpu"}, ResultName: "gpuOperatorSharedGpu", Enabled: "gpu_operator", Required: "gpu_operator_shared"},
	{Suite: SuiteK8s, Name: "gpuOperatorMig", Description: "GPU Operator MIG integration", Group: "gpu-operator-mig", MarkerName: "dcgm_exporter_e2e_gpu_operator_mig", Capabilities: []string{"cluster:gpu_operator", "host:mig"}, ResultName: "gpuOperatorMig", Enabled: "gpu_operator", Required: "gpu_operator_mig"},
	{Suite: SuiteK8s, Name: "gpuOperatorDRA", Description: "GPU Operator DRA integration", Group: "gpu-operator-dra", MarkerName: "dcgm_exporter_e2e_gpu_operator_dra", Capabilities: []string{"cluster:gpu_operator", "cluster:dra"}, ResultName: "gpuOperatorDRA", Enabled: "gpu_operator", Required: "gpu_operator_dra"},
	{Suite: SuiteK8s, Name: "gpuOperatorIPv6", Description: "GPU Operator exporter over IPv6 service networking", Group: "gpu-operator-baseline", MarkerName: "dcgm_exporter_e2e_gpu_operator_ipv6", Capabilities: []string{"cluster:gpu_operator", "cluster:ipv6"}, ResultName: "gpuOperatorIPv6", Enabled: "gpu_operator", Required: "gpu_operator_ipv6"},
}

// WriteRows writes the pipe-delimited catalog rows.
func WriteRows(w io.Writer) error {
	for _, entry := range Catalog {
		if _, err := fmt.Fprintln(w, entry.Row()); err != nil {
			return err
		}
	}
	return nil
}

// WriteList writes the human-readable scenario list.
func WriteList(w io.Writer) error {
	for _, entry := range Catalog {
		if summary := entry.CapabilitySummary(); summary != "" {
			if _, err := fmt.Fprintf(w, "%-22s %s (%s)\n", entry.Selector(), entry.Description, summary); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%-22s %s\n", entry.Selector(), entry.Description); err != nil {
			return err
		}
	}
	return nil
}

// SelectorsMatching returns selectors for catalog entries that match fn.
func SelectorsMatching(cat []Scenario, fn func(Scenario) bool) []string {
	var selectors []string
	for _, entry := range cat {
		if fn(entry) {
			selectors = append(selectors, entry.Selector())
		}
	}
	return selectors
}

// MarkerBaseNames returns scenario label to marker base-name mappings for one suite.
func MarkerBaseNames(cat []Scenario, suite Suite) map[string]string {
	names := map[string]string{}
	for _, entry := range cat {
		if entry.Suite != suite || entry.ResultName == "" {
			continue
		}
		if base, ok := entry.MarkerBaseName(); ok {
			names[entry.ResultName] = base
		}
	}
	return names
}
