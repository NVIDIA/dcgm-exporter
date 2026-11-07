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

package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

// fieldIDList parses repeatable unique DCGM field-ID options while retaining order.
type fieldIDList []dcgm.Short

// cacheCountsOptions selects the standalone session, requested fields and private report path.
type cacheCountsOptions struct {
	hostengine string
	outputPath string
	fieldIDs   fieldIDList
}

// String displays field IDs in their command-line order.
func (ids *fieldIDList) String() string {
	values := make([]string, 0, len(*ids))
	for _, id := range *ids {
		values = append(values, strconv.FormatUint(uint64(id), 10))
	}
	return strings.Join(values, ",")
}

// Set accepts one positive 16-bit field ID and rejects duplicates.
func (ids *fieldIDList) Set(value string) error {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 16)
	if err != nil || parsed == 0 {
		return fmt.Errorf("invalid DCGM field ID %q", value)
	}
	id := dcgm.Short(parsed)
	for _, existing := range *ids {
		if existing == id {
			return fmt.Errorf("duplicate DCGM field ID %d", parsed)
		}
	}
	*ids = append(*ids, id)
	return nil
}

// newCacheCountsCommand validates standalone connection, field IDs and private report options before querying DCGM.
func newCacheCountsCommand() *cli.Command {
	options := cacheCountsOptions{}
	return &cli.Command{
		Name:      "cache-counts",
		Usage:     "Write cached DCGM sample counts",
		UsageText: "e2e-dcgm-probe cache-counts [options]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "hostengine", Destination: &options.hostengine, Usage: "standalone DCGM host:port or connection URI"},
			&cli.GenericFlag{Name: "field-id", Value: &options.fieldIDs, Usage: "DCGM field ID to query; repeat for multiple fields"},
			&cli.StringFlag{Name: "output", Destination: &options.outputPath, Usage: "private path for the temporary JSON report"},
		},
		// Validate options before opening the native session.
		Action: func(c *cli.Context) error {
			if c.NArg() != 0 {
				return fmt.Errorf("unexpected positional arguments: %s", strings.Join(c.Args().Slice(), " "))
			}
			if strings.TrimSpace(options.hostengine) == "" {
				return errors.New("--hostengine is required")
			}
			if len(options.fieldIDs) == 0 {
				return errors.New("at least one --field-id is required")
			}
			if strings.TrimSpace(options.outputPath) == "" {
				return errors.New("--output is required")
			}
			return runCacheCounts(options)
		},
	}
}

// runCacheCounts owns a standalone DCGM session while collecting and privately writing cached sample counts.
func runCacheCounts(options cacheCountsOptions) error {
	cleanup, err := initStandaloneDCGM(options.hostengine)
	if err != nil {
		return probeStage("DCGM initialization", err)
	}
	defer cleanup()

	report, err := queryCacheCounts(options.fieldIDs)
	if err != nil {
		return err
	}
	return writePrivateJSON(options.outputPath, report)
}

// initStandaloneDCGM passes transport URIs directly and preserves the legacy TCP connection argument.
func initStandaloneDCGM(hostengine string) (func(), error) {
	lower := strings.ToLower(hostengine)
	if strings.HasPrefix(lower, "tcp://") ||
		strings.HasPrefix(lower, "unix://") ||
		strings.HasPrefix(lower, "vsock://") {
		return dcgm.Init(dcgm.Standalone, hostengine)
	}
	return dcgm.Init(dcgm.Standalone, hostengine, "0")
}

// queryCacheCounts counts cached observations for selected entities while releasing its temporary groups.
func queryCacheCounts(fieldIDs []dcgm.Short) (report dcgmprobe.CacheCounts, resultErr error) {
	gpuIDs, err := dcgm.GetSupportedDevices()
	if err != nil {
		return report, probeStage("GPU discovery", err)
	}
	if len(gpuIDs) == 0 {
		return report, errors.New("DCGM cache query found no supported GPUs")
	}

	hierarchy, err := dcgm.GetGPUInstanceHierarchy()
	if err != nil {
		return report, probeStage("MIG hierarchy discovery", err)
	}
	entities := cacheEntities(gpuIDs, hierarchy, fieldIDs...)

	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	entityGroup, err := dcgm.CreateGroup("dcgm-exporter-e2e-cache-entities-" + suffix)
	if err != nil {
		return report, probeStage("entity group creation", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, probeStage("entity group destruction", dcgm.DestroyGroup(entityGroup)))
	}()
	if err := addCacheEntitiesToGroup(entityGroup, entities, dcgm.AddEntityToGroup); err != nil {
		return report, probeStage("entity group population", err)
	}

	fieldGroup, err := dcgm.FieldGroupCreate("dcgm-exporter-e2e-cache-fields-"+suffix, fieldIDs)
	if err != nil {
		return report, probeStage("field group creation", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, probeStage("field group destruction", dcgm.FieldGroupDestroy(fieldGroup)))
	}()

	values, _, err := dcgm.GetValuesSince(entityGroup, fieldGroup, time.Time{})
	if err != nil {
		return report, probeStage("cached field query", err)
	}
	report = aggregateCacheCounts(entities, fieldIDs, values)
	if err := populateCacheEntityLabels(&report, entities, &hierarchy); err != nil {
		return report, err
	}
	return report, nil
}

// populateCacheEntityLabels records exporter identities, resolving instances to their parent GPUs.
// Labels already collected remain in the report if a later identity query fails.
func populateCacheEntityLabels(
	report *dcgmprobe.CacheCounts,
	entities []dcgm.GroupEntityPair,
	hierarchy *dcgm.MigHierarchy_v2,
) error {
	report.EntityLabels = make(map[string]map[string]string, len(entities))
	for _, entity := range entities {
		gpuID := entity.EntityId
		labels := map[string]string{}
		if entity.EntityGroupId == dcgm.FE_GPU_I {
			for index := uint(0); index < hierarchy.Count; index++ {
				entry := hierarchy.EntityList[index]
				if entry.Entity == entity && entry.Parent.EntityGroupId == dcgm.FE_GPU {
					gpuID = entry.Parent.EntityId
					labels["GPU_I_ID"] = strconv.FormatUint(uint64(entry.Info.NvmlInstanceId), 10)
					break
				}
			}
		}
		device, err := dcgm.GetDeviceInfo(gpuID)
		if err != nil {
			return probeStage("GPU identity query", err)
		}
		labels["gpu"] = strconv.FormatUint(uint64(device.GPU), 10)
		labels["UUID"] = device.UUID
		report.EntityLabels[cacheEntityKey(entity)] = labels
	}
	return nil
}

// cacheEntities retains instance selection and includes parent GPUs when energy
// history is requested, so both whole-GPU and instance exposition can be checked.
func cacheEntities(gpuIDs []uint, hierarchy dcgm.MigHierarchy_v2, fieldIDs ...dcgm.Short) []dcgm.GroupEntityPair {
	instances := make(map[uint][]dcgm.GroupEntityPair)
	for index := uint(0); index < hierarchy.Count; index++ {
		entry := hierarchy.EntityList[index]
		if entry.Parent.EntityGroupId != dcgm.FE_GPU || entry.Entity.EntityGroupId != dcgm.FE_GPU_I {
			continue
		}
		instances[entry.Parent.EntityId] = append(instances[entry.Parent.EntityId], entry.Entity)
	}
	for gpuID := range instances {
		sort.Slice(instances[gpuID], func(i, j int) bool {
			return instances[gpuID][i].EntityId < instances[gpuID][j].EntityId
		})
	}

	sortedGPUs := append([]uint(nil), gpuIDs...)
	sort.Slice(sortedGPUs, func(i, j int) bool { return sortedGPUs[i] < sortedGPUs[j] })
	entities := make([]dcgm.GroupEntityPair, 0, len(sortedGPUs))
	includeParentEnergy := slices.Contains(fieldIDs, dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION) ||
		slices.Contains(fieldIDs, dcgm.DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL)
	for _, gpuID := range sortedGPUs {
		if len(instances[gpuID]) == 0 || includeParentEnergy {
			entities = append(entities, dcgm.GroupEntityPair{
				EntityGroupId: dcgm.FE_GPU,
				EntityId:      gpuID,
			})
		}
		entities = append(entities, instances[gpuID]...)
	}
	return entities
}

// addCacheEntitiesToGroup adds each selected entity and reports the identity whose registration failed.
func addCacheEntitiesToGroup(
	entityGroup dcgm.GroupHandle,
	entities []dcgm.GroupEntityPair,
	add func(dcgm.GroupHandle, dcgm.Field_Entity_Group, uint) error,
) error {
	for _, entity := range entities {
		if err := add(entityGroup, entity.EntityGroupId, entity.EntityId); err != nil {
			return fmt.Errorf("add entity %s to entity group: %w", cacheEntityKey(entity), err)
		}
	}
	return nil
}

// aggregateCacheCounts seeds zeroes and counts requested field/entity observations, excluding unrelated values.
func aggregateCacheCounts(
	entities []dcgm.GroupEntityPair,
	fieldIDs []dcgm.Short,
	values []dcgm.FieldValue_v2,
) dcgmprobe.CacheCounts {
	report := dcgmprobe.CacheCounts{
		Version:  dcgmprobe.CurrentCacheCountsVersion,
		Entities: make(map[string]map[string]int, len(entities)),
	}
	requestedEntities := make(map[dcgm.GroupEntityPair]string, len(entities))
	requestedFields := make(map[dcgm.Short]struct{}, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		requestedFields[fieldID] = struct{}{}
	}
	for _, entity := range entities {
		key := cacheEntityKey(entity)
		requestedEntities[entity] = key
		fields := make(map[string]int, len(fieldIDs))
		for _, fieldID := range fieldIDs {
			fields[strconv.FormatUint(uint64(fieldID), 10)] = 0
		}
		report.Entities[key] = fields
	}
	for _, value := range values {
		entity := dcgm.GroupEntityPair{
			EntityGroupId: value.EntityGroupId,
			EntityId:      value.EntityID,
		}
		key, ok := requestedEntities[entity]
		if !ok {
			continue
		}
		if _, ok := requestedFields[value.FieldID]; !ok {
			continue
		}
		field := strconv.FormatUint(uint64(value.FieldID), 10)
		report.Entities[key][field]++
		// Counts include every observation; freshness comparisons use only
		// typed integer samples and retain native status and sample time.
		if value.FieldType == dcgm.DCGM_FT_INT64 {
			report.History = append(report.History, dcgmprobe.HistorySample{
				Entity:        dcgmprobe.Entity{Group: uint(value.EntityGroupId), ID: value.EntityID},
				FieldID:       uint(value.FieldID),
				FieldType:     value.FieldType,
				Status:        value.Status,
				TimestampUsec: value.TS,
				Value:         value.Int64(),
			})
		}
	}
	return report
}

// cacheEntityKey keeps entity group and ID together so GPU and MIG IDs cannot collide.
func cacheEntityKey(entity dcgm.GroupEntityPair) string {
	return fmt.Sprintf("%d:%d", entity.EntityGroupId, entity.EntityId)
}
