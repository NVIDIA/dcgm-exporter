/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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
	"slices"
	"strconv"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// systemHealthWatches are the health systems set on the system health group.
// IMEX is the one it reports. DRIVER is set as well because DCGM uses one
// health-watch owner for all groups on a connection: when a group's HealthSet
// leaves DRIVER off, DCGM removes that owner's DCGM_FI_DEV_GPU_RECOVERY_ACTION
// watch on each GPU in the group, and the GPU health collector's group then
// silently stops reporting GPU recovery actions.
const systemHealthWatches = dcgm.DCGM_HEALTH_WATCH_IMEX | dcgm.DCGM_HEALTH_WATCH_DRIVER

// errIMEXHealthUnsupported reports that the connected DCGM hostengine cannot
// watch IMEX health, so the configured system health metric cannot be built.
var errIMEXHealthUnsupported = errors.New("the DCGM hostengine does not support the IMEX health watch")

// systemHealthStatusCollector reports DCGM's host-level health as one sample
// with no device labels. IMEX is the only host-level health watch DCGM has, so
// it reports IMEX only; another host-level watch would become another
// health_watch value on the same metric. It owns a DCGM health group on client
// and must be released with Cleanup.
type systemHealthStatusCollector struct {
	client   dcgmprovider.DCGM
	counter  counters.Counter
	hostname string
	groupID  dcgm.GroupHandle
	cleanup  func()
}

// newSystemHealthStatusCollectorWithClient creates the host-level health
// collector, which reports IMEX, and primes its dedicated DCGM health group
// before the first scrape. It fails with errIMEXHealthUnsupported when the
// hostengine cannot watch IMEX health.
// Every DCGM call, including scrapes and cleanup, uses client, so the group
// handle is never sent to a client that replaced it during DCGM recovery.
func newSystemHealthStatusCollectorWithClient(
	client dcgmprovider.DCGM,
	counterList counters.CounterList,
	hostname string,
) (Collector, error) {
	counterIndex := systemHealthStatusCounterIndex(counterList)
	if counterIndex < 0 {
		return nil, fmt.Errorf("%s collector is disabled", counters.DCGMExpSystemHealthStatus)
	}

	systems, err := client.HealthGetSupportedSystems()
	// A hostengine older than DCGM 4.7.1 has no supported-systems query, so it
	// cannot report IMEX health either; say so before DCGM's own error.
	if dcgm.IsFeatureUnavailable(err) {
		return nil, fmt.Errorf("%w: %w", errIMEXHealthUnsupported, err)
	}
	if err != nil {
		return nil, fmt.Errorf("query supported health systems: %w", err)
	}
	if systems&dcgm.DCGM_HEALTH_WATCH_IMEX == 0 {
		return nil, errIMEXHealthUnsupported
	}

	groupID, cleanup, err := createHealthGroup(client, "system_health_monitor", systemHealthWatches)
	if err != nil {
		return nil, fmt.Errorf("initialize system health group: %w", err)
	}

	if _, err := client.HealthCheck(groupID); err != nil {
		cleanup()
		return nil, fmt.Errorf("prime system health check: %w", err)
	}

	return &systemHealthStatusCollector{
		client:   client,
		counter:  counterList[counterIndex],
		hostname: hostname,
		groupID:  groupID,
		cleanup:  cleanup,
	}, nil
}

// GetMetrics reports the most severe host-level IMEX incident as one FE_NONE
// metric, or a passing metric when DCGM reports no matching incident.
func (c *systemHealthStatusCollector) GetMetrics() (MetricsByCounter, error) {
	response, err := c.client.HealthCheck(c.groupID)
	if err != nil {
		return nil, fmt.Errorf("check system health: %w", err)
	}

	selected := dcgm.Incident{
		System: dcgm.DCGM_HEALTH_WATCH_IMEX,
		Health: dcgm.DCGM_HEALTH_RESULT_PASS,
		Error: dcgm.DiagErrorDetail{
			Code: dcgm.DCGM_FR_OK,
		},
		EntityInfo: dcgm.GroupEntityPair{
			EntityGroupId: dcgm.FE_NONE,
			EntityId:      0,
		},
	}

	for _, incident := range response.Incidents {
		if incident.System != dcgm.DCGM_HEALTH_WATCH_IMEX ||
			incident.EntityInfo.EntityGroupId != dcgm.FE_NONE ||
			incident.EntityInfo.EntityId != 0 {
			continue
		}
		if incident.Health > selected.Health {
			selected = incident
		}
	}

	severity, category := healthErrorMetadataLabelsWithClient(c.client, selected.Error.Code)
	metric := Metric{
		Counter:  c.counter,
		Value:    strconv.FormatUint(uint64(selected.Health), 10),
		Hostname: c.hostname,
		Labels: map[string]string{
			HealthWatchLabel:         healthSystemWatchToString(selected.System),
			HealthErrorCodeLabel:     healthCheckErrorToString(selected.Error.Code),
			HealthErrorSeverityLabel: severity,
			HealthErrorCategoryLabel: category,
		},
		Attributes: map[string]string{},
	}

	return MetricsByCounter{
		c.counter: []Metric{metric},
	}, nil
}

// Cleanup destroys the dedicated DCGM health group.
func (c *systemHealthStatusCollector) Cleanup() {
	if c.cleanup != nil {
		c.cleanup()
	}
}

// IsDCGMExpSystemHealthStatusEnabled reports whether the exporter counter list
// requests the optional host-level (IMEX) health collector.
func IsDCGMExpSystemHealthStatusEnabled(counterList counters.CounterList) bool {
	return systemHealthStatusCounterIndex(counterList) >= 0
}

// systemHealthStatusCounterIndex locates the system IMEX metric configuration
// used to initialize the collector.
func systemHealthStatusCounterIndex(counterList counters.CounterList) int {
	return slices.IndexFunc(counterList, func(counter counters.Counter) bool {
		return counter.FieldName == counters.DCGMExpSystemHealthStatus
	})
}
