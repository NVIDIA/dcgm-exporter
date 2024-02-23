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

package dcgmexporter

import (
	"fmt"
	"io"
	"maps"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
)

var expMetricsFormat = `

{{- range $counter, $metrics := . -}}
# HELP {{ $counter.FieldName }} {{ $counter.Help }}
# TYPE {{ $counter.FieldName }} {{ $counter.PromType }}
{{- range $metric := $metrics }}
{{ $counter.FieldName }}{gpu="{{ $metric.GPU }}",{{ $metric.UUID }}="{{ $metric.GPUUUID }}",device="{{ $metric.GPUDevice }}",modelName="{{ $metric.GPUModelName }}"{{if $metric.MigProfile}},GPU_I_PROFILE="{{ $metric.MigProfile }}",GPU_I_ID="{{ $metric.GPUInstanceID }}"{{end}}{{if $metric.Hostname }},Hostname="{{ $metric.Hostname }}"{{end}}

{{- range $k, $v := $metric.Labels -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}
{{- range $k, $v := $metric.Attributes -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}

} {{ $metric.Value -}}
{{- end }}
{{ end }}`

// Collector interface
type Collector interface {
	GetMetrics() (MetricsByCounter, error)
	Cleanup()
}

var getExpMetricTemplate = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("expMetrics").Parse(expMetricsFormat))

})

func encodeExpMetrics(w io.Writer, metrics MetricsByCounter) error {
	tmpl := getExpMetricTemplate()
	return tmpl.Execute(w, metrics)
}

var expCollectorFieldGroupIdx atomic.Uint32

type expCollector struct {
	sysInfo             SystemInfo                     // Hardware system info
	counter             Counter                        // Counter that collector
	hostname            string                         // Hostname
	config              *Config                        // Configuration settings
	labelDeviceFields   []dcgm.Short                   // Fields used for labels
	counterDeviceFields []dcgm.Short                   // Fields used for the counter
	labelsCounters      []Counter                      // Counters used for labels
	cleanups            []func()                       // Cleanup functions
	fieldValueParser    func(val int64) []int64        // Function to parse the field value
	labelFiller         func(map[string]string, int64) // Function to fill labels
	windowSize          int                            // Window size
	transformations     []Transform                    // Transformers for metric postprocessing
}

func (c *expCollector) getMetrics() (MetricsByCounter, error) {

	fieldGroupIdx := expCollectorFieldGroupIdx.Add(1)

	fieldGroupName := fmt.Sprintf("expCollectorFieldGroupName%d", fieldGroupIdx)
	fieldsGroup, err := dcgm.FieldGroupCreate(fieldGroupName, c.counterDeviceFields)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = dcgm.FieldGroupDestroy(fieldsGroup)
	}()

	err = dcgm.UpdateAllFields()
	if err != nil {
		return nil, err
	}

	mapEntityIDToValues := map[uint]map[int64]int{}

	window := time.Now().Add(-time.Duration(c.windowSize) * time.Millisecond)

	values, _, err := dcgm.GetValuesSince(dcgm.GroupAllGPUs(), fieldsGroup, window)
	if err != nil {
		return nil, err
	}

	for _, val := range values {
		if val.Status == 0 {
			if _, exists := mapEntityIDToValues[val.EntityId]; !exists {
				mapEntityIDToValues[val.EntityId] = map[int64]int{}
			}
			for _, v := range c.fieldValueParser(val.Int64()) {
				mapEntityIDToValues[val.EntityId][v] += 1
			}
		}
	}

	labels := map[string]string{}
	labels[windowSizeInMSLabel] = fmt.Sprint(c.windowSize)

	monitoringInfo := GetMonitoredEntities(c.sysInfo)
	metrics := make(MetricsByCounter)
	useOld := c.config.UseOldNamespace
	uuid := "UUID"
	if useOld {
		uuid = "uuid"
	}
	for _, mi := range monitoringInfo {
		if len(c.labelsCounters) > 0 {
			err := c.getLabelsFromCounters(mi, labels)
			if err != nil {
				return nil, err
			}
		}
		entityValues, exists := mapEntityIDToValues[mi.DeviceInfo.GPU]
		if exists {
			for entityValue, val := range entityValues {

				metricValueLabels := maps.Clone(labels)
				c.labelFiller(metricValueLabels, entityValue)

				gpuModel := getGPUModel(mi.DeviceInfo, c.config.ReplaceBlanksInModelName)

				m := Metric{
					Counter:      c.counter,
					Value:        fmt.Sprint(val),
					UUID:         uuid,
					GPU:          fmt.Sprintf("%d", mi.DeviceInfo.GPU),
					GPUUUID:      mi.DeviceInfo.UUID,
					GPUDevice:    fmt.Sprintf("nvidia%d", mi.DeviceInfo.GPU),
					GPUModelName: gpuModel,
					Hostname:     c.hostname,

					Labels:     metricValueLabels,
					Attributes: map[string]string{},
				}
				if mi.InstanceInfo != nil {
					m.MigProfile = mi.InstanceInfo.ProfileName
					m.GPUInstanceID = fmt.Sprintf("%d", mi.InstanceInfo.Info.NvmlInstanceId)
				} else {
					m.MigProfile = ""
					m.GPUInstanceID = ""
				}

				metrics[c.counter] = append(metrics[c.counter], m)
			}
		}
	}

	for _, transform := range c.transformations {
		err := transform.Process(metrics, c.sysInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to transform metrics for transform '%s'; err: %v", transform.Name(), err)
		}
	}

	return metrics, nil
}

func (c *expCollector) getLabelsFromCounters(mi MonitoringInfo, labels map[string]string) error {
	latestValues, err := dcgm.EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId, c.labelDeviceFields)
	if err != nil {
		return err
	}
	// Extract Labels
	for _, val := range latestValues {
		v := ToString(val)
		// Filter out counters with no value and ignored fields for this entity
		if v == SkipDCGMValue {
			continue
		}

		counter, err := FindCounterField(c.labelsCounters, val.FieldId)
		if err != nil {
			continue
		}

		if counter.PromType == "label" {
			labels[counter.FieldName] = v
			continue
		}
	}
	return nil
}

func (c *expCollector) Cleanup() {
	for _, cleanup := range c.cleanups {
		cleanup()
	}
}

// newExpCollector is a constructor for the expCollector
func newExpCollector(
	counters []Counter,
	hostname string,
	counterDeviceFields []dcgm.Short,
	config *Config,
	fieldEntityGroupTypeSystemInfo FieldEntityGroupTypeSystemInfoItem,
) expCollector {
	var labelsCounters []Counter
	for i := 0; i < len(counters); i++ {
		if counters[i].PromType == "label" {
			labelsCounters = append(labelsCounters, counters[i])
		}
	}

	labelDeviceFields := NewDeviceFields(labelsCounters, dcgm.FE_GPU)

	transformations := getTransformations(config)

	collector := expCollector{
		hostname:            hostname,
		config:              config,
		labelDeviceFields:   labelDeviceFields,
		labelsCounters:      labelsCounters,
		counterDeviceFields: counterDeviceFields,
		fieldValueParser: func(val int64) []int64 {
			return []int64{val}
		},
		labelFiller:     func(metricValueLabels map[string]string, entityValue int64) {},
		transformations: transformations,
	}

	collector.sysInfo = fieldEntityGroupTypeSystemInfo.SystemInfo

	var err error

	collector.cleanups, err = SetupDcgmFieldsWatch(collector.counterDeviceFields,
		collector.sysInfo,
		int64(config.CollectInterval)*1000)
	if err != nil {
		logrus.Fatal("Failed to watch metrics: ", err)
	}

	return collector
}
