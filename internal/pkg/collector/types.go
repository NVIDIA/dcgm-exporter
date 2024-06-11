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

package collector

import (
	"fmt"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/collector/mock_collector.go -package=collector -copyright_file=../../../hack/header.txt . Collector

// Collector interface
type Collector interface {
	GetMetrics() (MetricsByCounter, error)
	Cleanup()
}

type EntityCollectorTuple struct {
	entity    dcgm.Field_Entity_Group
	collector Collector
}

func (e *EntityCollectorTuple) SetEntity(entity dcgm.Field_Entity_Group) {
	e.entity = entity
}

func (e *EntityCollectorTuple) Entity() dcgm.Field_Entity_Group {
	return e.entity
}

func (e *EntityCollectorTuple) SetCollector(collector Collector) {
	e.collector = collector
}

func (e *EntityCollectorTuple) Collector() Collector {
	return e.collector
}

type Metric struct {
	Counter counters.Counter
	Value   string

	GPU          string
	GPUUUID      string
	GPUDevice    string
	GPUModelName string
	GPUPCIBusID  string

	UUID string

	MigProfile    string
	GPUInstanceID string
	Hostname      string

	Labels     map[string]string
	Attributes map[string]string
}

func (m Metric) GetIDOfType(idType appconfig.KubernetesGPUIDType) (string, error) {
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

// MetricsByCounter represents a map where each Counter is associated with a slice of Metric objects
type MetricsByCounter map[counters.Counter][]Metric
