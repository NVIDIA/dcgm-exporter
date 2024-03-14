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
	"fmt"
	"maps"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/pkg/common"
	dcgmClient "github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/dcgm_client"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/sysinfo"
)

var expCollectorFieldGroupIdx atomic.Uint32

type expCollector struct {
	sysInfo             sysinfo.SystemInfo             // Hardware system info
	counter             common.Counter                 // Counter that collector
	hostname            string                         // Hostname
	config              *common.Config                 // Configuration settings
	labelDeviceFields   []dcgm.Short                   // Fields used for labels
	counterDeviceFields []dcgm.Short                   // Fields used for the counter
	labelsCounters      []common.Counter               // Counters used for labels
	cleanups            []func()                       // Cleanup functions
	fieldValueParser    func(val int64) []int64        // Function to parse the field value
	labelFiller         func(map[string]string, int64) // Function to fill labels
	windowSize          int                            // Window size
}

// newExpCollector is a constructor for the expCollector
func newExpCollector(
	counters []common.Counter,
	hostname string,
	counterDeviceFields []dcgm.Short,
	config *common.Config,
	fieldEntityGroupTypeSystemInfo sysinfo.FieldEntityGroupTypeSystemInfoItem,
) expCollector {
	var labelsCounters []common.Counter
	for i := 0; i < len(counters); i++ {
		if counters[i].PromType == "label" {
			labelsCounters = append(labelsCounters, counters[i])
		}
	}

	labelDeviceFields := sysinfo.NewDeviceFields(labelsCounters, dcgm.FE_GPU)

	collector := expCollector{
		hostname:            hostname,
		config:              config,
		labelDeviceFields:   labelDeviceFields,
		labelsCounters:      labelsCounters,
		counterDeviceFields: counterDeviceFields,
		fieldValueParser: func(val int64) []int64 {
			return []int64{val}
		},
		labelFiller: func(metricValueLabels map[string]string, entityValue int64) {},
	}

	collector.sysInfo = fieldEntityGroupTypeSystemInfo.SystemInfo

	var err error

	collector.cleanups, err = sysinfo.SetupDcgmFieldsWatch(collector.counterDeviceFields,
		collector.sysInfo,
		int64(config.CollectInterval)*1000)
	if err != nil {
		logrus.Fatal("Failed to watch metrics: ", err)
	}

	return collector
}

func (c *expCollector) GetMetrics() (MetricsByCounter, error) {

	fieldGroupIdx := expCollectorFieldGroupIdx.Add(1)

	fieldGroupName := fmt.Sprintf("expCollectorFieldGroupName%d", fieldGroupIdx)
	fieldsGroup, err := dcgmClient.Client().FieldGroupCreate(fieldGroupName, c.counterDeviceFields)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = dcgmClient.Client().FieldGroupDestroy(fieldsGroup)
	}()

	err = dcgmClient.Client().UpdateAllFields()
	if err != nil {
		return nil, err
	}

	mapEntityIDToValues := map[uint]map[int64]int{}

	window := time.Now().Add(-time.Duration(c.windowSize) * time.Millisecond)

	values, _, err := dcgmClient.Client().GetValuesSince(dcgmClient.Client().GroupAllGPUs(), fieldsGroup, window)
	if err != nil {
		return nil, err
	}

	for _, val := range values {
		if val.Status == 0 {
			if _, exists := mapEntityIDToValues[val.EntityId]; !exists {
				mapEntityIDToValues[val.EntityId] = map[int64]int{}
			}
			for _, v := range c.fieldValueParser(val.Int64()) {
				mapEntityIDToValues[val.EntityId][v] += 1
			}
		}
	}

	labels := map[string]string{}
	labels[WindowSizeInMSLabel] = fmt.Sprint(c.windowSize)

	monitoringInfo := sysinfo.GetMonitoredEntities(c.sysInfo)
	metrics := make(MetricsByCounter)
	useOld := c.config.UseOldNamespace
	uuid := "UUID"
	if useOld {
		uuid = "uuid"
	}
	for _, mi := range monitoringInfo {
		if len(c.labelsCounters) > 0 {
			err := c.getLabelsFromCounters(mi, labels)
			if err != nil {
				return nil, err
			}
		}
		entityValues, exists := mapEntityIDToValues[mi.DeviceInfo.GPU]
		if exists {
			for entityValue, val := range entityValues {

				metricValueLabels := maps.Clone(labels)
				c.labelFiller(metricValueLabels, entityValue)

				gpuModel := getGPUModel(mi.DeviceInfo, c.config.ReplaceBlanksInModelName)

				m := Metric{
					Counter:      c.counter,
					Value:        fmt.Sprint(val),
					UUID:         uuid,
					GPU:          fmt.Sprintf("%d", mi.DeviceInfo.GPU),
					GPUUUID:      mi.DeviceInfo.UUID,
					GPUDevice:    fmt.Sprintf("nvidia%d", mi.DeviceInfo.GPU),
					GPUModelName: gpuModel,
					Hostname:     c.hostname,

					Labels:     metricValueLabels,
					Attributes: map[string]string{},
				}
				if mi.InstanceInfo != nil {
					m.MigProfile = mi.InstanceInfo.ProfileName
					m.GPUInstanceID = fmt.Sprintf("%d", mi.InstanceInfo.Info.NvmlInstanceId)
				} else {
					m.MigProfile = ""
					m.GPUInstanceID = ""
				}

				metrics[c.counter] = append(metrics[c.counter], m)
			}
		}
	}

	return metrics, nil
}

func (c *expCollector) getLabelsFromCounters(mi sysinfo.MonitoringInfo, labels map[string]string) error {
	latestValues, err := dcgmClient.Client().EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId,
		c.labelDeviceFields)
	if err != nil {
		return err
	}
	// Extract Labels
	for _, val := range latestValues {
		v := ToString(val)
		// Filter out counters with no value and ignored fields for this entity
		if v == SkipDCGMValue {
			continue
		}

		counter, err := FindCounterField(c.labelsCounters, val.FieldId)
		if err != nil {
			continue
		}

		if counter.PromType == "label" {
			labels[counter.FieldName] = v
			continue
		}
	}
	return nil
}

func (c *expCollector) GetSysinfo() sysinfo.SystemInfo {
	return c.sysInfo
}

func (c *expCollector) Cleanup() {
	for _, cleanup := range c.cleanups {
		cleanup()
	}
}
