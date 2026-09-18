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

package appconfig_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestConfigCloneDeepCopiesWatchRetentionOverrides verifies cloned configs can be mutated without changing the source.
func TestConfigCloneDeepCopiesWatchRetentionOverrides(t *testing.T) {
	maxAge := time.Minute
	maxSamples := int64(2)
	config := &appconfig.Config{
		WatchGroups: []appconfig.WatchGroup{
			{
				Name: "latest-values",
				Retention: appconfig.WatchRetentionOverride{
					MaxAge:     &maxAge,
					MaxSamples: &maxSamples,
				},
			},
		},
	}

	clone := config.Clone()

	require.NotNil(t, clone)
	require.NotSame(t, config.WatchGroups[0].Retention.MaxAge, clone.WatchGroups[0].Retention.MaxAge)
	require.NotSame(t, config.WatchGroups[0].Retention.MaxSamples, clone.WatchGroups[0].Retention.MaxSamples)
	*clone.WatchGroups[0].Retention.MaxAge = 2 * time.Minute
	*clone.WatchGroups[0].Retention.MaxSamples = 4
	assert.Equal(t, time.Minute, *config.WatchGroups[0].Retention.MaxAge)
	assert.Equal(t, int64(2), *config.WatchGroups[0].Retention.MaxSamples)
}

func TestConfigCloneRetainsRuntimeDRAResourceSliceCallback(t *testing.T) {
	var calls int
	config := &appconfig.Config{}
	config.SetDRAResourceSliceChangeCallback(func() { calls++ })

	clone := config.Clone()
	callback := clone.DRAResourceSliceChangeCallback()

	require.NotNil(t, callback)
	callback()
	assert.Equal(t, 1, calls)
}

// TestWatchRetentionValidate covers every bound accepted or rejected before a retention policy reaches DCGM.
func TestWatchRetentionValidate(t *testing.T) {
	tests := []struct {
		name    string
		value   appconfig.WatchRetention
		wantErr string
	}{
		{name: "default", value: appconfig.DefaultWatchRetention()},
		{name: "sample bounded", value: appconfig.WatchRetention{MaxSamples: 2}},
		{name: "both bounds", value: appconfig.WatchRetention{MaxAge: time.Minute, MaxSamples: 2}},
		{name: "unbounded", value: appconfig.WatchRetention{}, wantErr: "cannot both be zero"},
		{name: "negative age", value: appconfig.WatchRetention{MaxAge: -time.Second}, wantErr: "maxAge"},
		{name: "negative samples", value: appconfig.WatchRetention{MaxAge: time.Second, MaxSamples: -1}, wantErr: "maxSamples"},
		{name: "samples overflow", value: appconfig.WatchRetention{MaxAge: time.Second, MaxSamples: 2147483648}, wantErr: "2147483647"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.value.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestWatchRetentionDCGMMaxKeepSamples verifies validated sample bounds convert safely to the DCGM int32 type.
func TestWatchRetentionDCGMMaxKeepSamples(t *testing.T) {
	tests := []struct {
		name    string
		value   appconfig.WatchRetention
		want    int32
		wantErr string
	}{
		{name: "unlimited", value: appconfig.DefaultWatchRetention()},
		{name: "bounded", value: appconfig.WatchRetention{MaxSamples: 2}, want: 2},
		{
			name:    "overflow",
			value:   appconfig.WatchRetention{MaxAge: time.Second, MaxSamples: 2147483648},
			wantErr: "2147483647",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.value.DCGMMaxKeepSamples()
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
