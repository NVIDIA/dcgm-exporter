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

// EnabledNVMLInjection marks a scenario that runs only when selected and needs
// a DCGM NVML injection fixture.
const EnabledNVMLInjection = "nvml_injection"

const (
	SuiteStatic    Suite = "static"
	SuiteHost      Suite = "host"
	SuiteContainer Suite = "container"
	SuiteK8s       Suite = "k8s"
)

var allSuites = []Suite{SuiteStatic, SuiteHost, SuiteContainer, SuiteK8s}

const (
	GroupStaticBaseline             = "static-baseline"
	GroupHostBaseline               = "host-baseline"
	GroupHostRemoteDCGM             = "host-remote-dcgm"
	GroupHostDefaultPreset          = "host-default-preset"
	GroupHostVSOCK                  = "host-vsock"
	GroupHostProfiling              = "host-profiling"
	GroupHostGPULinkFields          = "host-gpu-link-fields"
	GroupHostNVMLInjection          = "host-nvml-injection"
	GroupHostConfigInjection        = "host-config-injection"
	GroupHostGPULoad                = "host-gpu-load"
	GroupHostGPUBindUnbind          = "host-gpu-bind-unbind"
	GroupHostMIGTwoInstancesPerGPU  = "host-mig-two-instances-per-gpu"
	GroupHostNVSDMMock              = "host-nvsdm-mock"
	GroupContainerBaseline          = "container-baseline"
	GroupContainerRemoteDCGM        = "container-remote-dcgm"
	GroupK8sEmbeddedBaseline        = "embedded-baseline"
	GroupK8sEmbeddedHardware        = "embedded-hardware"
	GroupK8sEmbeddedMIG             = "embedded-mig"
	GroupK8sEmbeddedDRA             = "embedded-dra"
	GroupK8sEmbeddedSharedGPU       = "embedded-shared-gpu"
	GroupK8sEmbeddedRenamedGPU      = "embedded-renamed-gpu"
	GroupK8sEmbeddedNVSwitch        = "embedded-nvswitch"
	GroupK8sStandaloneBaseline      = "standalone-baseline"
	GroupK8sStandaloneIPv6          = "standalone-ipv6"
	GroupK8sGPUOperatorBaseline     = "gpu-operator-baseline"
	GroupK8sGPUOperatorSharedGPU    = "gpu-operator-shared-gpu"
	GroupK8sGPUOperatorMIG          = "gpu-operator-mig"
	GroupK8sGPUOperatorDRA          = "gpu-operator-dra"
	GroupK8sGPUOperatorMainEmbedded = "gpu-operator-main-embedded"
	GroupK8sGPUOperatorMainRemote   = "gpu-operator-main-remote"
	GroupK8sGPUOperatorMainDRA      = "gpu-operator-main-dra"
)

var groupsBySuite = map[Suite]map[string]struct{}{
	SuiteStatic: {GroupStaticBaseline: {}},
	SuiteHost: {
		GroupHostBaseline: {}, GroupHostDefaultPreset: {}, GroupHostRemoteDCGM: {}, GroupHostVSOCK: {}, GroupHostProfiling: {}, GroupHostGPULinkFields: {}, GroupHostNVMLInjection: {}, GroupHostConfigInjection: {}, GroupHostNVSDMMock: {}, GroupHostGPULoad: {}, GroupHostGPUBindUnbind: {},
		GroupHostMIGTwoInstancesPerGPU: {},
	},
	SuiteContainer: {GroupContainerBaseline: {}, GroupContainerRemoteDCGM: {}},
	SuiteK8s: {
		GroupK8sEmbeddedBaseline: {}, GroupK8sEmbeddedHardware: {}, GroupK8sEmbeddedMIG: {},
		GroupK8sEmbeddedDRA: {}, GroupK8sEmbeddedSharedGPU: {}, GroupK8sEmbeddedRenamedGPU: {},
		GroupK8sEmbeddedNVSwitch: {}, GroupK8sStandaloneBaseline: {}, GroupK8sStandaloneIPv6: {}, GroupK8sGPUOperatorBaseline: {},
		GroupK8sGPUOperatorSharedGPU: {}, GroupK8sGPUOperatorMIG: {}, GroupK8sGPUOperatorDRA: {},
		GroupK8sGPUOperatorMainEmbedded: {}, GroupK8sGPUOperatorMainRemote: {}, GroupK8sGPUOperatorMainDRA: {},
	},
}

// ValidateCatalog rejects selectors or dependency groups that cannot be executed deterministically.
func ValidateCatalog(cat []Scenario) error {
	seen := make(map[string]struct{}, len(cat))
	for _, entry := range cat {
		if err := validateScenarioIdentity(entry); err != nil {
			return err
		}
		if _, ok := seen[entry.Selector()]; ok {
			return fmt.Errorf("duplicate scenario selector %s", entry.Selector())
		}
		seen[entry.Selector()] = struct{}{}
		if err := validateScenarioSetup(entry); err != nil {
			return err
		}
	}
	return validateSuiteRequirements()
}

// validateScenarioIdentity checks the suite, name, and result name that make a scenario selectable.
func validateScenarioIdentity(entry Scenario) error {
	if !ValidSuite(entry.Suite) {
		return fmt.Errorf("scenario %s uses unknown suite %q", entry.Name, entry.Suite)
	}
	if entry.Name == "" {
		return fmt.Errorf("scenario in suite %s has an empty name", entry.Suite)
	}
	if entry.ResultName == "" {
		return fmt.Errorf("scenario %s has an empty result name", entry.Selector())
	}
	return nil
}

// validateScenarioSetup checks a scenario's dependency group, Kubernetes allocation, requirements, and probe inputs.
func validateScenarioSetup(entry Scenario) error {
	if entry.Group == "" {
		return fmt.Errorf("scenario %s has an empty dependency group", entry.Selector())
	}
	if _, ok := groupsBySuite[entry.Suite][entry.Group]; !ok {
		return fmt.Errorf("scenario %s uses unknown dependency group %q for suite %s", entry.Selector(), entry.Group, entry.Suite)
	}
	if !entry.Allocation.Valid() {
		return fmt.Errorf("scenario %s uses unknown Kubernetes allocation %q", entry.Selector(), entry.Allocation)
	}
	if entry.Suite != SuiteK8s && entry.Allocation != KubernetesAllocationNone {
		return fmt.Errorf("scenario %s declares a Kubernetes allocation outside the Kubernetes suite", entry.Selector())
	}
	if err := validateRequirements(entry.Requirements); err != nil {
		return fmt.Errorf("scenario %s: %w", entry.Selector(), err)
	}
	if err := validateCapabilityProbeInputs(entry.CapabilityProbeInputs); err != nil {
		return fmt.Errorf("scenario %s: %w", entry.Selector(), err)
	}
	return nil
}

// validateSuiteRequirements checks the default requirements declared for each suite.
func validateSuiteRequirements() error {
	for suite, requirements := range suiteRequirements {
		if !ValidSuite(suite) {
			return fmt.Errorf("unknown suite requirement defaults for %q", suite)
		}
		if err := validateRequirements(requirements); err != nil {
			return fmt.Errorf("suite %s: %w", suite, err)
		}
	}
	return nil
}

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
	Suite                 Suite
	Name                  string
	Description           string
	Group                 string
	MarkerName            string
	Features              []string
	Applicability         []string
	ResultName            string
	Enabled               string
	Remote                bool
	Allocation            KubernetesAllocation
	MIGLayout             MIGLayout
	Requirements          []Requirement
	CapabilityProbeInputs []CapabilityProbeInput
}

// Selector returns the suite/name selector used by the e2e CLI.
func (s Scenario) Selector() string {
	return string(s.Suite) + "/" + s.Name
}

// CapabilitySummary returns the tiered gate summary used by --list-scenarios.
func (s Scenario) CapabilitySummary() string {
	var parts []string
	if required := s.RequiredCapabilities(); len(required) != 0 {
		parts = append(parts, "features: "+strings.Join(required, ", "))
	}
	if len(s.Applicability) != 0 {
		parts = append(parts, "applies: "+strings.Join(s.Applicability, ", "))
	}
	return strings.Join(parts, "; ")
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
	{
		Suite:       SuiteStatic,
		Name:        "chartRbacRendering",
		Group:       GroupStaticBaseline,
		Description: "Helm chart RBAC rendering for Kubernetes attribution features",
		ResultName: "TestChartKubernetesRBACRenderContract," +
			"TestChartKubernetesAccessFollowsTheConfig," +
			"TestChartDRAEnrichmentDefaultsToAuto",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartConfigSettings",
		Group:       GroupStaticBaseline,
		Description: "Helm chart exporter settings taken only from config",
		ResultName: "TestChartDefaultSetsNoSettingOutsideTheConfig," +
			"TestChartConfigSettingsReachTheExporter," +
			"TestChartRejectsRemovedSettingValues," +
			"TestChartSettingOverridesRejected," +
			"TestChartSettingOverridesAllowedWithoutAChartConfig," +
			"TestChartOtherSettingOverridesRender," +
			"TestChartSettingOverrideCheckMatchesWholeNames," +
			"TestChart484ValuesMoveToConfig",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartNetworkPolicy",
		Group:       GroupStaticBaseline,
		Description: "Optional ingress policy rendering and value checks",
		ResultName: "TestChartNetworkPolicyRenderContract," +
			"TestChartNetworkPolicyDisabledPreservesRendering," +
			"TestChartNetworkPolicyRejectsAmbiguousListenerAndPeers," +
			"TestRawNetworkPolicyExampleMatchesExporter",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartDaemonSetAnnotations",
		Group:       GroupStaticBaseline,
		Description: "Helm chart DaemonSet annotation rendering",
		ResultName:  "TestChartDaemonSetAnnotationsRenderContract",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartScheduling",
		Group:       GroupStaticBaseline,
		Description: "Helm and raw-manifest scheduling contracts",
		ResultName: "TestChartTolerationsRenderContract,TestGPUTaintedNodesPatch," +
			"TestRawManifestSelectorsRemainStableAcrossReleaseVersions," +
			"TestRawManifestVersionedSelectorMigration",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartImageRendering",
		Group:       GroupStaticBaseline,
		Description: "Helm chart image and pull-secret rendering",
		ResultName:  "TestChartImageRenderingContract",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartDefaultMetrics",
		Group:       GroupStaticBaseline,
		Description: "Helm chart default metrics source and packaging",
		ResultName: "TestChartDefaultMetricsUseCanonicalCSV,TestSourceChartDefaultMetrics," +
			"TestChartMissingDefaultMetrics,TestChartRejectsSymlinkPlaceholder",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "grafanaDashboardMetrics",
		Group:       GroupStaticBaseline,
		Description: "Grafana dashboard queries only metrics in the default metric file",
		ResultName:  "TestGrafanaDashboardUsesDefaultMetrics",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartServiceMonitorRendering",
		Group:       GroupStaticBaseline,
		Description: "Helm chart ServiceMonitor rendering",
		ResultName: "TestChartServiceMonitorRenderingContract," +
			"TestChartServiceMonitorIgnoresExporterConfig",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartNaming",
		Group:       GroupStaticBaseline,
		Description: "Helm chart renders name and namespace overrides and release-scoped object names",
		ResultName:  "TestChartNamesFollowTheOverrides,TestChartNameOverrideKeepsTheReleaseName,TestChartNamesAreCutAtTheNameLimit",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartPodSettings",
		Group:       GroupStaticBaseline,
		Description: "Helm chart renders update strategy, resources and init containers",
		ResultName:  "TestChartRollingUpdateValues,TestChartRejectsInvalidRollingUpdate,TestChartResourcesAndInitContainers",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartWebConfig",
		Group:       GroupStaticBaseline,
		Description: "Helm chart TLS and basic-auth web configuration rendering",
		ResultName: "TestChartWebConfigIsAbsentByDefault,TestChartWebConfigUsesASecret," +
			"TestChartWebConfigPathReachesTheConfig,TestChartWebConfigWithExistingConfigMap," +
			"TestChartWebConfigConflictsRejected,TestChartExistingNameWebConfigNotes,TestChartTLSExistingSecret",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartYamlConfig",
		Group:       GroupStaticBaseline,
		Description: "Helm chart YAML config rendering",
		ResultName: "TestChartConfigDefaultsRenderTheExporterConfig," +
			"TestChartConfigNestedKeyOverlayKeepsTheOtherDefaults," +
			"TestChartConfigMapKey," +
			"TestChartConfigInlineFieldsRender," +
			"TestChartConfigMapDefaultsFillMissingValues," +
			"TestChartConfigMapExistingNameIsMounted," +
			"TestChartConfigNullMountsNothing," +
			"TestChartRejects484ConfigOptions," +
			"TestChartNotesHaveNoDeprecationText," +
			"TestChartDefaultConfigMatchesTheImageDefaultFile," +
			"TestChartConfigNullWithExistingNameMountsIt," +
			"TestChartConfigWithOnlyVersionRendersValidYAML," +
			"TestChartConfigChecksumFollowsTheConfig," +
			"TestChartSetsNoKubernetesEnvironment," +
			"TestChartConfigKubernetesLabelSettingReachesTheConfigMap," +
			"TestChartConfigListenMustUseTheServicePort," +
			"TestChartConfigMustBeAMap," +
			"TestChartConfigVersionMustBe2",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartMounts",
		Group:       GroupStaticBaseline,
		Description: "Helm chart file mounts (whole directories, no subPath) and metrics list value checks",
		ResultName: "TestChartOwnedMountsUseNoSubPath," +
			"TestChartMountsTheMetricsList," +
			"TestChartTemplatesHaveNoSubPath," +
			"TestChartCustomMetricsChangeOnlyTheMetricsConfigMap," +
			"TestChartRejectsAMountAtTheImageMetricsFile," +
			"TestChartKeepsOtherExtraMounts," +
			"TestChartRejectsUnusedCustomMetrics," +
			"TestChartNotesNameTheCustomMetricsPath," +
			"TestChartRejectsConfigMapMountPath",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartConfigRead",
		Group:       GroupStaticBaseline,
		Description: "Helm chart ConfigMaps and README config examples read with the exporter's YAML reader",
		ResultName:  "TestChartConfigMapsRead",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartArgumentsRendering",
		Group:       GroupStaticBaseline,
		Description: "Helm chart argument string rendering",
		ResultName:  "TestChartArgumentsRenderAsStrings",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartFieldWatchRetention",
		Group:       GroupStaticBaseline,
		Description: "Helm chart field-watch retention argument rendering",
		ResultName:  "TestChartFieldWatchRetentionArgumentsRenderContract",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartDebugDumpStorage",
		Group:       GroupStaticBaseline,
		Description: "Helm chart default and user-supplied debug dump storage",
		ResultName:  "TestChartDumpVolume,TestChartDumpUserVolume,TestChartDumpSettingsComeFromConfig",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "chartProbeSettings",
		Group:       GroupStaticBaseline,
		Description: "Helm chart health probe rendering",
		ResultName: "TestChartProbeSettingsRenderContract,TestChartStartupProbeRenderContract," +
			"TestChartProbeHandlersRenderContract",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "latestValueBatchingFixture",
		Group:       GroupStaticBaseline,
		Description: "Latest-value batching fixture field ID contract",
		ResultName:  "TestLatestValueBatchingFixtureUsesUniqueDCGMFieldIDs",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "paths",
		Group:       GroupStaticBaseline,
		Description: "Source and package path resolution checks",
		ResultName: "TestRepoPathResolvesSourceAndPackageFiles," +
			"TestFieldDefinitionsPathUsesStagedMetadata," +
			"TestFieldDefinitionsPathFallsBackWhenStagedMetadataIsUnavailable",
		Enabled: "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "e2eLocalNodeRuntime",
		Group:       GroupStaticBaseline,
		Description: "Local E2E node NVIDIA runtime configuration",
		ResultName:  "TestE2ELocalNodeConfiguresLegacyRuntimeDriverRoot",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "hostSuiteImports",
		Group:       GroupStaticBaseline,
		Description: "Host suite product-package import boundary",
		ResultName:  "TestHostSuiteDoesNotImportExporterInternals,TestGoExecutable,TestRunfilesRoot",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "readmeDocumentation",
		Group:       GroupStaticBaseline,
		Description: "Repository documentation link contract",
		ResultName:  "TestReadmeDocumentationContract,TestChartReadmeListsEveryValue",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "watchGroupsSelection",
		Group:       GroupStaticBaseline,
		Description: "Compiled host watch-groups label selection",
		ResultName:  "TestWatchGroupsLabelSelectsHostSpecs",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "packageSystemdUnit",
		Group:       GroupStaticBaseline,
		Description: "Packaged systemd unit contract",
		ResultName:  "TestPackageSystemdUnitContract",
		Enabled:     "always",
	},
	{
		Suite:       SuiteStatic,
		Name:        "dockerDefaultConfig",
		Group:       GroupStaticBaseline,
		Description: "Container images load the default config file",
		ResultName:  "TestDockerfileLoadsDefaultConfigFile",
		Enabled:     "always",
	},

	{
		Suite:       SuiteHost,
		Name:        "startupMetrics",
		Group:       GroupHostBaseline,
		Description: "Direct host exporter startup and metrics scrape",
		MarkerName:  "dcgm_exporter_e2e_host_startup_metrics",
		ResultName:  "startupMetrics",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "defaultPreset",
		Group:       GroupHostDefaultPreset,
		Description: "Shipped default counter preset on a privileged host",
		MarkerName:  "dcgm_exporter_e2e_host_default_preset",
		ResultName:  "defaultPreset",
		Enabled:     "manual",
	},
	{
		Suite:       SuiteHost,
		Name:        "dcgmLibraryPath",
		Group:       GroupHostBaseline,
		Description: "Direct host exporter loads DCGM from LD_LIBRARY_PATH",
		MarkerName:  "dcgm_exporter_e2e_host_dcgm_library_path",
		ResultName:  "dcgmLibraryPath",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "configFile",
		Group:       GroupHostConfigInjection,
		Description: "Direct host YAML config file behavior",
		MarkerName:  "dcgm_exporter_e2e_host_config_file",
		ResultName:  "configFile",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "configPrecedence",
		Group:       GroupHostConfigInjection,
		Description: "Direct host environment override precedence over YAML metrics",
		MarkerName:  "dcgm_exporter_e2e_host_config_precedence",
		ResultName:  "configPrecedence",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "configStartupErrors",
		Group:       GroupHostBaseline,
		Description: "Direct host exporter rejects unusable config file, flag or environment input before DCGM starts",
		MarkerName:  "dcgm_exporter_e2e_host_config_startup_errors",
		ResultName:  "configStartupErrors",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "configInspection",
		Group:       GroupHostBaseline,
		Description: "Direct host config show, validate and explain: exit statuses, located diagnostics, JSON and round trip",
		MarkerName:  "dcgm_exporter_e2e_host_config_inspection",
		ResultName:  "configInspection",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "configServesMetrics",
		Group:       GroupHostConfigInjection,
		Description: "Direct host YAML config file serves only its listed fields under NVML injection",
		MarkerName:  "dcgm_exporter_e2e_host_config_serves_metrics",
		ResultName:  "configServesMetrics",
		Enabled:     EnabledNVMLInjection,
		Requirements: []Requirement{
			{Kind: RequirementDCGMDevelopment},
		},
	},
	{
		Suite:       SuiteHost,
		Name:        "configEnrichment",
		Group:       GroupHostConfigInjection,
		Description: "Direct host workload enrichment from the config file: container `auto`, `true` (waits up to 60 s) and `false` against a fake runtime socket, Kubernetes `auto` with a missing kubelet socket, and HPC job switches",
		MarkerName:  "dcgm_exporter_e2e_host_config_enrichment",
		ResultName:  "configEnrichment",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "nvmlInjectionMetrics",
		Group:       GroupHostNVMLInjection,
		Description: "Direct DCGM/exporter parity for every available scalar-exportable GPU field",
		MarkerName:  "dcgm_exporter_e2e_host_nvml_injection_metrics",
		ResultName:  "nvmlInjectionMetrics",
		Enabled:     EnabledNVMLInjection,
		Requirements: []Requirement{
			{Kind: RequirementDCGMDevelopment},
		},
	},
	{
		Suite:       SuiteHost,
		Name:        "migXidCounters",
		Group:       GroupHostNVMLInjection,
		Description: "MIG CI XID counters against independently read native history",
		MarkerName:  "dcgm_exporter_e2e_host_mig_xid_counters",
		ResultName:  "migXidCounters",
		Enabled:     EnabledNVMLInjection,
		Requirements: []Requirement{
			{Kind: RequirementDCGMDevelopment},
		},
	},
	{
		Suite:       SuiteHost,
		Name:        "nvsdmMockMetrics",
		Group:       GroupHostNVSDMMock,
		Description: "Direct host NVSwitch metrics from DCGM NVSDM mock telemetry",
		MarkerName:  "dcgm_exporter_e2e_host_nvsdm_mock_metrics",
		ResultName:  "nvsdmMockMetrics",
		Enabled:     "nvsdm_mock",
	},
	{
		Suite:       SuiteHost,
		Name:        "watchGroups",
		Group:       GroupHostConfigInjection,
		Description: "Direct host YAML watch group behavior",
		MarkerName:  "dcgm_exporter_e2e_host_watch_groups",
		ResultName:  "watchGroups",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "latestValueBatching",
		Group:       GroupHostBaseline,
		Description: "Direct host latest-value reads beyond the DCGM request boundary",
		MarkerName:  "dcgm_exporter_e2e_host_latest_value_batching",
		ResultName:  "latestValueBatching",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "oversizedFieldGroup",
		Group:       GroupHostBaseline,
		Description: "Direct host oversized logical field-group splitting",
		MarkerName:  "dcgm_exporter_e2e_host_oversized_field_group",
		ResultName:  "oversizedFieldGroup",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "runtimeContainerLabels",
		Group:       GroupHostBaseline,
		Description: "Direct host runtime container labels",
		MarkerName:  "dcgm_exporter_e2e_host_runtime_container_labels",
		ResultName:  "runtimeContainerLabels",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "hpcJobMapping",
		Group:       GroupHostConfigInjection,
		Description: "Direct host HPC/SLURM job mapping labels",
		MarkerName:  "dcgm_exporter_e2e_host_hpc_job_mapping",
		ResultName:  "hpcJobMapping",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "startupTLS",
		Group:       GroupHostBaseline,
		Description: "Direct host TLS and basic auth behavior",
		MarkerName:  "dcgm_exporter_e2e_host_startup_tls",
		ResultName:  "startupTLS",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "reload",
		Group:       GroupHostConfigInjection,
		Description: "Direct host SIGHUP and file-watcher reload behavior without profiling",
		MarkerName:  "dcgm_exporter_e2e_host_reload",
		ResultName:  "reload",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "profilingReload",
		Group:       GroupHostProfiling,
		Description: "Direct host profiling metrics across SIGHUP and file-watcher reloads",
		MarkerName:  "dcgm_exporter_e2e_host_profiling_reload",
		ResultName:  "profilingReload",
		Enabled:     "manual",
	},
	{
		Suite:       SuiteHost,
		Name:        "gpuLinkFields",
		Group:       GroupHostGPULinkFields,
		Description: "Check available GPU-owned link fields against direct DCGM field and physical-link identity",
		MarkerName:  "dcgm_exporter_e2e_host_gpu_link_fields",
		ResultName:  "gpuLinkFields",
		Enabled:     "manual",
	},
	{
		Suite:       SuiteHost,
		Name:        "dcgmUri",
		Group:       GroupHostRemoteDCGM,
		Description: "Direct host remote DCGM TCP URI behavior",
		MarkerName:  "dcgm_exporter_e2e_host_dcgm_uri",
		ResultName:  "dcgmUri",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "dcgmUriUnix",
		Group:       GroupHostRemoteDCGM,
		Description: "Direct host remote DCGM Unix URI behavior",
		MarkerName:  "dcgm_exporter_e2e_host_dcgm_uri_unix",
		ResultName:  "dcgmUriUnix",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "dcgmUriVSOCK",
		Group:       GroupHostVSOCK,
		Description: "Direct host remote DCGM VSOCK URI behavior",
		MarkerName:  "dcgm_exporter_e2e_host_dcgm_uri_vsock",
		ResultName:  "dcgmUriVSOCK",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementVSOCK},
		},
	},
	{
		Suite:       SuiteHost,
		Name:        "ipv6Listen",
		Group:       GroupHostBaseline,
		Description: "Direct host listen address and IPv6 scrape behavior",
		MarkerName:  "dcgm_exporter_e2e_host_ipv6_listen",
		ResultName:  "ipv6Listen",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "gpuBindUnbindWatch",
		Group:       GroupHostBaseline,
		Description: "Direct host GPU bind/unbind watcher startup smoke",
		MarkerName:  "dcgm_exporter_e2e_host_gpu_bind_unbind_watch",
		ResultName:  "gpuBindUnbindWatch",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "systemdSocket",
		Group:       GroupHostBaseline,
		Description: "Direct host systemd socket activation behavior",
		MarkerName:  "dcgm_exporter_e2e_host_systemd_socket",
		ResultName:  "systemdSocket",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "gpuSelection",
		Group:       GroupHostBaseline,
		Description: "Direct host GPU selection flag and config key report only the selected GPUs",
		MarkerName:  "dcgm_exporter_e2e_host_gpu_selection",
		ResultName:  "gpuSelection",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "modelNameLabels",
		Group:       GroupHostBaseline,
		Description: "Direct host blank replacement in every model name label",
		MarkerName:  "dcgm_exporter_e2e_host_model_name_labels",
		ResultName:  "modelNameLabels",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "scrapeLimit",
		Group:       GroupHostBaseline,
		Description: "Direct host scrape limit refuses overlapping scrapes",
		MarkerName:  "dcgm_exporter_e2e_host_scrape_limit",
		ResultName:  "scrapeLimit",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "httpTimeouts",
		Group:       GroupHostBaseline,
		Description: "Direct host HTTP read and write timeouts close stalled connections and fail slow writes",
		MarkerName:  "dcgm_exporter_e2e_host_http_timeouts",
		ResultName:  "httpTimeouts",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "logLevels",
		Group:       GroupHostBaseline,
		Description: "Direct host debug log level and embedded DCGM log reach the process output",
		MarkerName:  "dcgm_exporter_e2e_host_log_levels",
		ResultName:  "logLevels",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "injectedEventCounts",
		Group:       GroupHostBaseline,
		Description: "XID and clock-event counts, totals and last XID follow events injected into one real GPU through a private hostengine, and a metrics override sets how long an XID is counted",
		MarkerName:  "dcgm_exporter_e2e_host_injected_event_counts",
		ResultName:  "injectedEventCounts",
		Enabled:     "always",
	},
	{
		Suite:       SuiteHost,
		Name:        "gpuBindUnbindEvents",
		Group:       GroupHostGPUBindUnbind,
		Description: "Unbind one idle GPU from the nvidia driver and bind it again; its series disappear and return with the bind/unbind watch on. Refuses on a host with one GPU, no idle MIG-off GPU, persistence mode on, an NVLink fabric, or a driver older than 590; needs passwordless sudo",
		MarkerName:  "dcgm_exporter_e2e_host_gpu_bind_unbind_events",
		ResultName:  "gpuBindUnbindEvents",
		Enabled:     "manual",
	},
	{
		Suite:       SuiteHost,
		Name:        "migGpuLevelFields",
		Group:       GroupHostMIGTwoInstancesPerGPU,
		Description: "Every GPU in MIG mode with two instances of the largest profile that fits twice; the exporter defaults stay up for a minute and report GPU-level families once per physical GPU while instance-level families keep their instance series. Changes MIG on every GPU and restores it; skipped with fewer than two GPUs or a GPU without MIG; needs passwordless sudo",
		MarkerName:  "dcgm_exporter_e2e_host_mig_gpu_level_fields",
		ResultName:  "migGpuLevelFields",
		Enabled:     "manual",
		MIGLayout:   MIGLayoutTwoInstancesPerGPU,
	},
	{
		Suite:       SuiteHost,
		Name:        "gpuLoadUtilization",
		Group:       GroupHostGPULoad,
		Description: "GPU utilization and energy follow dcgmproftester load on one GPU against the other idle GPUs; skipped with fewer than two idle GPUs",
		MarkerName:  "dcgm_exporter_e2e_host_gpu_load_utilization",
		ResultName:  "gpuLoadUtilization",
		Enabled:     "manual",
		Requirements: []Requirement{
			{Kind: RequirementGPURuntime},
			{Kind: RequirementContainerImage, Image: ContainerImageDCGM},
		},
	},

	{
		Suite:       SuiteContainer,
		Name:        "imageStartup",
		Group:       GroupContainerBaseline,
		Description: "Container image startup, health, and default metrics",
		MarkerName:  "dcgm_exporter_e2e_container_image_startup",
		ResultName:  "imageStartup",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementGPURuntime},
		},
	},
	{
		Suite:       SuiteContainer,
		Name:        "dcgmLibraryFailure",
		Group:       GroupContainerBaseline,
		Description: "Container missing DCGM library exits cleanly with actionable guidance",
		MarkerName:  "dcgm_exporter_e2e_container_dcgm_library_failure",
		ResultName:  "dcgmLibraryFailure",
		Enabled:     "always",
	},
	{
		Suite:       SuiteContainer,
		Name:        "configuration",
		Group:       GroupContainerBaseline,
		Description: "Container runtime configuration and custom collectors",
		MarkerName:  "dcgm_exporter_e2e_container_configuration",
		ResultName:  "configuration",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementGPURuntime},
		},
	},
	{
		Suite:       SuiteContainer,
		Name:        "logFormat",
		Group:       GroupContainerBaseline,
		Description: "Container emits structured JSON startup logs",
		MarkerName:  "dcgm_exporter_e2e_container_log_format",
		ResultName:  "logFormat",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementGPURuntime},
		},
	},
	{
		Suite:       SuiteContainer,
		Name:        "pprofRequiresWebConfig",
		Group:       GroupContainerBaseline,
		Description: "Container startup rejects pprof without a TLS or basic auth web config",
		MarkerName:  "dcgm_exporter_e2e_container_pprof_requires_web_config",
		ResultName:  "pprofRequiresWebConfig",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementGPURuntime},
		},
	},
	{
		Suite:       SuiteContainer,
		Name:        "invalidDeviceSelectors",
		Group:       GroupContainerBaseline,
		Description: "Container invalid device selector diagnostics",
		MarkerName:  "dcgm_exporter_e2e_container_invalid_device_selectors",
		ResultName:  "invalidDeviceSelectors",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementGPURuntime},
		},
	},
	{
		Suite:       SuiteContainer,
		Name:        "remoteDcgmUri",
		Group:       GroupContainerRemoteDCGM,
		Description: "Container remote DCGM URI behavior",
		MarkerName:  "dcgm_exporter_e2e_container_remote_dcgm_uri",
		ResultName:  "remoteDcgmUri",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementVSOCK},
			{Kind: RequirementGPURuntime},
			{Kind: RequirementContainerImage, Image: ContainerImageDCGM},
		},
	},

	{
		Suite:       SuiteK8s,
		Name:        "default",
		Description: "Default Helm deployment, default metric contract and workload GPU attribution",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "default",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "configMatrix",
		Description: "Custom metrics replacement through Helm",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_config_matrix",
		ResultName:  "configMatrix",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "configMapData",
		Description: "Kubernetes ConfigMap metric loading through --configmap-data",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "configMapData",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "yamlConfig",
		Description: "Kubernetes YAML config loading through Helm",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_yaml_config",
		ResultName:  "yamlConfig",
		Enabled:     "always",
	},
	{
		Suite:       SuiteK8s,
		Name:        "oldNamespace",
		Description: "Legacy Kubernetes attribution label names",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "oldNamespace",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "clockEventsCounters",
		Description: "Clock event exporter counters",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "clockEventsCounters",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "tls",
		Description: "TLS scrape endpoint",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "tls",
		Enabled:     "always",
	},
	{
		Suite:       SuiteK8s,
		Name:        "basicAuth",
		Description: "Basic auth scrape endpoint",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "basicAuth",
		Enabled:     "always",
	},
	{
		Suite:       SuiteK8s,
		Name:        "tlsBasicAuth",
		Description: "TLS and basic auth together",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_tls_basic_auth",
		ResultName:  "tlsBasicAuth",
		Enabled:     "always",
	},
	{
		Suite:       SuiteK8s,
		Name:        "defaultChartMounts",
		Description: "Default chart's directory mounts in a running pod, also with a read-only root filesystem and with TLS; an edited customMetrics reloads without a restart",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_default_chart_mounts",
		ResultName:  "defaultChartMounts",
		Enabled:     "always",
	},
	{
		Suite:       SuiteK8s,
		Name:        "chartExistingConfig",
		Description: "Chart mounts a user-managed config ConfigMap (configMap.existingName and configMap.key) and the exporter reads it",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_chart_existing_config",
		ResultName:  "chartExistingConfig",
		Enabled:     "always",
	},
	{
		Suite:       SuiteK8s,
		Name:        "chartUpgrade",
		Description: "Helm upgrade from the last released chart: removed values are rejected with their replacements, and the replacement values upgrade the release",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_chart_upgrade",
		ResultName:  "chartUpgrade",
		// Installs the released chart from its public repository, so the cluster
		// controller needs network access to it.
		Enabled: "manual",
	},
	{
		Suite:       SuiteK8s,
		Name:        "serviceAccess",
		Description: "Kubernetes Service and direct scrape behavior",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_service_access",
		ResultName:  "serviceAccess",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "metricsNetworkPolicy",
		Description: "Helm NetworkPolicy admits only allowed scrapers; needs an enforcing CNI",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_metrics_network_policy",
		ResultName:  "metricsNetworkPolicy",
		// Not every network plugin enforces NetworkPolicy, and no probe can tell
		// without trying, so the scenario runs only when named.
		Enabled: "manual",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageBusybox},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "podHardening",
		Description: "Running exporter pod security settings and ServiceAccount RBAC",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_pod_hardening",
		ResultName:  "podHardening",
		Enabled:     "always",
	},
	{
		Suite:       SuiteK8s,
		Name:        "podLabels",
		Description: "Kubernetes pod label attribution",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_pod_labels",
		ResultName:  "podLabels",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "podUID",
		Description: "Kubernetes pod UID equals the workload pod's metadata.uid on its GPU",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_pod_uid",
		ResultName:  "podUID",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "podLabelAllowlist",
		Description: "Pod label allowlist filtering",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_pod_label_allowlist",
		ResultName:  "podLabelAllowlist",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "kubernetesGpuId",
		Description: "Kubernetes GPU ID mapping in uid and device-name modes",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_kubernetes_gpu_id",
		ResultName:  "kubernetesGpuId",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "noHostname",
		Description: "Hostname label suppression",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_no_hostname",
		ResultName:  "noHostname",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "modelName",
		Description: "Model name normalization",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_model_name",
		ResultName:  "modelName",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "pprof",
		Description: "pprof endpoint enablement",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "pprof",
		Enabled:     "always",
	},
	{
		Suite:       SuiteK8s,
		Name:        "debugDump",
		Description: "Debug dump file output",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_debug_dump",
		ResultName:  "debugDump",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageBusybox},
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "hpcJobMapping",
		Description: "HPC job mapping labels",
		Group:       GroupK8sEmbeddedBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_hpc_job_mapping",
		ResultName:  "hpcJobMapping",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "customResourceNames",
		Description: "Renamed nvidia.com/gpu.shared attribution needs --nvidia-resource-names",
		Group:       GroupK8sEmbeddedRenamedGPU,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_custom_resource_names",
		ResultName:  "customResourceNames",
		Enabled:     "shared_gpu",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "profiling",
		Description: "Datacenter profiling metrics",
		Group:       GroupK8sEmbeddedHardware,
		Features:    []string{"host:profiling"},
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "profiling",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputDCGMImage,
		},
	},
	{
		Suite:         SuiteK8s,
		Name:          "p2pStatus",
		Description:   "P2P status exporter metric",
		Group:         GroupK8sEmbeddedHardware,
		MarkerName:    "dcgm_exporter_e2e_p2p_status",
		Features:      []string{"host:gpu"},
		Allocation:    KubernetesAllocationGPU,
		Applicability: []string{"host:multi_gpu"},
		ResultName:    "p2pStatus",
		Enabled:       "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "fieldUnsupported",
		Description: "Unsupported DCGM field omission behavior",
		Group:       GroupK8sEmbeddedHardware,
		MarkerName:  "dcgm_exporter_e2e_field_unsupported",
		Features:    []string{"host:gpu"},
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "fieldUnsupported",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputDCGMImage,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "nvlinkFabric",
		Description: "NVLink fabric health and exporter samples for every active link",
		Group:       GroupK8sEmbeddedHardware,
		MarkerName:  "dcgm_exporter_e2e_nvlink_fabric",
		Features:    []string{"host:nvlink_fabric"},
		Allocation:  KubernetesAllocationGPU,
		// The checked per-link fields are DCGM profiling fields or NVLink 5
		// totals, so the scenario applies where DCGM supports profiling.
		Applicability: []string{"host:profiling"},
		ResultName:    "nvlinkFabric",
		Enabled:       "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputDCGMImage,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "nvlink",
		Description: "NVLink bandwidth metrics",
		Group:       GroupK8sEmbeddedHardware,
		Features:    []string{"host:nvlink_fabric"},
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "nvlink",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:         SuiteK8s,
		Name:          "nvlinkIntegrity",
		Description:   "NVLink 5+ integrity metrics",
		Group:         GroupK8sEmbeddedHardware,
		Features:      []string{"host:nvlink_fabric"},
		Allocation:    KubernetesAllocationGPU,
		Applicability: []string{"host:nvlink5"},
		ResultName:    "nvlinkIntegrity",
		Enabled:       "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "mig",
		Description: "MIG metrics and attribution",
		Group:       GroupK8sEmbeddedMIG,
		Allocation:  KubernetesAllocationMIG,
		ResultName:  "mig",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputMIGState,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "migFullGpuSelection",
		Description: "DCGM_EXPORTER_DEVICES_STR full GPU selection in mixed MIG mode",
		Group:       GroupK8sEmbeddedMIG,
		MarkerName:  "dcgm_exporter_e2e_mig_full_gpu_selection",
		Allocation:  KubernetesAllocationMixedMIG,
		ResultName:  "migFullGpuSelection",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputMIGState,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "migInstanceSelection",
		Description: "DCGM_EXPORTER_DEVICES_STR all-MIG-instance selection in mixed MIG mode",
		Group:       GroupK8sEmbeddedMIG,
		MarkerName:  "dcgm_exporter_e2e_mig_instance_selection",
		Allocation:  KubernetesAllocationMixedMIG,
		ResultName:  "migInstanceSelection",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputMIGState,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "migSpecificInstanceSelection",
		Description: "DCGM_EXPORTER_DEVICES_STR specific MIG instance selection in mixed MIG mode",
		Group:       GroupK8sEmbeddedMIG,
		MarkerName:  "dcgm_exporter_e2e_mig_specific_instance_selection",
		Allocation:  KubernetesAllocationMixedMIG,
		ResultName:  "migSpecificInstanceSelection",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputDCGMImage,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "migCombinedDeviceSelection",
		Description: "DCGM_EXPORTER_DEVICES_STR combined full-GPU and MIG-instance selection in mixed MIG mode",
		Group:       GroupK8sEmbeddedMIG,
		MarkerName:  "dcgm_exporter_e2e_mig_combined_device_selection",
		Allocation:  KubernetesAllocationMixedMIG,
		ResultName:  "migCombinedDeviceSelection",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputMIGState,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "migPodAttribution",
		Description: "Kubernetes attribution of a pod on a MIG instance, kept off Grace CPU metrics",
		Group:       GroupK8sEmbeddedMIG,
		MarkerName:  "dcgm_exporter_e2e_mig_pod_attribution",
		Features:    []string{"host:grace_cpu"},
		Allocation:  KubernetesAllocationMIG,
		ResultName:  "migPodAttribution",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputDCGMImage,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "dra",
		Description: "DRA attribution",
		Group:       GroupK8sEmbeddedDRA,
		Allocation:  KubernetesAllocationDRA,
		ResultName:  "dra",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "sharedGpu",
		Description: "Time-sliced GPU attribution",
		Group:       GroupK8sEmbeddedSharedGPU,
		MarkerName:  "dcgm_exporter_e2e_shared_gpu",
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "sharedGpu",
		Enabled:     "shared_gpu",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:         SuiteK8s,
		Name:          "nvswitch",
		Description:   "NVSwitch temperature metrics",
		Group:         GroupK8sEmbeddedNVSwitch,
		Features:      []string{"host:nvswitch"},
		Allocation:    KubernetesAllocationGPU,
		Applicability: []string{"host:nvswitch_local_telemetry"},
		ResultName:    "nvswitch",
		Enabled:       "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:         SuiteK8s,
		Name:          "nvswitchLink",
		Description:   "Legacy NVSwitch link error metrics",
		Group:         GroupK8sEmbeddedNVSwitch,
		Features:      []string{"host:nvswitch"},
		Allocation:    KubernetesAllocationGPU,
		Applicability: []string{"host:nvswitch_nscq"},
		ResultName:    "nvswitchLink",
		Enabled:       "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:         SuiteK8s,
		Name:          "nvswitchThroughput",
		Description:   "NVSwitch link throughput metrics",
		Group:         GroupK8sEmbeddedNVSwitch,
		Features:      []string{"host:nvswitch"},
		Allocation:    KubernetesAllocationGPU,
		Applicability: []string{"host:nvswitch_nvsdm"},
		ResultName:    "nvswitchThroughput",
		Enabled:       "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "graceCpu",
		Description: "Grace CPU/Sysmon metrics",
		Group:       GroupK8sEmbeddedHardware,
		MarkerName:  "dcgm_exporter_e2e_grace_cpu",
		Features:    []string{"host:grace_cpu"},
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "graceCpu",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputDCGMImage,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "c2c",
		Description: "C2C metrics",
		Group:       GroupK8sEmbeddedHardware,
		Features:    []string{"host:c2c"},
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "c2c",
		Enabled:     "always",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputDCGMImage,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "watchRetention",
		Description: "DCGM cache honors global and per-watch-group retention bounds",
		Group:       GroupK8sStandaloneBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_watch_retention",
		ResultName:  "watchRetention",
		Enabled:     "always",
		Remote:      true,
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageDCGM},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "remoteDcgm",
		Description: "Exporter connected to standalone DCGM",
		Group:       GroupK8sStandaloneBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_remote_dcgm",
		ResultName:  "remoteDcgm",
		Enabled:     "always",
		Remote:      true,
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageDCGM},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "remoteDcgmRestart",
		Description: "Exporter recovers after standalone DCGM restart",
		Group:       GroupK8sStandaloneBaseline,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_remote_dcgm_restart",
		ResultName:  "remoteDcgmRestart",
		Enabled:     "always",
		Remote:      true,
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageDCGM},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "remoteDcgmIPv6Hostname",
		Description: "Exporter reaches standalone DCGM through an IPv6-only Service hostname",
		Group:       GroupK8sStandaloneIPv6,
		Allocation:  KubernetesAllocationGPU,
		MarkerName:  "dcgm_exporter_e2e_remote_dcgm_ipv6_hostname",
		Features:    []string{"cluster:ipv6_single_stack"},
		ResultName:  "remoteDcgmIPv6Hostname",
		Enabled:     "manual",
		Remote:      true,
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageDCGM},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "gpuOperatorChart",
		Description: "GPU Operator ClusterPolicy reaches ready",
		Group:       GroupK8sGPUOperatorBaseline,
		MarkerName:  "dcgm_exporter_e2e_gpu_operator_chart",
		ResultName:  "gpuOperatorChart",
		Enabled:     "gpu_operator",
	},
	{
		Suite:       SuiteK8s,
		Name:        "gpuOperatorExporter",
		Description: "GPU Operator exporter DaemonSet is ready and runs the image under test",
		Group:       GroupK8sGPUOperatorBaseline,
		MarkerName:  "dcgm_exporter_e2e_gpu_operator_exporter",
		ResultName:  "gpuOperatorExporter",
		Enabled:     "gpu_operator",
	},
	{
		Suite:       SuiteK8s,
		Name:        "gpuOperatorSharedGpu",
		Description: "GPU Operator time-slicing replicas on node labels and capacity",
		Group:       GroupK8sGPUOperatorSharedGPU,
		MarkerName:  "dcgm_exporter_e2e_gpu_operator_shared_gpu",
		Allocation:  KubernetesAllocationGPU,
		ResultName:  "gpuOperatorSharedGpu",
		Enabled:     "gpu_operator",
	},
	{
		Suite:       SuiteK8s,
		Name:        "gpuOperatorMig",
		Description: "GPU Operator MIG integration",
		Group:       GroupK8sGPUOperatorMIG,
		MarkerName:  "dcgm_exporter_e2e_gpu_operator_mig",
		Allocation:  KubernetesAllocationMIG,
		ResultName:  "gpuOperatorMig",
		Enabled:     "gpu_operator",
		CapabilityProbeInputs: []CapabilityProbeInput{
			CapabilityProbeInputMIGState,
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "gpuOperatorDRA",
		Description: "GPU Operator DRA DeviceClass and GPU ResourceSlices",
		Group:       GroupK8sGPUOperatorDRA,
		MarkerName:  "dcgm_exporter_e2e_gpu_operator_dra",
		Features:    []string{"cluster:dra"},
		ResultName:  "gpuOperatorDRA",
		Enabled:     "gpu_operator",
	},
	{
		Suite:       SuiteK8s,
		Name:        "gpuOperatorIPv6",
		Description: "GPU Operator exporter over IPv6 service networking",
		Group:       GroupK8sGPUOperatorBaseline,
		MarkerName:  "dcgm_exporter_e2e_gpu_operator_ipv6",
		Features:    []string{"cluster:ipv6"},
		ResultName:  "gpuOperatorIPv6",
		Enabled:     "gpu_operator",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageBusybox},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "gpuOperatorMainEmbedded",
		Description: "GPU Operator main chart custom metrics and pod metadata",
		Group:       GroupK8sGPUOperatorMainEmbedded,
		MarkerName:  "dcgm_exporter_e2e_gpu_operator_main_embedded",
		ResultName:  "gpuOperatorMainEmbedded",
		Enabled:     "gpu_operator_chart",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageBusybox},
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "gpuOperatorMainRemote",
		Description: "GPU Operator main chart remote DCGM custom metrics",
		Group:       GroupK8sGPUOperatorMainRemote,
		MarkerName:  "dcgm_exporter_e2e_gpu_operator_main_remote",
		ResultName:  "gpuOperatorMainRemote",
		Enabled:     "gpu_operator_chart",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageGPUOperatorDCGM},
		},
	},
	{
		Suite:       SuiteK8s,
		Name:        "gpuOperatorMainDRA",
		Description: "GPU Operator main chart GPUCluster DRA attribution",
		Group:       GroupK8sGPUOperatorMainDRA,
		Features:    []string{"cluster:dra"},
		MarkerName:  "dcgm_exporter_e2e_gpu_operator_main_dra",
		ResultName:  "gpuOperatorMainDRA",
		Enabled:     "gpu_operator_chart",
		Requirements: []Requirement{
			{Kind: RequirementContainerImage, Image: ContainerImageGPUOperatorDCGM},
			{Kind: RequirementContainerImage, Image: ContainerImageCUDAWorkload},
		},
	},
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
