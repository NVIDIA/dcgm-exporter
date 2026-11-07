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

// Collector supplies metrics during a scrape and releases its owned resources
// when the registry retires the collector's runtime.
type Collector interface {
	GetMetrics() (MetricsByCounter, error)
	Cleanup()
}

// CleanupCompleter is implemented by collectors whose Cleanup method only
// requests a background worker to stop. The registry waits for this channel
// before it allows the application to release the native client that worker
// might still be using.
type CleanupCompleter interface {
	CleanupComplete() <-chan struct{}
}

// CleanupCompletionError wraps a failed collector build whose rollback asked a
// background worker to stop. Application code waits for CleanupComplete before
// releasing the client that worker may still use.
type CleanupCompletionError interface {
	error
	CleanupComplete() <-chan struct{}
}

// EntityCollectorTuple pairs a DCGM entity group with the collector registered for it.
type EntityCollectorTuple struct {
	entity    dcgm.Field_Entity_Group
	collector Collector
}

// SetEntity assigns the entity group used when registering this collector.
func (e *EntityCollectorTuple) SetEntity(entity dcgm.Field_Entity_Group) {
	e.entity = entity
}

// Entity returns the entity group used when registering this collector.
func (e *EntityCollectorTuple) Entity() dcgm.Field_Entity_Group {
	return e.entity
}

// SetCollector assigns the collector registered for this entity group.
func (e *EntityCollectorTuple) SetCollector(collector Collector) {
	e.collector = collector
}

// Collector returns the collector registered for this entity group.
func (e *EntityCollectorTuple) Collector() Collector {
	return e.collector
}

// Metric is one raw collector sample. Renderers turn its entity identities and
// labels into the stable Prometheus contract for the sample's DCGM group.
type Metric struct {
	Counter counters.Counter `json:"counter"`
	Value   string           `json:"value"`
	// Timestamp is Unix microseconds: the DCGM sample time, or the read time
	// for synthesized metrics and native samples without a timestamp.
	// Some synthesized metrics may leave it zero.
	Timestamp int64 `json:"timestamp,omitempty"`
	// StartTimestamp is the exporter collector start in Unix microseconds, including health state series.
	StartTimestamp int64  `json:"-"`
	GPU            string `json:"gpu,omitempty"`
	GPUUUID        string `json:"gpu_uuid,omitempty"`
	GPUDevice      string `json:"gpu_device,omitempty"`
	CPUSerial      string `json:"cpu_serial,omitempty"`
	GPUModelName   string `json:"gpu_model,omitempty"`
	GPUPCIBusID    string `json:"pci_bus_id,omitempty"`
	UUID           string `json:"uuid,omitempty"`
	// UUIDLabelName is the renderer label name for GPUUUID. UUID remains for
	// debug JSON compatibility while producers move to this truthful name.
	UUIDLabelName string `json:"-"`
	// CPU and CPUCore identify CPU entities without storing them in GPU fields.
	CPU           string `json:"cpu,omitempty"`
	CPUCore       string `json:"cpu_core,omitempty"`
	MigProfile    string `json:"mig_profile,omitempty"`
	NvSwitch      string `json:"nv_switch,omitempty"`
	NvLink        string `json:"nv_link,omitempty"`
	GPUInstanceID string `json:"gpu_instance_id,omitempty"`
	// GPUComputeInstanceID becomes the GPU_CI_ID metric label for compute-instance metrics.
	GPUComputeInstanceID string                  `json:"gpu_compute_instance_id,omitempty"`
	Hostname             string                  `json:"hostname"`
	Labels               map[string]string       `json:"labels"`
	Attributes           map[string]string       `json:"attributes"`
	ParentType           dcgm.Field_Entity_Group `json:"parent_type"`
	// Source preserves raw identity for OTLP and is nil when OTLP is disabled.
	Source *SourceIdentity `json:"-"`
}

// SourceIdentity retains approved identity lost by Prometheus label formatting.
// It is populated only for OTLP and is immutable after collection/enrichment.
type SourceIdentity struct {
	EntityGroup                                                dcgm.Field_Entity_Group
	SyntheticGPUUUID                                           bool
	GPUModelName, GPUSerial, GPUVBIOS, GPUDriverVersion        string
	NVLinkIndex, NVSwitchIndex                                 *int64
	PCIeMaxGeneration, PCIeMaxWidth, PCIeGeneration, PCIeWidth *int64
	PodLabels                                                  map[string]string
	DevicePluginResourceName                                   string
	Containers                                                 []ContainerIdentity
	PeerGPUUUID, PeerPCIPathType                               string
}

// ContainerIdentity retains runtime identity independently of the legacy label.
type ContainerIdentity struct {
	Name, ID string
}

// SelectedUUIDLabelName returns the configured renderer label name. It reads
// UUID only for older fixtures and serialized metrics during this migration.
func (m Metric) SelectedUUIDLabelName() string {
	if m.UUIDLabelName != "" {
		return m.UUIDLabelName
	}
	return m.UUID
}

// GetIDOfType returns the GPU UUID or device name that idType selects. A MIG
// metric always returns "<gpu>-<gpu instance ID>". An unsupported idType
// returns an error.
func (m Metric) GetIDOfType(idType appconfig.KubernetesGPUIDType) (string, error) {
	// A MIG metric is identified by its GPU and GPU instance, whatever idType is.
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

		debugMetrics := fmt.Sprintf("%#v", metrics)
		debugMetrics = strings.ReplaceAll(debugMetrics, `, Timestamp:0`, "")
		debugMetrics = strings.ReplaceAll(debugMetrics, `, StartTimestamp:0`, "")
		// Preserve the long-standing debug shape for each absent identity while
		// retaining the new identities that are present on a sample.
		debugMetrics = strings.ReplaceAll(debugMetrics, `, UUIDLabelName:""`, "")
		debugMetrics = strings.ReplaceAll(debugMetrics, `, CPU:""`, "")
		debugMetrics = strings.ReplaceAll(debugMetrics, `, CPUCore:""`, "")
		debugMetrics = strings.ReplaceAll(debugMetrics, `, Source:(*collector.SourceIdentity)(nil)`, "")
		fmt.Fprintf(&result, "%q: %s", counter.FieldName, debugMetrics)
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
