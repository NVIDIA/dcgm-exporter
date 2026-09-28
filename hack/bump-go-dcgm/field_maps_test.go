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
	"os"
	"path/filepath"
	"testing"
)

// TestReadFieldMaps verifies canonical and legacy maps are parsed and combined.
func TestReadFieldMaps(t *testing.T) {
	moduleDir := writeConstFields(t, `package dcgm

type Short uint16

var dcgmFields = map[string]Short{
	"DCGM_FI_DEV_GPU_TEMP": 150,
	"DCGM_FI_DEV_POWER_USAGE": 0x9b,
}

var legacyDCGMFields = map[string]Short{
	"dcgm_gpu_temp": 150,
	"DCGM_FI_DEV_OLD_POWER_USAGE": 155,
}
`)

	maps, err := readFieldMaps(moduleDir)
	if err != nil {
		t.Fatalf("readFieldMaps: %v", err)
	}

	combined, err := maps.combined()
	if err != nil {
		t.Fatalf("combined: %v", err)
	}
	want := map[string]int{
		"DCGM_FI_DEV_GPU_TEMP":        150,
		"DCGM_FI_DEV_POWER_USAGE":     155,
		"dcgm_gpu_temp":               150,
		"DCGM_FI_DEV_OLD_POWER_USAGE": 155,
	}
	if len(combined) != len(want) {
		t.Fatalf("combined map length = %d, want %d: %v", len(combined), len(want), combined)
	}
	for name, id := range want {
		if combined[name] != id {
			t.Fatalf("combined[%q] = %d, want %d", name, combined[name], id)
		}
	}
}

// TestCombinedRejectsConflictingLegacyID verifies legacy aliases cannot mask ID drift.
func TestCombinedRejectsConflictingLegacyID(t *testing.T) {
	maps := fieldMaps{
		fields: map[string]int{"DCGM_FI_DEV_GPU_TEMP": 150},
		legacy: map[string]int{"DCGM_FI_DEV_GPU_TEMP": 151},
	}

	if _, err := maps.combined(); err == nil {
		t.Fatal("expected conflicting field ID to fail")
	}
}

// TestReadFieldMapsMissingMap verifies generated files without both maps fail loudly.
func TestReadFieldMapsMissingMap(t *testing.T) {
	moduleDir := writeConstFields(t, `package dcgm

type Short uint16

var dcgmFields = map[string]Short{
	"DCGM_FI_DEV_GPU_TEMP": 150,
}
`)

	if _, err := readFieldMaps(moduleDir); err == nil {
		t.Fatal("expected missing legacyDCGMFields to fail")
	}
}

// TestDiffFields verifies added, removed, and ID-changed field names are reported.
func TestDiffFields(t *testing.T) {
	current := map[string]int{
		"DCGM_FI_DEV_GPU_TEMP":    150,
		"DCGM_FI_DEV_POWER_USAGE": 155,
		"DCGM_FI_DEV_REMOVED":     999,
	}
	target := map[string]int{
		"DCGM_FI_DEV_GPU_TEMP":    150,
		"DCGM_FI_DEV_POWER_USAGE": 156,
		"DCGM_FI_DEV_ADDED":       1000,
	}

	diff := diffFields("current", "target", current, target)

	assertChanges(t, "added", diff.Added, []fieldChange{addedField("DCGM_FI_DEV_ADDED", 1000)})
	assertChanges(t, "removed", diff.Removed, []fieldChange{removedField("DCGM_FI_DEV_REMOVED", 999)})
	assertChanges(t, "id changes", diff.IDChanges, []fieldChange{
		changedField("DCGM_FI_DEV_POWER_USAGE", 155, 156),
	})
	if !diff.hasBlockedChanges(false) {
		t.Fatal("expected removed fields and ID changes to block")
	}
	if !diff.hasBlockedChanges(true) {
		t.Fatal("expected ID changes to block even when removals are allowed")
	}
}

// TestDiffFieldsAllowsReviewedRemovals verifies removal-only diffs can be allowed.
func TestDiffFieldsAllowsReviewedRemovals(t *testing.T) {
	diff := diffFields(
		"current",
		"target",
		map[string]int{"DCGM_FI_DEV_REMOVED": 999},
		map[string]int{},
	)

	if !diff.hasBlockedChanges(false) {
		t.Fatal("expected removed field to block by default")
	}
	if diff.hasBlockedChanges(true) {
		t.Fatal("expected removal-only diff to pass when removals are allowed")
	}
}

// writeConstFields writes a minimal generated field-map file under a fake module root.
func writeConstFields(t *testing.T, contents string) string {
	t.Helper()

	moduleDir := t.TempDir()
	fieldDir := filepath.Join(moduleDir, "pkg", "dcgm")
	if err := os.MkdirAll(fieldDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fieldDir, "const_fields.go"), []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	return moduleDir
}

// assertChanges compares field-change slices, including whether optional IDs are present.
func assertChanges(t *testing.T, name string, got []fieldChange, want []fieldChange) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("%s length = %d, want %d: %v", name, len(got), len(want), got)
	}
	for i := range want {
		if got[i].Name != want[i].Name ||
			valueOf(got[i].Current) != valueOf(want[i].Current) ||
			valueOf(got[i].Target) != valueOf(want[i].Target) ||
			(got[i].Current == nil) != (want[i].Current == nil) ||
			(got[i].Target == nil) != (want[i].Target == nil) {
			t.Fatalf("%s[%d] = %#v, want %#v", name, i, got[i], want[i])
		}
	}
}
