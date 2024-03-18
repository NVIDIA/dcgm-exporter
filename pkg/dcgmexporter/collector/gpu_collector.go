/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package collector

import (
	"errors"
	"fmt"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/pkg/common"
	dcgmClient "github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/dcgm_client"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/sysinfo"
)

type DCGMCollectorConstructor func(
	[]common.Counter, string, *common.Config, sysinfo.FieldEntityGroupTypeSystemInfoItem,
) (*DCGMCollector, func(), error)

func NewDCGMCollector(
	c []common.Counter,
	hostname string,
	config *common.Config,
	fieldEntityGroupTypeSystemInfo sysinfo.FieldEntityGroupTypeSystemInfoItem,
) (*DCGMCollector, func(), error) {

	if fieldEntityGroupTypeSystemInfo.IsEmpty() {
		return nil, func() {}, errors.New("fieldEntityGroupTypeSystemInfo is empty")
	}

	collector := &DCGMCollector{
		Counters:     c,
		DeviceFields: fieldEntityGroupTypeSystemInfo.DeviceFields,
		SysInfo:      fieldEntityGroupTypeSystemInfo.SystemInfo,
		Hostname:     hostname,
	}

	if config == nil {
		logrus.Warn("Config is empty")
		return collector, func() { collector.Cleanup() }, nil
	}

	collector.UseOldNamespace = config.UseOldNamespace
	collector.ReplaceBlanksInModelName = config.ReplaceBlanksInModelName

	cleanups, err := sysinfo.SetupDcgmFieldsWatch(collector.DeviceFields,
		fieldEntityGroupTypeSystemInfo.SystemInfo,
		int64(config.CollectInterval)*1000)
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

func (c *DCGMCollector) GetMetrics() (MetricsByCounter, error) {
	monitoringInfo := sysinfo.GetMonitoredEntities(c.SysInfo)

	metrics := make(MetricsByCounter)

	for _, mi := range monitoringInfo {
		var vals []dcgm.FieldValue_v1
		var err error
		if mi.Entity.EntityGroupId == dcgm.FE_LINK {
			vals, err = dcgmClient.Client().LinkGetLatestValues(mi.Entity.EntityId, mi.ParentId, c.DeviceFields)
		} else {
			vals, err = dcgmClient.Client().EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId,
				c.DeviceFields)
		}

		if err != nil {
			if derr, ok := err.(*dcgm.DcgmError); ok {
				if derr.Code == dcgm.DCGM_ST_CONNECTION_NOT_VALID {
					logrus.Fatal("Could not retrieve metrics: ", err)
				}
			}
			return nil, err
		}

		// InstanceInfo will be nil for gpus
		if c.SysInfo.InfoType() == dcgm.FE_SWITCH || c.SysInfo.InfoType() == dcgm.FE_LINK {
			ToSwitchMetric(metrics, vals, c.Counters, mi, c.UseOldNamespace, c.Hostname)
		} else if c.SysInfo.InfoType() == dcgm.FE_CPU || c.SysInfo.InfoType() == dcgm.FE_CPU_CORE {
			ToCPUMetric(metrics, vals, c.Counters, mi, c.UseOldNamespace, c.Hostname)
		} else {
			ToMetric(metrics,
				vals,
				c.Counters,
				mi.DeviceInfo,
				mi.InstanceInfo,
				c.UseOldNamespace,
				c.Hostname,
				c.ReplaceBlanksInModelName)
		}
	}

	return metrics, nil
}

func (c *DCGMCollector) GetSysinfo() sysinfo.SystemInfoInterface {
	return c.SysInfo
}

func FindCounterField(c []common.Counter, fieldId uint) (common.Counter, error) {
	for i := 0; i < len(c); i++ {
		if uint(c[i].FieldID) == fieldId {
			return c[i], nil
		}
	}

	return c[0], fmt.Errorf("could not find counter corresponding to field ID '%d'", fieldId)
}

func ToSwitchMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1, c []common.Counter, mi sysinfo.MonitoringInfo, useOld bool, hostname string,
) {
	labels := map[string]string{}

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
				Labels:       labels,
				Attributes:   nil,
			}
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

func ToCPUMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1, c []common.Counter, mi sysinfo.MonitoringInfo, useOld bool, hostname string,
) {
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
				Labels:       labels,
				Attributes:   nil,
			}
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

func ToMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	c []common.Counter,
	d dcgm.Device,
	instanceInfo *sysinfo.GPUInstanceInfo,
	useOld bool,
	hostname string,
	replaceBlanksInModelName bool,
) {
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

		gpuModel := getGPUModel(d, replaceBlanksInModelName)

		m := Metric{
			Counter: counter,
			Value:   v,

			UUID:         uuid,
			GPU:          fmt.Sprintf("%d", d.GPU),
			GPUUUID:      d.UUID,
			GPUDevice:    fmt.Sprintf("nvidia%d", d.GPU),
			GPUModelName: gpuModel,
			Hostname:     hostname,

			Labels:     labels,
			Attributes: map[string]string{},
		}
		if instanceInfo != nil {
			m.MigProfile = instanceInfo.ProfileName
			m.GPUInstanceID = fmt.Sprintf("%d", instanceInfo.Info.NvmlInstanceId)
		} else {
			m.MigProfile = ""
			m.GPUInstanceID = ""
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

func getGPUModel(d dcgm.Device, replaceBlanksInModelName bool) string {
	gpuModel := d.Identifiers.Model

	if replaceBlanksInModelName {
		parts := strings.Fields(gpuModel)
		gpuModel = strings.Join(parts, " ")
		gpuModel = strings.ReplaceAll(gpuModel, " ", "-")
	}
	return gpuModel
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
	}

	return FailedToConvert
}
