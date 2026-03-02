/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package watcher

import (
	"context"
	"errors"
	"testing"
	"time"
	"unsafe"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
)

// Helper function to create a FieldValue_v1 with an int64 value
func makeFieldValueInt64(value int64, ts int64) dcgm.FieldValue_v1 {
	fv := dcgm.FieldValue_v1{
		FieldID:   dcgm.DCGM_FI_BIND_UNBIND_EVENT,
		FieldType: uint(dcgm.DCGM_FT_INT64),
		Status:    0,
		TS:        ts,
	}
	// Write int64 value to the byte array
	*(*int64)(unsafe.Pointer(&fv.Value[0])) = value
	return fv
}

func TestNewGPUBindUnbindWatcher(t *testing.T) {
	tests := []struct {
		name     string
		opts     []GPUBindUnbindWatcherOption
		expected time.Duration
	}{
		{
			name:     "default interval",
			opts:     nil,
			expected: 1 * time.Second,
		},
		{
			name:     "custom interval",
			opts:     []GPUBindUnbindWatcherOption{WithPollInterval(2 * time.Second)},
			expected: 2 * time.Second,
		},
		{
			name:     "custom interval 500ms",
			opts:     []GPUBindUnbindWatcherOption{WithPollInterval(500 * time.Millisecond)},
			expected: 500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewGPUBindUnbindWatcher(tt.opts...)
			require.NotNil(t, w)
			assert.Equal(t, tt.expected, w.pollInterval)
		})
	}
}

func TestGPUBindUnbindWatcher_Watch_FieldGroupCreateError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)
	dcgmprovider.SetClient(mockDCGM)

	mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
	mockNVML.EXPECT().Cleanup().AnyTimes()
	realNVML := nvmlprovider.Client()
	defer nvmlprovider.SetClient(realNVML)
	nvmlprovider.SetClient(mockNVML)

	// Expect FieldGroupCreate to fail
	mockDCGM.EXPECT().
		FieldGroupCreate("dcgm_exporter_bind_unbind_watch", []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return(dcgm.FieldHandle{}, errors.New("field group creation failed"))

	w := NewGPUBindUnbindWatcher()
	ctx := context.Background()
	onChange := func() {}

	err := w.Watch(ctx, onChange)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create bind/unbind field group")
}

func TestGPUBindUnbindWatcher_Watch_NVMLNotAvailable(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)
	dcgmprovider.SetClient(mockDCGM)

	mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
	mockNVML.EXPECT().Cleanup().AnyTimes()
	realNVML := nvmlprovider.Client()
	defer nvmlprovider.SetClient(realNVML)
	nvmlprovider.SetClient(mockNVML)

	// Expect FieldGroupCreate to fail with NVML not available error
	mockDCGM.EXPECT().
		FieldGroupCreate("dcgm_exporter_bind_unbind_watch", []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return(dcgm.FieldHandle{}, errors.New("Cannot perform the requested operation because NVML doesn't exist on this system."))

	w := NewGPUBindUnbindWatcher()
	ctx := context.Background()
	onChange := func() {}

	err := w.Watch(ctx, onChange)
	// Should return nil immediately (graceful degradation - watcher exits cleanly)
	require.NoError(t, err)
}

func TestGPUBindUnbindWatcher_Watch_WatchFieldsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)
	dcgmprovider.SetClient(mockDCGM)

	mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
	mockNVML.EXPECT().Cleanup().AnyTimes()
	realNVML := nvmlprovider.Client()
	defer nvmlprovider.SetClient(realNVML)
	nvmlprovider.SetClient(mockNVML)

	mockFieldGroup := dcgm.FieldHandle{}
	mockFieldGroup.SetHandle(uintptr(123))

	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(uintptr(456))

	// Expect successful field group creation
	mockDCGM.EXPECT().
		FieldGroupCreate("dcgm_exporter_bind_unbind_watch", []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return(mockFieldGroup, nil)

	// Expect GroupAllGPUs
	mockDCGM.EXPECT().
		GroupAllGPUs().
		Return(mockGroupHandle)

	// Expect WatchFieldsWithGroupEx to fail
	mockDCGM.EXPECT().
		WatchFieldsWithGroupEx(mockFieldGroup, mockGroupHandle, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("watch failed"))

	// Expect cleanup
	mockDCGM.EXPECT().
		FieldGroupDestroy(mockFieldGroup).
		Return(nil)

	w := NewGPUBindUnbindWatcher()
	ctx := context.Background()
	onChange := func() {}

	err := w.Watch(ctx, onChange)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to watch bind/unbind events")
}

func TestGPUBindUnbindWatcher_Watch_ContextCancellation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)
	dcgmprovider.SetClient(mockDCGM)

	mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
	mockNVML.EXPECT().Cleanup().AnyTimes()
	realNVML := nvmlprovider.Client()
	defer nvmlprovider.SetClient(realNVML)
	nvmlprovider.SetClient(mockNVML)

	mockFieldGroup := dcgm.FieldHandle{}
	mockFieldGroup.SetHandle(uintptr(123))

	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(uintptr(456))

	// Setup successful initialization
	mockDCGM.EXPECT().
		FieldGroupCreate("dcgm_exporter_bind_unbind_watch", []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return(mockFieldGroup, nil)

	mockDCGM.EXPECT().
		GroupAllGPUs().
		Return(mockGroupHandle)

	mockDCGM.EXPECT().
		WatchFieldsWithGroupEx(mockFieldGroup, mockGroupHandle, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil)

	// Initialization phase: read current state
	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(nil)

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return([]dcgm.FieldValue_v1{}, nil) // No events initially

	// Expect cleanup when context is cancelled
	mockDCGM.EXPECT().
		UnwatchFields(mockFieldGroup, mockGroupHandle).
		Return(nil)

	mockDCGM.EXPECT().
		FieldGroupDestroy(mockFieldGroup).
		Return(nil)

	w := NewGPUBindUnbindWatcher(WithPollInterval(100 * time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	onChange := func() {}
	err := w.Watch(ctx, onChange)

	// Should return context.Canceled error
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestGPUBindUnbindWatcher_Watch_UnbindEventDetected(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)
	dcgmprovider.SetClient(mockDCGM)

	mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
	mockNVML.EXPECT().Cleanup().AnyTimes()
	realNVML := nvmlprovider.Client()
	defer nvmlprovider.SetClient(realNVML)
	nvmlprovider.SetClient(mockNVML)

	mockFieldGroup := dcgm.FieldHandle{}
	mockFieldGroup.SetHandle(uintptr(123))

	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(uintptr(456))

	// Setup successful initialization
	mockDCGM.EXPECT().
		FieldGroupCreate("dcgm_exporter_bind_unbind_watch", []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return(mockFieldGroup, nil)

	mockDCGM.EXPECT().
		GroupAllGPUs().
		Return(mockGroupHandle)

	mockDCGM.EXPECT().
		WatchFieldsWithGroupEx(mockFieldGroup, mockGroupHandle, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil)

	// Initialization phase: read current state (no events)
	initialTimestamp := time.Now().UnixNano()
	noEventValue := makeFieldValueInt64(0, initialTimestamp)

	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(nil)

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return([]dcgm.FieldValue_v1{noEventValue}, nil)

	// First poll after initialization: unbind event with new timestamp
	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(nil)

	// Create a field value with unbind event (newer timestamp)
	eventValue := makeFieldValueInt64(
		int64(dcgm.DcgmBUEventStateSystemReinitializing),
		initialTimestamp+1000000, // 1ms later
	)

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return([]dcgm.FieldValue_v1{eventValue}, nil)

	// After event detection, watcher continues polling until context cancelled
	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(nil).
		AnyTimes()

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return([]dcgm.FieldValue_v1{}, nil).
		AnyTimes()

	// Expect cleanup when context is cancelled
	mockDCGM.EXPECT().
		UnwatchFields(mockFieldGroup, mockGroupHandle).
		Return(nil)

	mockDCGM.EXPECT().
		FieldGroupDestroy(mockFieldGroup).
		Return(nil)

	w := NewGPUBindUnbindWatcher(WithPollInterval(10 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	onChangeCalled := false
	onChange := func() {
		onChangeCalled = true
	}

	err := w.Watch(ctx, onChange)

	// Should return context error after timeout, but onChange should have been called
	require.Error(t, err)
	assert.True(t, onChangeCalled, "onChange should have been called")
}

func TestGPUBindUnbindWatcher_Watch_BindEventDetected(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)
	dcgmprovider.SetClient(mockDCGM)

	mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
	mockNVML.EXPECT().Cleanup().AnyTimes()
	realNVML := nvmlprovider.Client()
	defer nvmlprovider.SetClient(realNVML)
	nvmlprovider.SetClient(mockNVML)

	mockFieldGroup := dcgm.FieldHandle{}
	mockFieldGroup.SetHandle(uintptr(123))

	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(uintptr(456))

	// Setup successful initialization
	mockDCGM.EXPECT().
		FieldGroupCreate("dcgm_exporter_bind_unbind_watch", []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return(mockFieldGroup, nil)

	mockDCGM.EXPECT().
		GroupAllGPUs().
		Return(mockGroupHandle)

	mockDCGM.EXPECT().
		WatchFieldsWithGroupEx(mockFieldGroup, mockGroupHandle, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil)

	// Initialization phase: read current state (no events)
	initialTimestamp := time.Now().UnixNano()
	noEventValue := makeFieldValueInt64(0, initialTimestamp)

	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(nil)

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return([]dcgm.FieldValue_v1{noEventValue}, nil)

	// First poll after initialization: bind event with new timestamp
	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(nil)

	// Create a field value with bind event (newer timestamp)
	eventValue := makeFieldValueInt64(
		int64(dcgm.DcgmBUEventStateSystemReinitializationCompleted),
		initialTimestamp+1000000, // 1ms later
	)

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return([]dcgm.FieldValue_v1{eventValue}, nil)

	// After event detection, watcher continues polling until context cancelled
	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(nil).
		AnyTimes()

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return([]dcgm.FieldValue_v1{}, nil).
		AnyTimes()

	// Expect cleanup when context is cancelled
	mockDCGM.EXPECT().
		UnwatchFields(mockFieldGroup, mockGroupHandle).
		Return(nil)

	mockDCGM.EXPECT().
		FieldGroupDestroy(mockFieldGroup).
		Return(nil)

	w := NewGPUBindUnbindWatcher(WithPollInterval(10 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	onChangeCalled := false
	onChange := func() {
		onChangeCalled = true
	}

	err := w.Watch(ctx, onChange)

	// Should return context error after timeout, but onChange should have been called
	require.Error(t, err)
	assert.True(t, onChangeCalled, "onChange should have been called")
}

func TestGPUBindUnbindWatcher_Watch_UpdateFieldsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)
	dcgmprovider.SetClient(mockDCGM)

	mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
	mockNVML.EXPECT().Cleanup().AnyTimes()
	realNVML := nvmlprovider.Client()
	defer nvmlprovider.SetClient(realNVML)
	nvmlprovider.SetClient(mockNVML)

	mockFieldGroup := dcgm.FieldHandle{}
	mockFieldGroup.SetHandle(uintptr(123))

	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(uintptr(456))

	// Setup successful initialization
	mockDCGM.EXPECT().
		FieldGroupCreate("dcgm_exporter_bind_unbind_watch", []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return(mockFieldGroup, nil)

	mockDCGM.EXPECT().
		GroupAllGPUs().
		Return(mockGroupHandle)

	mockDCGM.EXPECT().
		WatchFieldsWithGroupEx(mockFieldGroup, mockGroupHandle, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil)

	// First update fails, second succeeds with event
	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(errors.New("update failed"))

	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(nil)

	// Create event value
	eventValue := makeFieldValueInt64(
		int64(dcgm.DcgmBUEventStateSystemReinitializing),
		time.Now().UnixNano(),
	)

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return([]dcgm.FieldValue_v1{eventValue}, nil)

	// After event detection, watcher continues polling until context cancelled
	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(nil).
		AnyTimes()

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return([]dcgm.FieldValue_v1{}, nil).
		AnyTimes()

	// Expect cleanup
	mockDCGM.EXPECT().
		UnwatchFields(mockFieldGroup, mockGroupHandle).
		Return(nil)

	mockDCGM.EXPECT().
		FieldGroupDestroy(mockFieldGroup).
		Return(nil)

	w := NewGPUBindUnbindWatcher(WithPollInterval(10 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	onChangeCalled := false
	onChange := func() {
		onChangeCalled = true
	}

	err := w.Watch(ctx, onChange)

	require.Error(t, err)
	assert.True(t, onChangeCalled)
}

func TestGPUBindUnbindWatcher_Watch_NoEventsAvailable(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)
	dcgmprovider.SetClient(mockDCGM)

	mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
	mockNVML.EXPECT().Cleanup().AnyTimes()
	realNVML := nvmlprovider.Client()
	defer nvmlprovider.SetClient(realNVML)
	nvmlprovider.SetClient(mockNVML)

	mockFieldGroup := dcgm.FieldHandle{}
	mockFieldGroup.SetHandle(uintptr(123))

	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(uintptr(456))

	// Setup
	mockDCGM.EXPECT().
		FieldGroupCreate("dcgm_exporter_bind_unbind_watch", []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return(mockFieldGroup, nil)

	mockDCGM.EXPECT().
		GroupAllGPUs().
		Return(mockGroupHandle)

	mockDCGM.EXPECT().
		WatchFieldsWithGroupEx(mockFieldGroup, mockGroupHandle, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil)

	// Multiple polls until context cancelled
	mockDCGM.EXPECT().
		UpdateAllFields().
		Return(nil).
		AnyTimes()

	mockDCGM.EXPECT().
		EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT}).
		Return([]dcgm.FieldValue_v1{}, nil).
		AnyTimes()

	mockDCGM.EXPECT().
		UnwatchFields(mockFieldGroup, mockGroupHandle).
		Return(nil)

	mockDCGM.EXPECT().
		FieldGroupDestroy(mockFieldGroup).
		Return(nil)

	w := NewGPUBindUnbindWatcher(WithPollInterval(50 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	onChange := func() {}
	err := w.Watch(ctx, onChange)

	// Should return context error (deadline exceeded or canceled)
	require.Error(t, err)
}
