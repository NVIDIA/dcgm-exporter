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
	"bytes"
	"fmt"
	"sync"
	"text/template"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
)

func NewMetricsPipeline(config *Config,
	counters []Counter,
	hostname string,
	newDCGMCollector DCGMCollectorConstructor,
	fieldEntityGroupTypeSystemInfo *FieldEntityGroupTypeSystemInfo,
) (*MetricsPipeline, func(), error) {

	logrus.WithField(LoggerDumpKey, fmt.Sprintf("%+v", counters)).Debug("Counters are initialized")

	cleanups := []func(){}

	var (
		gpuCollector    *DCGMCollector
		switchCollector *DCGMCollector
		linkCollector   *DCGMCollector
		cpuCollector    *DCGMCollector
		coreCollector   *DCGMCollector
		err             error
	)

	if item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU); exists {
		var cleanup func()
		gpuCollector, cleanup, err = newDCGMCollector(counters, hostname, config, item)
		if err != nil {
			logrus.Warn("Cannot create DCGMCollector for dcgm.FE_GPU")
		}
		cleanups = append(cleanups, cleanup)
	}

	if item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_SWITCH); exists {
		var cleanup func()
		switchCollector, cleanup, err = newDCGMCollector(counters, hostname, config, item)
		if err != nil {
			logrus.Warn("Cannot create DCGMCollector for dcgm.FE_SWITCH")
		}
		cleanups = append(cleanups, cleanup)
	}

	if item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_LINK); exists {
		var cleanup func()
		linkCollector, cleanup, err = newDCGMCollector(counters, hostname, config, item)
		if err != nil {
			logrus.Warn("Cannot create DCGMCollector for dcgm.FE_LINK")
		}
		cleanups = append(cleanups, cleanup)
	}

	if item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_CPU); exists {
		var cleanup func()
		cpuCollector, cleanup, err = newDCGMCollector(counters, hostname, config, item)
		if err != nil {
			logrus.Warn("Cannot create DCGMCollector for dcgm.FE_CPU")
		}
		cleanups = append(cleanups, cleanup)
	}

	if item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_CPU_CORE); exists {
		var cleanup func()
		coreCollector, cleanup, err = newDCGMCollector(counters, hostname, config, item)
		if err != nil {
			logrus.Warn("Cannot create DCGMCollector for dcgm.FE_CPU_CORE")
		}
		cleanups = append(cleanups, cleanup)
	}

	transformations := getTransformations(config)

	return &MetricsPipeline{
			config: config,

			migMetricsFormat:     template.Must(template.New("migMetrics").Parse(migMetricsFormat)),
			switchMetricsFormat:  template.Must(template.New("switchMetrics").Parse(switchMetricsFormat)),
			linkMetricsFormat:    template.Must(template.New("switchMetrics").Parse(linkMetricsFormat)),
			cpuMetricsFormat:     template.Must(template.New("cpuMetrics").Parse(cpuMetricsFormat)),
			cpuCoreMetricsFormat: template.Must(template.New("cpuMetrics").Parse(cpuCoreMetricsFormat)),

			counters:        counters,
			gpuCollector:    gpuCollector,
			switchCollector: switchCollector,
			linkCollector:   linkCollector,
			transformations: transformations,
			cpuCollector:    cpuCollector,
			coreCollector:   coreCollector,
		}, func() {
			for _, cleanup := range cleanups {
				cleanup()
			}
		}, nil
}

func getTransformations(c *Config) []Transform {
	transformations := []Transform{}
	if c.Kubernetes {
		podMapper, err := NewPodMapper(c)
		if err != nil {
			logrus.Warnf("Could not enable kubernetes metric collection: %v", err)
		} else {
			transformations = append(transformations, podMapper)
		}
	}

	return transformations
}

// Primarely for testing, caller expected to cleanup the collector
func NewMetricsPipelineWithGPUCollector(c *Config, collector *DCGMCollector) (*MetricsPipeline, func(), error) {
	return &MetricsPipeline{
		config: c,

		migMetricsFormat:     template.Must(template.New("migMetrics").Parse(migMetricsFormat)),
		switchMetricsFormat:  template.Must(template.New("switchMetrics").Parse(switchMetricsFormat)),
		linkMetricsFormat:    template.Must(template.New("switchMetrics").Parse(linkMetricsFormat)),
		cpuMetricsFormat:     template.Must(template.New("cpuMetrics").Parse(cpuMetricsFormat)),
		cpuCoreMetricsFormat: template.Must(template.New("cpuMetrics").Parse(cpuCoreMetricsFormat)),

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
				logrus.Errorf("Failed to collect metrics; err: %v", err)
				/* flush output rather than output stale data */
				out <- ""
				continue
			}

			if len(out) == cap(out) {
				logrus.Errorf("Channel is full skipping.")
			} else {
				out <- o
			}
		}
	}
}

func (m *MetricsPipeline) run() (string, error) {
	var metrics map[Counter][]Metric
	var err error
	var formatted string

	if m.gpuCollector != nil {
		/* Collect GPU Metrics */
		metrics, err = m.gpuCollector.GetMetrics()
		if err != nil {
			return "", fmt.Errorf("failed to collect gpu metrics; err: %w", err)
		}

		for _, transform := range m.transformations {
			err := transform.Process(metrics, m.gpuCollector.SysInfo)
			if err != nil {
				return "", fmt.Errorf("failed to transform metrics for transform '%s'; err: %w", transform.Name(), err)
			}
		}

		formatted, err = FormatMetrics(m.migMetricsFormat, metrics)
		if err != nil {
			return "", fmt.Errorf("failed to format metrics; err: %w", err)
		}
	}

	if m.switchCollector != nil {
		/* Collect Switch Metrics */
		metrics, err = m.switchCollector.GetMetrics()
		if err != nil {
			return "", fmt.Errorf("failed to collect switch metrics; err: %w", err)
		}

		if len(metrics) > 0 {
			switchFormatted, err := FormatMetrics(m.switchMetricsFormat, metrics)
			if err != nil {
				logrus.Warnf("Failed to format switch metrics with error: %v", err)
			}

			formatted = formatted + switchFormatted
		}
	}

	if m.linkCollector != nil {
		/* Collect Link Metrics */
		metrics, err = m.linkCollector.GetMetrics()
		if err != nil {
			return "", fmt.Errorf("failed to collect link metrics; err: %w", err)
		}

		if len(metrics) > 0 {
			switchFormatted, err := FormatMetrics(m.linkMetricsFormat, metrics)
			if err != nil {
				logrus.Warnf("failed to format link metrics; err: %v", err)
			}

			formatted = formatted + switchFormatted
		}
	}

	if m.cpuCollector != nil {
		/* Collect CPU Metrics */
		metrics, err = m.cpuCollector.GetMetrics()
		if err != nil {
			return "", fmt.Errorf("failed to collect CPU metrics; err: %w", err)
		}

		if len(metrics) > 0 {
			cpuFormatted, err := FormatMetrics(m.cpuMetricsFormat, metrics)
			if err != nil {
				logrus.Warnf("Failed to format cpu metrics with error: %v", err)
			}

			formatted = formatted + cpuFormatted
		}
	}

	if m.coreCollector != nil {
		/* Collect cpu core Metrics */
		metrics, err = m.coreCollector.GetMetrics()
		if err != nil {
			return "", fmt.Errorf("failed to collect CPU core metrics; err: %w", err)
		}

		if len(metrics) > 0 {
			coreFormatted, err := FormatMetrics(m.cpuCoreMetricsFormat, metrics)
			if err != nil {
				logrus.Warnf("failed to format cpu core metrics; err: %v", err)
			}

			formatted = formatted + coreFormatted
		}
	}

	return formatted, nil
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

var migMetricsFormat = `
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

var switchMetricsFormat = `
{{- range $counter, $metrics := . -}}
# HELP {{ $counter.FieldName }} {{ $counter.Help }}
# TYPE {{ $counter.FieldName }} {{ $counter.PromType }}
{{- range $metric := $metrics }}
{{ $counter.FieldName }}{nvswitch="{{ $metric.GPU }}"{{if $metric.Hostname }},Hostname="{{ $metric.Hostname }}"{{end}}

{{- range $k, $v := $metric.Labels -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}
} {{ $metric.Value -}}
{{- end }}
{{ end }}`

var linkMetricsFormat = `
{{- range $counter, $metrics := . -}}
# HELP {{ $counter.FieldName }} {{ $counter.Help }}
# TYPE {{ $counter.FieldName }} {{ $counter.PromType }}
{{- range $metric := $metrics }}
{{ $counter.FieldName }}{nvlink="{{ $metric.GPU }}",nvswitch="{{ $metric.GPUDevice }}"{{if $metric.Hostname }},Hostname="{{ $metric.Hostname }}"{{end}}

{{- range $k, $v := $metric.Labels -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}
} {{ $metric.Value -}}
{{- end }}
{{ end }}`

var cpuMetricsFormat = `
{{- range $counter, $metrics := . -}}
# HELP {{ $counter.FieldName }} {{ $counter.Help }}
# TYPE {{ $counter.FieldName }} {{ $counter.PromType }}
{{- range $metric := $metrics }}
{{ $counter.FieldName }}{cpu="{{ $metric.GPU }}"{{if $metric.Hostname }},Hostname="{{ $metric.Hostname }}"{{end}}

{{- range $k, $v := $metric.Labels -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}
} {{ $metric.Value -}}
{{- end }}
{{ end }}`

var cpuCoreMetricsFormat = `
{{- range $counter, $metrics := . -}}
# HELP {{ $counter.FieldName }} {{ $counter.Help }}
# TYPE {{ $counter.FieldName }} {{ $counter.PromType }}
{{- range $metric := $metrics }}
{{ $counter.FieldName }}{cpucore="{{ $metric.GPU }}",cpu="{{ $metric.GPUDevice }}"{{if $metric.Hostname }},Hostname="{{ $metric.Hostname }}"{{end}}

{{- range $k, $v := $metric.Labels -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}
} {{ $metric.Value -}}
{{- end }}
{{ end }}`

// Template is passed here so that it isn't recompiled at each iteration
func FormatMetrics(t *template.Template, groupedMetrics MetricsByCounter) (string, error) {
	// Format metrics
	var res bytes.Buffer
	if err := t.Execute(&res, groupedMetrics); err != nil {
		return "", err
	}

	return res.String(), nil
}
