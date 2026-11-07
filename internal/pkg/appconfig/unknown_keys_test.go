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

package appconfig

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Unknown keys are reported with their YAML path and line. Keys that this
// build does not implement are unknown.
func TestUnknownKeysNameThePath(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "top level",
			yaml: `
version: 2
foo: 1
`,
			wantErr: "foo: unknown key at line 2",
		},
		{
			name: "include",
			yaml: `
version: 2
metrics:
  - name: gpu
    include: ['@gpu.basic']
`,
			wantErr: "metrics[0].include: unknown key at line 4",
		},
		{
			name: "second entry",
			yaml: `
version: 2
metrics:
  - {name: a, file: x}
  - name: b
    typo: 1
`,
			wantErr: "metrics[1].typo: unknown key at line 5",
		},
		{
			name: "override key",
			yaml: `
version: 2
metrics:
  - name: gpu
    overrides:
      - match: [A]
        interval: 1s
`,
			wantErr: "metrics[0].overrides[0].interval: unknown key at line 6",
		},
		{
			name: "camelCase v2 key",
			yaml: `
version: 2
sources:
  dcgm:
    watch:
      maxKeepAge: 1m
`,
			wantErr: "sources.dcgm.watch.maxKeepAge: unknown key at line 5",
		},
		{
			name: "camelCase detectBindUnbind",
			yaml: `
version: 2
sources:
  dcgm:
    detectBindUnbind: {}
`,
			wantErr: "sources.dcgm.detectBindUnbind: unknown key at line 4",
		},
		{
			name: "old collections",
			yaml: `
version: 2
collections:
  - name: a
`,
			wantErr: "collections: unknown key at line 2",
		},
		{
			name: "old v1 collection in v2",
			yaml: `
version: 2
collection:
  interval: 30s
`,
			wantErr: "collection: unknown key at line 2",
		},
		{
			name: "kubernetes resource_names",
			yaml: `
version: 2
sources:
  kubernetes:
    resource_names: [x]
`,
			wantErr: "sources.kubernetes.resource_names: unknown key at line 4",
		},
		{
			name: "enrichment",
			yaml: `
version: 2
metrics:
  - name: gpu
    enrichment: {slurm: true}
`,
			wantErr: "metrics[0].enrichment.slurm: unknown key at line 4",
		},
		{
			name: "v1 sources block",
			yaml: `
version: 1
sources:
  dcgm:
    detectBindUnbind:
      enabled: true
`,
			wantErr: "sources: unknown key at line 2; version 1 does not support this key; newer settings need version 2",
		},
		{
			name: "v2 collections in v1",
			yaml: `
version: 1
collections:
  - name: a
`,
			wantErr: "collections: unknown key at line 2",
		},
		{
			name: "v2 key in v1 server",
			yaml: `
version: 1
server:
  max_concurrent_scrapes: 4
`,
			wantErr: "server.max_concurrent_scrapes: unknown key at line 3",
		},
		{
			name: "v1 key in v2 server",
			yaml: `
version: 2
server:
  maxConcurrentScrapes: 4
`,
			wantErr: "server.maxConcurrentScrapes: unknown key at line 3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireParseError(t, strings.TrimPrefix(tt.yaml, "\n"), tt.wantErr)
		})
	}
}

const (
	mergeKeyV1 = `
version: 1
collection:
  watchGroups:
    - &g {name: a, interval: 1s, fields: [X]}
    - {<<: *g, name: b}
`
	mergeKeyV2 = `
version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
    overrides:
      - &g {name: a, match: [X], every: 1s}
      - {<<: *g, name: b}
` + v2Outputs
)

// Merge keys (<<) keep working as they did before the key check existed.
func TestMergeKeysAreFollowed(t *testing.T) {
	want := []WatchGroup{
		{Name: "a", Interval: 1000, Fields: []string{"X"}},
		{Name: "b", Interval: 1000, Fields: []string{"X"}},
	}
	for name, input := range map[string]string{"version 1": mergeKeyV1, "version 2": mergeKeyV2} {
		t.Run(name, func(t *testing.T) {
			_, config := parseAndApply(t, input)
			for i := range config.WatchGroups {
				config.WatchGroups[i].ExtendedMatch = false
				config.WatchGroups[i].Path = ""
			}
			assert.Equal(t, want, config.WatchGroups)
		})
	}
}

// A mapping that is merged in is checked like any other mapping.
func TestMergedMappingsRejectUnknownKeys(t *testing.T) {
	requireParseError(t, `
version: 1
collection:
  watchGroups:
    - &g {name: a, interval: 1s, fields: [X], typo: 1}
    - {<<: *g, name: b}
`, "collection.watchGroups[0].typo: unknown key")
	requireParseError(t, `
version: 1
collection:
  watchGroups:
    - &g {name: a, interval: 1s, fields: [X]}
    - {<<: [*g, {typo: 1}], name: b}
`, "collection.watchGroups[1].typo: unknown key")
}

// A merge key whose value is an alias to a sequence of mappings is checked too.
func TestMergedAliasToSequenceRejectsUnknownKeys(t *testing.T) {
	requireParseError(t, `
version: 1
metrics:
  fields: &rows [{name: A, prometheusType: gauge, help: h}]
collection:
  watchGroups:
    - {<<: *rows, interval: 1s, fields: [X]}
`, "collection.watchGroups[0].prometheusType: unknown key")
}

// The reader's own duplicate-key check (rejectDuplicateMappingKeys) rejects two merge keys in one mapping, with a line number.
func TestTwoMergeKeysInOneMappingAreRejected(t *testing.T) {
	requireParseError(t, `
version: 1
collection:
  watchGroups:
    - &a {name: a, interval: 1s}
    - &b {fields: [X]}
    - {<<: *a, <<: *b}
`, `duplicate YAML key "<<"`)
}

// A merge key directly inside the outputs map merges output entries, not output fields.
func TestMergeKeyInsideOutputsMap(t *testing.T) {
	_, config := parseAndApply(t, `
version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
outputs:
  <<: {prometheus: {type: prometheus, schema: dcgm/v1}}
`)
	assert.Equal(t, "/tmp/a.csv", config.MetricSource.File)

	requireParseError(t, `
version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
outputs:
  <<: {prometheus: {type: prometheus, schema: dcgm/v1, typo: 1}}
`, "outputs.prometheus.typo: unknown key")
}

// Heavily shared anchors and self-referencing aliases are checked once and do not stall parsing.
func TestAliasHeavyFilesParseQuickly(t *testing.T) {
	var wide strings.Builder
	wide.WriteString("version: 1\ncollection:\n  watchGroups:\n    - &g {name: n0, interval: 1s, fields: [X]}\n")
	for i := 1; i < 3000; i++ {
		fmt.Fprintf(&wide, "    - {<<: *g, name: n%d}\n", i)
	}
	// A mapping that merges itself must be checked once, not forever.
	selfMerge := "version: 1\ncollection:\n  watchGroups:\n    - &g {<<: *g, name: a, interval: 1s, fields: [X]}\n"
	for name, input := range map[string]string{"wide fan-out": wide.String(), "self merge": selfMerge} {
		t.Run(name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = ParseYAMLConfig([]byte(input))
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("parsing did not finish")
			}
		})
	}
}
