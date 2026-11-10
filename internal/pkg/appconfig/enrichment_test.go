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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// enrichmentDoc builds a config file whose metrics entry has the given
// enrichment block, or none when it is empty.
func enrichmentDoc(enrichment string) string {
	return enrichmentDocWithSources(enrichment, "")
}

// enrichmentDocWithSources is enrichmentDoc with a sources section added.
func enrichmentDocWithSources(enrichment, sources string) string {
	doc := "version: 2\nmetrics:\n  - name: gpu\n    file: /etc/dcgm-exporter/default-counters.csv\n"
	if enrichment != "" {
		doc += "    enrichment: " + enrichment + "\n"
	}
	return doc + sources + v2Outputs
}

func TestV2EnrichmentLowering(t *testing.T) {
	const allOff = "{kubernetes: false, container: false, dra: false, hpc_jobs: false}"
	tests := []struct {
		name       string
		enrichment string
		sources    string
		kubernetes bool
		container  bool
		dra        bool
		modes      EnrichmentModes
		socket     string
		hpcDir     string
	}{
		{
			name: "omitted block means auto, DRA stays off without a DRA source", enrichment: "",
			kubernetes: true, container: true,
			modes:  EnrichmentModes{Kubernetes: EnrichmentAuto, Container: EnrichmentAuto, HPCJobs: EnrichmentAuto},
			socket: defaultContainerRuntimeSocket, hpcDir: defaultHPCJobMappingDir,
		},
		{
			name: "auto DRA applies when the file has a DRA source", enrichment: "",
			sources:    "sources:\n  dra: {mode: kubernetes}\n",
			kubernetes: true, container: true, dra: true,
			modes: EnrichmentModes{
				Kubernetes: EnrichmentAuto, Container: EnrichmentAuto, DRA: EnrichmentAuto, HPCJobs: EnrichmentAuto,
			},
			socket: defaultContainerRuntimeSocket, hpcDir: defaultHPCJobMappingDir,
		},
		{
			name: "omitted switch means auto", enrichment: "{kubernetes: false, dra: false, hpc_jobs: false}",
			container: true,
			modes:     EnrichmentModes{Container: EnrichmentAuto},
			socket:    defaultContainerRuntimeSocket,
		},
		{
			name: "configured source paths replace the default paths", enrichment: "{kubernetes: false, dra: false}",
			sources:   "sources:\n  container: {runtime_socket: /run/podman/podman.sock}\n  hpc_jobs: {mapping_dir: /jobs}\n",
			container: true,
			modes:     EnrichmentModes{Container: EnrichmentAuto, HPCJobs: EnrichmentAuto},
			socket:    "/run/podman/podman.sock", hpcDir: "/jobs",
		},
		{
			name: "true is required", enrichment: "{kubernetes: true, container: false, dra: false, hpc_jobs: true}",
			kubernetes: true,
			modes:      EnrichmentModes{Kubernetes: EnrichmentRequired, HPCJobs: EnrichmentRequired},
			hpcDir:     defaultHPCJobMappingDir,
		},
		{
			name: "false clears a configured directory", enrichment: allOff,
			sources: "sources:\n  hpc_jobs: {mapping_dir: /jobs}\n",
		},
		{name: "all false tries nothing", enrichment: allOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, config := parseAndApply(t, enrichmentDocWithSources(tt.enrichment, tt.sources))
			assert.Equal(t, tt.kubernetes, config.Kubernetes)
			assert.Equal(t, tt.container, config.ContainerLabels)
			assert.Equal(t, tt.dra, config.KubernetesEnableDRA)
			assert.Equal(t, tt.modes, config.EnrichmentModes)
			assert.Equal(t, tt.socket, config.ContainerRuntimeSocket)
			assert.Equal(t, tt.hpcDir, config.HPCJobMappingDir)
		})
	}
}

func TestV2EnrichmentErrors(t *testing.T) {
	tests := []struct {
		name, enrichment, want string
	}{
		{
			"both required", "{kubernetes: true, container: true}",
			"metrics[0].enrichment: kubernetes and container cannot both be true",
		},
		{"bad value", "{dra: maybe}", `metrics[0].enrichment.dra: must be true, false or auto, got "maybe"`},
		{"required DRA needs a DRA source", "{dra: true}", "metrics[0].enrichment.dra: true requires a sources.dra block"},
		{"yes is not a boolean", "{hpc_jobs: yes}", "metrics[0].enrichment.hpc_jobs: must be true, false or auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireParseError(t, enrichmentDoc(tt.enrichment), tt.want)
		})
	}
}

func TestV2EnrichmentOrigins(t *testing.T) {
	y, _ := parseAndApply(t, enrichmentDoc("{kubernetes: true, hpc_jobs: false}"))
	origins := y.SettingOrigins()
	assert.Equal(t, "metrics[0].enrichment.kubernetes", origins["Kubernetes"])
	assert.Equal(t, "metrics[0].enrichment", origins["ContainerLabels"])
	assert.Equal(t, "metrics[0].enrichment", origins["KubernetesEnableDRA"])
	assert.Equal(t, "metrics[0].enrichment", origins["ContainerRuntimeSocket"],
		"a defaulted socket is recorded as set by the switch")
	assert.Equal(t, "metrics[0].enrichment.hpc_jobs", origins["HPCJobMappingDir"])

	y, _ = parseAndApply(t, enrichmentDocWithSources("{container: auto, hpc_jobs: true}", `sources:
  container: {runtime_socket: /run/podman/podman.sock}
`))
	origins = y.SettingOrigins()
	assert.Equal(t, "sources.container.runtime_socket", origins["ContainerRuntimeSocket"])
	assert.Equal(t, "metrics[0].enrichment.hpc_jobs", origins["HPCJobMappingDir"],
		"a defaulted directory is recorded as set by the switch")

	y, _ = parseAndApply(t, enrichmentDocWithSources("{hpc_jobs: auto}", "sources:\n  hpc_jobs: {mapping_dir: /jobs}\n"))
	assert.Equal(t, "sources.hpc_jobs.mapping_dir", y.SettingOrigins()["HPCJobMappingDir"])
}

func TestV2EnrichmentMarshalKeepsBooleans(t *testing.T) {
	y, _ := parseAndApply(t, enrichmentDoc("{kubernetes: true, container: auto, dra: false}"))
	out, err := yaml.Marshal(y)
	require.NoError(t, err)
	assert.Contains(t, string(out), "kubernetes: true\n")
	assert.Contains(t, string(out), "container: auto\n")
	assert.Contains(t, string(out), "dra: false\n")
}

func TestV1KeepsEnrichmentUnset(t *testing.T) {
	_, config := parseAndApply(t, powerGroupV1)
	assert.Equal(t, EnrichmentModes{}, config.EnrichmentModes)
	assert.False(t, config.Kubernetes || config.ContainerLabels || config.KubernetesEnableDRA)
}

// A version 2 file with no metrics entry has no enrichment block to read, so
// it turns no workload-label source on and leaves their paths alone.
func TestV2WithoutMetricsTurnsNoEnrichmentOn(t *testing.T) {
	_, config := parseAndApply(t, "version: 2\nserver: {max_concurrent_scrapes: 2}\n")
	assert.Equal(t, EnrichmentModes{}, config.EnrichmentModes)
	assert.False(t, config.Kubernetes || config.ContainerLabels || config.KubernetesEnableDRA)
	assert.Equal(t, baseConfig().ContainerRuntimeSocket, config.ContainerRuntimeSocket)
	assert.Equal(t, baseConfig().HPCJobMappingDir, config.HPCJobMappingDir)
}

func TestClearEnrichmentModeClearsOnlyTheNamedSource(t *testing.T) {
	sources := []EnrichmentSource{EnrichmentKubernetes, EnrichmentContainer, EnrichmentDRA, EnrichmentHPCJobs}
	for _, source := range sources {
		t.Run(source.Key(), func(t *testing.T) {
			config := &Config{EnrichmentModes: EnrichmentModes{
				Kubernetes: EnrichmentAuto, Container: EnrichmentRequired, DRA: EnrichmentAuto, HPCJobs: EnrichmentRequired,
			}}
			config.ClearEnrichmentMode(source)
			modes := map[EnrichmentSource]EnrichmentMode{
				EnrichmentKubernetes: config.EnrichmentModes.Kubernetes,
				EnrichmentContainer:  config.EnrichmentModes.Container,
				EnrichmentDRA:        config.EnrichmentModes.DRA,
				EnrichmentHPCJobs:    config.EnrichmentModes.HPCJobs,
			}
			for other, mode := range modes {
				if other == source {
					assert.Equal(t, EnrichmentUnset, mode)
				} else {
					assert.NotEqual(t, EnrichmentUnset, mode, "%s must keep its mode", other.Key())
				}
			}
		})
	}
}

func TestV2EnrichmentAcceptsYAMLBooleanSpellings(t *testing.T) {
	tests := []struct {
		value string
		on    bool
		mode  EnrichmentMode
	}{
		{"true", true, EnrichmentRequired},
		{"True", true, EnrichmentRequired},
		{"TRUE", true, EnrichmentRequired},
		{"false", false, EnrichmentUnset},
		{"False", false, EnrichmentUnset},
		{"FALSE", false, EnrichmentUnset},
		{"auto", true, EnrichmentAuto},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			_, config := parseAndApply(t, enrichmentDoc("{kubernetes: "+tt.value+", container: false, dra: false, hpc_jobs: false}"))
			assert.Equal(t, tt.on, config.Kubernetes)
			assert.Equal(t, tt.mode, config.EnrichmentModes.Kubernetes)
		})
	}
	requireParseError(t, enrichmentDoc("{kubernetes: AUTO}"), `must be true, false or auto, got "AUTO"`)
}

func TestYAMLEnrichmentModeJSONRoundTrip(t *testing.T) {
	for in, want := range map[string]YAMLEnrichmentMode{`true`: "true", `false`: "false", `"auto"`: "auto"} {
		var mode YAMLEnrichmentMode
		require.NoError(t, json.Unmarshal([]byte(in), &mode), in)
		assert.Equal(t, want, mode)

		out, err := json.Marshal(mode)
		require.NoError(t, err)
		assert.Equal(t, in, string(out))
	}

	var mode YAMLEnrichmentMode
	assert.Error(t, json.Unmarshal([]byte(`3`), &mode))
}

func TestContainerDisplacesKubernetes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kubernetes bool
		modes      EnrichmentModes
		want       bool
	}{
		{"required container, Kubernetes auto", true, EnrichmentModes{Container: EnrichmentRequired, Kubernetes: EnrichmentAuto}, true},
		{"required container, Kubernetes turned on by a flag or variable", true, EnrichmentModes{Container: EnrichmentRequired}, true},
		{"required container, Kubernetes required", true, EnrichmentModes{Container: EnrichmentRequired, Kubernetes: EnrichmentRequired}, false},
		{"required container, Kubernetes off", false, EnrichmentModes{Container: EnrichmentRequired, Kubernetes: EnrichmentAuto}, false},
		{"auto container", true, EnrichmentModes{Container: EnrichmentAuto, Kubernetes: EnrichmentAuto}, false},
		{"container not set", true, EnrichmentModes{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := &Config{Kubernetes: tc.kubernetes, EnrichmentModes: tc.modes}
			assert.Equal(t, tc.want, ContainerDisplacesKubernetes(config))
		})
	}
}
