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

// Command e2e-dcgm-probe reports the numeric field/entity contract exposed by
// an injected DCGM instance. Its JSON output is temporary test input and must
// never be published as a CI artifact.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvmlinjection"
)

const (
	watchFrequencyUsec = 100_000
)

type candidateField struct {
	ID        dcgm.Short
	Name      string
	Profiling bool
}

type monitoredEntity struct {
	Pair   dcgm.GroupEntityPair
	Labels map[string]string
}

type probeStageError struct {
	stage string
	err   error
}

func (e probeStageError) Error() string { return "DCGM probe failed during " + e.stage }

func (e probeStageError) Unwrap() error { return e.err }

func probeStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	var staged probeStageError
	if errors.As(err, &staged) {
		return err
	}
	return probeStageError{stage: stage, err: err}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("e2e-dcgm-probe", flag.ContinueOnError)
	fieldsSource := flags.String("field-names", "", "path to go-dcgm const_fields.go")
	outputPath := flags.String("output", "", "private path for the temporary JSON contract")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*fieldsSource) == "" {
		return errors.New("--field-names is required")
	}
	if strings.TrimSpace(*outputPath) == "" {
		return errors.New("--output is required")
	}

	names, err := fieldNames(*fieldsSource)
	if err != nil {
		return err
	}
	cleanup, err := dcgm.Init(dcgm.Embedded)
	if err != nil {
		return probeStage("DCGM initialization", err)
	}
	defer cleanup()

	entities, deviceCount, err := monitoredEntities()
	if err != nil {
		return probeStage("entity discovery", err)
	}
	contract := nvmlinjection.Contract{
		Version:     nvmlinjection.CurrentVersion,
		DeviceCount: deviceCount,
	}
	if deviceCount == 0 {
		return writeContract(*outputPath, contract)
	}

	fields, unmapped := numericGPUFields(names)
	if len(unmapped) != 0 {
		return fmt.Errorf("numeric GPU field IDs missing from go-dcgm field names: %v", unmapped)
	}
	metrics, unavailable, err := probeFields(fields, entities)
	if err != nil {
		return probeStage("field availability discovery", err)
	}
	contract.Metrics = metrics
	contract.Unavailable = unavailable
	if err := contract.Validate(); err != nil {
		return err
	}
	return writeContract(*outputPath, contract)
}

func writeContract(path string, contract nvmlinjection.Contract) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create private contract file: %w", err)
	}
	if err := json.NewEncoder(file).Encode(contract); err != nil {
		_ = file.Close()
		return fmt.Errorf("write private contract file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close private contract file: %w", err)
	}
	return nil
}

func fieldNames(path string) (map[dcgm.Short]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse go-dcgm field names: %w", err)
	}
	names := map[dcgm.Short]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		value, ok := node.(*ast.ValueSpec)
		if !ok || len(value.Names) != 1 || len(value.Values) != 1 || value.Names[0].Name != "dcgmFields" {
			return true
		}
		literal, ok := value.Values[0].(*ast.CompositeLit)
		if !ok {
			return false
		}
		for _, element := range literal.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, keyOK := pair.Key.(*ast.BasicLit)
			value, valueOK := pair.Value.(*ast.BasicLit)
			if !keyOK || !valueOK {
				continue
			}
			name, unquoteErr := strconv.Unquote(key.Value)
			id, parseErr := strconv.ParseUint(value.Value, 10, 16)
			if unquoteErr == nil && parseErr == nil && strings.HasPrefix(name, "DCGM_FI_") {
				names[dcgm.Short(id)] = name
			}
		}
		return false
	})
	if len(names) == 0 {
		return nil, errors.New("dcgmFields map was not found")
	}
	return names, nil
}

func numericGPUFields(names map[dcgm.Short]string) ([]candidateField, []int) {
	metadata := dcgm.GetAllSupportedFieldsMetadata()
	fields := make([]candidateField, 0, len(metadata))
	var unmapped []int
	for id, field := range metadata {
		if field.EntityLevel != dcgm.FE_GPU {
			continue
		}
		if uint(field.FieldType) != dcgm.DCGM_FT_INT64 && uint(field.FieldType) != dcgm.DCGM_FT_DOUBLE {
			continue
		}
		name := names[id]
		if name == "" {
			unmapped = append(unmapped, int(id))
			continue
		}
		fields = append(fields, candidateField{ID: id, Name: name, Profiling: strings.HasPrefix(name, "DCGM_FI_PROF_")})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].ID < fields[j].ID })
	sort.Ints(unmapped)
	return fields, unmapped
}

func monitoredEntities() ([]monitoredEntity, int, error) {
	gpuIDs, err := dcgm.GetSupportedDevices()
	if err != nil {
		return nil, 0, fmt.Errorf("get supported GPUs: %w", err)
	}
	if len(gpuIDs) == 0 {
		return nil, 0, nil
	}
	sort.Slice(gpuIDs, func(i, j int) bool { return gpuIDs[i] < gpuIDs[j] })
	devices := make(map[uint]dcgm.Device, len(gpuIDs))
	for _, gpu := range gpuIDs {
		device, err := dcgm.GetDeviceInfo(gpu)
		if err != nil {
			return nil, 0, fmt.Errorf("get GPU %d identity: %w", gpu, err)
		}
		devices[gpu] = device
	}

	hierarchy, err := dcgm.GetGPUInstanceHierarchy()
	instances := map[uint][]dcgm.MigHierarchyInfo_v2{}
	// Non-MIG injection fixtures do not necessarily inject the hierarchy API.
	// In that case DCGM's physical GPU list remains the authoritative entity set.
	if err == nil {
		for index := uint(0); index < hierarchy.Count; index++ {
			entry := hierarchy.EntityList[index]
			if entry.Parent.EntityGroupId == dcgm.FE_GPU && entry.Entity.EntityGroupId == dcgm.FE_GPU_I {
				instances[entry.Parent.EntityId] = append(instances[entry.Parent.EntityId], entry)
			}
		}
	}
	for gpu := range instances {
		sort.Slice(instances[gpu], func(i, j int) bool {
			return instances[gpu][i].Entity.EntityId < instances[gpu][j].Entity.EntityId
		})
	}

	hostname, err := os.Hostname()
	if err != nil {
		return nil, 0, fmt.Errorf("get hostname: %w", err)
	}
	var result []monitoredEntity
	for _, gpu := range gpuIDs {
		device := devices[gpu]
		base := map[string]string{
			"gpu":        strconv.FormatUint(uint64(device.GPU), 10),
			"UUID":       device.UUID,
			"pci_bus_id": device.PCI.BusID,
			"device":     fmt.Sprintf("nvidia%d", device.GPU),
			"modelName":  device.Identifiers.Model,
			"hostname":   hostname,
		}
		if len(instances[gpu]) == 0 {
			result = append(result, monitoredEntity{Pair: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: device.GPU}, Labels: base})
			continue
		}
		for _, instance := range instances[gpu] {
			profile, err := gpuInstanceProfile(instance.Entity.EntityId)
			if err != nil {
				return nil, 0, err
			}
			labels := cloneLabels(base)
			labels["GPU_I_ID"] = strconv.FormatUint(uint64(instance.Info.NvmlInstanceId), 10)
			labels["GPU_I_PROFILE"] = profile
			result = append(result, monitoredEntity{Pair: instance.Entity, Labels: labels})
		}
	}
	return result, len(gpuIDs), nil
}

func gpuInstanceProfile(entityID uint) (string, error) {
	values, err := dcgm.EntitiesGetLatestValues(
		[]dcgm.GroupEntityPair{{EntityGroupId: dcgm.FE_GPU_I, EntityId: entityID}},
		[]dcgm.Short{dcgm.DCGM_FI_DEV_NAME},
		dcgm.DCGM_FV_FLAG_LIVE_DATA,
	)
	if err != nil {
		return "", fmt.Errorf("get GPU-instance profile: %w", err)
	}
	if len(values) != 1 || values[0].Status != dcgm.DCGM_ST_OK {
		return "", errors.New("GPU-instance profile is unavailable")
	}
	profile := strings.TrimSpace(dcgm.Fv2_String(values[0]))
	if profile == "" {
		return "", errors.New("GPU-instance profile is empty")
	}
	return profile, nil
}

func cloneLabels(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+2)
	for name, value := range source {
		result[name] = value
	}
	return result
}

func probeFields(fields []candidateField, entities []monitoredEntity) ([]nvmlinjection.Metric, []nvmlinjection.Unavailable, error) {
	group, err := dcgm.CreateGroup("dcgm-exporter-e2e-oracle")
	if err != nil {
		return nil, nil, probeStage("entity group creation", err)
	}
	defer func() {
		_ = dcgm.DestroyGroup(group)
	}()
	for _, entity := range entities {
		if err := dcgm.AddEntityToGroup(group, entity.Pair.EntityGroupId, entity.Pair.EntityId); err != nil {
			return nil, nil, probeStage("entity group population", err)
		}
	}

	availability := make(map[dcgm.Short]map[string]int, len(fields))
	metrics := make(map[dcgm.Short]*nvmlinjection.Metric, len(fields))
	for _, field := range fields {
		availability[field.ID] = map[string]int{}
		metrics[field.ID] = &nvmlinjection.Metric{ID: int(field.ID), Name: field.Name, Profiling: field.Profiling}
	}
	var regular, profiling []candidateField
	for _, field := range fields {
		if field.Profiling {
			profiling = append(profiling, field)
		} else {
			regular = append(regular, field)
		}
	}
	if err := probeFieldSet(group, regular, nvmlinjection.RegularBatchSize, entities, metrics, availability); err != nil {
		return nil, nil, err
	}
	if err := probeFieldSet(group, profiling, nvmlinjection.ProfilingBatchSize, entities, metrics, availability); err != nil {
		return nil, nil, err
	}

	var available []nvmlinjection.Metric
	var unavailable []nvmlinjection.Unavailable
	for _, field := range fields {
		metric := metrics[field.ID]
		if len(metric.Samples) != 0 {
			available = append(available, *metric)
			continue
		}
		unavailable = append(unavailable, nvmlinjection.Unavailable{Name: field.Name, Reason: joinedReasons(availability[field.ID])})
	}
	return available, unavailable, nil
}

func probeFieldSet(
	group dcgm.GroupHandle,
	fields []candidateField,
	batchSize int,
	entities []monitoredEntity,
	metrics map[dcgm.Short]*nvmlinjection.Metric,
	availability map[dcgm.Short]map[string]int,
) error {
	for first := 0; first < len(fields); first += batchSize {
		last := min(first+batchSize, len(fields))
		if err := probeFieldBatch(group, fields[first:last], entities, metrics, availability); err != nil {
			return err
		}
	}
	return nil
}

func probeFieldBatch(
	group dcgm.GroupHandle,
	fields []candidateField,
	entities []monitoredEntity,
	metrics map[dcgm.Short]*nvmlinjection.Metric,
	availability map[dcgm.Short]map[string]int,
) error {
	ids := make([]dcgm.Short, 0, len(fields))
	for _, field := range fields {
		ids = append(ids, field.ID)
	}
	fieldGroup, err := dcgm.FieldGroupCreate(fmt.Sprintf("dcgm-exporter-e2e-oracle-%d-%d", fields[0].ID, fields[len(fields)-1].ID), ids)
	if err != nil {
		return probeStage("field group creation", err)
	}
	if err := dcgm.WatchFieldsWithGroupEx(fieldGroup, group, watchFrequencyUsec, 60, 0); err != nil {
		_ = dcgm.FieldGroupDestroy(fieldGroup)
		reason := watchFailureReason(err)
		switch {
		case reason == "profiling-not-supported":
			for _, field := range fields {
				availability[field.ID][reason] += len(entities)
			}
			return nil
		case reason == "module-not-loaded" && len(fields) == 1:
			availability[fields[0].ID][reason] += len(entities)
			return nil
		case reason == "module-not-loaded":
			middle := len(fields) / 2
			if err := probeFieldBatch(group, fields[:middle], entities, metrics, availability); err != nil {
				return err
			}
			return probeFieldBatch(group, fields[middle:], entities, metrics, availability)
		default:
			return probeStage("field watch", err)
		}
	}

	// The watch runs at 100 ms. Waiting for two intervals gives GPM fields
	// their required baseline and current sample before the one oracle read.
	time.Sleep(2 * watchFrequencyUsec * time.Microsecond)
	if err := dcgm.UpdateAllFields(); err != nil {
		_ = dcgm.UnwatchFields(fieldGroup, group)
		_ = dcgm.FieldGroupDestroy(fieldGroup)
		return probeStage("field update", err)
	}
	for _, entity := range entities {
		values, err := dcgm.EntityGetLatestValues(entity.Pair.EntityGroupId, entity.Pair.EntityId, ids)
		if err != nil {
			_ = dcgm.UnwatchFields(fieldGroup, group)
			_ = dcgm.FieldGroupDestroy(fieldGroup)
			return probeStage("field read", err)
		}
		for _, value := range values {
			reason := availabilityReason(value)
			if reason == "available" {
				metrics[value.FieldID].Samples = append(metrics[value.FieldID].Samples, nvmlinjection.Sample{
					EntityGroup: entity.Pair.EntityGroupId.String(),
					EntityID:    entity.Pair.EntityId,
					Labels:      entity.Labels,
				})
			} else {
				availability[value.FieldID][reason]++
			}
		}
	}
	if err := dcgm.UnwatchFields(fieldGroup, group); err != nil {
		_ = dcgm.FieldGroupDestroy(fieldGroup)
		return probeStage("field unwatch", err)
	}
	if err := dcgm.FieldGroupDestroy(fieldGroup); err != nil {
		return probeStage("field group destruction", err)
	}
	return nil
}

func watchFailureReason(err error) string {
	switch message := err.Error(); {
	case strings.Contains(message, "Profiling is not supported"):
		return "profiling-not-supported"
	case strings.Contains(message, "module of DCGM that is not currently loaded"):
		return "module-not-loaded"
	default:
		return ""
	}
}

func availabilityReason(value dcgm.FieldValue_v1) string {
	if value.Status != dcgm.DCGM_ST_OK {
		switch value.Status {
		case dcgm.DCGM_ST_NO_DATA:
			return "no-data"
		case dcgm.DCGM_ST_NOT_SUPPORTED:
			return "not-supported"
		case dcgm.DCGM_ST_NO_PERMISSION:
			return "not-permissioned"
		default:
			return fmt.Sprintf("dcgm-status-%d", value.Status)
		}
	}
	if value.FieldType == dcgm.DCGM_FT_INT64 {
		switch value.Int64() {
		case dcgm.DCGM_FT_INT32_BLANK, dcgm.DCGM_FT_INT64_BLANK:
			return "blank"
		case dcgm.DCGM_FT_INT32_NOT_FOUND, dcgm.DCGM_FT_INT64_NOT_FOUND:
			return "not-found"
		case dcgm.DCGM_FT_INT32_NOT_SUPPORTED, dcgm.DCGM_FT_INT64_NOT_SUPPORTED:
			return "not-supported"
		case dcgm.DCGM_FT_INT32_NOT_PERMISSIONED, dcgm.DCGM_FT_INT64_NOT_PERMISSIONED:
			return "not-permissioned"
		}
	}
	if value.FieldType == dcgm.DCGM_FT_DOUBLE {
		switch value.Float64() {
		case dcgm.DCGM_FT_FP64_BLANK:
			return "blank"
		case dcgm.DCGM_FT_FP64_NOT_FOUND:
			return "not-found"
		case dcgm.DCGM_FT_FP64_NOT_SUPPORTED:
			return "not-supported"
		case dcgm.DCGM_FT_FP64_NOT_PERMISSIONED:
			return "not-permissioned"
		}
	}
	return "available"
}

func joinedReasons(reasons map[string]int) string {
	if len(reasons) == 0 {
		return "no-data"
	}
	names := make([]string, 0, len(reasons))
	for reason := range reasons {
		names = append(names, reason)
	}
	sort.Strings(names)
	return strings.Join(names, "+")
}
