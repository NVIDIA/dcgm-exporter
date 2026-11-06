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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettingOriginText(t *testing.T) {
	for origin, want := range map[SettingOrigin]string{
		{Kind: OriginDefault}:                    "default",
		{Kind: OriginFile, Key: "a.b"}:           "YAML: a.b",
		{Kind: OriginEnv, Name: "X"}:             "environment: X",
		{Kind: OriginFlag, Name: "--x"}:          "argv: --x",
		{Kind: OriginDerived, From: "argv: --x"}: "derived: argv: --x",
	} {
		assert.Equal(t, want, origin.String())
	}
}

func TestResolvedOriginFallsBackToTheDefault(t *testing.T) {
	r := &Resolved{Origins: map[string]SettingOrigin{"Address": {Kind: OriginFlag, Name: "--address"}}}
	assert.Equal(t, SettingOrigin{Kind: OriginFlag, Name: "--address"}, r.OriginOf("Address"))
	assert.Equal(t, "argv: --address", r.Origin("Address"))
	assert.Equal(t, SettingOrigin{Kind: OriginDefault}, r.OriginOf("CollectInterval"))
	assert.Equal(t, "default", r.Origin("CollectInterval"))
}

// resolvedFromFile applies a config file to the defaults the way startup does.
func resolvedFromFile(t *testing.T, document string) (*Resolved, error) {
	t.Helper()
	y, err := ParseYAMLConfig([]byte(document))
	require.NoError(t, err)
	r := &Resolved{Config: DefaultConfig(), Origins: map[string]SettingOrigin{}}

	return r, r.ApplyConfigFile(y, SettingOrigin{Kind: OriginFlag, Name: "--config-file"})
}

func TestApplyConfigFileRecordsWhatTheFileSupplied(t *testing.T) {
	r, err := resolvedFromFile(t, "version: 2\nserver:\n  read_timeout: 7s\n")
	require.NoError(t, err)

	assert.NotNil(t, r.YAML)
	assert.Equal(t, 7*time.Second, r.Config.WebReadTimeout)
	assert.Equal(t, SettingOrigin{Kind: OriginFile, Key: "server.read_timeout"}, r.OriginOf("WebReadTimeout"))
	assert.Equal(t, SettingOrigin{Kind: OriginFlag, Name: "--config-file"}, r.OriginOf("ConfigFile"))
	assert.Equal(t, 7*time.Second, r.FileConfig.WebReadTimeout)
	assert.Equal(t, r.Origins, r.FileOrigins, "the snapshot is the file layer as it stood")

	// A later layer changes the config, not the snapshot.
	r.Config.WebReadTimeout = time.Second
	assert.Equal(t, 7*time.Second, r.FileConfig.WebReadTimeout)
}

func TestApplyConfigFileGivesTheHPCSwitchTheOriginOfItsSource(t *testing.T) {
	r, err := resolvedFromFile(t, "version: 2\nmetrics:\n  - name: gpu\n    file: x.csv\n    enrichment: {hpc_jobs: auto}\noutputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n")
	require.NoError(t, err)
	assert.Equal(t, SettingOrigin{Kind: OriginFile, Key: EnrichmentHPCJobs.Key()}, r.OriginOf(HPCJobsSwitchField))

	r, err = resolvedFromFile(t, "version: 2\nsources:\n  hpc_jobs: {mapping_dir: /jobs}\n")
	require.NoError(t, err)
	assert.Equal(t, r.OriginOf("HPCJobMappingDir"), r.OriginOf(HPCJobsSwitchField))
}
