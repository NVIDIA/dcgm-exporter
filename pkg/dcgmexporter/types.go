/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmexporter

import (
	"fmt"
	"net/http"
	"sync"
	"text/template"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/prometheus/exporter-toolkit/web"
)

var (
	SkipDCGMValue   = "SKIPPING DCGM VALUE"
	FailedToConvert = "ERROR - FAILED TO CONVERT TO STRING"

	nvidiaResourceName      = "nvidia.com/gpu"
	nvidiaMigResourcePrefix = "nvidia.com/mig-"
	MIG_UUID_PREFIX         = "MIG-"

	// Note standard resource attributes
	podAttribute       = "pod"
	namespaceAttribute = "namespace"
	containerAttribute = "container"

	oldPodAttribute       = "pod_name"
	oldNamespaceAttribute = "pod_namespace"
	oldContainerAttribute = "container_name"

	undefinedConfigMapData = "none"
)

type KubernetesGPUIDType string

const (
	GPUUID     KubernetesGPUIDType = "uid"
	DeviceName KubernetesGPUIDType = "device-name"
)

type DeviceOptions struct {
	Flex       bool  // If true, then monitor all GPUs if MIG mode is disabled or all GPU instances if MIG is enabled.
	MajorRange []int // The indices of each GPU/NvSwitch to monitor, or -1 to monitor all
	MinorRange []int // The indices of each GPUInstance/NvLink to monitor, or -1 to monitor all
}

type Config struct {
	CollectorsFile      string
	Address             string
	CollectInterval     int
	Kubernetes          bool
	KubernetesGPUIdType KubernetesGPUIDType
	CollectDCP          bool
	UseOldNamespace     bool
	UseRemoteHE         bool
	RemoteHEInfo        string
	GPUDevices          DeviceOptions
	SwitchDevices       DeviceOptions
	NoHostname          bool
	UseFakeGpus         bool
	ConfigMapData       string
	MetricGroups        []dcgm.MetricGroup
	WebSystemdSocket    bool
	WebConfigFile       string
}

type Transform interface {
	Process(metrics [][]Metric, sysInfo SystemInfo) error
	Name() string
}

type MetricsPipeline struct {
	config *Config

	transformations     []Transform
	migMetricsFormat    *template.Template
	switchMetricsFormat *template.Template
	linkMetricsFormat   *template.Template

	counters        []Counter
	gpuCollector    *DCGMCollector
	switchCollector *DCGMCollector
	linkCollector   *DCGMCollector
}

type DCGMCollector struct {
	Counters        []Counter
	DeviceFields    []dcgm.Short
	Cleanups        []func()
	UseOldNamespace bool
	SysInfo         SystemInfo
	Hostname        string
}

type Counter struct {
	FieldID   dcgm.Short
	FieldName string
	PromType  string
	Help      string
}

type Metric struct {
	Counter *Counter
	Value   string

	GPU          string
	GPUUUID      string
	GPUDevice    string
	GPUModelName string

	UUID string

	MigProfile    string
	GPUInstanceID string
	Hostname      string

	Labels     *map[string]string
	Attributes map[string]string
}

func (m Metric) getIDOfType(idType KubernetesGPUIDType) (string, error) {
	// For MIG devices, return the MIG profile instead of
	if m.MigProfile != "" {
		return fmt.Sprintf("%s-%s", m.GPU, m.GPUInstanceID), nil
	}
	switch idType {
	case GPUUID:
		return m.GPUUUID, nil
	case DeviceName:
		return m.GPUDevice, nil
	}
	return "", fmt.Errorf("unsupported KubernetesGPUIDType for MetricID '%s'", idType)
}

var promMetricType = map[string]bool{
	"gauge":     true,
	"counter":   true,
	"histogram": true,
	"summary":   true,
	"label":     true,
}

type MetricsServer struct {
	sync.Mutex

	server      *http.Server
	webConfig   web.FlagConfig
	metrics     string
	metricsChan chan string
}

type PodMapper struct {
	Config *Config
}

type PodInfo struct {
	Name      string
	Namespace string
	Container string
}
