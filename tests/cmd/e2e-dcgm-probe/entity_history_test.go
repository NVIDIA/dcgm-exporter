/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

func TestEntityHistoryRejectsSharedOrAmbiguousBackend(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    string
		address string
		want    string
	}{
		{"unknown mode", "remote", "", "--mode"},
		{"embedded remote", "embedded", "unix:///tmp/owned/engine.sock", "requires standalone"},
		{"missing owned backend", "standalone", "", "isolated absolute"},
		{"shared TCP", "standalone", "localhost:5555", "isolated absolute"},
		{"TCP URI", "standalone", "tcp://localhost:5555", "isolated absolute"},
		{"relative Unix", "standalone", "unix://engine.sock", "isolated absolute"},
		{"unclean Unix", "standalone", "unix:///tmp/../engine.sock", "clean path"},
		{"query", "standalone", "unix:///tmp/owned/engine.sock?shared=1", "clean path"},
		{"fragment", "standalone", "unix:///tmp/owned/engine.sock#other", "clean path"},
		{"NUL", "standalone", "unix:///tmp/owned/engine.sock%00suffix", "clean path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateEntityHistoryOptions(entityHistoryOptions{mode: test.mode, ownedHostengine: test.address, outputPath: "private.json"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
	for _, test := range []struct{ mode, address, want string }{{"embedded", "", ""}, {"standalone", "unix:///tmp/owned/engine.sock", "/tmp/owned/engine.sock"}, {"standalone", "unix:///tmp/owned%20dir/engine.sock", "/tmp/owned dir/engine.sock"}} {
		socket, err := validateEntityHistoryOptions(entityHistoryOptions{mode: test.mode, ownedHostengine: test.address, outputPath: "private.json"})
		if err != nil || socket != test.want {
			t.Fatalf("socket = %q, error = %v", socket, err)
		}
	}
}

func TestEntityHistoryCommandValidatesBeforeNativeInitialization(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"field-entity-history"}, "--output is required"},
		{[]string{"field-entity-history", "--output", "private.json", "--mode", "standalone"}, "isolated absolute"},
		{[]string{"field-entity-history", "extra"}, "unexpected positional"},
	} {
		if err := run(test.args); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("run(%v) = %v, want %q", test.args, err, test.want)
		}
	}
}

func TestSameHistoryPreservesTypedScopeAndSampleMultiplicity(t *testing.T) {
	ci := dcgmprobe.HistorySample{Entity: dcgmprobe.Entity{Group: 5, ID: 64}, FieldID: 230, FieldType: dcgm.DCGM_FT_INT64, TimestampUsec: 123, Value: 45}
	expected := []dcgmprobe.HistorySample{ci, ci}
	if !sameHistory(expected, slices.Clone(expected)) {
		t.Fatal("equal samples must retain their multiplicity")
	}
	for _, test := range []struct {
		name   string
		actual []dcgmprobe.HistorySample
	}{
		{"missing CI", nil},
		{"deduplicated observation", []dcgmprobe.HistorySample{ci}},
		{"overlapping read", []dcgmprobe.HistorySample{ci, ci, ci}},
		{"parent substitution", []dcgmprobe.HistorySample{{Entity: dcgmprobe.Entity{Group: 4, ID: 64}, FieldID: 230, FieldType: dcgm.DCGM_FT_INT64, TimestampUsec: 123, Value: 45}, ci}},
		{"wrong field type", []dcgmprobe.HistorySample{{Entity: ci.Entity, FieldID: ci.FieldID, FieldType: dcgm.DCGM_FT_DOUBLE, TimestampUsec: ci.TimestampUsec, Value: ci.Value}, ci}},
		{"wrong status", []dcgmprobe.HistorySample{{Entity: ci.Entity, FieldID: 230, FieldType: dcgm.DCGM_FT_INT64, Status: 1, TimestampUsec: 123, Value: 45}, ci}},
		{"label contamination", []dcgmprobe.HistorySample{{Entity: ci.Entity, FieldID: 1, FieldType: dcgm.DCGM_FT_INT64, TimestampUsec: 123, Value: 45}, ci}},
		{"wrong timestamp", []dcgmprobe.HistorySample{{Entity: ci.Entity, FieldID: 230, FieldType: dcgm.DCGM_FT_INT64, TimestampUsec: 124, Value: 45}, ci}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if sameHistory(expected, test.actual) {
				t.Fatal("unexpected history accepted")
			}
		})
	}
	before := slices.Clone(expected)
	reversed := []dcgmprobe.HistorySample{ci, {Entity: ci.Entity, FieldID: 230, FieldType: dcgm.DCGM_FT_INT64, TimestampUsec: 124, Value: 46}}
	actual := []dcgmprobe.HistorySample{reversed[1], reversed[0]}
	actualBefore := slices.Clone(actual)
	if !sameHistory(reversed, actual) {
		t.Fatal("history ordering must not matter")
	}
	if !slices.Equal(before, expected) || !slices.Equal(actualBefore, actual) {
		t.Fatal("comparison mutated inputs")
	}
}

func TestEntityInjectionErrorPreservesPrerequisitesAndCleanup(t *testing.T) {
	for _, test := range []struct {
		status, cleanup int
		want            []string
	}{
		{0, 0, nil},
		{-1001, 0, []string{"load native injection library libdcgm.so.4 failed"}},
		{-1002, 0, []string{"lacks dcgmInjectEntityFieldValue"}},
		{-1003, 0, []string{"lacks dcgmConnect_v2 or dcgmDisconnect"}},
		{-3, 0, []string{"injection or connection failed with DCGM status -3"}},
		{0, -4, []string{"disconnect native injection client failed with DCGM status -4"}},
		{-3, -4, []string{"injection or connection failed with DCGM status -3", "disconnect native injection client failed with DCGM status -4"}},
	} {
		err := entityInjectionError(test.status, test.cleanup)
		if len(test.want) == 0 {
			if err != nil {
				t.Fatalf("success returned error: %v", err)
			}
			continue
		}
		for _, want := range test.want {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %q", err, want)
			}
		}
	}
}
