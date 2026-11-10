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
package configinspect

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// inlineMetrics is a metric entry with definitions written in the file, so a
// test needs no metric file.
const inlineMetrics = `version: 2
metrics:
  - name: gpu
    fields:
      - {name: DCGM_FI_DEV_GPU_TEMP, prometheus_type: gauge, help: Temperature.}
      - {name: DCGM_FI_DEV_SM_CLOCK, prometheus_type: gauge, help: Clock.}
      - {name: DCGM_EXP_XID_ERRORS_COUNT, prometheus_type: gauge, help: XID errors.}
      - {name: DCGM_EXP_CLOCK_EVENTS_COUNT, prometheus_type: gauge, help: Clock events.}
`

const outputsBlock = "outputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n"

// resolvedFrom resolves a config file the way startup does for a file with no
// flags or environment: the file's own origins on top of the defaults.
func resolvedFrom(t *testing.T, document string) *appconfig.Resolved {
	t.Helper()
	y, err := appconfig.ParseYAMLConfig([]byte(document))
	require.NoError(t, err)
	r := &appconfig.Resolved{Config: appconfig.DefaultConfig(), Origins: map[string]appconfig.SettingOrigin{}}
	require.NoError(t, r.ApplyConfigFile(y, appconfig.SettingOrigin{Kind: appconfig.OriginDefault}))
	r.Config.ConfigFile = "exporter.yaml"

	// Selecting the file by PATH names the config file's own origin after the
	// file's layer was recorded.
	r.Origins["ConfigFile"] = appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "PATH"}
	r.ReplacedEnv = map[string][]appconfig.EnvValue{}

	return r
}

// resolvedDefaults is the configuration of a run with no file.
func resolvedDefaults() *appconfig.Resolved {
	return &appconfig.Resolved{
		Config:  appconfig.DefaultConfig(),
		Origins: map[string]appconfig.SettingOrigin{},
	}
}
