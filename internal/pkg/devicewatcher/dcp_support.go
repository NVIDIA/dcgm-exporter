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
	"math"
	"sort"
	"sync"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
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
//
// fallback, when set, answers for every model not in byModel. RefreshDCPSupport
// sets it to the primary GPU's support when GPUs cannot be enumerated, which
// matches the single-GPU discovery used before per-model support. client is
// the DCGM client RefreshDCPSupport queried; the cache answers only for that
// client, and until it has run for a client, its watches keep a single shared
// group.
var dcpSupport = struct {
	sync.RWMutex
	client   dcgmprovider.DCGM
	byModel  map[string]map[dcgm.Short]bool
	fallback map[dcgm.Short]bool
}{byModel: map[string]map[dcgm.Short]bool{}}

// RefreshDCPSupport queries DCGM profiling support for one representative GPU
// per model, replaces the per-model cache, and returns the union of the
// supported metric groups plus the models that answered. It returns an error
// only when no model answered. A model whose query fails is cached as
// supporting no DCP fields, so it is never watched or scraped for them.
//
// primaryGPU is the attached GPU the caller would otherwise query alone; it
// represents its own model. Call this on startup and after every DCGM reinit,
// before watches are built.
func RefreshDCPSupport(client dcgmprovider.DCGM, primaryGPU uint) ([]dcgm.MetricGroup, []string, error) {
	primaryGroups, primaryErr := client.GetSupportedMetricGroups(primaryGPU)

	representatives, err := representativeGPUsByModel(client, primaryGPU)
	if err != nil || len(representatives) == 0 {
		// GPU enumeration failed: the primary GPU's answer applies to every
		// model, as it did before per-model support.
		if primaryErr != nil {
			storeDCPSupport(client, map[string]map[dcgm.Short]bool{}, map[dcgm.Short]bool{})
			return nil, nil, primaryErr
		}
		storeDCPSupport(client, map[string]map[dcgm.Short]bool{}, dcpFieldSet(primaryGroups))
		return primaryGroups, []string{"unknown"}, nil
	}

	byModel := make(map[string]map[dcgm.Short]bool, len(representatives))
	var union []dcgm.MetricGroup
	var models []string
	var firstErr error
	for _, rep := range representatives {
		groups, err := primaryGroups, primaryErr
		if rep.gpuID != primaryGPU {
			groups, err = client.GetSupportedMetricGroups(rep.gpuID)
		}
		if err != nil {
			slog.Warn("Could not query DCP metric group support for GPU model; not collecting profiling fields for it",
				slog.String("gpu_model", rep.model),
				slog.Uint64("gpu_id", uint64(rep.gpuID)),
				slog.String(logging.ErrorKey, err.Error()))
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
	storeDCPSupport(client, byModel, nil)

	if len(models) == 0 {
		return nil, nil, firstErr
	}
	return union, models, nil
}

// FilterUnwatchedDCPFields drops DCP fields that were not watched for the
// device's GPU model through client, leaving other fields untouched. Models
// with no cached answer were never watched through the per-model path, so
// their fields pass through.
func FilterUnwatchedDCPFields(client dcgmprovider.DCGM, device dcgm.Device, fields []dcgm.Short) []dcgm.Short {
	supported, ok := cachedDCPSupport(client, gpuModelKey(device))
	if !ok {
		return fields
	}
	return filterFieldsForModel(fields, supported)
}

type gpuModelRepresentative struct {
	model string
	gpuID uint
}

// representativeGPUsByModel returns one attached GPU per model: primaryGPU for
// its own model, otherwise the lowest-indexed one. DCGM reports profiling
// support per model, so one GPU per model suffices.
func representativeGPUsByModel(client dcgmprovider.DCGM, primaryGPU uint) ([]gpuModelRepresentative, error) {
	gpuCount, err := client.GetAllDeviceCount()
	if err != nil {
		return nil, err
	}

	gpuIDs := []uint{primaryGPU}
	for gpuID := uint(0); gpuID < gpuCount; gpuID++ {
		if gpuID != primaryGPU {
			gpuIDs = append(gpuIDs, gpuID)
		}
	}

	seen := make(map[string]bool)
	var representatives []gpuModelRepresentative
	for _, gpuID := range gpuIDs {
		if gpuID != primaryGPU && client.GetGPUStatus(gpuID) == dcgm.EntityStatusDetached {
			continue
		}
		device, err := client.GetDeviceInfo(gpuID)
		if err != nil {
			slog.Debug("Skipping GPU while querying DCP support; device info unavailable",
				slog.Uint64("gpu_id", uint64(gpuID)), slog.String(logging.ErrorKey, err.Error()))
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
func dcpSupportForModel(client dcgmprovider.DCGM, model string, representativeGPUID uint) map[dcgm.Short]bool {
	if supported, ok := cachedDCPSupport(client, model); ok {
		return supported
	}

	var supported map[dcgm.Short]bool
	groups, err := client.GetSupportedMetricGroups(representativeGPUID)
	if err != nil {
		slog.Warn("Could not query DCP metric group support for GPU model; watching non-profiling fields only for it",
			slog.String("gpu_model", model), slog.String(logging.ErrorKey, err.Error()))
		supported = map[dcgm.Short]bool{}
	} else {
		supported = dcpFieldSet(groups)
	}

	dcpSupport.Lock()
	defer dcpSupport.Unlock()
	if dcpSupport.client != client {
		return supported
	}
	if cached, ok := dcpSupport.byModel[model]; ok {
		return cached
	}
	dcpSupport.byModel[model] = supported
	return supported
}

// ResetDCPSupport clears the per-model cache and its fallback, returning
// watches to a single shared group until the next RefreshDCPSupport.
func ResetDCPSupport() {
	storeDCPSupport(nil, map[string]map[dcgm.Short]bool{}, nil)
}

func storeDCPSupport(client dcgmprovider.DCGM, byModel map[string]map[dcgm.Short]bool, fallback map[dcgm.Short]bool) {
	dcpSupport.Lock()
	defer dcpSupport.Unlock()
	dcpSupport.client = client
	dcpSupport.byModel = byModel
	dcpSupport.fallback = fallback
}

// dcpSupportRefreshed reports whether DCP discovery has run through client,
// so per-model support is available to its watches.
func dcpSupportRefreshed(client dcgmprovider.DCGM) bool {
	dcpSupport.RLock()
	defer dcpSupport.RUnlock()
	return client != nil && dcpSupport.client == client
}

// cachedDCPSupport returns the support cached through client for model, or
// the fallback when the model has no entry of its own.
func cachedDCPSupport(client dcgmprovider.DCGM, model string) (map[dcgm.Short]bool, bool) {
	dcpSupport.RLock()
	defer dcpSupport.RUnlock()
	if client == nil || dcpSupport.client != client {
		return nil, false
	}
	if supported, ok := dcpSupport.byModel[model]; ok {
		return supported, true
	}
	return dcpSupport.fallback, dcpSupport.fallback != nil
}

func dcpFieldSet(groups []dcgm.MetricGroup) map[dcgm.Short]bool {
	supported := make(map[dcgm.Short]bool)
	for _, group := range groups {
		for _, fieldID := range group.FieldIds {
			if fieldID > math.MaxUint16 {
				continue
			}
			supported[dcgm.Short(fieldID)] = true //nolint:gosec // G115: bounds checked above
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
