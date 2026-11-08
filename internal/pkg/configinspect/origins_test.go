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

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestOriginSourceDescription(t *testing.T) {
	source := settingSource{Kind: appconfig.OriginFile, File: "c.yaml", Line: 3}
	assert.Equal(t, "file c.yaml:3", source.describe())
	assert.Equal(t, "file c.yaml", settingSource{Kind: appconfig.OriginFile, File: "c.yaml"}.describe())
	assert.Equal(t, "env X", settingSource{Kind: appconfig.OriginEnv, Name: "X"}.describe())
}

func TestSourceOfLocatesFileOriginsAndSkipsTheirPositionWithoutAFile(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+"    every: 2s\n"+outputsBlock)
	source := sourceOf(r, appconfig.SettingOrigin{Kind: appconfig.OriginFile, Key: "metrics[0].every"})
	assert.Equal(t, appconfig.OriginFile, source.Kind)
	assert.Equal(t, "exporter.yaml", source.File)
	assert.Equal(t, "metrics[0].every", source.Path)
	assert.Positive(t, source.Line)
	assert.Positive(t, source.Column)

	assert.Equal(t, settingSource{Kind: appconfig.OriginFile, File: "exporter.yaml"},
		sourceOf(r, appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "PATH"}), "selecting the file by PATH reports the file")

	assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--x"},
		sourceOf(r, appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--x"}))

	assert.Equal(t, settingSource{Kind: appconfig.OriginEnv, Name: "X"},
		sourceOf(r, appconfig.SettingOrigin{Kind: appconfig.OriginEnv, Name: "X"}))

	assert.Equal(t, settingSource{Kind: appconfig.OriginDerived},
		sourceOf(r, appconfig.SettingOrigin{Kind: appconfig.OriginDerived, From: "argv: --x"}))

	assert.Equal(t, settingSource{Kind: appconfig.OriginDefault}, sourceOf(r, appconfig.SettingOrigin{Kind: appconfig.OriginDefault}))
}

func TestForProblemDropsSourcesThatNoInputSet(t *testing.T) {
	assert.Equal(t, settingSource{}, settingSource{Kind: appconfig.OriginDefault}.forProblem())
	assert.Equal(t, settingSource{}, settingSource{Kind: appconfig.OriginDerived}.forProblem())
	flag := settingSource{Kind: appconfig.OriginFlag, Name: "--x"}
	assert.Equal(t, flag, flag.forProblem())
}
