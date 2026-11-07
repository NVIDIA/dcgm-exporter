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
		KubernetesPodLabelAllowlistRegex: []string{"^team$"},
		NvidiaResourceNames:              []string{"example.com/gpu"},
		MetricSource: appconfig.MetricSource{
			Fields: []appconfig.MetricField{{Name: "DCGM_FI_DEV_GPU_TEMP"}},
		},
		WatchGroups: []appconfig.WatchGroup{
			{
				Name:     "latest-values",
				Interval: 2,
				Fields:   []string{"DCGM_FI_DEV_GPU_TEMP"},
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
	clone.KubernetesPodLabelAllowlistRegex[0] = "^service$"
	clone.NvidiaResourceNames[0] = "example.com/custom-gpu"
	clone.MetricSource.Fields[0].Name = "DCGM_FI_DEV_GPU_UTIL"
	clone.WatchGroups[0].Name = "changed"
	clone.WatchGroups[0].Interval = 3
	clone.WatchGroups[0].Fields[0] = "DCGM_FI_DEV_GPU_UTIL"
	assert.Equal(t, time.Minute, *config.WatchGroups[0].Retention.MaxAge)
	assert.Equal(t, int64(2), *config.WatchGroups[0].Retention.MaxSamples)
	assert.Equal(t, []string{"^team$"}, config.KubernetesPodLabelAllowlistRegex)
	assert.Equal(t, []string{"example.com/gpu"}, config.NvidiaResourceNames)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", config.MetricSource.Fields[0].Name)
	assert.Equal(t, "latest-values", config.WatchGroups[0].Name)
	assert.Equal(t, 2, config.WatchGroups[0].Interval)
	assert.Equal(t, []string{"DCGM_FI_DEV_GPU_TEMP"}, config.WatchGroups[0].Fields)
}

// TestCloneOTLPCollections checks that sources and retention overrides belong to each snapshot.
func TestCloneOTLPCollections(t *testing.T) {
	age := time.Minute
	samples := int64(2)
	tests := []struct {
		name      string
		retention appconfig.WatchRetentionOverride
	}{
		{name: "inherited retention"},
		{name: "explicit retention", retention: appconfig.WatchRetentionOverride{MaxAge: &age, MaxSamples: &samples}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &appconfig.Config{OTLPWatchCollections: []appconfig.OTLPWatchCollection{{
				Name: "gpu", Interval: 5000, Retention: tt.retention,
				Sources: []appconfig.WatchSource{{Source: appconfig.Source{Name: "original"}, Entity: appconfig.EntityGPU}},
			}}}
			clone := config.Clone()
			require.Equal(t, config.OTLPWatchCollections, clone.OTLPWatchCollections)
			clone.OTLPWatchCollections[0].Name = "changed"
			clone.OTLPWatchCollections[0].Interval = 30000
			clone.OTLPWatchCollections[0].Sources[0].Source.Name = "changed"
			clone.OTLPWatchCollections[0].Sources[0].Entity = appconfig.EntityExporter
			if tt.retention.MaxAge != nil {
				*clone.OTLPWatchCollections[0].Retention.MaxAge = time.Hour
				assert.Equal(t, time.Minute, *config.OTLPWatchCollections[0].Retention.MaxAge)
			}
			if tt.retention.MaxSamples != nil {
				*clone.OTLPWatchCollections[0].Retention.MaxSamples = 99
				assert.Equal(t, int64(2), *config.OTLPWatchCollections[0].Retention.MaxSamples)
			}
			assert.Equal(t, "gpu", config.OTLPWatchCollections[0].Name)
			assert.Equal(t, 5000, config.OTLPWatchCollections[0].Interval)
			assert.Equal(t, "original", config.OTLPWatchCollections[0].Sources[0].Source.Name)
			assert.Equal(t, appconfig.EntityGPU, config.OTLPWatchCollections[0].Sources[0].Entity)
		})
	}
}

// TestConfigCloneDeepCopiesDeviceSelectors verifies reload snapshots own every
// selector range, including all three collector families that share the same
// DeviceOptions shape.
func TestConfigCloneDeepCopiesDeviceSelectors(t *testing.T) {
	config := &appconfig.Config{
		GPUDeviceOptions:    appconfig.DeviceOptions{MajorRange: []int{1}, MinorRange: []int{2}},
		SwitchDeviceOptions: appconfig.DeviceOptions{MajorRange: []int{3}, MinorRange: []int{4}},
		CPUDeviceOptions:    appconfig.DeviceOptions{MajorRange: []int{5}, MinorRange: []int{6}},
	}

	clone := config.Clone()

	clone.GPUDeviceOptions.MajorRange[0] = 10
	clone.GPUDeviceOptions.MinorRange[0] = 11
	clone.SwitchDeviceOptions.MajorRange[0] = 19
	clone.SwitchDeviceOptions.MinorRange[0] = 20
	clone.CPUDeviceOptions.MajorRange[0] = 30
	clone.CPUDeviceOptions.MinorRange[0] = 31
	assert.Equal(t, []int{1}, config.GPUDeviceOptions.MajorRange)
	assert.Equal(t, []int{2}, config.GPUDeviceOptions.MinorRange)
	assert.Equal(t, []int{3}, config.SwitchDeviceOptions.MajorRange)
	assert.Equal(t, []int{4}, config.SwitchDeviceOptions.MinorRange)
	assert.Equal(t, []int{5}, config.CPUDeviceOptions.MajorRange)
	assert.Equal(t, []int{6}, config.CPUDeviceOptions.MinorRange)
}

// TestConfigClonePreservesNilAndEmptySlices keeps explicit empty values distinct from unset values.
func TestConfigClonePreservesNilAndEmptySlices(t *testing.T) {
	config := &appconfig.Config{
		KubernetesPodLabelAllowlistRegex: []string{},
		NvidiaResourceNames:              []string{},
		GPUDeviceOptions: appconfig.DeviceOptions{
			MajorRange: []int{},
			MinorRange: []int{},
		},
		SwitchDeviceOptions: appconfig.DeviceOptions{
			MajorRange: []int{},
			MinorRange: []int{},
		},
		CPUDeviceOptions: appconfig.DeviceOptions{
			MajorRange: []int{},
			MinorRange: []int{},
		},
		MetricSource: appconfig.MetricSource{Fields: []appconfig.MetricField{}},
		WatchGroups:  []appconfig.WatchGroup{{Fields: []string{}}},
	}

	clone := config.Clone()

	assert.NotNil(t, clone.KubernetesPodLabelAllowlistRegex)
	assert.NotNil(t, clone.NvidiaResourceNames)
	assert.NotNil(t, clone.GPUDeviceOptions.MajorRange)
	assert.NotNil(t, clone.GPUDeviceOptions.MinorRange)
	assert.NotNil(t, clone.SwitchDeviceOptions.MajorRange)
	assert.NotNil(t, clone.SwitchDeviceOptions.MinorRange)
	assert.NotNil(t, clone.CPUDeviceOptions.MajorRange)
	assert.NotNil(t, clone.CPUDeviceOptions.MinorRange)
	assert.NotNil(t, clone.MetricSource.Fields)
	require.Len(t, clone.WatchGroups, 1)
	assert.NotNil(t, clone.WatchGroups[0].Fields)

	assert.Nil(t, (&appconfig.Config{}).Clone().MetricSource.Fields)
	assert.Nil(t, (&appconfig.Config{}).Clone().WatchGroups)
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
