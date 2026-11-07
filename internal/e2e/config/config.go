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

// Package config contains parsed e2e CLI options.
package config

// BootstrapMode controls whether host prerequisites are checked, installed, or skipped.
type BootstrapMode string

const (
	// BootstrapCheck observes and verifies prerequisites without changing the host.
	BootstrapCheck BootstrapMode = "check"
	// BootstrapInstall installs missing prerequisites and verifies the result.
	BootstrapInstall BootstrapMode = "install"
	// BootstrapSkip bypasses prerequisite checks and host changes.
	BootstrapSkip BootstrapMode = "skip"
)

// ValueSource records where a CLI setting came from.
type ValueSource string

const (
	// ValueDefault identifies an artifact-mode default.
	ValueDefault ValueSource = "default"
	// ValueEnvironment identifies an environment variable.
	ValueEnvironment ValueSource = "environment"
	// ValueCLI identifies an explicit command-line flag.
	ValueCLI ValueSource = "cli"
)

// Config is the grouped e2e CLI configuration.
type Config struct {
	TestOptions TestOptions
}

// Dependencies contains pinned tool and chart versions supplied by the invoking environment.
type Dependencies struct {
	DCGMVersion             string
	K3DVersion              string
	K3SVersion              string
	HelmVersion             string
	DevicePluginVersion     string
	ContainerToolkitVersion string
	GPUOperatorVersion      string
	DRADriverVersion        string
	K3DAMD64SHA256          string
	K3DARM64SHA256          string
	KubectlAMD64SHA256      string
	KubectlARM64SHA256      string
	HelmAMD64SHA256         string
	HelmARM64SHA256         string
}

// TestOptions holds options for the tests command.
type TestOptions struct {
	RunName          string
	BuildOnly        bool
	PrebuiltBinaries bool
	ToolsDir         string
	ExporterBinary   string
	// BootstrapMode is the bootstrap mode selected after CLI parsing.
	BootstrapMode BootstrapMode
	// BootstrapComponents sets the mode for individual dependencies; a missing key uses BootstrapMode.
	BootstrapComponents map[BootstrapComponent]BootstrapMode
	// BootstrapOnly stops a run after prerequisite setup completes.
	BootstrapOnly bool
	// BootstrapSource records whether the mode came from a default, environment, or CLI flag.
	BootstrapSource  ValueSource
	DryRun           bool
	Verbose          bool
	ListScenarios    bool
	ResultMarkers    bool
	E2EResultMarkers bool
	NoResultMarkers  bool
	Suites           []string
	SkipSuites       []string
	Scenarios        []string
	SkipScenarios    []string
	Kubeconfig       string
	// Cleanup controls removal of test-owned resources; CLI/default constructors set it to true.
	Cleanup                 bool
	KeepCluster             bool
	ClusterName             string
	Namespace               string
	ReleaseName             string
	DockerRegistryLogins    []string
	DockerRegistryLoginFile string
	DockerConfigDir         string
	// CreateK8sImagePullSecret is derived after explicit Docker registry login succeeds.
	CreateK8sImagePullSecret  bool
	K8sImagePullSecret        string
	ExporterImage             string
	DCGMImage                 string
	K3SImage                  string
	K3DNodeBaseImage          string
	K3DNodeOutputImage        string
	BusyboxImage              string
	ContainerToolkitTestImage string
	CUDAWorkloadImage         string
	Dependencies              Dependencies
	MIGInstanceEntityID       string
	MIGInstanceNVMLID         string
	UnsupportedFieldCandidate string
	UnsupportedFieldEvidence  string
	RemoteDCGM                string
	DCGMNamespace             string
	DCGMName                  string
	DCGMPort                  string
	Timeout                   string // Bounds the complete tests command; empty leaves it unbounded.
	WaitTimeout               string
	K8sRuntimeClass           string
	K8sNodeSelectorKey        string
	K8sNodeSelectorValue      string
	K3dIPFamily               string
	K3dCgroupV1               FeatureMode
	SharedGPUConfigure        FeatureMode
	SharedGPUReplicas         string
	DRAConfigure              FeatureMode
	MIGConfigure              string
	DCGMNVMLInjectionYAML     string
	DCGMNVSDMMockYAML         string
	GPUOperator               GPUOperatorMode
	GPUOperatorChart          string
	GPUOperatorImage          string
	GPUOperatorDCGMImage      string
}
