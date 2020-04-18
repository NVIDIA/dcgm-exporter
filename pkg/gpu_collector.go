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
	"C"
	"bytes"
	"fmt"
	"text/template"

	//"github.com/golang/glog"
	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
)

/*
* The goal here is to get to the following format:
* ```
* # HELP FIELD_ID HELP_MSG
* # TYPE FIELD_ID PROM_TYPE
* ...
* FIELD_ID{gpu="GPU_INDEX_0",uuid="GPU_UUID"} VALUE
* ...
* FIELD_ID{gpu="GPU_INDEX_N",uuid="GPU_UUID"} VALUE
* ```
*
* The expectation is that the template will be given the following
* values: {.Fields, .Devices, .Values[Device][Field]}
*
*/

	var format = `
{{- range $field := .Fields -}}
# HELP {{ $field.FieldName }} {{ $field.Help }}
# TYPE {{ $field.FieldName }} {{ $field.PromType }}
{{ end }}
{{ range $i, $dev := .Devices }}{{ range $j, $field := $.Fields }}
{{ $field.FieldName }}{gpu="{{ $dev.GPU }}" UUID="{{ $dev.UUID }}"} {{ index (index $.Values $i) $j | ToString }}
{{- end }}
{{ end }}`


func NewDCGMCollector(fields []DCGMField) (*DCGMCollector, func(), error) {
	collector := &DCGMCollector{
		Template: template.Must(template.New("DCGMFormat").Funcs(
			template.FuncMap{"ToString": ToString}).Parse(format)),
		Fields: fields,
		DeviceFields: NewDeviceFields(fields),
	}

	cleanups, err := SetupDcgmFieldsWatch(collector.DeviceFields)
	if err != nil {
		return nil, func(){}, err
	}

	collector.Cleanups = cleanups

	return collector, func() { collector.Cleanup() }, nil
}

func (c *DCGMCollector) Cleanup() {
	for _, c := range c.Cleanups {
		c()
	}
}

func ToString(value dcgm.FieldValue_v1) string {
	switch v := value.FieldType; v {
	case dcgm.DCGM_FT_STRING:
		return value.String()
	case dcgm.DCGM_FT_DOUBLE:
		return fmt.Sprintf("%f", value.Float64())
	case dcgm.DCGM_FT_INT64:
		return fmt.Sprintf("%d", value.Int64())
	default:
		return "ERROR - FAILED TO CONVERT TO STRING"
	}
	return "ERROR - FAILED TO CONVERT TO STRING"
}

func (c *DCGMCollector) GetMetrics() (string, error) {
	count, err := dcgm.GetAllDeviceCount()
	if err != nil {
		return "", err
	}

	values := make([][]dcgm.FieldValue_v1, count)
	devices := make([]dcgm.Device, count)
	for i := uint(0); i < count; i++ {
		vals, err := dcgm.GetLatestValuesForFields(i, c.DeviceFields)
		if err != nil {
			return "", err
		}

		values[i] = vals

		deviceInfo, err := dcgm.GetDeviceInfo(i)
		if err != nil {
			return "", err
		}

		devices[i] = deviceInfo
	}


	var res bytes.Buffer
	if err := c.Template.Execute(&res, struct {
			Fields []DCGMField
			Devices []dcgm.Device
			Values [][]dcgm.FieldValue_v1
		} { 
		Fields: c.Fields,
		Devices: devices,
		Values: values,
	}); err != nil {
		return "", err
	}

	return res.String(), nil
}
