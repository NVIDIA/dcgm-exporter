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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settingOf returns the setting for field and fails the test when there is none.
func settingOf(t *testing.T, settings []SettingKey, field string) SettingKey {
	t.Helper()
	for _, setting := range settings {
		if setting.Field == field {
			return setting
		}
	}

	require.Failf(t, "missing setting", "no setting %s", field)
	return SettingKey{}
}

func TestSettingsListEachFieldOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, setting := range SettingKeys(DefaultConfig(), ConfigNames{}) {
		assert.False(t, seen[setting.Field], "%s is listed twice", setting.Field)
		seen[setting.Field] = true
		assert.True(t, setting.Flag != "" || setting.Key != "" || setting.Field == "", "%s has neither a flag nor a key", setting.Field)
	}

	assert.True(t, seen[HPCJobsSwitchField])
}

func TestSettingsFollowTheMetricSource(t *testing.T) {
	config := DefaultConfig()
	file := settingOf(t, SettingKeys(config, ConfigNames{}), "MetricSource")
	assert.Equal(t, SettingKey{Field: "MetricSource", Flag: FlagFieldsFile, Key: "metrics[0].file"}, file)

	config.MetricSource = MetricSource{Kind: MetricSourceInline}
	assert.Equal(t, "metrics[0].fields", settingOf(t, SettingKeys(config, ConfigNames{}), "MetricSource").Key)

	config.MetricSource = MetricSource{Kind: MetricSourceConfigMap}
	configMap := settingOf(t, SettingKeys(config, ConfigNames{}), "MetricSource")
	assert.Equal(t, FlagConfigMapData, configMap.Flag)
	assert.Empty(t, configMap.Key, "version 2 has no key for a ConfigMap source")
}

func TestSettingsUseTheOutputName(t *testing.T) {
	assert.Equal(t, "outputs.scrape.listen", settingOf(t, SettingKeys(DefaultConfig(), ConfigNames{Output: "scrape"}), "Address").Key)
	assert.Equal(t, "outputs."+DefaultOutputName+".listen", settingOf(t, SettingKeys(DefaultConfig(), ConfigNames{}), "Address").Key)
}

func TestSettingsListWatchGroupKeysAndOnlyTheRetentionThatIsSet(t *testing.T) {
	age := DefaultWatchRetention().MaxAge
	config := DefaultConfig()
	config.WatchGroups = []WatchGroup{
		{Name: "temps", Interval: 1000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}, Retention: WatchRetentionOverride{MaxAge: &age}},
		{Name: "metrics[0].overrides[1]", Interval: 2000, Fields: []string{"DCGM_FI_DEV_SM_CLOCK"}},
	}

	settings := SettingKeys(config, ConfigNames{})
	assert.NotEmpty(t, settingOf(t, settings, "WatchGroups[0].Name").Key)
	assert.NotEmpty(t, settingOf(t, settings, "WatchGroups[0].Retention.MaxAge").Key)
	for _, setting := range settings {
		assert.NotEqual(t, "WatchGroups[0].Retention.MaxSamples", setting.Field)
		assert.NotEqual(t, "WatchGroups[1].Name", setting.Field, "an unnamed override has no name key")
	}

	assert.NotEmpty(t, settingOf(t, settings, "WatchGroups[1].Fields").Key)
}

func TestSettingsPutCountWindowsAtTheirOverride(t *testing.T) {
	config := DefaultConfig()
	config.WatchGroups = []WatchGroup{{Name: "temps", Interval: 1000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}}}
	config.ClockEventsCountWindowSize = 1000

	settings := SettingKeys(config, ConfigNames{})
	assert.Equal(t, FlagXIDCountWindowSize, settingOf(t, settings, "XIDCountWindowSize").Flag)
	assert.Empty(t, settingOf(t, settings, "XIDCountWindowSize").Key, "a window at its default has no override")
	assert.Equal(t, "metrics[0].overrides[1].window", settingOf(t, settings, "ClockEventsCountWindowSize").Key,
		"the window override follows the watch group overrides")

	config.XIDCountWindowSize = 2000
	settings = SettingKeys(config, ConfigNames{})
	assert.Equal(t, "metrics[0].overrides[1].window", settingOf(t, settings, "XIDCountWindowSize").Key)
	assert.Equal(t, "metrics[0].overrides[2].window", settingOf(t, settings, "ClockEventsCountWindowSize").Key)
}
