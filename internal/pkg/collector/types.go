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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

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
	Counter       counters.Counter        `json:"counter"`
	Value         string                  `json:"value"`
	GPU           string                  `json:"gpu,omitempty"`
	GPUUUID       string                  `json:"gpu_uuid,omitempty"`
	GPUDevice     string                  `json:"gpu_device,omitempty"`
	GPUModelName  string                  `json:"gpu_model,omitempty"`
	GPUPCIBusID   string                  `json:"pci_bus_id,omitempty"`
	UUID          string                  `json:"uuid,omitempty"`
	MigProfile    string                  `json:"mig_profile,omitempty"`
	NvSwitch      string                  `json:"nv_switch,omitempty"`
	NvLink        string                  `json:"nv_link,omitempty"`
	GPUInstanceID string                  `json:"gpu_instance_id,omitempty"`
	Hostname      string                  `json:"hostname"`
	Labels        map[string]string       `json:"labels"`
	Attributes    map[string]string       `json:"attributes"`
	ParentType    dcgm.Field_Entity_Group `json:"parent_type"`
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

// MarshalJSON implements custom JSON marshaling for MetricsByCounter
func (m MetricsByCounter) MarshalJSON() ([]byte, error) {
	metrics := make(map[string]any)

	// Use range over function for cleaner iteration
	for counter, metricList := range m {
		counterStr := counter.FieldName
		// Always include full metric details using original structs
		metrics[counterStr] = metricList
	}

	result := map[string]any{
		"metrics": metrics,
	}

	return json.Marshal(result)
}

// UnmarshalJSON implements custom JSON unmarshaling for MetricsByCounter
func (m *MetricsByCounter) UnmarshalJSON(data []byte) error {
	var result struct {
		Metrics map[string][]Metric `json:"metrics"`
	}

	err := json.Unmarshal(data, &result)
	if err != nil {
		return fmt.Errorf("failed to unmarshal metrics: %w", err)
	}

	// Convert the map[string][]Metric to map[counters.Counter][]Metric
	*m = make(MetricsByCounter)
	for fieldName, metricList := range result.Metrics {
		// Create a counter for each field name
		counter := counters.Counter{
			FieldName: fieldName,
		}
		// Use the first metric to populate counter details
		if len(metricList) > 0 {
			counter.FieldID = metricList[0].Counter.FieldID
			counter.PromType = metricList[0].Counter.PromType
		}
		(*m)[counter] = metricList
	}

	return nil
}

// String implements the Stringer interface to return JSON representation
func (m MetricsByCounter) String() string {
	jsonData, err := m.MarshalJSON()
	if err != nil {
		return fmt.Sprintf("error marshaling metrics: %v", err)
	}
	return string(jsonData)
}

// GoString implements the GoStringer interface to return Go syntax representation
func (m MetricsByCounter) GoString() string {
	var result strings.Builder
	result.WriteString("MetricsByCounter{")

	first := true
	for counter, metrics := range m {
		if !first {
			result.WriteString(", ")
		}
		first = false

		result.WriteString(fmt.Sprintf("%q: %#v", counter.FieldName, metrics))
	}

	result.WriteString("}")
	return result.String()
}

// ToBase64 returns the JSON representation encoded as base64
func (m MetricsByCounter) ToBase64() string {
	jsonData, err := m.MarshalJSON()
	if err != nil {
		return fmt.Sprintf("error marshaling metrics: %v", err)
	}
	return base64.StdEncoding.EncodeToString(jsonData)
}
