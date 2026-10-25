/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package devicewatcher

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	. "github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

// DCP/profiling fields (DCGM_FI_PROF_*) are validated for an entire DCGM watch
// group at registration time: if any GPU in the group lacks a requested
// profiling field, the whole watch call fails. On a node mixing GPU models
// that turns "one model lacks this field" into "the exporter won't start"
// (https://github.com/NVIDIA/dcgm-exporter/issues/657). GPU watches with DCP
// fields therefore use one DCGM group per model, each watching only the DCP
// fields that model supports.
//
// dcpSupport is the single per-model answer that both watch registration and
// scrape-time field selection read, so the two always agree. If they did not,
// a scrape would request a DCP field that was never watched for an entity,
// get DCGM_ST_NOT_WATCHED, and enter the stale-profiling-watch repair loop.
var dcpSupport = struct {
	sync.RWMutex
	byModel map[string]map[dcgm.Short]bool
}{byModel: map[string]map[dcgm.Short]bool{}}

// RefreshDCPSupport queries DCGM profiling support for one representative GPU
// per model, replaces the per-model cache, and returns the union of the
// supported metric groups plus the models that answered. It returns an error
// only when no model answered. A model whose query fails is cached as
// supporting no DCP fields, so it is never watched or scraped for them.
//
// Call it on startup and after every DCGM reinit, before watches are built.
func RefreshDCPSupport() ([]dcgm.MetricGroup, []string, error) {
	gpu0Groups, gpu0Err := dcgmprovider.Client().GetSupportedMetricGroups(0)

	representatives, err := representativeGPUsByModel()
	if err != nil || len(representatives) == 0 {
		// GPU enumeration failed: keep the legacy GPU 0 answer and leave the
		// cache empty so watches query each model on demand.
		ResetDCPSupport()
		if gpu0Err != nil {
			return nil, nil, gpu0Err
		}
		return gpu0Groups, []string{"unknown"}, nil
	}

	byModel := make(map[string]map[dcgm.Short]bool, len(representatives))
	var union []dcgm.MetricGroup
	var models []string
	var firstErr error
	for _, rep := range representatives {
		groups, err := gpu0Groups, gpu0Err
		if rep.gpuID != 0 {
			groups, err = dcgmprovider.Client().GetSupportedMetricGroups(rep.gpuID)
		}
		if err != nil {
			slog.Warn("Could not query DCP metric group support for GPU model; not collecting profiling fields for it",
				slog.String("gpu_model", rep.model),
				slog.Uint64("gpu_id", uint64(rep.gpuID)),
				slog.String(ErrorKey, err.Error()))
			byModel[rep.model] = map[dcgm.Short]bool{}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		byModel[rep.model] = dcpFieldSet(groups)
		union = append(union, groups...)
		models = append(models, rep.model)
	}
	storeDCPSupport(byModel)

	if len(models) == 0 {
		return nil, nil, firstErr
	}
	return union, models, nil
}

// FilterUnwatchedDCPFields drops DCP fields that were not watched for the
// device's GPU model, leaving other fields untouched. Models absent from the
// cache were never watched through the per-model path, so their fields pass
// through.
func FilterUnwatchedDCPFields(device dcgm.Device, fields []dcgm.Short) []dcgm.Short {
	dcpSupport.RLock()
	supported, ok := dcpSupport.byModel[gpuModelKey(device)]
	dcpSupport.RUnlock()
	if !ok {
		return fields
	}
	return filterFieldsForModel(fields, supported)
}

type gpuModelRepresentative struct {
	model string
	gpuID uint
}

// representativeGPUsByModel returns the lowest-indexed GPU of each model.
// DCGM reports profiling support per model, so one GPU per model suffices.
func representativeGPUsByModel() ([]gpuModelRepresentative, error) {
	gpuCount, err := dcgmprovider.Client().GetAllDeviceCount()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var representatives []gpuModelRepresentative
	for gpuID := uint(0); gpuID < gpuCount; gpuID++ {
		device, err := dcgmprovider.Client().GetDeviceInfo(gpuID)
		if err != nil {
			slog.Debug("Skipping GPU while querying DCP support; device info unavailable",
				slog.Uint64("gpu_id", uint64(gpuID)), slog.String(ErrorKey, err.Error()))
			continue
		}
		model := gpuModelKey(device)
		if seen[model] {
			continue
		}
		seen[model] = true
		representatives = append(representatives, gpuModelRepresentative{model: model, gpuID: gpuID})
	}
	return representatives, nil
}

// dcpSupportForModel returns the cached DCP support for a model, querying
// representativeGPUID on a cache miss. A failed query is cached as no support
// so watch registration and scraping fail closed together.
func dcpSupportForModel(model string, representativeGPUID uint) map[dcgm.Short]bool {
	dcpSupport.RLock()
	supported, ok := dcpSupport.byModel[model]
	dcpSupport.RUnlock()
	if ok {
		return supported
	}

	groups, err := dcgmprovider.Client().GetSupportedMetricGroups(representativeGPUID)
	if err != nil {
		slog.Warn("Could not query DCP metric group support for GPU model; watching non-profiling fields only for it",
			slog.String("gpu_model", model), slog.String(ErrorKey, err.Error()))
		supported = map[dcgm.Short]bool{}
	} else {
		supported = dcpFieldSet(groups)
	}

	dcpSupport.Lock()
	defer dcpSupport.Unlock()
	if cached, ok := dcpSupport.byModel[model]; ok {
		return cached
	}
	dcpSupport.byModel[model] = supported
	return supported
}

// ResetDCPSupport clears the per-model cache.
func ResetDCPSupport() {
	storeDCPSupport(map[string]map[dcgm.Short]bool{})
}

func storeDCPSupport(byModel map[string]map[dcgm.Short]bool) {
	dcpSupport.Lock()
	defer dcpSupport.Unlock()
	dcpSupport.byModel = byModel
}

func dcpFieldSet(groups []dcgm.MetricGroup) map[dcgm.Short]bool {
	supported := make(map[dcgm.Short]bool)
	for _, group := range groups {
		for _, fieldID := range group.FieldIds {
			supported[dcgm.Short(fieldID)] = true
		}
	}
	return supported
}

// anyDCPField reports whether any field across the watch groups is a DCP field.
func anyDCPField(fieldWatchGroups []FieldWatchGroup) bool {
	for _, fieldWatchGroup := range fieldWatchGroups {
		for _, fieldID := range fieldWatchGroup.Fields {
			if counters.IsDCPField(uint(fieldID)) {
				return true
			}
		}
	}
	return false
}

// gpuModelKey identifies a GPU's model for per-model DCP support. A GPU with
// no model name (DCGM returned only a minimal identity) gets a key of its own,
// so unidentified GPUs of different models never share a watch group.
func gpuModelKey(device dcgm.Device) string {
	if device.Identifiers.Model != "" {
		return device.Identifiers.Model
	}
	return fmt.Sprintf("unidentified GPU %d", device.GPU)
}

// anyFieldToWatch reports whether any watch group keeps a field after DCP
// filtering with supported.
func anyFieldToWatch(fieldWatchGroups []FieldWatchGroup, supported map[dcgm.Short]bool) bool {
	for _, fieldWatchGroup := range fieldWatchGroups {
		if len(filterFieldsForModel(fieldWatchGroup.Fields, supported)) > 0 {
			return true
		}
	}
	return false
}

// partitionMonitoredEntitiesByModel groups entities by their GPU's model key.
// models is sorted so group creation order is deterministic.
func partitionMonitoredEntitiesByModel(
	monitoringInfo []devicemonitoring.Info,
) ([]string, map[string][]devicemonitoring.Info) {
	byModel := make(map[string][]devicemonitoring.Info)
	for _, mi := range monitoringInfo {
		model := gpuModelKey(mi.DeviceInfo)
		byModel[model] = append(byModel[model], mi)
	}

	models := make([]string, 0, len(byModel))
	for model := range byModel {
		models = append(models, model)
	}
	sort.Strings(models)

	return models, byModel
}

// filterFieldsForModel drops DCP fields the model does not support. Other
// fields always pass: DCGM reports an unsupported ordinary field as a
// per-entity blank value instead of failing the watch. The input slice is
// returned as-is when nothing is dropped.
func filterFieldsForModel(fields []dcgm.Short, supported map[dcgm.Short]bool) []dcgm.Short {
	keep := func(fieldID dcgm.Short) bool {
		return !counters.IsDCPField(uint(fieldID)) || supported[fieldID]
	}

	firstDropped := -1
	for i, fieldID := range fields {
		if !keep(fieldID) {
			firstDropped = i
			break
		}
	}
	if firstDropped < 0 {
		return fields
	}

	filtered := append(make([]dcgm.Short, 0, len(fields)-1), fields[:firstDropped]...)
	for _, fieldID := range fields[firstDropped+1:] {
		if keep(fieldID) {
			filtered = append(filtered, fieldID)
		}
	}
	return filtered
}
