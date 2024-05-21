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
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
)

const unknownErr = "Unknown Error"

type DCGMCollectorConstructor func(
	[]appconfig.Counter, string, *appconfig.Config, FieldEntityGroupTypeSystemInfoItem,
) (*DCGMCollector, func(), error)

func NewDCGMCollector(
	c []appconfig.Counter,
	hostname string,
	config *appconfig.Config,
	watcher devicewatcher.Watcher,
	fieldEntityGroupTypeSystemInfo FieldEntityGroupTypeSystemInfoItem,
) (*DCGMCollector, error) {
	if fieldEntityGroupTypeSystemInfo.isEmpty() {
		return nil, errors.New("fieldEntityGroupTypeSystemInfo is empty")
	}

	collector := &DCGMCollector{
		Counters:     c,
		DeviceFields: fieldEntityGroupTypeSystemInfo.DeviceFields,
		DeviceInfo:   fieldEntityGroupTypeSystemInfo.DeviceInfo,
		Hostname:     hostname,
	}

	if config == nil {
		logrus.Warn("Config is empty")
		return collector, nil
	}

	collector.UseOldNamespace = config.UseOldNamespace
	collector.ReplaceBlanksInModelName = config.ReplaceBlanksInModelName

	cleanups, err := watcher.WatchDeviceFields(collector.DeviceFields, fieldEntityGroupTypeSystemInfo.DeviceInfo,
		int64(config.CollectInterval)*1000)
	if err != nil {
		return nil, err
	}

	collector.Cleanups = cleanups

	return collector, nil
}

func GetDeviceInfo(config *appconfig.Config, entityType dcgm.Field_Entity_Group) (deviceinfo.Provider, error) {
	deviceInfo, err := deviceinfo.Initialize(config.GPUDevices,
		config.SwitchDevices,
		config.CPUDevices,
		config.UseFakeGPUs, entityType)
	if err != nil {
		return nil, err
	}
	return deviceInfo, err
}

func (c *DCGMCollector) Cleanup() {
	for _, c := range c.Cleanups {
		c()
	}
}

func (c *DCGMCollector) GetMetrics() (MetricsByCounter, error) {
	monitoringInfo := devicemonitoring.GetMonitoredEntities(c.DeviceInfo)

	metrics := make(MetricsByCounter)

	for _, mi := range monitoringInfo {
		var vals []dcgm.FieldValue_v1
		var err error
		if mi.Entity.EntityGroupId == dcgm.FE_LINK {
			vals, err = dcgmprovider.Client().LinkGetLatestValues(mi.Entity.EntityId, mi.ParentId, c.DeviceFields)
		} else {
			vals, err = dcgmprovider.Client().EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId,
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

		// InstanceInfo will be nil for GPUs
		if c.DeviceInfo.InfoType() == dcgm.FE_SWITCH || c.DeviceInfo.InfoType() == dcgm.FE_LINK {
			ToSwitchMetric(metrics, vals, c.Counters, mi, c.UseOldNamespace, c.Hostname)
		} else if c.DeviceInfo.InfoType() == dcgm.FE_CPU || c.DeviceInfo.InfoType() == dcgm.FE_CPU_CORE {
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

func ShouldMonitorDeviceType(fields []dcgm.Short, entityType dcgm.Field_Entity_Group) bool {
	if len(fields) == 0 {
		return false
	}

	if len(fields) == 1 && fields[0] == dcgm.DCGM_FI_DRIVER_VERSION {
		return false
	}

	return true
}

func FindCounterField(c []appconfig.Counter, fieldID uint) (appconfig.Counter, error) {
	for i := 0; i < len(c); i++ {
		if uint(c[i].FieldID) == fieldID {
			return c[i], nil
		}
	}

	return appconfig.Counter{}, fmt.Errorf("could not find counter corresponding to field ID '%d'", fieldID)
}

func ToSwitchMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1, c []appconfig.Counter, mi devicemonitoring.Info, useOld bool, hostname string,
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
	values []dcgm.FieldValue_v1, c []appconfig.Counter, mi devicemonitoring.Info, useOld bool, hostname string,
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
	c []appconfig.Counter,
	d dcgm.Device,
	instanceInfo *deviceinfo.GPUInstanceInfo,
	useOld bool,
	hostname string,
	replaceBlanksInModelName bool,
) {
	labels := map[string]string{}

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

		attrs := map[string]string{}
		if counter.FieldID == dcgm.DCGM_FI_DEV_XID_ERRORS {
			errCode := int(val.Int64())
			attrs["err_code"] = strconv.Itoa(errCode)
			if 0 <= errCode && errCode < len(xidErrCodeToText) {
				attrs["err_msg"] = xidErrCodeToText[errCode]
			} else {
				attrs["err_msg"] = unknownErr
			}
		}

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
			Attributes: attrs,
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
