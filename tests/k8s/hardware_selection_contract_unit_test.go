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

package k8s

import (
	"errors"
	"strings"
	"testing"
)

// samples builds Prometheus text with one gauge sample per label set of family.
func samples(family string, labelSets ...string) []byte {
	var text strings.Builder
	text.WriteString("# TYPE " + family + " gauge\n")
	for _, labels := range labelSets {
		text.WriteString(family + "{" + labels + "} 1\n")
	}
	return []byte(text.String())
}

// errorContains reports whether err is set and its text holds want.
func errorContains(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}

// TestSelectHighestEntity checks the highest ID is chosen with the switch
// label prefix removed, and that a lone entity is refused.
func TestSelectHighestEntity(t *testing.T) {
	coversAll := errSelectionCoversAll.Error()
	tests := []struct {
		name    string
		metrics []byte
		prefix  string
		want    string
		wantErr string
	}{
		{name: "cpu", metrics: samples(cpuUtilFamily, `cpu="1"`, `cpu="0"`, `cpu="10"`), want: "10"},
		{name: "switch prefix", metrics: samples(switchTempFamily, `nvswitch="nvswitch2"`, `nvswitch="nvswitch11"`, `nvswitch="nvswitch0"`), prefix: switchPrefix, want: "11"},
		{name: "one entity", metrics: samples(cpuUtilFamily, `cpu="0",cpucore="0"`, `cpu="0",cpucore="1"`), wantErr: coversAll},
		{name: "no samples", metrics: samples("OTHER_FAMILY", `gpu="0"`), wantErr: "no DCGM_FI_DEV_CPU_UTIL_TOTAL sample"},
		{name: "prefix missing", metrics: samples(switchTempFamily, `nvswitch="0"`, `nvswitch="1"`), prefix: switchPrefix, wantErr: "plus a number"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			family, label := cpuUtilFamily, cpuLabel
			if tt.prefix != "" {
				family, label = switchTempFamily, switchLabel
			}

			got, err := selectHighestEntity(tt.metrics, family, label, tt.prefix)
			if tt.wantErr != "" {
				if !errorContains(err, tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}

			if err != nil || got.entityID != tt.want {
				t.Fatalf("got %q, %v; want %q", got.entityID, err, tt.want)
			}
		})
	}
}

// TestSelectHighestMembers checks the highest entity and its two highest links
// or cores are chosen, all of them when there are fewer, and that a selection
// that equals everything is skipped.
func TestSelectHighestMembers(t *testing.T) {
	tests := []struct {
		name        string
		metrics     []byte
		cpu         bool
		wantEntity  string
		wantMembers string
		wantErr     error
	}{
		{
			name: "links of the highest switch",
			metrics: samples(switchLinkFamily, `nvswitch="nvswitch0",nvlink="9"`, `nvswitch="nvswitch1",nvlink="3"`,
				`nvswitch="nvswitch1",nvlink="1"`, `nvswitch="nvswitch1",nvlink="10"`, `nvswitch="nvswitch1",nvlink="2"`),
			wantEntity: "1", wantMembers: "3,10",
		},
		{
			name:    "cores of the highest cpu",
			metrics: samples(cpuUtilFamily, `cpu="0",cpucore="7"`, `cpu="1",cpucore="0"`, `cpu="1",cpucore="1"`, `cpu="1",cpucore="2"`),
			cpu:     true, wantEntity: "1", wantMembers: "1,2",
		},
		{
			name:       "one link on the highest of two switches",
			metrics:    samples(switchLinkFamily, `nvswitch="nvswitch0",nvlink="0"`, `nvswitch="nvswitch1",nvlink="4"`),
			wantEntity: "1", wantMembers: "4",
		},
		{
			name:    "one switch with two links",
			metrics: samples(switchLinkFamily, `nvswitch="nvswitch0",nvlink="0"`, `nvswitch="nvswitch0",nvlink="1"`),
			wantErr: errSelectionCoversAll,
		},
		{
			name:    "one switch with one link",
			metrics: samples(switchLinkFamily, `nvswitch="nvswitch0",nvlink="0"`),
			wantErr: errSelectionCoversAll,
		},
		{name: "no cores reported", metrics: samples(cpuUtilFamily, `cpu="0"`, `cpu="1"`), cpu: true, wantErr: errSelectionCoversAll},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			family, label, prefix, member := switchLinkFamily, switchLabel, switchPrefix, linkLabel
			if tt.cpu {
				family, label, prefix, member = cpuUtilFamily, cpuLabel, "", coreLabel
			}

			got, err := selectHighestMembers(tt.metrics, family, label, prefix, member)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}

			if err != nil || got.entityID != tt.wantEntity || strings.Join(got.members, ",") != tt.wantMembers {
				t.Fatalf("got %+v, %v; want entity %s members %s", got, err, tt.wantEntity, tt.wantMembers)
			}
		})
	}
}

// TestOnlySelected checks the selected-subset check fails when the selection
// has no effect, when a selected entity is missing, and when nothing is reported.
func TestOnlySelected(t *testing.T) {
	check := onlySelectedLinks(entitySelection{entityID: "0", members: []string{"0", "1"}})
	tests := []struct {
		name    string
		metrics []byte
		wantErr string
	}{
		{name: "selected only", metrics: samples(switchLinkFamily, `nvswitch="nvswitch0",nvlink="0"`, `nvswitch="nvswitch0",nvlink="1"`)},
		{name: "other switch", metrics: samples(switchLinkFamily, `nvswitch="nvswitch0",nvlink="0"`, `nvswitch="nvswitch0",nvlink="1"`, `nvswitch="nvswitch1",nvlink="0"`), wantErr: `nvswitch="nvswitch1"`},
		{name: "other link", metrics: samples(switchLinkFamily, `nvswitch="nvswitch0",nvlink="0"`, `nvswitch="nvswitch0",nvlink="1"`, `nvswitch="nvswitch0",nvlink="2"`), wantErr: `nvlink="2"`},
		{name: "selected link missing", metrics: samples(switchLinkFamily, `nvswitch="nvswitch0",nvlink="0"`), wantErr: `no nvlink="1" sample`},
		{name: "selection ignored", metrics: samples(switchLinkFamily, `nvswitch="nvswitch1",nvlink="7"`, `nvswitch="nvswitch2",nvlink="3"`), wantErr: "want one of"},
		{name: "unprefixed switch", metrics: samples(switchLinkFamily, `nvswitch="0",nvlink="0"`, `nvswitch="0",nvlink="1"`), wantErr: "want one of"},
		{name: "no samples", metrics: samples("OTHER_FAMILY", `gpu="0"`), wantErr: "no DCGM_FI_DEV_NVSWITCH_LINK_THROUGHPUT_TX sample"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := check(tt.metrics)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && !errorContains(err, tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestOnlySelectedEntity checks the single-entity check accepts the selected
// switch and fails when another switch is reported.
func TestOnlySelectedEntity(t *testing.T) {
	single := onlySelectedEntity(switchTempFamily, switchLabel, "nvswitch3")
	if err := single(samples(switchTempFamily, `nvswitch="nvswitch3"`)); err != nil {
		t.Fatalf("selected switch: %v", err)
	}
	if err := single(samples(switchTempFamily, `nvswitch="nvswitch3"`, `nvswitch="nvswitch4"`)); err == nil {
		t.Fatal("an unselected switch must fail")
	}
}

// TestOnlySelectedCores checks the core check accepts the selected CPU's own
// sample and its selected cores, and fails for another CPU, an unselected core
// or a missing selected core.
func TestOnlySelectedCores(t *testing.T) {
	cores := onlySelectedCores(entitySelection{entityID: "1", members: []string{"2", "3"}})
	if err := cores(samples(cpuUtilFamily, `cpu="1",cpucore="2"`, `cpu="1",cpucore="3"`)); err != nil {
		t.Fatalf("selected cores: %v", err)
	}
	if err := cores(samples(cpuUtilFamily, `cpu="1",cpucore="2"`, `cpu="1",cpucore="3"`, `cpu="1",cpucore="4"`)); err == nil {
		t.Fatal("an unselected core must fail")
	}
	if err := cores(samples(cpuUtilFamily, `cpu="1"`, `cpu="1",cpucore="2"`, `cpu="1",cpucore="3"`)); err != nil {
		t.Fatalf("the selected CPU's own sample is not a core: %v", err)
	}
	if err := cores(samples(cpuUtilFamily, `cpu="0"`, `cpu="1",cpucore="2"`, `cpu="1",cpucore="3"`)); err == nil {
		t.Fatal("another CPU's sample must fail")
	}
	if err := cores(samples(cpuUtilFamily, `cpu="1"`, `cpu="1",cpucore="2"`)); err == nil {
		t.Fatal("a missing selected core must fail")
	}
}

// TestSettledScrape checks a scrape is accepted only when the previous one
// reported the same entities.
func TestSettledScrape(t *testing.T) {
	settled := settledScrape(entitySignature(switchTempFamily, switchLabel))
	one := samples(switchTempFamily, `nvswitch="nvswitch0"`)
	two := samples(switchTempFamily, `nvswitch="nvswitch0"`, `nvswitch="nvswitch1"`)

	if err := settled(one); err == nil {
		t.Fatal("the first scrape must not settle")
	}
	if err := settled(two); err == nil {
		t.Fatal("a scrape with more entities must not settle")
	}
	if err := settled(two); err != nil {
		t.Fatalf("the same entities twice must settle: %v", err)
	}
	if err := settled(samples("OTHER_FAMILY", `gpu="0"`)); err == nil {
		t.Fatal("a scrape without the family must not settle")
	}
}

// TestMIGInstanceSelection checks a single instance is refused for selection,
// and that the selected run must report exactly one instance with the ID.
func TestMIGInstanceSelection(t *testing.T) {
	const family = "DCGM_FI_DEV_GPU_TEMP"
	two := samples(family, `gpu="0",GPU_I_ID="1"`, `gpu="0",GPU_I_ID="2"`, `gpu="1",GPU_I_ID="1"`)

	if err := requireSeveralMIGInstances(two); err != nil {
		t.Fatalf("several instances: %v", err)
	}
	if err := requireSeveralMIGInstances(samples(family, `gpu="0",GPU_I_ID="1"`)); !errors.Is(err, errSelectionCoversAll) {
		t.Fatalf("one instance: error = %v, want %v", err, errSelectionCoversAll)
	}

	signature, err := migSignature(two)
	if err != nil || signature != "0/1;0/2;1/1" {
		t.Fatalf("signature = %q, %v; want 0/1;0/2;1/1", signature, err)
	}

	tests := []struct {
		name    string
		metrics []byte
		wantErr bool
	}{
		{name: "the selected instance only", metrics: samples(family, `gpu="0",GPU_I_ID="2"`)},
		{name: "same ID on two GPUs", metrics: samples(family, `gpu="0",GPU_I_ID="2"`, `gpu="1",GPU_I_ID="2"`), wantErr: true},
		{name: "another instance", metrics: samples(family, `gpu="0",GPU_I_ID="1"`), wantErr: true},
		{name: "several instances", metrics: two, wantErr: true},
		{name: "no instance labels", metrics: samples(family, `gpu="0"`), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := onlyOneMIGInstance(tt.metrics, "2"); (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestSelectors checks the flag selectors the steps pass to the exporter. The
// exporter's selector parser is not exported, so the grammar is checked in its
// own package tests and this test pins the strings built here.
func TestSelectors(t *testing.T) {
	firstOnly := entitySelection{entityID: "2"}
	withMembers := entitySelection{entityID: "2", members: []string{"6", "7"}}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "switch", got: switchSelector(firstOnly), want: "g:2"},
		{name: "switch links", got: switchSelector(withMembers), want: "g:2+i:6,7"},
		{name: "cpu all cores", got: cpuSelector(firstOnly), want: "g:2+i"},
		{name: "cpu cores", got: cpuSelector(withMembers), want: "g:2+i:6,7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
