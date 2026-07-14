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
	"encoding/json"
	"strings"
	"testing"
)

// TestParseOptions verifies all supported flags are accepted and stored.
func TestParseOptions(t *testing.T) {
	var stderr bytes.Buffer

	opts, err := parseOptions([]string{
		"-version=latest",
		"-output=json",
		"-dry-run=true",
		"-allow-field-removals=true",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseOptions: %v", err)
	}

	if opts.version != "latest" ||
		opts.output != "json" ||
		!opts.dryRun ||
		!opts.allowFieldRemovals {
		t.Fatalf("unexpected options: %#v", opts)
	}
}

// TestParseOptionsRequiresVersion verifies an omitted target version is rejected.
func TestParseOptionsRequiresVersion(t *testing.T) {
	var stderr bytes.Buffer

	_, err := parseOptions(nil, &stderr)
	if err == nil {
		t.Fatal("expected missing version to fail")
	}
	if !strings.Contains(err.Error(), "GO_DCGM_VERSION") {
		t.Fatalf("error %q does not mention GO_DCGM_VERSION", err)
	}
}

// TestParseOptionsRejectsUnknownOutput verifies unsupported output formats fail.
func TestParseOptionsRejectsUnknownOutput(t *testing.T) {
	var stderr bytes.Buffer

	_, err := parseOptions([]string{"-version=latest", "-output=yaml"}, &stderr)
	if err == nil {
		t.Fatal("expected unknown output to fail")
	}
	if !strings.Contains(err.Error(), "GO_DCGM_OUTPUT") {
		t.Fatalf("error %q does not mention GO_DCGM_OUTPUT", err)
	}
}

// TestWriteDiffJSONIncludesZeroIDs verifies JSON output preserves real zero IDs.
func TestWriteDiffJSONIncludesZeroIDs(t *testing.T) {
	diff := diffFields(
		"current",
		"target",
		map[string]int{
			"DCGM_FI_UNKNOWN":         0,
			"DCGM_FI_DEV_POWER_USAGE": 155,
		},
		map[string]int{
			"DCGM_FI_DEV_NEW_ZERO":    0,
			"DCGM_FI_DEV_POWER_USAGE": 0,
		},
	)

	var out bytes.Buffer
	if err := writeDiff(&out, "json", diff); err != nil {
		t.Fatalf("writeDiff: %v", err)
	}

	var got fieldDiff
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, out.String())
	}

	assertChanges(t, "added", got.Added, []fieldChange{addedField("DCGM_FI_DEV_NEW_ZERO", 0)})
	assertChanges(t, "removed", got.Removed, []fieldChange{removedField("DCGM_FI_UNKNOWN", 0)})
	assertChanges(t, "id changes", got.IDChanges, []fieldChange{
		changedField("DCGM_FI_DEV_POWER_USAGE", 155, 0),
	})
	if !strings.Contains(out.String(), `"target": 0`) {
		t.Fatalf("JSON output should include zero target IDs:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `"current": 0`) {
		t.Fatalf("JSON output should include zero current IDs:\n%s", out.String())
	}
}

// TestWriteDiffText verifies the human-readable diff includes each change section.
func TestWriteDiffText(t *testing.T) {
	diff := diffFields(
		"current",
		"target",
		map[string]int{"DCGM_FI_OLD": 1},
		map[string]int{"DCGM_FI_NEW": 2},
	)

	var out bytes.Buffer
	if err := writeDiff(&out, "text", diff); err != nil {
		t.Fatalf("writeDiff: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"current: current (1 fields)",
		"target:  target (1 fields)",
		"+ DCGM_FI_NEW = 2",
		"- DCGM_FI_OLD = 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("text output missing %q:\n%s", want, got)
		}
	}
}

// TestBlockedChangesError verifies blocked diffs explain the required review action.
func TestBlockedChangesError(t *testing.T) {
	diff := fieldDiff{
		Removed:   []fieldChange{removedField("DCGM_FI_REMOVED", 1)},
		IDChanges: []fieldChange{changedField("DCGM_FI_CHANGED", 2, 3)},
	}

	err := blockedChangesError(diff, false)
	if err == nil {
		t.Fatal("expected blocked changes error")
	}

	for _, want := range []string{
		"field ID change",
		"removed field",
		"GO_DCGM_ALLOW_FIELD_REMOVALS=true",
		"always blocked",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %s", want, err)
		}
	}
}
