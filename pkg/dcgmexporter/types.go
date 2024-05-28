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
	"net/http"
	"sync"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/prometheus/exporter-toolkit/web"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

var (
	// Note standard resource attributes
	podAttribute       = "pod"
	namespaceAttribute = "namespace"
	containerAttribute = "container"

	hpcJobAttribute = "hpc_job"

	oldPodAttribute       = "pod_name"
	oldNamespaceAttribute = "pod_namespace"
	oldContainerAttribute = "container_name"
)

//go:generate go run -v go.uber.org/mock/mockgen  -destination=./mock_transformator.go -package=dcgmexporter -copyright_file=../../hack/header.txt . Transform

type Transform interface {
	Process(metrics collector.MetricsByCounter, deviceInfo deviceinfo.Provider) error
	Name() string
}

type MetricsServer struct {
	sync.Mutex

	server                 *http.Server
	webConfig              *web.FlagConfig
	metrics                string
	metricsChan            chan string
	registry               *Registry
	config                 *appconfig.Config
	transformations        []Transform
	deviceWatchListManager devicewatchlistmanager.Manager
}

type PodMapper struct {
	Config *appconfig.Config
}

type PodInfo struct {
	Name      string
	Namespace string
	Container string
}

// MetricsByCounterGroup represents a group of metrics by specific counter groups
type MetricsByCounterGroup map[dcgm.Field_Entity_Group]collector.MetricsByCounter
