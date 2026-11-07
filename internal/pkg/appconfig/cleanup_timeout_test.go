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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDCGMCleanupTimeoutDefault checks the default is five minutes.
func TestDCGMCleanupTimeoutDefault(t *testing.T) {
	assert.Equal(t, 5*time.Minute, DefaultConfig().DCGMCleanupTimeout)
}

// TestDCGMCleanupTimeoutFromFile checks a version 2 file sets the timeout and
// records its key, and that a value that is not positive or not a duration is
// rejected at that key.
func TestDCGMCleanupTimeoutFromFile(t *testing.T) {
	tests := []struct {
		value   string
		want    time.Duration
		wantErr string
	}{
		{value: "90s", want: 90 * time.Second},
		{value: "1500ms", want: 1500 * time.Millisecond},
		{value: "0s", wantErr: "sources.dcgm.cleanup_timeout: must be greater than 0"},
		{value: "-1s", wantErr: "sources.dcgm.cleanup_timeout: must be greater than 0"},
		{value: "soon", wantErr: `sources.dcgm.cleanup_timeout: invalid duration "soon"`},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			y, err := ParseYAMLConfig([]byte("version: 2\nsources: {dcgm: {cleanup_timeout: " + tt.value + "}}\n"))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)

			config := DefaultConfig()
			require.NoError(t, y.ApplyTo(config))

			assert.Equal(t, tt.want, config.DCGMCleanupTimeout)
			assert.Equal(t, DCGMCleanupTimeoutKey, y.SettingOrigins()[FieldDCGMCleanupTimeout])
		})
	}
}

// TestDCGMCleanupTimeoutFileWithoutKeyKeepsTheValue checks a file that leaves
// the key out keeps the value it is applied over.
func TestDCGMCleanupTimeoutFileWithoutKeyKeepsTheValue(t *testing.T) {
	y, err := ParseYAMLConfig([]byte("version: 2\nsources: {dcgm: {hostengine: \"\"}}\n"))
	require.NoError(t, err)
	config := DefaultConfig()
	config.DCGMCleanupTimeout = time.Minute

	require.NoError(t, y.ApplyTo(config))

	assert.Equal(t, time.Minute, config.DCGMCleanupTimeout)
	assert.NotContains(t, y.SettingOrigins(), FieldDCGMCleanupTimeout)
}

// TestValidateValuesChecksTheDCGMCleanupTimeout checks the resolved timeout
// from any input must be positive, and the error names the input that set it.
func TestValidateValuesChecksTheDCGMCleanupTimeout(t *testing.T) {
	origin := func(field string) string {
		if field == FieldDCGMCleanupTimeout {
			return "environment: DCGM_EXPORTER_DCGM_CLEANUP_TIMEOUT"
		}

		return "default"
	}

	tests := []struct {
		name    string
		timeout time.Duration
		wantErr string
	}{
		{name: "positive", timeout: time.Minute},
		{
			name: "zero", timeout: 0, wantErr: `invalid dcgm-cleanup-timeout parameter value: "0s", ` +
				"must be greater than 0; set by environment: DCGM_EXPORTER_DCGM_CLEANUP_TIMEOUT",
		},
		{
			name: "negative", timeout: -time.Second, wantErr: `invalid dcgm-cleanup-timeout parameter value: "-1s", ` +
				"must be greater than 0; set by environment: DCGM_EXPORTER_DCGM_CLEANUP_TIMEOUT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			config.DCGMCleanupTimeout = tt.timeout

			errs := ValidateValues(config, origin)

			if tt.wantErr == "" {
				assert.Empty(t, errs)
				return
			}

			require.Len(t, errs, 1)
			assert.EqualError(t, errs[0], tt.wantErr)
			var settingErr *SettingError
			require.True(t, errors.As(errs[0], &settingErr))
			assert.Equal(t, FieldDCGMCleanupTimeout, settingErr.Field)
		})
	}
}

// TestFullConfigWritesTheDCGMCleanupTimeout checks the full config writes the
// timeout under sources.dcgm.
func TestFullConfigWritesTheDCGMCleanupTimeout(t *testing.T) {
	config := DefaultConfig()
	config.DCGMCleanupTimeout = 90 * time.Second

	full := NewFullConfig(config, ConfigNames{})

	assert.Equal(t, "1m30s", full.Sources.DCGM.CleanupTimeout)
}

// TestEffectiveDCGMCleanupTimeout checks a positive timeout is used as
// configured, and that a nil config or one without a positive timeout falls
// back to the default and says it is not configured.
func TestEffectiveDCGMCleanupTimeout(t *testing.T) {
	tests := []struct {
		name           string
		config         *Config
		want           time.Duration
		wantConfigured bool
	}{
		{name: "configured", config: &Config{DCGMCleanupTimeout: 7 * time.Minute}, want: 7 * time.Minute, wantConfigured: true},
		{name: "nil config", want: DefaultDCGMCleanupTimeout},
		{name: "zero", config: &Config{}, want: DefaultDCGMCleanupTimeout},
		{name: "negative", config: &Config{DCGMCleanupTimeout: -time.Second}, want: DefaultDCGMCleanupTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, configured := tt.config.EffectiveDCGMCleanupTimeout()

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantConfigured, configured)
		})
	}
}
