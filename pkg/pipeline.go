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
	counters, err := ExtractCounters(c.CollectorsFile)
	if err != nil {
		return nil, func() {}, err
	}

	// Note this is an optimisation, we don't need to format these
	// at every pipeline run.
	countersText, err := FormatCounters(counters)
	if err != nil {
		return nil, func() {}, err
	}

	gpuCollector, cleanup, err := NewDCGMCollector(counters)
	if err != nil {
		return nil, func() {}, err
	}

	transformations := []Transform{}
	if c.Kubernetes {
		transformations = append(transformations, NewPodMapper(c))
	}

	return &MetricsPipeline{
			config: c,

			metricsFormat: template.Must(template.New("metrics").Parse(metricsFormat)),
			countersText:  countersText,

			gpuCollector:    gpuCollector,
			transformations: transformations,
		}, func() {
			cleanup()
		}, nil
}

// Primarely for testing, caller expected to cleanup the collector
func NewMetricsPipelineWithGPUCollector(c *Config, collector *DCGMCollector) (*MetricsPipeline, func(), error) {
	countersText, err := FormatCounters(collector.Counters)
	if err != nil {
		return nil, func() {}, err
	}

	return &MetricsPipeline{
		config: c,

		metricsFormat: template.Must(template.New("metrics").Parse(metricsFormat)),
		countersText:  countersText,

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
		err := transform.Process(metrics)
		if err != nil {
			return "", fmt.Errorf("Failed to transform metrics for transorm %s: %v", err, transform.Name())
		}
	}

	formated, err := FormatMetrics(m.countersText, m.metricsFormat, metrics)
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
* ...
* FIELD_ID{gpu="GPU_INDEX_0",uuid="GPU_UUID", attr...} VALUE
* ...
* FIELD_ID{gpu="GPU_INDEX_N",uuid="GPU_UUID", attr...} VALUE
* ```
*
* The expectation is that the template will be given the following
* values: {.Fields, .Devices, .Values[Device][Field]}
*
 */

var countersFormat = `{{- range $c := . -}}
# HELP {{ $c.FieldName }} {{ $c.Help }}
# TYPE {{ $c.FieldName }} {{ $c.PromType }}
{{ end }}`

func FormatCounters(c []Counter) (string, error) {
	var res bytes.Buffer

	t := template.Must(template.New("counters").Parse(countersFormat))
	if err := t.Execute(&res, c); err != nil {
		return "", err
	}

	return res.String(), nil
}

var metricsFormat = `
{{ range $dev := . }}{{ range $val := $dev }}
{{ $val.Name }}{gpu="{{ $val.GPU }}", UUID="{{ $val.GPUUUID }}"

{{- range $k, $v := $val.Attributes -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}

} {{ $val.Value }}
{{- end }}
{{ end }}`

// Template is passed here so that it isn't recompiled at each iteration
func FormatMetrics(countersText string, t *template.Template, m [][]Metric) (string, error) {
	var res bytes.Buffer

	if err := t.Execute(&res, m); err != nil {
		return "", err
	}

	return countersText + res.String(), nil
}
