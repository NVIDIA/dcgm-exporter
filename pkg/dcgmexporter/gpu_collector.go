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
	"os"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
)

func NewDCGMCollector(c []Counter, config *Config, entityType dcgm.Field_Entity_Group) (*DCGMCollector, func(), error) {
	sysInfo, err := InitializeSystemInfo(config.GPUDevices, config.SwitchDevices, config.CPUDevices, config.UseFakeGpus, entityType)
	if err != nil {
		return nil, func() {}, err
	}

	hostname := ""
	if config.NoHostname == false {
		if nodeName := os.Getenv("NODE_NAME"); nodeName != "" {
			hostname = nodeName
		} else {
			hostname, err = os.Hostname()
			if err != nil {
				return nil, func() {}, err
			}
		}
	}

	var deviceFields = NewDeviceFields(c, entityType)

	if len(deviceFields) <= 0 {
		return nil, func() {}, fmt.Errorf("No fields to watch for device type: %d", entityType)
	}

	collector := &DCGMCollector{
		Counters:        c,
		DeviceFields:    deviceFields,
		UseOldNamespace: config.UseOldNamespace,
		SysInfo:         sysInfo,
		Hostname:        hostname,
	}

	cleanups, err := SetupDcgmFieldsWatch(collector.DeviceFields, sysInfo, int64(config.CollectInterval)*1000)
	if err != nil {
		logrus.Fatal("Failed to watch metrics: ", err)
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
		var vals []dcgm.FieldValue_v1
		var err error
		if mi.Entity.EntityGroupId == dcgm.FE_LINK {
			vals, err = dcgm.LinkGetLatestValues(mi.Entity.EntityId, mi.ParentId, c.DeviceFields)
		} else {
			vals, err = dcgm.EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId, c.DeviceFields)
		}

		if err != nil {
			if derr, ok := err.(*dcgm.DcgmError); ok {
				if derr.Code == dcgm.DCGM_ST_CONNECTION_NOT_VALID {
					logrus.Fatal("Could not retrieve metrics: ", err)
				}
			}
			return nil, err
		}

		// InstanceInfo will be nil for GPUs
		if c.SysInfo.InfoType == dcgm.FE_SWITCH || c.SysInfo.InfoType == dcgm.FE_LINK {
			metrics[i] = ToSwitchMetric(vals, c.Counters, mi, c.UseOldNamespace, c.Hostname)
		} else if c.SysInfo.InfoType == dcgm.FE_CPU || c.SysInfo.InfoType == dcgm.FE_CPU_CORE {
			metrics[i] = ToCPUMetric(vals, c.Counters, mi, c.UseOldNamespace, c.Hostname)
		} else {
			metrics[i] = ToMetric(vals, c.Counters, mi.DeviceInfo, mi.InstanceInfo, c.UseOldNamespace, c.Hostname)
		}
	}

	return metrics, nil
}

func FindCounterField(c []Counter, fieldId uint) (*Counter, error) {
	for i := 0; i < len(c); i++ {
		if uint(c[i].FieldID) == fieldId {
			return &c[i], nil
		}
	}

	return &c[0], fmt.Errorf("Could not find corresponding counter")
}

func ToSwitchMetric(values []dcgm.FieldValue_v1, c []Counter, mi MonitoringInfo, useOld bool, hostname string) []Metric {
	var metrics []Metric
	var labels = map[string]string{}

	for _, val := range values {
		v := ToString(val)
		// Filter out counters with no value and ignored fields for this entity

		counter, err := FindCounterField(c, val.FieldId)
		if err != nil {
			continue
		}

		if counter.PromType == "label" {
			labels[counter.FieldName] = v
			continue
		}
		uuid := "UUID"
		if useOld {
			uuid = "uuid"
		}
		var m Metric
		if v == SkipDCGMValue {
			continue
		} else {
			m = Metric{
				Counter:      counter,
				Value:        v,
				UUID:         uuid,
				GPU:          fmt.Sprintf("%d", mi.Entity.EntityId),
				GPUUUID:      "",
				GPUDevice:    fmt.Sprintf("nvswitch%d", mi.ParentId),
				GPUModelName: "",
				Hostname:     hostname,
				Labels:       &labels,
				Attributes:   nil,
			}
		}
		metrics = append(metrics, m)
	}

	return metrics
}

func ToCPUMetric(values []dcgm.FieldValue_v1, c []Counter, mi MonitoringInfo, useOld bool, hostname string) []Metric {
	var metrics []Metric
	var labels = map[string]string{}

	for _, val := range values {
		v := ToString(val)
		// Filter out counters with no value and ignored fields for this entity

		counter, err := FindCounterField(c, val.FieldId)
		if err != nil {
			continue
		}

		if counter.PromType == "label" {
			labels[counter.FieldName] = v
			continue
		}
		uuid := "UUID"
		if useOld {
			uuid = "uuid"
		}
		var m Metric
		if v == SkipDCGMValue {
			continue
		} else {
			m = Metric{
				Counter:      counter,
				Value:        v,
				UUID:         uuid,
				GPU:          fmt.Sprintf("%d", mi.Entity.EntityId),
				GPUUUID:      "",
				GPUDevice:    fmt.Sprintf("%d", mi.ParentId),
				GPUModelName: "",
				Hostname:     hostname,
				Labels:       &labels,
				Attributes:   nil,
			}
		}
		metrics = append(metrics, m)
	}

	return metrics
}

func ToMetric(values []dcgm.FieldValue_v1, c []Counter, d dcgm.Device, instanceInfo *GpuInstanceInfo, useOld bool, hostname string) []Metric {
	var metrics []Metric
	var labels = map[string]string{}

	for _, val := range values {
		v := ToString(val)
		// Filter out counters with no value and ignored fields for this entity
		if v == SkipDCGMValue {
			continue
		}

		counter, err := FindCounterField(c, val.FieldId)
		if err != nil {
			continue
		}

		if counter.PromType == "label" {
			labels[counter.FieldName] = v
			continue
		}
		uuid := "UUID"
		if useOld {
			uuid = "uuid"
		}
		m := Metric{
			Counter: counter,
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
