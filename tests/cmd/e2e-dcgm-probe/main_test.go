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
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/clioutput"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvmlinjection"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvsdmmock"
)

// TestRunValidatesCommandsAndFlags checks unknown commands and required options fail before native work starts.
func TestRunValidatesCommandsAndFlags(t *testing.T) {
	// Each command-line case pins the validation error before any DCGM initialization.
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown", args: []string{"dcgm-contract"}, want: "unknown e2e-dcgm-probe command"},
		{name: "NVML flags", args: []string{"nvml-injection"}, want: "--field-names is required"},
		{name: "NVSDM flags", args: []string{"nvsdm-mock"}, want: "--field-names is required"},
		{name: "cache requirements", args: []string{"cache-counts"}, want: "--hostengine is required"},
		{
			name: "cache field requirement",
			args: []string{"cache-counts", "--hostengine", "localhost:5555"},
			want: "at least one --field-id is required",
		},
		{
			name: "cache output requirement",
			args: []string{"cache-counts", "--hostengine", "localhost:5555", "--field-id", "100"},
			want: "--output is required",
		},
		{
			name: "cache positional arguments",
			args: []string{"cache-counts", "--hostengine", "localhost:5555", "extra"},
			want: "unexpected positional arguments: extra",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run(%v) error = %v, want containing %q", test.args, err, test.want)
			}
		})
	}
}

// TestRunWithoutCommandShowsHelp checks an empty invocation returns help successfully.
func TestRunWithoutCommandShowsHelp(t *testing.T) {
	var stdout bytes.Buffer
	app := newApp(&stdout, io.Discard)
	if err := app.Run([]string{"e2e-dcgm-probe"}); err != nil {
		t.Fatalf("app.Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "e2e-dcgm-probe <command> [options]") {
		t.Fatalf("missing help output:\n%s", stdout.String())
	}
}

// TestProbeHelpUsesUrfaveCommandTree checks all probe commands appear in generated help.
func TestProbeHelpUsesUrfaveCommandTree(t *testing.T) {
	var stdout bytes.Buffer
	app := newApp(&stdout, io.Discard)
	if err := app.Run([]string{"e2e-dcgm-probe", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"e2e-dcgm-probe <command> [options]",
		"nvml-injection",
		"nvsdm-mock",
		"cache-counts",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help output missing %q:\n%s", want, stdout.String())
		}
	}
}

// TestProbeUsageErrorIsRenderedOnce checks a parser error is printed once without duplicate stderr output.
func TestProbeUsageErrorIsRenderedOnce(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := newApp(&stdout, &stderr).Run([]string{
		"e2e-dcgm-probe",
		"cache-counts",
		"--unknown",
	})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("Run() error = %v, want unknown flag", err)
	}
	clioutput.WriteError(&stderr, err)
	if got := strings.Count(stdout.String(), "flag provided but not defined"); got != 1 {
		t.Fatalf("parser error rendered %d times, want once:\n%s", got, stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// TestProbeActionErrorIsWrittenByEntrypoint checks action errors reach the entrypoint error writer.
func TestProbeActionErrorIsWrittenByEntrypoint(t *testing.T) {
	var stderr bytes.Buffer
	err := newApp(io.Discard, &stderr).Run([]string{"e2e-dcgm-probe", "cache-counts"})
	if err == nil {
		t.Fatal("Run() error = nil")
	}
	clioutput.WriteError(&stderr, err)
	if got := stderr.String(); got != "--hostengine is required\n" {
		t.Fatalf("stderr = %q", got)
	}
}

// TestFieldIDListRejectsInvalidAndDuplicateIDs checks field order, valid numeric range and duplicate rejection.
func TestFieldIDListRejectsInvalidAndDuplicateIDs(t *testing.T) {
	var ids fieldIDList
	if err := ids.Set("150"); err != nil {
		t.Fatal(err)
	}
	if err := ids.Set("100"); err != nil {
		t.Fatal(err)
	}
	if got := ids.String(); got != "150,100" {
		t.Fatalf("String() = %q", got)
	}
	for _, value := range []string{"150", "0", "-1", "65536", "not-an-id"} {
		if err := ids.Set(value); err == nil {
			t.Fatalf("Set(%q) error = nil", value)
		}
	}
}

// TestCacheEntitiesUsesGPUInstancesWhenPresent checks MIG GPUs use sorted instances and other GPUs retain whole-GPU entities.
func TestCacheEntitiesUsesGPUInstancesWhenPresent(t *testing.T) {
	hierarchy := dcgm.MigHierarchy_v2{Count: 3}
	hierarchy.EntityList[0] = dcgm.MigHierarchyInfo_v2{
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7},
	}
	hierarchy.EntityList[1] = dcgm.MigHierarchyInfo_v2{
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 2},
	}
	hierarchy.EntityList[2] = dcgm.MigHierarchyInfo_v2{
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 2},
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 11},
	}

	want := []dcgm.GroupEntityPair{
		{EntityGroupId: dcgm.FE_GPU_I, EntityId: 2},
		{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7},
		{EntityGroupId: dcgm.FE_GPU, EntityId: 3},
	}
	withParent := append([]dcgm.GroupEntityPair{{EntityGroupId: dcgm.FE_GPU, EntityId: 0}}, want...)
	for _, testCase := range []struct {
		name   string
		fields []dcgm.Short
		want   []dcgm.GroupEntityPair
	}{
		{name: "existing selection", want: want},
		{name: "retention", fields: []dcgm.Short{100, 150}, want: want},
		{name: "energy", fields: []dcgm.Short{dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION}, want: withParent},
		{name: "energy in joules", fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL}, want: withParent},
		{name: "mixed fields", fields: []dcgm.Short{100, dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION}, want: withParent},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := cacheEntities([]uint{3, 0}, hierarchy, testCase.fields...)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("cacheEntities(fields=%v) = %#v, want %#v", testCase.fields, got, testCase.want)
			}
		})
	}
}

// TestAggregateCacheCountsSeedsEveryEntityAndRequestedField checks sampled counts, zeroes and exclusion of unrequested values.
func TestAggregateCacheCountsSeedsEveryEntityAndRequestedField(t *testing.T) {
	entities := []dcgm.GroupEntityPair{
		{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		{EntityGroupId: dcgm.FE_GPU_I, EntityId: 0},
	}
	values := []dcgm.FieldValue_v2{
		{EntityGroupId: dcgm.FE_GPU, EntityID: 0, FieldID: 100, FieldType: dcgm.DCGM_FT_INT64, TS: 123, Value: [4096]byte{42}},
		{EntityGroupId: dcgm.FE_GPU, EntityID: 0, FieldID: 100, FieldType: dcgm.DCGM_FT_INT64, Status: 99, TS: 124, Value: [4096]byte{7}},
		{EntityGroupId: dcgm.FE_GPU, EntityID: 0, FieldID: 150},
		{EntityGroupId: dcgm.FE_GPU_I, EntityID: 0, FieldID: 100},
		{EntityGroupId: dcgm.FE_GPU, EntityID: 9, FieldID: 100},
		{EntityGroupId: dcgm.FE_GPU, EntityID: 0, FieldID: 999},
	}
	got := aggregateCacheCounts(
		entities,
		[]dcgm.Short{100, 150},
		values,
	)
	want := dcgmprobe.CacheCounts{
		Version: dcgmprobe.CurrentCacheCountsVersion,
		Entities: map[string]map[string]int{
			cacheEntityKey(entities[0]): {"100": 2, "150": 1},
			cacheEntityKey(entities[1]): {"100": 1, "150": 0},
		},
		History: []dcgmprobe.HistorySample{
			{Entity: dcgmprobe.Entity{Group: uint(dcgm.FE_GPU), ID: 0}, FieldID: 100, FieldType: dcgm.DCGM_FT_INT64, TimestampUsec: 123, Value: 42},
			{Entity: dcgmprobe.Entity{Group: uint(dcgm.FE_GPU), ID: 0}, FieldID: 100, FieldType: dcgm.DCGM_FT_INT64, Status: 99, TimestampUsec: 124, Value: 7},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("aggregateCacheCounts() = %#v, want %#v", got, want)
	}
}

// TestAddCacheEntitiesToGroupIncludesFailingEntity checks the first failed registration wraps its identity and stops later additions.
func TestAddCacheEntitiesToGroupIncludesFailingEntity(t *testing.T) {
	sentinel := errors.New("add failed")
	entities := []dcgm.GroupEntityPair{
		{EntityGroupId: dcgm.FE_GPU, EntityId: 2},
		{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7},
		{EntityGroupId: dcgm.FE_GPU_I, EntityId: 9},
	}
	var added []dcgm.GroupEntityPair
	err := addCacheEntitiesToGroup(
		dcgm.GroupHandle{},
		entities,
		// Record attempted registrations and fail the second selected entity.
		func(_ dcgm.GroupHandle, entityGroupID dcgm.Field_Entity_Group, entityID uint) error {
			added = append(added, dcgm.GroupEntityPair{EntityGroupId: entityGroupID, EntityId: entityID})
			if entityID == 7 {
				return sentinel
			}
			return nil
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("addCacheEntitiesToGroup() error = %v, want wrapping %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), cacheEntityKey(entities[1])) {
		t.Fatalf("addCacheEntitiesToGroup() error = %q, want failing entity key", err)
	}
	if want := entities[:2]; !reflect.DeepEqual(added, want) {
		t.Fatalf("added entities = %v, want %v", added, want)
	}
}

// TestWritePrivateJSONUsesPrivateExclusiveFile checks private permissions and rejection of overwriting an existing report.
func TestWritePrivateJSONUsesPrivateExclusiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.json")
	if err := writePrivateJSON(path, map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output permissions = %04o, want 0600", got)
	}
	if err := writePrivateJSON(path, map[string]int{"version": 2}); err == nil {
		t.Fatal("second write unexpectedly replaced the existing output")
	}
}

// TestWritePrivateJSONRemovesOutputAfterWriteFailure checks failed encoding removes output and permits a later write.
func TestWritePrivateJSONRemovesOutputAfterWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.json")
	if err := writePrivateJSON(path, make(chan int)); err == nil {
		t.Fatal("writePrivateJSON() error = nil for unsupported JSON value")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output remains after write failure: %v", err)
	}
	if err := writePrivateJSON(path, map[string]int{"version": 1}); err != nil {
		t.Fatalf("retry writePrivateJSON() error = %v", err)
	}
}

// TestFieldNamesReadsCurrentMap checks the source parser reads the current go-dcgm field map.
func TestFieldNamesReadsCurrentMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "const_fields.go")
	content := "package dcgm\nvar dcgmFields = map[string]Short{\"DCGM_FI_DEV_GPU_TEMP\": 150}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	names, err := fieldNames(path)
	if err != nil {
		t.Fatal(err)
	}
	if names[dcgm.Short(150)] != "DCGM_FI_DEV_GPU_TEMP" {
		t.Fatalf("field name = %q", names[dcgm.Short(150)])
	}
}

// TestJoinedReasonsIsStable checks availability reasons are sorted in report output.
func TestJoinedReasonsIsStable(t *testing.T) {
	got := joinedReasons(map[string]int{"not-supported": 1, "blank": 2})
	if got != "blank+not-supported" {
		t.Fatalf("joinedReasons() = %q", got)
	}
}

// TestNumericEntityFieldsUsesMetadataEntityAndNumericType checks entity/type selection, unmapped IDs and profiling identification.
func TestNumericEntityFieldsUsesMetadataEntityAndNumericType(t *testing.T) {
	names := map[dcgm.Short]string{
		11: "DCGM_FI_DEV_SWITCH_INT",
		12: "DCGM_FI_DEV_SWITCH_DOUBLE",
		13: "DCGM_FI_DEV_SWITCH_STRING",
		14: "DCGM_FI_DEV_LINK_INT",
		16: "DCGM_FI_DEV_GPU_CI_INT",
		17: "DCGM_FI_PROF_GPU_CI_DOUBLE",
	}
	metadata := map[dcgm.Short]dcgm.FieldMeta{
		11: {FieldType: byte(dcgm.DCGM_FT_INT64), EntityLevel: dcgm.FE_SWITCH},
		12: {FieldType: byte(dcgm.DCGM_FT_DOUBLE), EntityLevel: dcgm.FE_SWITCH},
		13: {FieldType: byte(dcgm.DCGM_FT_STRING), EntityLevel: dcgm.FE_SWITCH},
		14: {FieldType: byte(dcgm.DCGM_FT_INT64), EntityLevel: dcgm.FE_LINK},
		15: {FieldType: byte(dcgm.DCGM_FT_INT64), EntityLevel: dcgm.FE_SWITCH},
		16: {FieldType: byte(dcgm.DCGM_FT_INT64), EntityLevel: dcgm.FE_GPU_CI},
		17: {FieldType: byte(dcgm.DCGM_FT_DOUBLE), EntityLevel: dcgm.FE_GPU_CI},
	}

	fields, unmapped := numericEntityFields(names, metadata, dcgm.FE_SWITCH)
	if len(fields) != 2 || fields[0].ID != 11 || fields[1].ID != 12 {
		t.Fatalf("fields = %#v", fields)
	}
	if len(unmapped) != 1 || unmapped[0] != 15 {
		t.Fatalf("unmapped = %#v", unmapped)
	}
	linkFields, linkUnmapped := numericEntityFields(names, metadata, dcgm.FE_LINK)
	if len(linkFields) != 1 || linkFields[0].ID != 14 || len(linkUnmapped) != 0 {
		t.Fatalf("link fields = %#v, unmapped = %#v", linkFields, linkUnmapped)
	}
	computeInstanceFields, computeInstanceUnmapped := numericEntityFields(names, metadata, dcgm.FE_GPU_CI)
	if len(computeInstanceFields) != 2 || computeInstanceFields[0].ID != 16 || computeInstanceFields[0].Profiling ||
		computeInstanceFields[1].ID != 17 || !computeInstanceFields[1].Profiling || len(computeInstanceUnmapped) != 0 {
		t.Fatalf("compute-instance fields = %#v, unmapped = %#v", computeInstanceFields, computeInstanceUnmapped)
	}
}

// TestSplitMultiScopeFieldsSeparatesGPUXID checks XID alone enters the multi-scope probe set.
func TestSplitMultiScopeFieldsSeparatesGPUXID(t *testing.T) {
	fields, _ := numericGPUFields(map[dcgm.Short]string{
		dcgm.DCGM_FI_DEV_GPU_TEMP:   "DCGM_FI_DEV_GPU_TEMP",
		dcgm.DCGM_FI_DEV_XID_ERRORS: "DCGM_FI_DEV_XID_ERRORS",
	}, map[dcgm.Short]dcgm.FieldMeta{
		dcgm.DCGM_FI_DEV_GPU_TEMP: {
			FieldType:   byte(dcgm.DCGM_FT_DOUBLE),
			EntityLevel: dcgm.FE_GPU,
		},
		dcgm.DCGM_FI_DEV_XID_ERRORS: {
			FieldType:   byte(dcgm.DCGM_FT_INT64),
			EntityLevel: dcgm.FE_GPU,
		},
	})
	singleScope, multiScope := splitMultiScopeFields(fields)

	if len(singleScope) != 1 || singleScope[0].ID != dcgm.DCGM_FI_DEV_GPU_TEMP {
		t.Fatalf("single-scope fields = %#v", singleScope)
	}
	if len(multiScope) != 1 || multiScope[0].ID != dcgm.DCGM_FI_DEV_XID_ERRORS {
		t.Fatalf("multi-scope fields = %#v", multiScope)
	}
}

// TestScalarExportableFieldsExcludesFieldsNotSafeForGenericScalarExport checks generic scalar eligibility against the checked-in field contract.
func TestScalarExportableFieldsExcludesFieldsNotSafeForGenericScalarExport(t *testing.T) {
	fields := []candidateField{
		{ID: dcgm.DCGM_FI_DEV_GPU_TEMP, Name: "DCGM_FI_DEV_GPU_TEMP"},
		{ID: dcgm.DCGM_FI_CUDA_GPU_COMPUTE_CAPABILITY, Name: "DCGM_FI_CUDA_GPU_COMPUTE_CAPABILITY"},
		{ID: dcgm.DCGM_FI_DEV_NVLINK_P2P_STATUS, Name: "DCGM_FI_DEV_NVLINK_P2P_STATUS"},
		{ID: dcgm.DCGM_FI_DEV_MIG_MODE, Name: "DCGM_FI_DEV_MIG_MODE"},
		{ID: dcgm.DCGM_FI_DEV_CPU_AFFINITY_0, Name: "DCGM_FI_DEV_CPU_AFFINITY_0"},
		{ID: dcgm.DCGM_FI_DEV_MEMORY_AFFINITY_0, Name: "DCGM_FI_DEV_MEMORY_AFFINITY_0"},
		{ID: dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, Name: "DCGM_FI_DEV_CLOCKS_EVENT_REASONS"},
		{ID: dcgm.DCGM_FI_DEV_CLOCKS_SUPPORTED, Name: "DCGM_FI_DEV_CLOCKS_SUPPORTED"},
		{ID: dcgm.DCGM_FI_DEV_BOARD_POWER_PROFILE_REQUESTED_MASK, Name: "DCGM_FI_DEV_BOARD_POWER_PROFILE_REQUESTED_MASK"},
		{ID: dcgm.DCGM_FI_DEV_BOARD_POWER_PROFILE_ENFORCED_MASK, Name: "DCGM_FI_DEV_BOARD_POWER_PROFILE_ENFORCED_MASK"},
		{ID: dcgm.DCGM_FI_DEV_BOARD_POWER_PROFILE_SUPPORTED_MASK, Name: "DCGM_FI_DEV_BOARD_POWER_PROFILE_SUPPORTED_MASK"},
		{ID: dcgm.DCGM_FI_DEV_FABRIC_HEALTH_MASK, Name: "DCGM_FI_DEV_FABRIC_HEALTH_MASK"},
		{ID: dcgm.DCGM_FI_DEV_CONNECTX_CORRECTABLE_ERROR_MASK, Name: "DCGM_FI_DEV_CONNECTX_CORRECTABLE_ERROR_MASK"},
		{ID: dcgm.DCGM_FI_DEV_CONNECTX_UNCORRECTABLE_ERROR_MASK, Name: "DCGM_FI_DEV_CONNECTX_UNCORRECTABLE_ERROR_MASK"},
	}

	exportable, excluded := scalarExportableFields(fields)
	if len(exportable) != 1 || exportable[0].ID != dcgm.DCGM_FI_DEV_GPU_TEMP {
		t.Fatalf("exportable fields = %#v", exportable)
	}
	wantExcluded := []string{
		"DCGM_FI_CUDA_GPU_COMPUTE_CAPABILITY",
		"DCGM_FI_DEV_NVLINK_P2P_STATUS",
		"DCGM_FI_DEV_MIG_MODE",
		"DCGM_FI_DEV_CPU_AFFINITY_0",
		"DCGM_FI_DEV_MEMORY_AFFINITY_0",
		"DCGM_FI_DEV_CLOCKS_EVENT_REASONS",
		"DCGM_FI_DEV_CLOCKS_SUPPORTED",
		"DCGM_FI_DEV_BOARD_POWER_PROFILE_REQUESTED_MASK",
		"DCGM_FI_DEV_BOARD_POWER_PROFILE_ENFORCED_MASK",
		"DCGM_FI_DEV_BOARD_POWER_PROFILE_SUPPORTED_MASK",
		"DCGM_FI_DEV_FABRIC_HEALTH_MASK",
		"DCGM_FI_DEV_CONNECTX_CORRECTABLE_ERROR_MASK",
		"DCGM_FI_DEV_CONNECTX_UNCORRECTABLE_ERROR_MASK",
	}
	gotExcluded := make([]string, len(excluded))
	for i, field := range excluded {
		gotExcluded[i] = field.Name
		if field.Reason != "not-scalar-exportable" {
			t.Fatalf("excluded field = %#v", field)
		}
	}
	if !reflect.DeepEqual(gotExcluded, wantExcluded) {
		t.Fatalf("excluded fields = %#v, want %#v", gotExcluded, wantExcluded)
	}
}

// TestNVMLInjectionFieldsExcludeFieldsThatNeedTheNVLinkModule checks NVLink-dependent fields are excluded with the correct reason.
func TestNVMLInjectionFieldsExcludeFieldsThatNeedTheNVLinkModule(t *testing.T) {
	fields := []candidateField{
		{ID: dcgm.DCGM_FI_DEV_GPU_TEMP, Name: "DCGM_FI_DEV_GPU_TEMP"},
		{ID: dcgm.DCGM_FI_PROF_NVLINK_TX_BYTES, Name: "DCGM_FI_PROF_NVLINK_TX_BYTES"},
		{ID: dcgm.DCGM_FI_PROF_NVLINK_RX_BYTES, Name: "DCGM_FI_PROF_NVLINK_RX_BYTES"},
	}

	exportable, excluded := nvmlInjectionFields(fields)
	wantExportable := []candidateField{{ID: dcgm.DCGM_FI_DEV_GPU_TEMP, Name: "DCGM_FI_DEV_GPU_TEMP"}}
	wantExcluded := []nvmlinjection.Unavailable{
		{Name: "DCGM_FI_PROF_NVLINK_TX_BYTES", Reason: "requires-nvlink-module"},
		{Name: "DCGM_FI_PROF_NVLINK_RX_BYTES", Reason: "requires-nvlink-module"},
	}
	if !reflect.DeepEqual(exportable, wantExportable) {
		t.Fatalf("exportable fields = %#v, want %#v", exportable, wantExportable)
	}
	if !reflect.DeepEqual(excluded, wantExcluded) {
		t.Fatalf("excluded fields = %#v, want %#v", excluded, wantExcluded)
	}
}

// TestMultiScopeEntitiesIncludesGPUAndMIGScopes checks all three entity groups retain their order in multi-scope observations.
func TestMultiScopeEntitiesIncludesGPUAndMIGScopes(t *testing.T) {
	entities := multiScopeEntities(
		[]monitoredEntity{{Pair: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 1}}},
		[]monitoredEntity{{Pair: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 2}}},
		[]monitoredEntity{{Pair: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 3}}},
	)

	if len(entities) != 3 {
		t.Fatalf("multi-scope entities = %#v", entities)
	}
	for index, want := range []dcgm.Field_Entity_Group{dcgm.FE_GPU, dcgm.FE_GPU_I, dcgm.FE_GPU_CI} {
		if entities[index].Pair.EntityGroupId != want {
			t.Fatalf("multi-scope entities[%d] group = %v, want %v", index, entities[index].Pair.EntityGroupId, want)
		}
	}
}

// TestComputeInstanceFieldEntitiesIncludesWholeGPUsAndComputeInstances checks compute-instance fields exclude GPU-instance entities.
func TestComputeInstanceFieldEntitiesIncludesWholeGPUsAndComputeInstances(t *testing.T) {
	selected := []monitoredEntity{
		{Pair: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 1}},
		{Pair: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 2}},
	}
	computeInstances := []monitoredEntity{
		{Pair: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 3}},
	}

	got := computeInstanceFieldEntities(selected, computeInstances)

	want := []dcgm.GroupEntityPair{
		{EntityGroupId: dcgm.FE_GPU, EntityId: 1},
		{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("compute-instance field entities = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index].Pair != want[index] {
			t.Fatalf("compute-instance field entities[%d] = %v, want %v", index, got[index].Pair, want[index])
		}
	}
}

// TestGPULevelFieldEntitiesIncludesOnlyPhysicalGPUs checks GPU-level fields are
// probed on physical GPUs and never on GPU instances or compute instances.
func TestGPULevelFieldEntitiesIncludesOnlyPhysicalGPUs(t *testing.T) {
	gpu := func(id uint) monitoredEntity {
		return monitoredEntity{Pair: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: id}}
	}
	instance := monitoredEntity{Pair: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 2}}
	compute := monitoredEntity{Pair: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 3}}
	for _, tt := range []struct {
		name     string
		entities []monitoredEntity
		want     []dcgm.GroupEntityPair
	}{
		{
			name:     "MIG GPUs with instances",
			entities: multiScopeEntities([]monitoredEntity{gpu(0), gpu(1)}, []monitoredEntity{instance}, []monitoredEntity{compute}),
			want:     []dcgm.GroupEntityPair{gpu(0).Pair, gpu(1).Pair},
		},
		{
			name:     "GPUs without MIG",
			entities: multiScopeEntities([]monitoredEntity{gpu(0)}, nil, nil),
			want:     []dcgm.GroupEntityPair{gpu(0).Pair},
		},
		{
			name:     "instances only",
			entities: []monitoredEntity{instance, compute},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got []dcgm.GroupEntityPair
			for _, entity := range gpuLevelFieldEntities(tt.entities) {
				got = append(got, entity.Pair)
			}

			if !slices.Equal(got, tt.want) {
				t.Fatalf("GPU-level field entities = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNewNVMLInjectionContractRecordsComputeInstanceCount checks that the probe
// records both physical GPUs and compute instances in its fixture contract.
func TestNewNVMLInjectionContractRecordsComputeInstanceCount(t *testing.T) {
	contract := newNVMLInjectionContract(2, []monitoredEntity{{}, {}})
	if contract.Version != nvmlinjection.CurrentVersion {
		t.Fatalf("contract version = %d, want %d", contract.Version, nvmlinjection.CurrentVersion)
	}
	if contract.DeviceCount != 2 {
		t.Fatalf("contract device count = %d, want 2", contract.DeviceCount)
	}
	if contract.ComputeInstanceCount != 2 {
		t.Fatalf("contract compute-instance count = %d, want 2", contract.ComputeInstanceCount)
	}
}

// TestNVSDMEntitiesSelectsActiveSwitchOwnedLinks checks stable switch/link identities and rejects inactive or foreign links.
func TestNVSDMEntitiesSelectsActiveSwitchOwnedLinks(t *testing.T) {
	switches, links := nvsdmEntities([]uint{29, 17}, []dcgm.NvLinkStatus{
		{ParentId: 29, ParentType: dcgm.FE_SWITCH, State: dcgm.LS_UP, Index: 8},
		{ParentId: 17, ParentType: dcgm.FE_SWITCH, State: dcgm.LS_DOWN, Index: 3},
		{ParentId: 17, ParentType: dcgm.FE_SWITCH, State: dcgm.LS_UP, Index: 4},
		{ParentId: 17, ParentType: dcgm.FE_GPU, State: dcgm.LS_UP, Index: 5},
		{ParentId: 99, ParentType: dcgm.FE_SWITCH, State: dcgm.LS_UP, Index: 6},
	}, "probe-host")

	if len(switches) != 2 || switches[0].Pair.EntityId != 17 || switches[1].Pair.EntityId != 29 {
		t.Fatalf("switches = %#v", switches)
	}
	if switches[1].Labels["nvswitch"] != "nvswitch29" || switches[1].Labels["hostname"] != "probe-host" {
		t.Fatalf("switch labels = %#v", switches[1].Labels)
	}
	if len(links) != 2 {
		t.Fatalf("links = %#v", links)
	}
	if links[0].ParentID != 17 || links[0].ParentType != dcgm.FE_SWITCH || links[0].Pair.EntityId != 4 {
		t.Fatalf("first link identity = %#v", links[0])
	}
	if links[0].Labels["nvswitch"] != "nvswitch17" || links[0].Labels["nvlink"] != "4" {
		t.Fatalf("first link labels = %#v", links[0].Labels)
	}
	if links[1].ParentID != 29 || links[1].Pair.EntityId != 8 {
		t.Fatalf("second link identity = %#v", links[1])
	}
}

// TestSelectFieldsKeepsRequestedFields checks a nil filter keeps every field
// and a filter keeps only listed fields in their original order.
func TestSelectFieldsKeepsRequestedFields(t *testing.T) {
	fields := []candidateField{{ID: 865}, {ID: 787}, {ID: 870}}

	for _, test := range []struct {
		name       string
		onlyFields map[dcgm.Short]struct{}
		want       []candidateField
	}{
		{name: "nil filter", onlyFields: nil, want: fields},
		{name: "empty filter", onlyFields: map[dcgm.Short]struct{}{}, want: []candidateField{}},
		{name: "listed fields", onlyFields: map[dcgm.Short]struct{}{870: {}, 865: {}}, want: []candidateField{{ID: 865}, {ID: 870}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := selectFields(fields, test.onlyFields); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("selectFields() = %#v, want %#v", got, test.want)
			}
		})
	}
}

// TestNVSDMProbeContractUsesSecondPassSamples checks the contract takes
// metrics from the second pass and keeps unavailable fields from both passes.
func TestNVSDMProbeContractUsesSecondPassSamples(t *testing.T) {
	link := monitoredEntity{
		Pair:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: 4},
		ParentID:   17,
		ParentType: dcgm.FE_SWITCH,
		Labels:     map[string]string{"nvswitch": "nvswitch17", "nvlink": "4", "hostname": "probe-host"},
	}
	status := candidateField{ID: 870, Name: "DCGM_FI_DEV_NVSWITCH_LINK_STATUS"}
	crc := candidateField{ID: 787, Name: "DCGM_FI_DEV_NVSWITCH_LINK_CRC_ERROR_TOTAL"}
	remoteBus := candidateField{ID: 873, Name: "DCGM_FI_DEV_NVSWITCH_LINK_REMOTE_PCIE_BUS"}

	// The first pass sees a sample for two fields and none for the CRC field.
	first := nvsdmProbe{SwitchCount: 1, LinkCount: 1, MetricsByID: map[dcgm.Short]*nvsdmmock.Metric{}}
	first.add(nvsdmmock.EntityGroupLink,
		[]probedMetric{
			{Field: status, Samples: []probedSample{{Entity: link, Value: 1}}},
			{Field: remoteBus, Samples: []probedSample{{Entity: link, Value: 3}}},
		},
		[]probedUnavailable{{Field: crc, Reason: "no-data"}},
	)
	if got, want := first.sampledFieldIDs(), map[dcgm.Short]struct{}{870: {}, 873: {}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sampledFieldIDs() = %#v, want %#v", got, want)
	}

	// The second pass reads the status field again and loses the remote bus field.
	second := nvsdmProbe{SwitchCount: 1, LinkCount: 1, MetricsByID: map[dcgm.Short]*nvsdmmock.Metric{}}
	second.add(nvsdmmock.EntityGroupLink,
		[]probedMetric{{Field: status, Samples: []probedSample{{Entity: link, Value: 2}}}},
		[]probedUnavailable{{Field: remoteBus, Reason: "blank"}},
	)
	contract := second.contract(first.Unavailable)

	wantMetrics := []nvsdmmock.Metric{{
		ID:   870,
		Name: "DCGM_FI_DEV_NVSWITCH_LINK_STATUS",
		Type: "gauge",
		Samples: []nvsdmmock.Sample{{
			EntityGroup: nvsdmmock.EntityGroupLink,
			EntityID:    4,
			ParentID:    17,
			Labels:      link.Labels,
			Value:       2,
		}},
	}}
	if !reflect.DeepEqual(contract.Metrics, wantMetrics) {
		t.Fatalf("contract metrics = %#v, want %#v", contract.Metrics, wantMetrics)
	}
	wantUnavailable := []nvsdmmock.Unavailable{
		{Name: crc.Name, EntityGroup: nvsdmmock.EntityGroupLink, Reason: "no-data"},
		{Name: remoteBus.Name, EntityGroup: nvsdmmock.EntityGroupLink, Reason: "blank"},
	}
	if !reflect.DeepEqual(contract.Unavailable, wantUnavailable) {
		t.Fatalf("contract unavailable = %#v, want %#v", contract.Unavailable, wantUnavailable)
	}
	if contract.Version != nvsdmmock.CurrentVersion || contract.SwitchCount != 1 || contract.LinkCount != 1 {
		t.Fatalf("contract inventory = %+v", contract)
	}
	if err := contract.Validate(); err != nil {
		t.Fatalf("contract.Validate() = %v", err)
	}
}

// TestAvailabilityReasonReportsUnavailableFields checks unavailable native statuses map to report reasons.
func TestAvailabilityReasonReportsUnavailableFields(t *testing.T) {
	// Native status codes map to stable report reason strings.
	for _, test := range []struct {
		status int
		want   string
	}{
		{status: dcgm.DCGM_ST_NO_DATA, want: "no-data"},
		{status: dcgm.DCGM_ST_NOT_SUPPORTED, want: "not-supported"},
		{status: dcgm.DCGM_ST_NO_PERMISSION, want: "not-permissioned"},
	} {
		if got := availabilityReason(dcgm.FieldValue_v1{Status: test.status}); got != test.want {
			t.Fatalf("availabilityReason(status %d) = %q, want %q", test.status, got, test.want)
		}
	}
}

// TestExporterFloatValueMatchesPublicMetricPrecision checks fixed six-decimal conversion matches exported values.
func TestExporterFloatValueMatchesPublicMetricPrecision(t *testing.T) {
	// Scalar examples cover underflow-to-zero, rounding and unchanged whole values.
	for _, test := range []struct {
		name  string
		value float64
		want  float64
	}{
		{name: "tiny value rounds to zero", value: 1.5e-254, want: 0},
		{name: "fraction rounds to six decimals", value: 1.23456789, want: 1.234568},
		{name: "whole value is unchanged", value: 42, want: 42},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := exporterFloatValue(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("exporterFloatValue(%v) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

// TestWatchFailureReason checks known watch failures and an unknown error fallback.
func TestWatchFailureReason(t *testing.T) {
	// Watch-error examples pin known classifications and the unknown fallback.
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "profiling", err: errors.New("Profiling is not supported for this group of GPUs"), want: "profiling-not-supported"},
		{name: "module", err: errors.New("This request is serviced by a module of DCGM that is not currently loaded"), want: "module-not-loaded"},
		{name: "unexpected", err: errors.New("another watch failure"), want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := watchFailureReason(test.err); got != test.want {
				t.Fatalf("watchFailureReason() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestProbeFieldBatchCleanupFailures checks failed cleanup cannot hide an original failure or report success.
func TestProbeFieldBatchCleanupFailures(t *testing.T) {
	watchErr := errors.New("error watching fields: watch failed")
	updateErr := errors.New("update failed")
	unwatchErr := errors.New("unwatch failed")
	destroyErr := errors.New("destroy failed")
	// Each case exercises the real batch path; nil entities avoid native reads.
	for _, test := range []struct {
		name       string
		watchErr   error
		updateErr  error
		unwatchErr error
		destroyErr error
		wantErrors []error
		wantCalls  []string
	}{
		{
			name:     "failed watch and destroy",
			watchErr: watchErr, destroyErr: destroyErr,
			wantErrors: []error{watchErr, destroyErr},
			wantCalls:  []string{"create", "watch", "destroy"},
		},
		{
			name:       "failed update inside watch with successful cleanup",
			watchErr:   updateErr,
			wantErrors: []error{updateErr},
			wantCalls:  []string{"create", "watch", "unwatch", "destroy"},
		},
		{
			name:     "failed update inside watch and both releases",
			watchErr: updateErr, unwatchErr: unwatchErr, destroyErr: destroyErr,
			wantErrors: []error{updateErr, unwatchErr, destroyErr},
			wantCalls:  []string{"create", "watch", "unwatch", "destroy"},
		},
		{
			name:      "failed update and both releases",
			updateErr: updateErr, unwatchErr: unwatchErr, destroyErr: destroyErr,
			wantErrors: []error{updateErr, unwatchErr, destroyErr},
			wantCalls:  []string{"create", "watch", "update", "unwatch", "destroy"},
		},
		{
			name:       "successful probe with failed unwatch",
			unwatchErr: unwatchErr,
			wantErrors: []error{unwatchErr},
			wantCalls:  []string{"create", "watch", "update", "unwatch", "destroy"},
		},
		{
			name:       "successful probe with failed destruction",
			destroyErr: destroyErr,
			wantErrors: []error{destroyErr},
			wantCalls:  []string{"create", "watch", "update", "unwatch", "destroy"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			create, destroy := probeCreateFieldGroup, probeDestroyFieldGroup
			watch, unwatch, update := probeWatchFields, probeUnwatchFields, probeUpdateFields
			t.Cleanup(func() {
				probeCreateFieldGroup, probeDestroyFieldGroup = create, destroy
				probeWatchFields, probeUnwatchFields, probeUpdateFields = watch, unwatch, update
			})
			var calls []string
			probeCreateFieldGroup = func(string, []dcgm.Short) (dcgm.FieldHandle, error) {
				calls = append(calls, "create")
				return dcgm.FieldHandle{}, nil
			}
			probeWatchFields = func(dcgm.FieldHandle, dcgm.GroupHandle, int64, float64, int32) error {
				calls = append(calls, "watch")
				return test.watchErr
			}
			probeUpdateFields = func() error {
				calls = append(calls, "update")
				return test.updateErr
			}
			probeUnwatchFields = func(dcgm.FieldHandle, dcgm.GroupHandle) error {
				calls = append(calls, "unwatch")
				return test.unwatchErr
			}
			probeDestroyFieldGroup = func(dcgm.FieldHandle) error {
				calls = append(calls, "destroy")
				return test.destroyErr
			}
			fields := []candidateField{{ID: 100}, {ID: 101}}
			err := probeFieldBatch(dcgm.GroupHandle{}, fields, nil, nil, nil)
			for _, want := range test.wantErrors {
				if !errors.Is(err, want) {
					t.Errorf("probe error %v does not preserve %v", err, want)
				}
			}
			if test.watchErr == updateErr && !strings.HasPrefix(err.Error(), "DCGM probe failed during field update") {
				t.Errorf("initial update error stage = %v, want field update", err)
			}
			if !reflect.DeepEqual(calls, test.wantCalls) {
				t.Errorf("native calls = %v, want %v", calls, test.wantCalls)
			}
		})
	}
}

// TestProbeFieldBatchSplitsAfterCleanup checks replacement batches are created only after the failed watch group is released.
func TestProbeFieldBatchSplitsAfterCleanup(t *testing.T) {
	create, destroy, watch := probeCreateFieldGroup, probeDestroyFieldGroup, probeWatchFields
	t.Cleanup(func() {
		probeCreateFieldGroup, probeDestroyFieldGroup, probeWatchFields = create, destroy, watch
	})
	var calls []string
	probeCreateFieldGroup = func(_ string, ids []dcgm.Short) (dcgm.FieldHandle, error) {
		calls = append(calls, fmt.Sprintf("create:%v", ids))
		return dcgm.FieldHandle{}, nil
	}
	// All watches fail; single fields are marked unavailable after their own group is released.
	probeWatchFields = func(dcgm.FieldHandle, dcgm.GroupHandle, int64, float64, int32) error {
		calls = append(calls, "watch")
		return errors.New("error watching fields: unsupported field")
	}
	probeDestroyFieldGroup = func(dcgm.FieldHandle) error {
		calls = append(calls, "destroy")
		return nil
	}
	fields := []candidateField{{ID: 100}, {ID: 101}}
	availability := map[dcgm.Short]map[string]int{100: {}, 101: {}}
	entities := []monitoredEntity{{}}
	if err := probeFieldBatch(dcgm.GroupHandle{}, fields, entities, nil, availability); err != nil {
		t.Fatalf("probeFieldBatch() error = %v", err)
	}
	want := []string{"create:[100 101]", "watch", "destroy", "create:[100]", "watch", "destroy", "create:[101]", "watch", "destroy"}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("native calls = %v, want %v", calls, want)
	}
	for _, field := range fields {
		if availability[field.ID]["watch-failed"] != 1 {
			t.Errorf("field %d unavailable observations = %v, want one watch-failed", field.ID, availability[field.ID])
		}
	}
}

// TestProbeNumericFieldsPreservesPopulationAndCleanupFailures checks entity cleanup cannot hide an earlier setup failure.
func TestProbeNumericFieldsPreservesPopulationAndCleanupFailures(t *testing.T) {
	create, destroy, add := probeCreateEntityGroup, probeDestroyEntityGroup, probeAddEntity
	t.Cleanup(func() {
		probeCreateEntityGroup, probeDestroyEntityGroup, probeAddEntity = create, destroy, add
	})
	populationErr := errors.New("population failed")
	cleanupErr := errors.New("entity group destruction failed")
	var calls []string
	probeCreateEntityGroup = func(string) (dcgm.GroupHandle, error) {
		calls = append(calls, "create")
		return dcgm.GroupHandle{}, nil
	}
	probeAddEntity = func(dcgm.GroupHandle, monitoredEntity) error {
		calls = append(calls, "populate")
		return populationErr
	}
	probeDestroyEntityGroup = func(dcgm.GroupHandle) error {
		calls = append(calls, "destroy")
		return cleanupErr
	}
	metrics, unavailable, err := probeNumericFields([]candidateField{{ID: 100}}, []monitoredEntity{{}})
	if !errors.Is(err, populationErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("probe error %v must preserve population and cleanup failures", err)
	}
	if len(metrics) != 0 || len(unavailable) != 0 {
		t.Errorf("failed probe returned metrics %v or availability %v", metrics, unavailable)
	}
	if want := []string{"create", "populate", "destroy"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("native calls = %v, want %v", calls, want)
	}
}

// TestProbeNumericFieldsFailsOnEntityCleanupAfterSuccessfulProbe checks cleanup failure after result collection.
func TestProbeNumericFieldsFailsOnEntityCleanupAfterSuccessfulProbe(t *testing.T) {
	createEntity, destroyEntity, addEntity := probeCreateEntityGroup, probeDestroyEntityGroup, probeAddEntity
	createFields, destroyFields, watch := probeCreateFieldGroup, probeDestroyFieldGroup, probeWatchFields
	t.Cleanup(func() {
		probeCreateEntityGroup, probeDestroyEntityGroup, probeAddEntity = createEntity, destroyEntity, addEntity
		probeCreateFieldGroup, probeDestroyFieldGroup, probeWatchFields = createFields, destroyFields, watch
	})
	cleanupErr := errors.New("entity group cleanup failed")
	var calls []string
	probeCreateEntityGroup = func(string) (dcgm.GroupHandle, error) {
		calls = append(calls, "create entity")
		return dcgm.GroupHandle{}, nil
	}
	probeAddEntity = func(dcgm.GroupHandle, monitoredEntity) error {
		calls = append(calls, "populate")
		return nil
	}
	probeCreateFieldGroup = func(string, []dcgm.Short) (dcgm.FieldHandle, error) {
		calls = append(calls, "create fields")
		return dcgm.FieldHandle{}, nil
	}
	probeWatchFields = func(dcgm.FieldHandle, dcgm.GroupHandle, int64, float64, int32) error {
		calls = append(calls, "watch")
		return errors.New("error watching fields: Profiling is not supported for this group of GPUs")
	}
	probeDestroyFieldGroup = func(dcgm.FieldHandle) error {
		calls = append(calls, "destroy fields")
		return nil
	}
	probeDestroyEntityGroup = func(dcgm.GroupHandle) error {
		calls = append(calls, "destroy entity")
		return cleanupErr
	}
	available, unavailable, err := probeNumericFields([]candidateField{{ID: 100, Profiling: true}}, []monitoredEntity{{}})
	if !errors.Is(err, cleanupErr) {
		t.Errorf("probe error = %v, must retain sole entity cleanup failure", err)
	}
	if len(available) != 0 || len(unavailable) != 1 || unavailable[0].Reason != "profiling-not-supported" {
		t.Errorf("probe did not reach successful result collection: available=%v unavailable=%v", available, unavailable)
	}
	wantCalls := []string{"create entity", "populate", "create fields", "watch", "destroy fields", "destroy entity"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("native calls = %v, want %v", calls, wantCalls)
	}
}

// TestProbeNumericFieldsSplitsEntitiesAcrossGroups checks that more entities
// than one DCGM group can hold are spread over several groups, each destroyed,
// with every entity added once.
func TestProbeNumericFieldsSplitsEntitiesAcrossGroups(t *testing.T) {
	createEntity, destroyEntity, addEntity := probeCreateEntityGroup, probeDestroyEntityGroup, probeAddEntity
	createFields, destroyFields, watch := probeCreateFieldGroup, probeDestroyFieldGroup, probeWatchFields
	maxEntities := probeGroupMaxEntities
	t.Cleanup(func() {
		probeCreateEntityGroup, probeDestroyEntityGroup, probeAddEntity = createEntity, destroyEntity, addEntity
		probeCreateFieldGroup, probeDestroyFieldGroup, probeWatchFields = createFields, destroyFields, watch
		probeGroupMaxEntities = maxEntities
	})
	probeGroupMaxEntities = 2

	var groupSizes []int
	var added []uint
	probeCreateEntityGroup = func(string) (dcgm.GroupHandle, error) {
		groupSizes = append(groupSizes, 0)
		return dcgm.GroupHandle{}, nil
	}
	probeAddEntity = func(_ dcgm.GroupHandle, entity monitoredEntity) error {
		groupSizes[len(groupSizes)-1]++
		added = append(added, entity.Pair.EntityId)
		return nil
	}
	destroyed := 0
	probeDestroyEntityGroup = func(dcgm.GroupHandle) error {
		destroyed++
		return nil
	}
	probeCreateFieldGroup = func(string, []dcgm.Short) (dcgm.FieldHandle, error) {
		return dcgm.FieldHandle{}, nil
	}
	probeWatchFields = func(dcgm.FieldHandle, dcgm.GroupHandle, int64, float64, int32) error {
		return errors.New("error watching fields: Profiling is not supported for this group of GPUs")
	}
	probeDestroyFieldGroup = func(dcgm.FieldHandle) error { return nil }

	entities := make([]monitoredEntity, 5)
	for index := range entities {
		entities[index].Pair.EntityId = uint(index)
	}
	_, _, err := probeNumericFields([]candidateField{{ID: 100, Profiling: true}}, entities)
	if err != nil {
		t.Fatalf("probe error = %v", err)
	}
	if want := []int{2, 2, 1}; !reflect.DeepEqual(groupSizes, want) {
		t.Errorf("entities per group = %v, want %v", groupSizes, want)
	}
	if want := []uint{0, 1, 2, 3, 4}; !reflect.DeepEqual(added, want) {
		t.Errorf("added entities = %v, want each entity once in order %v", added, want)
	}
	if destroyed != len(groupSizes) {
		t.Errorf("destroyed %d groups, want %d", destroyed, len(groupSizes))
	}
}

// TestStartStage checks that stage lines are discarded until a command sets a
// writer, and then use the "stage: <name>" format.
func TestStartStage(t *testing.T) {
	t.Cleanup(func() { stageWriter = io.Discard })

	if stageWriter != io.Discard {
		t.Fatal("stage writer must discard by default")
	}

	var out bytes.Buffer
	stageWriter = &out
	startStage("entity discovery")
	if got, want := out.String(), "stage: entity discovery\n"; got != want {
		t.Fatalf("stage line = %q, want %q", got, want)
	}
}
