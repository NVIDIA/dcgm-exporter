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
	"fmt"
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
	"os"
)

func NewDCGMCollector(c []Counter, config *Config) (*DCGMCollector, func(), error) {
	sysInfo, err := InitializeSystemInfo(config.Devices, config.UseFakeGpus)
	if err != nil {
		return nil, func() {}, err
	}

	hostname := ""
	if config.NoHostname == false {
		hostname, err = os.Hostname()
		if err != nil {
			return nil, func() {}, err
		}
	}

	collector := &DCGMCollector{
		Counters:        c,
		DeviceFields:    NewDeviceFields(c),
		UseOldNamespace: config.UseOldNamespace,
		SysInfo:         sysInfo,
		Hostname:        hostname,
	}

	cleanups, err := SetupDcgmFieldsWatch(collector.DeviceFields, sysInfo, int64(config.CollectInterval)*1000)
	if err != nil {
		return nil, func() {}, err
	}

	collector.Cleanups = cleanups

	return collector, func() { collector.Cleanup() }, nil
}

func (c *DCGMCollector) Cleanup() {
	for _, c := range c.Cleanups {
		c()
	}
}

func (c *DCGMCollector) GetMetrics() ([][]Metric, error) {
	monitoringInfo := GetMonitoredEntities(c.SysInfo)
	count := len(monitoringInfo)

	metrics := make([][]Metric, count)

	for i, mi := range monitoringInfo {
		vals, err := dcgm.EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId, c.DeviceFields)
		if err != nil {
			if derr, ok := err.(*dcgm.DcgmError); ok {
				if derr.Code == dcgm.DCGM_ST_CONNECTION_NOT_VALID {
					logrus.Fatal("Could not retrieve metrics: ", err)
				}
			}
			return nil, err
		}

		// InstanceInfo will be nil for GPUs
		metrics[i] = ToMetric(vals, c.Counters, mi.DeviceInfo, mi.InstanceInfo, c.UseOldNamespace, c.Hostname)
	}

	return metrics, nil
}

func ToMetric(values []dcgm.FieldValue_v1, c []Counter, d dcgm.Device, instanceInfo *GpuInstanceInfo, useOld bool, hostname string) []Metric {
	var metrics []Metric
	var labels = map[string]string{}

	for i, val := range values {
		v := ToString(val)
		// Filter out counters with no value and ignored fields for this entity
		if v == SkipDCGMValue {
			continue
		}
		if c[i].PromType == "label" {
			labels[c[i].FieldName] = v
			continue
		}
		uuid := "UUID"
		if useOld {
			uuid = "uuid"
		}
		m := Metric{
			Counter: &c[i],
			Value:   v,

			UUID:         uuid,
			GPU:          fmt.Sprintf("%d", d.GPU),
			GPUUUID:      d.UUID,
			GPUDevice:    fmt.Sprintf("nvidia%d", d.GPU),
			GPUModelName: d.Identifiers.Model,
			Hostname:     hostname,

			Labels:     &labels,
			Attributes: map[string]string{},
		}
		if instanceInfo != nil {
			m.MigProfile = instanceInfo.ProfileName
			m.GPUInstanceID = fmt.Sprintf("%d", instanceInfo.Info.NvmlInstanceId)
		} else {
			m.MigProfile = ""
			m.GPUInstanceID = ""
		}
		metrics = append(metrics, m)
	}

	return metrics
}

func ToString(value dcgm.FieldValue_v1) string {
	switch value.FieldType {
	case dcgm.DCGM_FT_INT64:
		switch v := value.Int64(); v {
		case dcgm.DCGM_FT_INT32_BLANK:
			return SkipDCGMValue
		case dcgm.DCGM_FT_INT32_NOT_FOUND:
			return SkipDCGMValue
		case dcgm.DCGM_FT_INT32_NOT_SUPPORTED:
			return SkipDCGMValue
		case dcgm.DCGM_FT_INT32_NOT_PERMISSIONED:
			return SkipDCGMValue
		case dcgm.DCGM_FT_INT64_BLANK:
			return SkipDCGMValue
		case dcgm.DCGM_FT_INT64_NOT_FOUND:
			return SkipDCGMValue
		case dcgm.DCGM_FT_INT64_NOT_SUPPORTED:
			return SkipDCGMValue
		case dcgm.DCGM_FT_INT64_NOT_PERMISSIONED:
			return SkipDCGMValue
		default:
			return fmt.Sprintf("%d", value.Int64())
		}
	case dcgm.DCGM_FT_DOUBLE:
		switch v := value.Float64(); v {
		case dcgm.DCGM_FT_FP64_BLANK:
			return SkipDCGMValue
		case dcgm.DCGM_FT_FP64_NOT_FOUND:
			return SkipDCGMValue
		case dcgm.DCGM_FT_FP64_NOT_SUPPORTED:
			return SkipDCGMValue
		case dcgm.DCGM_FT_FP64_NOT_PERMISSIONED:
			return SkipDCGMValue
		default:
			return fmt.Sprintf("%f", value.Float64())
		}
	case dcgm.DCGM_FT_STRING:
		switch v := value.String(); v {
		case dcgm.DCGM_FT_STR_BLANK:
			return SkipDCGMValue
		case dcgm.DCGM_FT_STR_NOT_FOUND:
			return SkipDCGMValue
		case dcgm.DCGM_FT_STR_NOT_SUPPORTED:
			return SkipDCGMValue
		case dcgm.DCGM_FT_STR_NOT_PERMISSIONED:
			return SkipDCGMValue
		default:
			return v
		}
	default:
		return FailedToConvert
	}

	return FailedToConvert
}
