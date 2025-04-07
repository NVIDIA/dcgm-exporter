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

package collector

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

const unknownErr = "Unknown Error"

type DCGMCollector struct {
	counters                 []counters.Counter
	cleanups                 []func()
	useOldNamespace          bool
	deviceWatchList          devicewatchlistmanager.WatchList
	hostname                 string
	replaceBlanksInModelName bool
}

func NewDCGMCollector(
	c []counters.Counter,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (*DCGMCollector, error) {
	if deviceWatchList.IsEmpty() {
		return nil, errors.New("deviceWatchList is empty")
	}

	collector := &DCGMCollector{
		counters:        c,
		deviceWatchList: deviceWatchList,
		hostname:        hostname,
	}

	if config == nil {
		slog.Warn("Config is empty")
		return collector, nil
	}

	collector.useOldNamespace = config.UseOldNamespace
	collector.replaceBlanksInModelName = config.ReplaceBlanksInModelName

	cleanups, err := deviceWatchList.Watch()
	if err != nil {
		return nil, err
	}

	collector.cleanups = cleanups

	return collector, nil
}

func (c *DCGMCollector) Cleanup() {
	for _, c := range c.cleanups {
		c()
	}
}

func (c *DCGMCollector) GetMetrics() (MetricsByCounter, error) {
	monitoringInfo := devicemonitoring.GetMonitoredEntities(c.deviceWatchList.DeviceInfo())

	metrics := make(MetricsByCounter)

	for _, mi := range monitoringInfo {
		var vals []dcgm.FieldValue_v1
		var err error
		if mi.Entity.EntityGroupId == dcgm.FE_LINK {
			vals, err = dcgmprovider.Client().LinkGetLatestValues(mi.Entity.EntityId, mi.ParentId,
				c.deviceWatchList.DeviceFields())
		} else {
			vals, err = dcgmprovider.Client().EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId,
				c.deviceWatchList.DeviceFields())
		}

		if err != nil {
			if derr, ok := err.(*dcgm.Error); ok {
				if derr.Code == dcgm.DCGM_ST_CONNECTION_NOT_VALID {
					slog.Error("Could not retrieve metrics: " + err.Error())
					os.Exit(1)
				}
			}
			return nil, err
		}

		// InstanceInfo will be nil for GPUs
		switch c.deviceWatchList.DeviceInfo().InfoType() {
		case dcgm.FE_SWITCH, dcgm.FE_LINK:
			toSwitchMetric(metrics, vals, c.counters, mi, c.useOldNamespace, c.hostname)
		case dcgm.FE_CPU, dcgm.FE_CPU_CORE:
			toCPUMetric(metrics, vals, c.counters, mi, c.useOldNamespace, c.hostname)
		default:
			toMetric(metrics,
				vals,
				c.counters,
				mi.DeviceInfo,
				mi.InstanceInfo,
				c.useOldNamespace,
				c.hostname,
				c.replaceBlanksInModelName)
		}
	}

	return metrics, nil
}

func findCounterField(c []counters.Counter, fieldID dcgm.Short) (counters.Counter, error) {
	for i := 0; i < len(c); i++ {
		if c[i].FieldID == fieldID {
			return c[i], nil
		}
	}

	return counters.Counter{}, fmt.Errorf("could not find counter corresponding to field ID '%d'", fieldID)
}

func toSwitchMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1, c []counters.Counter, mi devicemonitoring.Info, useOld bool, hostname string,
) {
	labels := map[string]string{}

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity

		counter, err := findCounterField(c, val.FieldID)
		if err != nil {
			continue
		}

		if counter.IsLabel() {
			labels[counter.FieldName] = v
			continue
		}
		uuid := "UUID"
		if useOld {
			uuid = "uuid"
		}
		var m Metric
		if v == skipDCGMValue {
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
				GPUPCIBusID:  "",
				Hostname:     hostname,
				Labels:       labels,
				Attributes:   nil,
			}
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

func toCPUMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1, c []counters.Counter, mi devicemonitoring.Info, useOld bool, hostname string,
) {
	labels := map[string]string{}

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity

		counter, err := findCounterField(c, val.FieldID)
		if err != nil {
			continue
		}

		if counter.IsLabel() {
			labels[counter.FieldName] = v
			continue
		}
		uuid := "UUID"
		if useOld {
			uuid = "uuid"
		}
		var m Metric
		if v == skipDCGMValue {
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
				GPUPCIBusID:  "",
				Hostname:     hostname,
				Labels:       labels,
				Attributes:   nil,
			}
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

func toMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	c []counters.Counter,
	d dcgm.Device,
	instanceInfo *deviceinfo.GPUInstanceInfo,
	useOld bool,
	hostname string,
	replaceBlanksInModelName bool,
) {
	labels := map[string]string{}

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity
		if v == skipDCGMValue {
			continue
		}

		counter, err := findCounterField(c, val.FieldID)
		if err != nil {
			continue
		}

		if counter.IsLabel() {
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
			GPUPCIBusID:  d.PCI.BusID,
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

func toString(value dcgm.FieldValue_v1) string {
	switch value.FieldType {
	case dcgm.DCGM_FT_INT64:
		switch v := value.Int64(); v {
		case dcgm.DCGM_FT_INT32_BLANK:
			return skipDCGMValue
		case dcgm.DCGM_FT_INT32_NOT_FOUND:
			return skipDCGMValue
		case dcgm.DCGM_FT_INT32_NOT_SUPPORTED:
			return skipDCGMValue
		case dcgm.DCGM_FT_INT32_NOT_PERMISSIONED:
			return skipDCGMValue
		case dcgm.DCGM_FT_INT64_BLANK:
			return skipDCGMValue
		case dcgm.DCGM_FT_INT64_NOT_FOUND:
			return skipDCGMValue
		case dcgm.DCGM_FT_INT64_NOT_SUPPORTED:
			return skipDCGMValue
		case dcgm.DCGM_FT_INT64_NOT_PERMISSIONED:
			return skipDCGMValue
		default:
			return fmt.Sprintf("%d", value.Int64())
		}
	case dcgm.DCGM_FT_DOUBLE:
		switch v := value.Float64(); v {
		case dcgm.DCGM_FT_FP64_BLANK:
			return skipDCGMValue
		case dcgm.DCGM_FT_FP64_NOT_FOUND:
			return skipDCGMValue
		case dcgm.DCGM_FT_FP64_NOT_SUPPORTED:
			return skipDCGMValue
		case dcgm.DCGM_FT_FP64_NOT_PERMISSIONED:
			return skipDCGMValue
		default:
			return fmt.Sprintf("%f", value.Float64())
		}
	case dcgm.DCGM_FT_STRING:
		switch v := value.String(); v {
		case dcgm.DCGM_FT_STR_BLANK:
			return skipDCGMValue
		case dcgm.DCGM_FT_STR_NOT_FOUND:
			return skipDCGMValue
		case dcgm.DCGM_FT_STR_NOT_SUPPORTED:
			return skipDCGMValue
		case dcgm.DCGM_FT_STR_NOT_PERMISSIONED:
			return skipDCGMValue
		default:
			return v
		}
	}

	return FailedToConvert
}
