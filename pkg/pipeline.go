/*
 * Copyright (c) 2020, NVIDIA CORPORATION.  All rights reserved.
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

package main

import (
	"bytes"
	"fmt"
	"sync"
	"text/template"
	"time"

	"github.com/sirupsen/logrus"
)

func NewMetricsPipeline(c *Config) (*MetricsPipeline, func(), error) {
	counters, err := ExtractCounters(c.CollectorsFile, c.CollectDCP)
	if err != nil {
		return nil, func() {}, err
	}

	gpuCollector, cleanup, err := NewDCGMCollector(counters, c)
	if err != nil {
		return nil, func() {}, err
	}

	transformations := []Transform{}
	if c.Kubernetes {
		transformations = append(transformations, NewPodMapper(c))
	}

	return &MetricsPipeline{
			config: c,

			metricsFormat:    template.Must(template.New("metrics").Parse(metricsFormat)),
			migMetricsFormat: template.Must(template.New("migMetrics").Parse(migMetricsFormat)),

			counters:        counters,
			gpuCollector:    gpuCollector,
			transformations: transformations,
		}, func() {
			cleanup()
		}, nil
}

// Primarely for testing, caller expected to cleanup the collector
func NewMetricsPipelineWithGPUCollector(c *Config, collector *DCGMCollector) (*MetricsPipeline, func(), error) {
	return &MetricsPipeline{
		config: c,

		metricsFormat:    template.Must(template.New("metrics").Parse(metricsFormat)),
		migMetricsFormat: template.Must(template.New("migMetrics").Parse(migMetricsFormat)),

		counters:     collector.Counters,
		gpuCollector: collector,
	}, func() {}, nil
}

func (m *MetricsPipeline) Run(out chan string, stop chan interface{}, wg *sync.WaitGroup) {
	defer wg.Done()

	logrus.Info("Pipeline starting")

	// Note we are using a ticker so that we can stick as close as possible to the collect interval.
	// e.g: The CollectInterval is 10s and the transformation pipeline takes 5s, the time will
	// ensure we really collect metrics every 10s by firing an event 5s after the run function completes.
	t := time.NewTicker(time.Millisecond * time.Duration(m.config.CollectInterval))
	defer t.Stop()

	for {
		select {
		case <-stop:
			return
		case <-t.C:
			o, err := m.run()
			if err != nil {
				logrus.Errorf("Failed to collect metrics with error: %v", err)
				continue
			}

			if len(out) == cap(out) {
				logrus.Errorf("Channel is full skipping")
			} else {
				out <- o
			}
		}
	}
}

func (m *MetricsPipeline) run() (string, error) {
	metrics, err := m.gpuCollector.GetMetrics()
	if err != nil {
		return "", fmt.Errorf("Failed to collect metrics with error: %v", err)
	}

	for _, transform := range m.transformations {
		err := transform.Process(metrics, m.gpuCollector.SysInfo)
		if err != nil {
			return "", fmt.Errorf("Failed to transform metrics for transorm %s: %v", err, transform.Name())
		}
	}

	formated, err := FormatMetrics(m.migMetricsFormat, metrics)
	if err != nil {
		return "", fmt.Errorf("Failed to format metrics with error: %v", err)
	}

	return formated, nil
}

/*
* The goal here is to get to the following format:
* ```
* # HELP FIELD_ID HELP_MSG
* # TYPE FIELD_ID PROM_TYPE
* FIELD_ID{gpu="GPU_INDEX_0",uuid="GPU_UUID", attr...} VALUE
* FIELD_ID{gpu="GPU_INDEX_N",uuid="GPU_UUID", attr...} VALUE
* ...
* ```
 */

var metricsFormat = `
{{- range $counter, $metrics := . -}}
# HELP {{ $counter.FieldName }} {{ $counter.Help }}
# TYPE {{ $counter.FieldName }} {{ $counter.PromType }}
{{- range $metric := $metrics }}
{{ $counter.FieldName }}{gpu="{{ $metric.GPU }}",{{ $metric.UUID }}="{{ $metric.GPUUUID }}",device="{{ $metric.GPUDevice }}",modelName="{{ $metric.GPUModelName }}"

{{- range $k, $v := $metric.Attributes -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}

} {{ $metric.Value -}}
{{- end }}
{{ end }}`

var migMetricsFormat = `
{{- range $counter, $metrics := . -}}
# HELP {{ $counter.FieldName }} {{ $counter.Help }}
# TYPE {{ $counter.FieldName }} {{ $counter.PromType }}
{{- range $metric := $metrics }}
{{ $counter.FieldName }}{gpu="{{ $metric.GPU }}",{{ $metric.UUID }}="{{ $metric.GPUUUID }}",device="{{ $metric.GPUDevice }}",modelName="{{ $metric.GPUModelName }}"{{if $metric.MigProfile}},GPU_I_PROFILE="{{ $metric.MigProfile }}",GPU_I_ID="{{ $metric.GPUInstanceID }}"{{end}}{{if $metric.Hostname }},Hostname="{{ $metric.Hostname }}"{{end}}

{{- range $k, $v := $metric.Attributes -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}

} {{ $metric.Value -}}
{{- end }}
{{ end }}`

// Template is passed here so that it isn't recompiled at each iteration
func FormatMetrics(t *template.Template, m [][]Metric) (string, error) {
	// Group metrics by counter instead of by device
	groupedMetrics := make(map[*Counter][]Metric)
	for _, deviceMetrics := range m {
		for _, deviceMetric := range deviceMetrics {
			groupedMetrics[deviceMetric.Counter] = append(groupedMetrics[deviceMetric.Counter], deviceMetric)
		}
	}

	// Format metrics
	var res bytes.Buffer
	if err := t.Execute(&res, groupedMetrics); err != nil {
		return "", err
	}

	return res.String(), nil
}
