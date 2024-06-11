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

package rendermetrics

import (
	"fmt"
	"io"
	"sync"
	"text/template"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
)

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

var (
	gpuMetricsFormat = `
{{- range $counter, $metrics := . -}}
# HELP {{ $counter.FieldName }} {{ $counter.Help }}
# TYPE {{ $counter.FieldName }} {{ $counter.PromType }}
{{- range $metric := $metrics }}
{{ $counter.FieldName }}{gpu="{{ $metric.GPU }}",{{ $metric.UUID }}="{{ $metric.GPUUUID }}",pci_bus_id="{{ $metric.GPUPCIBusID }}",device="{{ $metric.GPUDevice }}",modelName="{{ $metric.GPUModelName }}"{{if $metric.MigProfile}},GPU_I_PROFILE="{{ $metric.MigProfile }}",GPU_I_ID="{{ $metric.GPUInstanceID }}"{{end}}{{if $metric.Hostname }},Hostname="{{ $metric.Hostname }}"{{end}}

{{- range $k, $v := $metric.Labels -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}
{{- range $k, $v := $metric.Attributes -}}
	,{{ $k }}="{{ $v }}"
{{- end -}}

} {{ $metric.Value -}}
{{- end }}
{{ end }}`

	switchMetricsFormat = `
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

	linkMetricsFormat = `
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

	cpuMetricsFormat = `
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

	cpuCoreMetricsFormat = `
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
)

var getGPUMetricsTemplate = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("gpuMetricsFormat").Parse(gpuMetricsFormat))
})

var getSwitchMetricsTemplate = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("switchMetricsFormat").Parse(switchMetricsFormat))
})

var getLinkMetricsTemplate = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("linkMetricsFormat").Parse(linkMetricsFormat))
})

var getCPUMetricsTemplate = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("cpuMetricsFormat").Parse(cpuMetricsFormat))
})

var getCPUCoreMetricsTemplate = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("cpuMetricsFormat").Parse(cpuCoreMetricsFormat))
})

func RenderGroup(w io.Writer, group dcgm.Field_Entity_Group, metrics collector.MetricsByCounter) error {
	var tmpl *template.Template

	switch group {
	case dcgm.FE_GPU:
		tmpl = getGPUMetricsTemplate()
	case dcgm.FE_SWITCH:
		tmpl = getSwitchMetricsTemplate()
	case dcgm.FE_LINK:
		tmpl = getLinkMetricsTemplate()
	case dcgm.FE_CPU:
		tmpl = getCPUMetricsTemplate()
	case dcgm.FE_CPU_CORE:
		tmpl = getCPUCoreMetricsTemplate()
	default:
		return fmt.Errorf("unexpected group: %s", group.String())
	}
	return tmpl.Execute(w, metrics)
}
