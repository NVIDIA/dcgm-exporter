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
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RequiredEnrichmentWait is how long startup waits in total for the sources
// that the config requires before it fails. The startup wait and the text that
// explain shows for a required source both use it.
const RequiredEnrichmentWait = 60 * time.Second

// Paths the config file tries when container or HPC job enrichment is on and
// the file's sources do not name one. A switch left out means auto, so these
// paths are tried on every host and the source is used only if the
// path turns out to exist.
const (
	defaultContainerRuntimeSocket = "/var/run/docker.sock"
	defaultHPCJobMappingDir       = "/var/lib/dcgm-exporter/job-mapping"
)

// ApplyDefaultContainerRuntimeSocket fills in the default container runtime
// socket when container labels are on and no socket is set. It does not matter
// whether the config file, a flag or an environment variable turned the labels
// on, so it runs once after every layer has been merged. A socket that is set
// is never changed.
func (c *Config) ApplyDefaultContainerRuntimeSocket() {
	if c.ContainerLabels && strings.TrimSpace(c.ContainerRuntimeSocket) == "" {
		c.ContainerRuntimeSocket = defaultContainerRuntimeSocket
	}
}

// EnrichmentKeyBase is the YAML path of the enrichment block. This
// build accepts one metrics entry, so the path is fixed.
const EnrichmentKeyBase = "metrics[0].enrichment"

// EnrichmentSource names one workload-label source.
type EnrichmentSource int

const (
	// EnrichmentKubernetes is pod labels from the kubelet pod-resources socket.
	EnrichmentKubernetes EnrichmentSource = iota
	// EnrichmentContainer is container labels from the container runtime socket.
	EnrichmentContainer
	// EnrichmentDRA is dynamic resource allocation labels, which need Kubernetes.
	EnrichmentDRA
	// EnrichmentHPCJobs is job labels read from the HPC job mapping directory.
	EnrichmentHPCJobs
)

// Key is the YAML key that switches the source on or off.
func (s EnrichmentSource) Key() string {
	return EnrichmentKeyBase + "." + s.yamlName()
}

// yamlName is the source's key inside the enrichment block.
func (s EnrichmentSource) yamlName() string {
	switch s {
	case EnrichmentKubernetes:
		return "kubernetes"
	case EnrichmentContainer:
		return "container"
	case EnrichmentDRA:
		return "dra"
	default:
		return "hpc_jobs"
	}
}

// EnrichmentMode says how the config file asked for a workload-label source.
type EnrichmentMode int

const (
	// EnrichmentUnset means the file does not decide the source: a flag or
	// environment variable set it, the file wrote false, or the file has no
	// metrics entry. A file with no metrics entry has no
	// enrichment to switch, so it gets no modes. The plain boolean decides
	// whether the source is on, and when it is on startup treats it as auto.
	EnrichmentUnset EnrichmentMode = iota
	// EnrichmentAuto: the file left the switch out or wrote auto. Startup never
	// fails for it: a source that is not usable gets one warning, stays on, and
	// is used when it becomes usable.
	EnrichmentAuto
	// EnrichmentRequired: the file wrote true. Startup waits a fixed time for
	// the source and fails if it is still not usable.
	EnrichmentRequired
)

// EnrichmentModes holds the mode of each workload-label source that the
// config file asked for. A source the file switched off has no mode: its boolean
// setting is false and nothing is tried.
//
// Precedence, which the config reader, startup resolution and the
// transformer all follow:
//   - A flag or environment variable that sets a source replaces the file's
//     mode for it (ClearEnrichmentMode). It never becomes required, and startup
//     treats it as auto.
//   - A source is on when its boolean setting is true. Modes only add the
//     startup checks.
//   - Kubernetes wins over container labels when Kubernetes is active. An auto
//     Kubernetes source whose kubelet socket is not usable at startup is the
//     one exception: if container labels are on, this process uses them and
//     Kubernetes stays off.
//   - A file that makes both Kubernetes and container required is rejected
//     when it is read.
//   - Required container beats auto Kubernetes: Kubernetes is turned off.
//   - DRA needs Kubernetes to be active.
type EnrichmentModes struct {
	Kubernetes EnrichmentMode
	Container  EnrichmentMode
	DRA        EnrichmentMode
	HPCJobs    EnrichmentMode
}

// ContainerDisplacesKubernetes says startup turns Kubernetes off because the
// config file requires container labels. It applies to a Kubernetes source that
// is on and that the file does not itself require. A source that a flag or an
// environment variable turned on counts as auto, because only the file can
// require a source. Startup and inspection both use this rule.
func ContainerDisplacesKubernetes(c *Config) bool {
	modes := c.EnrichmentModes
	return c.Kubernetes && modes.Container == EnrichmentRequired && modes.Kubernetes != EnrichmentRequired
}

// ClearEnrichmentMode forgets the file's mode for a source. A flag or
// environment variable that sets the source calls it, so the setting keeps its
// best-effort meaning.
func (c *Config) ClearEnrichmentMode(source EnrichmentSource) {
	switch source {
	case EnrichmentKubernetes:
		c.EnrichmentModes.Kubernetes = EnrichmentUnset
	case EnrichmentContainer:
		c.EnrichmentModes.Container = EnrichmentUnset
	case EnrichmentDRA:
		c.EnrichmentModes.DRA = EnrichmentUnset
	case EnrichmentHPCJobs:
		c.EnrichmentModes.HPCJobs = EnrichmentUnset
	}
}

// YAMLEnrichment holds the workload-label switches of a metric entry.
// A switch left out means auto.
type YAMLEnrichment struct {
	Kubernetes *YAMLEnrichmentMode `yaml:"kubernetes,omitempty" json:"kubernetes,omitempty"`
	Container  *YAMLEnrichmentMode `yaml:"container,omitempty" json:"container,omitempty"`
	DRA        *YAMLEnrichmentMode `yaml:"dra,omitempty" json:"dra,omitempty"`
	HPCJobs    *YAMLEnrichmentMode `yaml:"hpc_jobs,omitempty" json:"hpc_jobs,omitempty"`
}

// YAMLEnrichmentMode is true (required), false (off) or auto, with true and
// false also accepted as True, TRUE, False and FALSE. Only "auto" in lower case
// is accepted.
// It is kept as text so an error can name the key and the bad value.
type YAMLEnrichmentMode string

// MarshalYAML writes true and false as YAML booleans, as a person would write them.
func (m YAMLEnrichmentMode) MarshalYAML() (interface{}, error) {
	return m.written(), nil
}

// MarshalJSON writes true and false as JSON booleans and any other value as text.
func (m YAMLEnrichmentMode) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.written())
}

// UnmarshalJSON reads a JSON boolean or string, so what MarshalJSON writes loads back.
func (m *YAMLEnrichmentMode) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}

	switch v := value.(type) {
	case bool:
		*m = YAMLEnrichmentMode(strconv.FormatBool(v))
	case string:
		*m = YAMLEnrichmentMode(v)
	default:
		return fmt.Errorf("an enrichment switch must be true, false or a string, got %s", data)
	}

	return nil
}

// written is true or false for a boolean spelling and the text itself otherwise.
func (m YAMLEnrichmentMode) written() any {
	switch strings.TrimSpace(string(m)) {
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	}

	return string(m)
}

// resolve converts the switch, treating a missing one as auto. It reports
// whether the source is on and, when it is, how startup must treat it.
func (m *YAMLEnrichmentMode) resolve(keyPath string) (bool, EnrichmentMode, error) {
	if m == nil {
		return true, EnrichmentAuto, nil
	}

	switch strings.TrimSpace(string(*m)) {
	case "auto":
		return true, EnrichmentAuto, nil
	case "true", "True", "TRUE": // the YAML core schema's spellings of a boolean
		return true, EnrichmentRequired, nil
	case "false", "False", "FALSE":
		return false, EnrichmentUnset, nil
	}

	return false, EnrichmentUnset, keyErrorf(keyPath, "must be true, false or auto, got %q", string(*m))
}

// enrichmentSwitch is one parsed switch: whether the source is on and the
// startup mode that goes with it.
type enrichmentSwitch struct {
	on   bool
	mode EnrichmentMode
}

// applyEnrichment turns the entry's switches into the runtime booleans and
// records each mode so startup can check the sources that were requested.
// A metric entry without an enrichment block means auto for every source.
// It runs after the source settings, because the defaults it fills in apply
// only when sources.container.runtime_socket and sources.hpc_jobs.mapping_dir
// are not set, and DRA needs a sources.dra block.
func (m *YAMLMetricEntry) applyEnrichment(config *Config, sources *YAMLSources, rec originRecorder) error {
	var e YAMLEnrichment
	if m.Enrichment != nil {
		e = *m.Enrichment
	}

	base := EnrichmentKeyBase

	switches, err := parseEnrichmentSwitches(e)
	if err != nil {
		return err
	}

	if switches.kubernetes.mode == EnrichmentRequired && switches.container.mode == EnrichmentRequired {
		return keyErrorf(base, "kubernetes and container cannot both be true")
	}

	if sources == nil || sources.DRA == nil {
		if switches.dra.mode == EnrichmentRequired {
			return keyErrorf(base+".dra", "true requires a sources.dra block")
		}

		// Auto DRA has nothing to try without a DRA source.
		switches.dra = enrichmentSwitch{}
	}

	// Store the modes and the runtime booleans.
	config.EnrichmentModes = EnrichmentModes{
		Kubernetes: switches.kubernetes.mode, Container: switches.container.mode,
		DRA: switches.dra.mode, HPCJobs: switches.hpcJobs.mode,
	}

	config.Kubernetes, config.ContainerLabels = switches.kubernetes.on, switches.container.on
	config.KubernetesEnableDRA = switches.dra.on

	// A switch the file leaves out is recorded as set by the enrichment block,
	// because the file still decided that it means auto.
	rec.record("Kubernetes", enrichmentOrigin(e.Kubernetes, EnrichmentKubernetes))
	rec.record("ContainerLabels", enrichmentOrigin(e.Container, EnrichmentContainer))
	rec.record("KubernetesEnableDRA", enrichmentOrigin(e.DRA, EnrichmentDRA))
	rec.record("HPCJobs", enrichmentOrigin(e.HPCJobs, EnrichmentHPCJobs))

	applyEnrichmentPaths(config, sources, e, switches, rec)

	return nil
}

// enrichmentOrigin names the key that decided a switch: the switch itself, or
// the whole enrichment block when the file leaves the switch out.
func enrichmentOrigin(value *YAMLEnrichmentMode, source EnrichmentSource) string {
	if value == nil {
		return EnrichmentKeyBase
	}

	return EnrichmentKeyBase + "." + source.yamlName()
}

// enrichmentSwitches holds the four parsed switches of an enrichment block.
type enrichmentSwitches struct {
	kubernetes, container, dra, hpcJobs enrichmentSwitch
}

// parseEnrichmentSwitches parses the four switches of an enrichment block. A
// missing switch means auto.
func parseEnrichmentSwitches(e YAMLEnrichment) (enrichmentSwitches, error) {
	var switches enrichmentSwitches

	for _, s := range []struct {
		value  *YAMLEnrichmentMode
		source EnrichmentSource
		dest   *enrichmentSwitch
	}{
		{e.Kubernetes, EnrichmentKubernetes, &switches.kubernetes},
		{e.Container, EnrichmentContainer, &switches.container},
		{e.DRA, EnrichmentDRA, &switches.dra},
		{e.HPCJobs, EnrichmentHPCJobs, &switches.hpcJobs},
	} {
		on, mode, parseErr := s.value.resolve(EnrichmentKeyBase + "." + s.source.yamlName())
		if parseErr != nil {
			return enrichmentSwitches{}, parseErr
		}

		*s.dest = enrichmentSwitch{on: on, mode: mode}
	}

	return switches, nil
}

// applyEnrichmentPaths fills in or clears the container runtime socket and the
// HPC job mapping directory that go with the container and HPC switches. A
// socket or directory that a switch fills in with its default, or clears, is
// recorded as set by that switch.
func applyEnrichmentPaths(config *Config, sources *YAMLSources, e YAMLEnrichment, switches enrichmentSwitches, rec originRecorder) {
	switch {
	case switches.container.on && !sources.hasContainerSocket():
		config.ContainerRuntimeSocket = defaultContainerRuntimeSocket
		rec.record("ContainerRuntimeSocket", enrichmentOrigin(e.Container, EnrichmentContainer))
	case !switches.container.on && strings.TrimSpace(config.ContainerRuntimeSocket) == defaultContainerRuntimeSocket:
		// The default socket is what container labels use when no socket is set,
		// so with the labels off it is not a setting. config show writes it, and
		// reading that back must give the settings it was written from. A socket
		// other than the default is kept for a flag that turns the labels on.
		config.ContainerRuntimeSocket = ""
		rec.record("ContainerRuntimeSocket", enrichmentOrigin(e.Container, EnrichmentContainer))
	}

	switch {
	case !switches.hpcJobs.on:
		config.HPCJobMappingDir = ""
		rec.record("HPCJobMappingDir", enrichmentOrigin(e.HPCJobs, EnrichmentHPCJobs))
	case !sources.hasHPCMappingDir():
		config.HPCJobMappingDir = defaultHPCJobMappingDir
		rec.record("HPCJobMappingDir", enrichmentOrigin(e.HPCJobs, EnrichmentHPCJobs))
	}
}

// hasContainerSocket reports whether the file sets a container runtime socket.
func (s *YAMLSources) hasContainerSocket() bool {
	return s != nil && s.Container != nil && strings.TrimSpace(s.Container.RuntimeSocket) != ""
}

// hasHPCMappingDir reports whether the file sets an HPC job mapping directory.
func (s *YAMLSources) hasHPCMappingDir() bool {
	return s != nil && s.HPCJobs != nil && strings.TrimSpace(s.HPCJobs.MappingDir) != ""
}
