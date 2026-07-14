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

// Config is the grouped e2e CLI configuration.
type Config struct {
	Tests Tests
}

// Dependencies contains pinned tool and chart versions supplied by the invoking environment.
type Dependencies struct {
	DCGMVersion         string
	K3DVersion          string
	K3SVersion          string
	HelmVersion         string
	DevicePluginVersion string
	GPUOperatorVersion  string
	DRADriverVersion    string
	K3DAMD64SHA256      string
	K3DARM64SHA256      string
	KubectlAMD64SHA256  string
	KubectlARM64SHA256  string
	HelmAMD64SHA256     string
	HelmARM64SHA256     string
}

// Tests holds options for the tests command.
type Tests struct {
	BuildOnly                 bool
	ToolsDir                  string
	InstallDeps               bool
	InstallDepsDocker         string
	InstallDepsNvidiaToolkit  string
	InstallDepsDCGM           string
	InstallDepsVSOCK          string
	DryRun                    bool
	Verbose                   bool
	ListScenarios             bool
	ResultMarkers             bool
	E2EResultMarkers          bool
	NoResultMarkers           bool
	Suites                    []string
	SkipSuites                []string
	Scenarios                 []string
	SkipScenarios             []string
	Kubeconfig                string
	KeepCluster               bool
	ClusterName               string
	Namespace                 string
	ReleaseName               string
	DockerRegistryLogins      []string
	DockerRegistryLoginFile   string
	DockerConfigDir           string
	K8sImagePullSecret        string
	ExporterImage             string
	ExporterUbuntuImage       string
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
	WaitTimeout               string
	K8sRuntimeClass           string
	K8sNodeSelectorKey        string
	K8sNodeSelectorValue      string
	K3dIPFamily               string
	SharedGPUConfigure        string
	SharedGPUReplicas         string
	DRAConfigure              string
	MIGConfigure              string
	DCGMFailureInjection      string
	DCGMNVMLInjectionYAML     string
	GPUOperator               string
}
