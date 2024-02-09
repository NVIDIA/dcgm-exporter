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
	"math/rand"
	"slices"
	"sync"
	"text/template"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
)

const (
	dcgmExpXIDErrorsCount = "DCGM_EXP_XID_ERRORS_COUNT"
	windowSizeInMSLabel   = "window_size_in_ms"
)

var xidMetricsFormat = `
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

type Collector interface {
	GetMetrics() (map[Counter][]Metric, error)
	Cleanup()
}

type xidCollector struct {
	sysInfo        *SystemInfo
	counter        Counter
	hostname       string
	config         *Config
	deviceFields   []dcgm.Short
	labelsCounters []Counter
	cleanups       []func()
}

func (c *xidCollector) GetMetrics() (map[Counter][]Metric, error) {
	// Create a group of fields
	const (
		xid int = iota
	)

	deviceFields := make([]dcgm.Short, 1)
	deviceFields[xid] = dcgm.DCGM_FI_DEV_XID_ERRORS

	fieldGroupName := fmt.Sprintf("fieldGroupName%d", rand.Uint64())
	fieldsGroup, err := dcgm.FieldGroupCreate(fieldGroupName, deviceFields)
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

	window := time.Now().Add(-time.Duration(c.config.XIDCountWindowSize) * time.Millisecond)

	values, _, err := dcgm.GetValuesSince(dcgm.GroupAllGPUs(), fieldsGroup, window)
	if err != nil {
		return nil, err
	}

	for _, val := range values {
		if val.Status == 0 {
			if _, exists := mapEntityIDToValues[val.EntityId]; !exists {
				mapEntityIDToValues[val.EntityId] = map[int64]int{}
			}
			mapEntityIDToValues[val.EntityId][val.Int64()] += 1
		}
	}

	labels := map[string]string{}
	labels[windowSizeInMSLabel] = fmt.Sprint(c.config.XIDCountWindowSize)

	monitoringInfo := GetMonitoredEntities(*c.sysInfo)
	metrics := make(map[Counter][]Metric)
	useOld := c.config.UseOldNamespace
	uuid := "UUID"
	if useOld {
		uuid = "uuid"
	}
	for _, mi := range monitoringInfo {
		vals, err := dcgm.EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId, c.deviceFields)
		if err != nil {
			return nil, err
		}
		// Extract Labels
		for _, val := range vals {
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

		gpuXIDErrors, exists := mapEntityIDToValues[mi.DeviceInfo.GPU]

		if exists {
			for xidErr, val := range gpuXIDErrors {

				metricValueLables := maps.Clone(labels)
				metricValueLables["xid"] = fmt.Sprint(xidErr)
				m := Metric{
					Counter:      c.counter,
					Value:        fmt.Sprint(val),
					UUID:         uuid,
					GPU:          fmt.Sprintf("%d", mi.DeviceInfo.GPU),
					GPUUUID:      mi.DeviceInfo.UUID,
					GPUDevice:    fmt.Sprintf("nvidia%d", mi.DeviceInfo.GPU),
					GPUModelName: mi.DeviceInfo.Identifiers.Model,
					Hostname:     c.hostname,

					Labels:     metricValueLables,
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

	return metrics, nil
}

func (c *xidCollector) Cleanup() {
	for _, cleanup := range c.cleanups {
		cleanup()
	}
}

func NewXIDCollector(config *Config, counters []Counter, hostname string) (Collector, error) {

	if !IsdcgmExpXIDErrorsCountEnabled(counters) {
		logrus.Error(dcgmExpXIDErrorsCount + " collector is disabled")
		return nil, fmt.Errorf(dcgmExpXIDErrorsCount + " collector is disabled")
	}

	sysInfo, err := GetSystemInfo(config, dcgm.FE_GPU)
	if err != nil {
		return nil, err
	}

	labelsCounters := []Counter{}
	for i := 0; i < len(counters); i++ {
		if counters[i].PromType == "label" {
			labelsCounters = append(labelsCounters, counters[i])
			counters = slices.Delete(counters, i, i+1)
		}
	}

	var deviceFields = NewDeviceFields(labelsCounters, dcgm.FE_GPU)

	cleanups, err := SetupDcgmFieldsWatch([]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS}, *sysInfo, int64(config.CollectInterval)*1000)
	if err != nil {
		logrus.Fatal("Failed to watch metrics: ", err)
	}

	counter := counters[slices.IndexFunc(counters, func(c Counter) bool {
		return c.FieldName == dcgmExpXIDErrorsCount
	})]

	collector := xidCollector{
		sysInfo:        sysInfo,
		counter:        counter,
		hostname:       hostname,
		config:         config,
		deviceFields:   deviceFields,
		labelsCounters: labelsCounters,
		cleanups:       cleanups,
	}
	return &collector, nil
}

var getXIDMetricTemplate = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("xidMetrics").Parse(xidMetricsFormat))
})

func encodeXIDMetrics(w io.Writer, metrics MetricsByCounter) error {
	template := getXIDMetricTemplate()
	return template.Execute(w, metrics)
}

func IsdcgmExpXIDErrorsCountEnabled(counters []Counter) bool {
	return slices.ContainsFunc(counters, func(c Counter) bool {
		return c.FieldName == dcgmExpXIDErrorsCount
	})
}
