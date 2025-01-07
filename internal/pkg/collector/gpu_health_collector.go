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
	"maps"
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

var gpuHealthChecks = []dcgm.HealthSystem{
	dcgm.DCGM_HEALTH_WATCH_PCIE,
	dcgm.DCGM_HEALTH_WATCH_NVLINK,
	dcgm.DCGM_HEALTH_WATCH_PMU,
	dcgm.DCGM_HEALTH_WATCH_MCU,
	dcgm.DCGM_HEALTH_WATCH_MEM,
	dcgm.DCGM_HEALTH_WATCH_SM,
	dcgm.DCGM_HEALTH_WATCH_INFOROM,
	dcgm.DCGM_HEALTH_WATCH_THERMAL,
	dcgm.DCGM_HEALTH_WATCH_POWER,
	dcgm.DCGM_HEALTH_WATCH_DRIVER,
}

type gpuHealthStatusCollector struct {
	baseExpCollector
	groupID            dcgm.GroupHandle
	deviceInfoProvider deviceinfo.Provider
}

func (c *gpuHealthStatusCollector) GetMetrics() (MetricsByCounter, error) {
	// Read the GPU health status.
	gpuHealthStatus, err := dcgmprovider.Client().HealthCheck(c.groupID)
	if err != nil {
		return MetricsByCounter{}, err
	}

	monitoringInfo := devicemonitoring.GetMonitoredEntities(c.deviceInfoProvider)

	// Get the GPU in the group
	groupInfo, err := dcgmprovider.Client().GetGroupInfo(c.groupID)
	if err != nil {
		return MetricsByCounter{}, err
	}

	groupEntityPairSet := make(map[dcgm.GroupEntityPair]struct{})

	for _, entityPair := range groupInfo.EntityList {
		groupEntityPairSet[entityPair] = struct{}{}
	}

	// Find monitoring info for GPU in the group
	monitoringInfoInGroup := make([]devicemonitoring.Info, 0)

	for _, info := range monitoringInfo {
		if _, exists := groupEntityPairSet[info.Entity]; exists {
			monitoringInfoInGroup = append(monitoringInfoInGroup, info)
		}
	}

	metrics := make(MetricsByCounter)
	metrics[c.counter] = make([]Metric, 0)

	useOld := c.config.UseOldNamespace
	uuid := "UUID"
	if useOld {
		uuid = "uuid"
	}

	entityHealthSystemToIncident := map[dcgm.GroupEntityPair]map[dcgm.HealthSystem]dcgm.Incident{}

	for _, mi := range monitoringInfoInGroup {
		entityHealthSystemToIncident[mi.Entity] = make(map[dcgm.HealthSystem]dcgm.Incident)
		// Populate the table with default values
		for _, healthSystem := range gpuHealthChecks {
			entityHealthSystemToIncident[mi.Entity][healthSystem] = dcgm.Incident{
				System: healthSystem,
				Health: dcgm.DCGM_HEALTH_RESULT_PASS,
				Error:  dcgm.DiagErrorDetail{},
			}
		}
	}

	// We assyme that each health check may produce only one incident per system
	for _, incident := range gpuHealthStatus.Incidents {
		entityHealthSystemToIncident[incident.EntityInfo][incident.System] = incident
	}

	labels := map[string]string{}

	for _, mi := range monitoringInfoInGroup {
		if len(c.labelsCounters) > 0 && len(c.deviceWatchList.LabelDeviceFields()) > 0 {
			err := c.getLabelsFromCounters(mi, labels)
			if err != nil {
				return nil, err
			}
		}
		for _, healthSystem := range gpuHealthChecks {
			incident := entityHealthSystemToIncident[mi.Entity][healthSystem]
			metricValueLabels := maps.Clone(labels)
			metricValueLabels["health_watch"] = healthSystemWatchToString(incident.System)
			metricValueLabels["health_error_code"] = healthCheckErrorToString(incident.Error.Code)
			m := c.createMetric(metricValueLabels, mi, uuid, int(incident.Health))
			metrics[c.counter] = append(metrics[c.counter], m)
		}
	}

	return metrics, nil
}

func (c *gpuHealthStatusCollector) Cleanup() {
	for _, cleanup := range c.cleanups {
		cleanup()
	}
}

func NewGPUHealthStatusCollector(
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (Collector, error) {
	if !IsDCGMExpGPUHealthStatusEnabled(counterList) {
		logrus.Error(counters.DCGMExpGPUHealthStatus + " collector is disabled")
		return nil, fmt.Errorf(counters.DCGMExpGPUHealthStatus + " collector is disabled")
	}

	supportedGPUs, err := dcgmprovider.Client().GetSupportedDevices()
	if err != nil {
		logrus.WithError(err).Error("Failed to get supported GPU devices")
		return nil, err
	}

	if len(supportedGPUs) == 0 {
		logrus.Error("No supported GPU devices found")
		return nil, errors.New("no supported GPU devices found")
	}

	// Create Group
	newGroupNumber, err := utils.RandUint64()
	if err != nil {
		logrus.WithError(err).Error("Failed to generate new group number")
		return nil, err
	}

	cleanups := []func(){}

	groupID, err := dcgmprovider.Client().CreateGroup(fmt.Sprintf("gpu_health_monitor_%d", newGroupNumber))
	if err != nil {
		logrus.WithError(err).Error("Failed to create group")
		return nil, err
	}

	cleanups = append(cleanups, func() {
		destroyErr := dcgmprovider.Client().DestroyGroup(groupID)
		if destroyErr != nil {
			logrus.WithFields(logrus.Fields{
				logging.GroupIDKey: groupID,
				logrus.ErrorKey:    destroyErr,
			}).Warn("cannot destroy group")
		}
	})

	for _, gpu := range supportedGPUs {
		err = dcgmprovider.Client().AddEntityToGroup(groupID, dcgm.FE_GPU, gpu)
		if err != nil {
			logrus.WithError(err).WithField("gpu", gpu).Error("Failed to add GPU device to group")
			return nil, err
		}
	}

	err = dcgmprovider.Client().HealthSet(groupID, dcgm.DCGM_HEALTH_WATCH_ALL)
	if err != nil {
		logrus.WithError(err).Error("Failed to set health watch")
		return nil, err
	}

	deviceInfoProvider, err := deviceinfo.Initialize(appconfig.DeviceOptions{
		MinorRange: []int{-1},
		MajorRange: []int{-1},
	},
		appconfig.DeviceOptions{},
		appconfig.DeviceOptions{},
		config.UseFakeGPUs, dcgm.FE_GPU)
	if err != nil {
		return nil, err
	}

	if !deviceWatchList.IsEmpty() {
		watchListCleanups, err := deviceWatchList.Watch()
		if err != nil {
			logrus.WithError(err).Error("Failed to watch metrics")
			return nil, err
		}

		cleanups = append(cleanups, watchListCleanups...)
	}

	return &gpuHealthStatusCollector{
		baseExpCollector: baseExpCollector{
			counter: counterList[slices.IndexFunc(counterList, func(c counters.Counter) bool {
				return c.FieldName == counters.DCGMExpGPUHealthStatus
			})],
			labelsCounters:  counterList.LabelCounters(),
			hostname:        hostname,
			config:          config,
			cleanups:        cleanups,
			deviceWatchList: deviceWatchList,
		},
		groupID:            groupID,
		deviceInfoProvider: deviceInfoProvider,
	}, nil
}

func IsDCGMExpGPUHealthStatusEnabled(counterList counters.CounterList) bool {
	return slices.ContainsFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpGPUHealthStatus
	})
}

var healthSystemWatchToStringMap = map[dcgm.HealthSystem]string{
	dcgm.DCGM_HEALTH_WATCH_PCIE:              "PCIE",
	dcgm.DCGM_HEALTH_WATCH_NVLINK:            "NVLINK",
	dcgm.DCGM_HEALTH_WATCH_PMU:               "PMU",
	dcgm.DCGM_HEALTH_WATCH_MCU:               "MCU",
	dcgm.DCGM_HEALTH_WATCH_MEM:               "MEM",
	dcgm.DCGM_HEALTH_WATCH_SM:                "SM",
	dcgm.DCGM_HEALTH_WATCH_INFOROM:           "INFOROM",
	dcgm.DCGM_HEALTH_WATCH_THERMAL:           "THERMAL",
	dcgm.DCGM_HEALTH_WATCH_POWER:             "POWER",
	dcgm.DCGM_HEALTH_WATCH_DRIVER:            "DRIVER",
	dcgm.DCGM_HEALTH_WATCH_NVSWITCH_NONFATAL: "NVSWITCH_NONFATAL",
	dcgm.DCGM_HEALTH_WATCH_NVSWITCH_FATAL:    "NVSWITCH_FATAL",
}

func healthSystemWatchToString(heathSystem dcgm.HealthSystem) string {
	name, ok := healthSystemWatchToStringMap[heathSystem]
	if !ok {
		return ""
	}
	return name
}

var healthCheckErrorToStringMap = map[dcgm.HealthCheckErrorCode]string{
	dcgm.DCGM_FR_OK:                              "DCGM_FR_OK",
	dcgm.DCGM_FR_UNKNOWN:                         "DCGM_FR_UNKNOWN",
	dcgm.DCGM_FR_UNRECOGNIZED:                    "DCGM_FR_UNRECOGNIZED",
	dcgm.DCGM_FR_PCI_REPLAY_RATE:                 "DCGM_FR_PCI_REPLAY_RATE",
	dcgm.DCGM_FR_VOLATILE_DBE_DETECTED:           "DCGM_FR_VOLATILE_DBE_DETECTED",
	dcgm.DCGM_FR_VOLATILE_SBE_DETECTED:           "DCGM_FR_VOLATILE_SBE_DETECTED",
	dcgm.DCGM_FR_PENDING_PAGE_RETIREMENTS:        "DCGM_FR_PENDING_PAGE_RETIREMENTS",
	dcgm.DCGM_FR_RETIRED_PAGES_LIMIT:             "DCGM_FR_RETIRED_PAGES_LIMIT",
	dcgm.DCGM_FR_RETIRED_PAGES_DBE_LIMIT:         "DCGM_FR_RETIRED_PAGES_DBE_LIMIT",
	dcgm.DCGM_FR_CORRUPT_INFOROM:                 "DCGM_FR_CORRUPT_INFOROM",
	dcgm.DCGM_FR_CLOCK_THROTTLE_THERMAL:          "DCGM_FR_CLOCK_THROTTLE_THERMAL",
	dcgm.DCGM_FR_POWER_UNREADABLE:                "DCGM_FR_POWER_UNREADABLE",
	dcgm.DCGM_FR_CLOCK_THROTTLE_POWER:            "DCGM_FR_CLOCK_THROTTLE_POWER",
	dcgm.DCGM_FR_NVLINK_ERROR_THRESHOLD:          "DCGM_FR_NVLINK_ERROR_THRESHOLD",
	dcgm.DCGM_FR_NVLINK_DOWN:                     "DCGM_FR_NVLINK_DOWN",
	dcgm.DCGM_FR_NVSWITCH_FATAL_ERROR:            "DCGM_FR_NVSWITCH_FATAL_ERROR",
	dcgm.DCGM_FR_NVSWITCH_NON_FATAL_ERROR:        "DCGM_FR_NVSWITCH_NON_FATAL_ERROR",
	dcgm.DCGM_FR_NVSWITCH_DOWN:                   "DCGM_FR_NVSWITCH_DOWN",
	dcgm.DCGM_FR_NO_ACCESS_TO_FILE:               "DCGM_FR_NO_ACCESS_TO_FILE",
	dcgm.DCGM_FR_NVML_API:                        "DCGM_FR_NVML_API",
	dcgm.DCGM_FR_DEVICE_COUNT_MISMATCH:           "DCGM_FR_DEVICE_COUNT_MISMATCH",
	dcgm.DCGM_FR_BAD_PARAMETER:                   "DCGM_FR_BAD_PARAMETER",
	dcgm.DCGM_FR_CANNOT_OPEN_LIB:                 "DCGM_FR_CANNOT_OPEN_LIB",
	dcgm.DCGM_FR_DENYLISTED_DRIVER:               "DCGM_FR_DENYLISTED_DRIVER",
	dcgm.DCGM_FR_NVML_LIB_BAD:                    "DCGM_FR_NVML_LIB_BAD",
	dcgm.DCGM_FR_GRAPHICS_PROCESSES:              "DCGM_FR_GRAPHICS_PROCESSES",
	dcgm.DCGM_FR_HOSTENGINE_CONN:                 "DCGM_FR_HOSTENGINE_CONN",
	dcgm.DCGM_FR_FIELD_QUERY:                     "DCGM_FR_FIELD_QUERY",
	dcgm.DCGM_FR_BAD_CUDA_ENV:                    "DCGM_FR_BAD_CUDA_ENV",
	dcgm.DCGM_FR_PERSISTENCE_MODE:                "DCGM_FR_PERSISTENCE_MODE",
	dcgm.DCGM_FR_LOW_BANDWIDTH:                   "DCGM_FR_LOW_BANDWIDTH",
	dcgm.DCGM_FR_HIGH_LATENCY:                    "DCGM_FR_HIGH_LATENCY",
	dcgm.DCGM_FR_CANNOT_GET_FIELD_TAG:            "DCGM_FR_CANNOT_GET_FIELD_TAG",
	dcgm.DCGM_FR_FIELD_VIOLATION:                 "DCGM_FR_FIELD_VIOLATION",
	dcgm.DCGM_FR_FIELD_THRESHOLD:                 "DCGM_FR_FIELD_THRESHOLD",
	dcgm.DCGM_FR_FIELD_VIOLATION_DBL:             "DCGM_FR_FIELD_VIOLATION_DBL",
	dcgm.DCGM_FR_FIELD_THRESHOLD_DBL:             "DCGM_FR_FIELD_THRESHOLD_DBL",
	dcgm.DCGM_FR_UNSUPPORTED_FIELD_TYPE:          "DCGM_FR_UNSUPPORTED_FIELD_TYPE",
	dcgm.DCGM_FR_FIELD_THRESHOLD_TS:              "DCGM_FR_FIELD_THRESHOLD_TS",
	dcgm.DCGM_FR_FIELD_THRESHOLD_TS_DBL:          "DCGM_FR_FIELD_THRESHOLD_TS_DBL",
	dcgm.DCGM_FR_THERMAL_VIOLATIONS:              "DCGM_FR_THERMAL_VIOLATIONS",
	dcgm.DCGM_FR_THERMAL_VIOLATIONS_TS:           "DCGM_FR_THERMAL_VIOLATIONS_TS",
	dcgm.DCGM_FR_TEMP_VIOLATION:                  "DCGM_FR_TEMP_VIOLATION",
	dcgm.DCGM_FR_THROTTLING_VIOLATION:            "DCGM_FR_THROTTLING_VIOLATION",
	dcgm.DCGM_FR_INTERNAL:                        "DCGM_FR_INTERNAL",
	dcgm.DCGM_FR_PCIE_GENERATION:                 "DCGM_FR_PCIE_GENERATION",
	dcgm.DCGM_FR_PCIE_WIDTH:                      "DCGM_FR_PCIE_WIDTH",
	dcgm.DCGM_FR_ABORTED:                         "DCGM_FR_ABORTED",
	dcgm.DCGM_FR_TEST_DISABLED:                   "DCGM_FR_TEST_DISABLED",
	dcgm.DCGM_FR_CANNOT_GET_STAT:                 "DCGM_FR_CANNOT_GET_STAT",
	dcgm.DCGM_FR_STRESS_LEVEL:                    "DCGM_FR_STRESS_LEVEL",
	dcgm.DCGM_FR_CUDA_API:                        "DCGM_FR_CUDA_API",
	dcgm.DCGM_FR_FAULTY_MEMORY:                   "DCGM_FR_FAULTY_MEMORY",
	dcgm.DCGM_FR_CANNOT_SET_WATCHES:              "DCGM_FR_CANNOT_SET_WATCHES",
	dcgm.DCGM_FR_CUDA_UNBOUND:                    "DCGM_FR_CUDA_UNBOUND",
	dcgm.DCGM_FR_ECC_DISABLED:                    "DCGM_FR_ECC_DISABLED",
	dcgm.DCGM_FR_MEMORY_ALLOC:                    "DCGM_FR_MEMORY_ALLOC",
	dcgm.DCGM_FR_CUDA_DBE:                        "DCGM_FR_CUDA_DBE",
	dcgm.DCGM_FR_MEMORY_MISMATCH:                 "DCGM_FR_MEMORY_MISMATCH",
	dcgm.DCGM_FR_CUDA_DEVICE:                     "DCGM_FR_CUDA_DEVICE",
	dcgm.DCGM_FR_ECC_UNSUPPORTED:                 "DCGM_FR_ECC_UNSUPPORTED",
	dcgm.DCGM_FR_ECC_PENDING:                     "DCGM_FR_ECC_PENDING",
	dcgm.DCGM_FR_MEMORY_BANDWIDTH:                "DCGM_FR_MEMORY_BANDWIDTH",
	dcgm.DCGM_FR_TARGET_POWER:                    "DCGM_FR_TARGET_POWER",
	dcgm.DCGM_FR_API_FAIL:                        "DCGM_FR_API_FAIL",
	dcgm.DCGM_FR_API_FAIL_GPU:                    "DCGM_FR_API_FAIL_GPU",
	dcgm.DCGM_FR_CUDA_CONTEXT:                    "DCGM_FR_CUDA_CONTEXT",
	dcgm.DCGM_FR_DCGM_API:                        "DCGM_FR_DCGM_API",
	dcgm.DCGM_FR_CONCURRENT_GPUS:                 "DCGM_FR_CONCURRENT_GPUS",
	dcgm.DCGM_FR_TOO_MANY_ERRORS:                 "DCGM_FR_TOO_MANY_ERRORS",
	dcgm.DCGM_FR_NVLINK_CRC_ERROR_THRESHOLD:      "DCGM_FR_NVLINK_CRC_ERROR_THRESHOLD",
	dcgm.DCGM_FR_NVLINK_ERROR_CRITICAL:           "DCGM_FR_NVLINK_ERROR_CRITICAL",
	dcgm.DCGM_FR_ENFORCED_POWER_LIMIT:            "DCGM_FR_ENFORCED_POWER_LIMIT",
	dcgm.DCGM_FR_MEMORY_ALLOC_HOST:               "DCGM_FR_MEMORY_ALLOC_HOST",
	dcgm.DCGM_FR_GPU_OP_MODE:                     "DCGM_FR_GPU_OP_MODE",
	dcgm.DCGM_FR_NO_MEMORY_CLOCKS:                "DCGM_FR_NO_MEMORY_CLOCKS",
	dcgm.DCGM_FR_NO_GRAPHICS_CLOCKS:              "DCGM_FR_NO_GRAPHICS_CLOCKS",
	dcgm.DCGM_FR_HAD_TO_RESTORE_STATE:            "DCGM_FR_HAD_TO_RESTORE_STATE",
	dcgm.DCGM_FR_L1TAG_UNSUPPORTED:               "DCGM_FR_L1TAG_UNSUPPORTED",
	dcgm.DCGM_FR_L1TAG_MISCOMPARE:                "DCGM_FR_L1TAG_MISCOMPARE",
	dcgm.DCGM_FR_ROW_REMAP_FAILURE:               "DCGM_FR_ROW_REMAP_FAILURE",
	dcgm.DCGM_FR_UNCONTAINED_ERROR:               "DCGM_FR_UNCONTAINED_ERROR",
	dcgm.DCGM_FR_EMPTY_GPU_LIST:                  "DCGM_FR_EMPTY_GPU_LIST",
	dcgm.DCGM_FR_DBE_PENDING_PAGE_RETIREMENTS:    "DCGM_FR_DBE_PENDING_PAGE_RETIREMENTS",
	dcgm.DCGM_FR_UNCORRECTABLE_ROW_REMAP:         "DCGM_FR_UNCORRECTABLE_ROW_REMAP",
	dcgm.DCGM_FR_PENDING_ROW_REMAP:               "DCGM_FR_PENDING_ROW_REMAP",
	dcgm.DCGM_FR_BROKEN_P2P_MEMORY_DEVICE:        "DCGM_FR_BROKEN_P2P_MEMORY_DEVICE",
	dcgm.DCGM_FR_BROKEN_P2P_WRITER_DEVICE:        "DCGM_FR_BROKEN_P2P_WRITER_DEVICE",
	dcgm.DCGM_FR_NVSWITCH_NVLINK_DOWN:            "DCGM_FR_NVSWITCH_NVLINK_DOWN",
	dcgm.DCGM_FR_EUD_BINARY_PERMISSIONS:          "DCGM_FR_EUD_BINARY_PERMISSIONS",
	dcgm.DCGM_FR_EUD_NON_ROOT_USER:               "DCGM_FR_EUD_NON_ROOT_USER",
	dcgm.DCGM_FR_EUD_SPAWN_FAILURE:               "DCGM_FR_EUD_SPAWN_FAILURE",
	dcgm.DCGM_FR_EUD_TIMEOUT:                     "DCGM_FR_EUD_TIMEOUT",
	dcgm.DCGM_FR_EUD_ZOMBIE:                      "DCGM_FR_EUD_ZOMBIE",
	dcgm.DCGM_FR_EUD_NON_ZERO_EXIT_CODE:          "DCGM_FR_EUD_NON_ZERO_EXIT_CODE",
	dcgm.DCGM_FR_EUD_TEST_FAILED:                 "DCGM_FR_EUD_TEST_FAILED",
	dcgm.DCGM_FR_FILE_CREATE_PERMISSIONS:         "DCGM_FR_FILE_CREATE_PERMISSIONS",
	dcgm.DCGM_FR_PAUSE_RESUME_FAILED:             "DCGM_FR_PAUSE_RESUME_FAILED",
	dcgm.DCGM_FR_PCIE_H_REPLAY_VIOLATION:         "DCGM_FR_PCIE_H_REPLAY_VIOLATION",
	dcgm.DCGM_FR_GPU_EXPECTED_NVLINKS_UP:         "DCGM_FR_GPU_EXPECTED_NVLINKS_UP",
	dcgm.DCGM_FR_NVSWITCH_EXPECTED_NVLINKS_UP:    "DCGM_FR_NVSWITCH_EXPECTED_NVLINKS_UP",
	dcgm.DCGM_FR_XID_ERROR:                       "DCGM_FR_XID_ERROR",
	dcgm.DCGM_FR_SBE_VIOLATION:                   "DCGM_FR_SBE_VIOLATION",
	dcgm.DCGM_FR_DBE_VIOLATION:                   "DCGM_FR_DBE_VIOLATION",
	dcgm.DCGM_FR_PCIE_REPLAY_VIOLATION:           "DCGM_FR_PCIE_REPLAY_VIOLATION",
	dcgm.DCGM_FR_SBE_THRESHOLD_VIOLATION:         "DCGM_FR_SBE_THRESHOLD_VIOLATION",
	dcgm.DCGM_FR_DBE_THRESHOLD_VIOLATION:         "DCGM_FR_DBE_THRESHOLD_VIOLATION",
	dcgm.DCGM_FR_PCIE_REPLAY_THRESHOLD_VIOLATION: "DCGM_FR_PCIE_REPLAY_THRESHOLD_VIOLATION",
	dcgm.DCGM_FR_CUDA_FM_NOT_INITIALIZED:         "DCGM_FR_CUDA_FM_NOT_INITIALIZED",
	dcgm.DCGM_FR_SXID_ERROR:                      "DCGM_FR_SXID_ERROR",
	dcgm.DCGM_FR_ERROR_SENTINEL:                  "DCGM_FR_ERROR_SENTINEL",
}

func healthCheckErrorToString(err dcgm.HealthCheckErrorCode) string {
	return healthCheckErrorToStringMap[err]
}
