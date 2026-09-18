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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

func makeBindUnbindFieldValue(state dcgm.BindUnbindEventState, ts int64) dcgm.FieldValue_v1 {
	value := dcgm.FieldValue_v1{
		FieldID:   dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
		FieldType: uint(dcgm.DCGM_FT_INT64),
		Status:    0,
		TS:        ts,
	}
	*(*int64)(unsafe.Pointer(&value.Value[0])) = int64(state)
	return value
}

func useMockDCGM(t *testing.T, mockDCGM *mockdcgm.MockDCGM) {
	t.Helper()
	previous := dcgmprovider.Client()
	dcgmprovider.SetClient(mockDCGM)
	t.Cleanup(func() { dcgmprovider.SetClient(previous) })
}

func TestNewGPUBindUnbindWatcher(t *testing.T) {
	w := NewGPUBindUnbindWatcher()

	require.NotNil(t, w)
	assert.Equal(t, time.Second, w.pollInterval)
}

func TestGPUBindUnbindWatcher_WatchUsesDirectGlobalFieldHistory(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	useMockDCGM(t, mockDCGM)

	w := &GPUBindUnbindWatcher{pollInterval: time.Millisecond}
	watch := mockDCGM.EXPECT().
		WatchFieldValue(
			uint(0),
			dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
			time.Millisecond,
			time.Duration(0),
			bindUnbindHistorySamples,
		).
		Return(nil)

	mockDCGM.EXPECT().
		GetMultipleValuesForField(
			uint(0),
			dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
			bindUnbindHistorySamples,
			gomock.Any(),
			time.Time{},
		).
		After(watch).
		DoAndReturn(func(_ uint, _ dcgm.Short, _ int, cursor, _ time.Time) ([]dcgm.FieldValue_v1, error) {
			assert.False(t, cursor.IsZero())
			now := time.Now().UnixMicro()
			return []dcgm.FieldValue_v1{
				makeBindUnbindFieldValue(dcgm.DcgmBUEventStateSystemReinitializing, now),
				makeBindUnbindFieldValue(dcgm.DcgmBUEventStateSystemReinitializationCompleted, now+1),
			}, nil
		})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []dcgm.BindUnbindEventState
	err := w.Watch(ctx, func(state dcgm.BindUnbindEventState) {
		got = append(got, state)
		cancel()
	})

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []dcgm.BindUnbindEventState{
		dcgm.DcgmBUEventStateSystemReinitializationCompleted,
	}, got)
}

func TestGPUBindUnbindWatcher_WatchRetriesSameCursorAfterReadError(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	useMockDCGM(t, mockDCGM)

	w := &GPUBindUnbindWatcher{pollInterval: time.Millisecond}
	watch := mockDCGM.EXPECT().
		WatchFieldValue(uint(0), dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, time.Millisecond, time.Duration(0), bindUnbindHistorySamples).
		Return(nil)

	var cursor time.Time
	firstRead := mockDCGM.EXPECT().
		GetMultipleValuesForField(uint(0), dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, bindUnbindHistorySamples, gomock.Any(), time.Time{}).
		After(watch).
		DoAndReturn(func(_ uint, _ dcgm.Short, _ int, gotCursor, _ time.Time) ([]dcgm.FieldValue_v1, error) {
			cursor = gotCursor
			return nil, errors.New("temporary DCGM read failure")
		})
	mockDCGM.EXPECT().
		GetMultipleValuesForField(uint(0), dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, bindUnbindHistorySamples, gomock.Any(), time.Time{}).
		After(firstRead).
		DoAndReturn(func(_ uint, _ dcgm.Short, _ int, gotCursor, _ time.Time) ([]dcgm.FieldValue_v1, error) {
			assert.Equal(t, cursor, gotCursor)
			return []dcgm.FieldValue_v1{
				makeBindUnbindFieldValue(dcgm.DcgmBUEventStateSystemReinitializationCompleted, time.Now().UnixMicro()),
			}, nil
		})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := w.Watch(ctx, func(dcgm.BindUnbindEventState) { cancel() })

	require.ErrorIs(t, err, context.Canceled)
}

func TestGPUBindUnbindWatcher_WatchReportsUnsupportedSetup(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	useMockDCGM(t, mockDCGM)

	setupErr := errors.New("field not supported")
	mockDCGM.EXPECT().
		WatchFieldValue(uint(0), dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, time.Second, time.Duration(0), bindUnbindHistorySamples).
		Return(setupErr)

	err := NewGPUBindUnbindWatcher().Watch(context.Background(), func(dcgm.BindUnbindEventState) {})

	require.ErrorIs(t, err, setupErr)
	assert.Contains(t, err.Error(), "requires DCGM detached-GPU support and NVIDIA driver 590+")
}

func TestGPUBindUnbindWatcher_WatchDoesNotStartAfterCancellation(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	useMockDCGM(t, mockDCGM)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewGPUBindUnbindWatcher().Watch(ctx, func(dcgm.BindUnbindEventState) {})

	require.ErrorIs(t, err, context.Canceled)
}

func TestGPUBindUnbindWatcher_DoesNotDeliverEventAfterCancellationDuringRead(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	useMockDCGM(t, mockDCGM)

	w := &GPUBindUnbindWatcher{pollInterval: time.Millisecond}
	mockDCGM.EXPECT().
		WatchFieldValue(uint(0), dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, time.Millisecond, time.Duration(0), bindUnbindHistorySamples).
		Return(nil)
	readStarted := make(chan struct{})
	allowRead := make(chan struct{})
	mockDCGM.EXPECT().
		GetMultipleValuesForField(
			uint(0),
			dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
			bindUnbindHistorySamples,
			gomock.Any(),
			time.Time{},
		).
		DoAndReturn(func(uint, dcgm.Short, int, time.Time, time.Time) ([]dcgm.FieldValue_v1, error) {
			close(readStarted)
			<-allowRead
			return []dcgm.FieldValue_v1{
				makeBindUnbindFieldValue(
					dcgm.DcgmBUEventStateSystemReinitializationCompleted,
					time.Now().UnixMicro(),
				),
			}, nil
		})

	ctx, cancel := context.WithCancel(context.Background())
	callbackCalled := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- w.Watch(ctx, func(dcgm.BindUnbindEventState) { callbackCalled <- struct{}{} })
	}()
	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("watcher did not begin its DCGM read")
	}
	cancel()
	close(allowRead)

	require.ErrorIs(t, <-done, context.Canceled)
	select {
	case <-callbackCalled:
		t.Fatal("watcher delivered an event after cancellation")
	default:
	}
}
