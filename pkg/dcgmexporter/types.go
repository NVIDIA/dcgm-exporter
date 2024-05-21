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

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/prometheus/exporter-toolkit/web"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
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

	hpcJobAttribute = "hpc_job"

	oldPodAttribute       = "pod_name"
	oldNamespaceAttribute = "pod_namespace"
	oldContainerAttribute = "container_name"

	undefinedConfigMapData = "none"
)

//go:generate go run -v go.uber.org/mock/mockgen  -destination=./mock_transformator.go -package=dcgmexporter -copyright_file=../../hack/header.txt . Transform

type Transform interface {
	Process(metrics MetricsByCounter, deviceInfo deviceinfo.Provider) error
	Name() string
}

type DCGMCollector struct {
	Counters                 []appconfig.Counter
	DeviceFields             []dcgm.Short
	Cleanups                 []func()
	UseOldNamespace          bool
	DeviceInfo               deviceinfo.Provider
	Hostname                 string
	ReplaceBlanksInModelName bool
}

type Metric struct {
	Counter appconfig.Counter
	Value   string

	GPU          string
	GPUUUID      string
	GPUDevice    string
	GPUModelName string

	UUID string

	MigProfile    string
	GPUInstanceID string
	Hostname      string

	Labels     map[string]string
	Attributes map[string]string
}

func (m Metric) getIDOfType(idType appconfig.KubernetesGPUIDType) (string, error) {
	// For MIG devices, return the MIG profile instead of
	if m.MigProfile != "" {
		return fmt.Sprintf("%s-%s", m.GPU, m.GPUInstanceID), nil
	}
	switch idType {
	case appconfig.GPUUID:
		return m.GPUUUID, nil
	case appconfig.DeviceName:
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

	server                         *http.Server
	webConfig                      *web.FlagConfig
	metrics                        string
	metricsChan                    chan string
	registry                       *Registry
	config                         *appconfig.Config
	transformations                []Transform
	fieldEntityGroupTypeSystemInfo *FieldEntityGroupTypeSystemInfo
}

type PodMapper struct {
	Config *appconfig.Config
}

type PodInfo struct {
	Name      string
	Namespace string
	Container string
}

// MetricsByCounter represents a map where each Counter is associated with a slice of Metric objects
type MetricsByCounter map[appconfig.Counter][]Metric

// CounterSet return
type CounterSet struct {
	DCGMCounters     []appconfig.Counter
	ExporterCounters []appconfig.Counter
}

// MetricsByCounterGroup represents a group of metrics by specific counter groups
type MetricsByCounterGroup map[dcgm.Field_Entity_Group]MetricsByCounter
