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

// makeBindUnbindFieldValue builds a field sample for one GPU bind or unbind event.
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

// useMockDCGM installs a mock as the package DCGM client and restores the prior client.
func useMockDCGM(t *testing.T, mockDCGM *mockdcgm.MockDCGM) {
	t.Helper()
	previous := dcgmprovider.Client()
	dcgmprovider.SetClient(mockDCGM)
	t.Cleanup(func() { dcgmprovider.SetClient(previous) })
}

// TestNewGPUBindUnbindWatcher checks that a new bind and unbind watcher is not nil and polls once a second.
func TestNewGPUBindUnbindWatcher(t *testing.T) {
	w := NewGPUBindUnbindWatcher()

	require.NotNil(t, w)
	assert.Equal(t, time.Second, w.pollInterval)
}

// TestNewGPUBindUnbindWatcherUsesCurrentClientAtStart verifies a restarted
// default watcher installs its next watch on the replacement DCGM client.
func TestNewGPUBindUnbindWatcherUsesCurrentClientAtStart(t *testing.T) {
	ctrl := gomock.NewController(t)
	staleClient := mockdcgm.NewMockDCGM(ctrl)
	currentClient := mockdcgm.NewMockDCGM(ctrl)
	useMockDCGM(t, staleClient)

	w := NewGPUBindUnbindWatcher()
	dcgmprovider.SetClient(currentClient)
	currentClient.EXPECT().
		WatchFieldValue(uint(0), dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, time.Second, time.Duration(0), bindUnbindHistorySamples).
		Return(nil)

	ctx, cancel := context.WithCancel(context.Background())
	run, err := w.Start(ctx, func(dcgm.BindUnbindEventState) {})
	require.NoError(t, err)
	cancel()
	require.ErrorIs(t, run(), context.Canceled)
}

// TestGPUBindUnbindWatcher_WatchUsesDirectGlobalFieldHistory expects Watch to watch the global
// bind event field, read its history from a non-zero cursor, and deliver only the latest state.
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
			timestamp := cursor.UnixMicro()
			return []dcgm.FieldValue_v1{
				makeBindUnbindFieldValue(dcgm.DcgmBUEventStateSystemReinitializing, timestamp),
				makeBindUnbindFieldValue(dcgm.DcgmBUEventStateSystemReinitializationCompleted, timestamp),
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

// TestGPUBindUnbindWatcher_WatchRetriesSameCursorAfterReadError expects the read after a
// failed DCGM read to use the same cursor as the failed one.
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

// TestGPUBindUnbindWatcher_WatchIgnoresEventsBeforeCursor confirms that a stale
// completion event cannot reload the exporter, while events at or after the
// cursor remain eligible for processing.
func TestGPUBindUnbindWatcher_WatchIgnoresEventsBeforeCursor(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	useMockDCGM(t, mockDCGM)

	w := &GPUBindUnbindWatcher{pollInterval: time.Millisecond}
	cursor := time.UnixMicro(1_000_000)
	firstRead := mockDCGM.EXPECT().
		GetMultipleValuesForField(
			uint(0),
			dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
			bindUnbindHistorySamples,
			cursor,
			time.Time{},
		).
		Return([]dcgm.FieldValue_v1{
			makeBindUnbindFieldValue(dcgm.DcgmBUEventStateSystemReinitializationCompleted, cursor.UnixMicro()-1),
			makeBindUnbindFieldValue(dcgm.DcgmBUEventStateSystemReinitializing, cursor.UnixMicro()),
		}, nil)
	mockDCGM.EXPECT().
		GetMultipleValuesForField(
			uint(0),
			dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
			bindUnbindHistorySamples,
			time.UnixMicro(cursor.UnixMicro()+1),
			time.Time{},
		).
		After(firstRead).
		Return([]dcgm.FieldValue_v1{
			makeBindUnbindFieldValue(dcgm.DcgmBUEventStateSystemReinitializationCompleted, cursor.UnixMicro()+1),
		}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []dcgm.BindUnbindEventState
	err := w.watchFieldEvents(ctx, cursor, func(state dcgm.BindUnbindEventState) {
		got = append(got, state)
		cancel()
	})

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []dcgm.BindUnbindEventState{
		dcgm.DcgmBUEventStateSystemReinitializationCompleted,
	}, got)
}

// TestGPUBindUnbindWatcher_WatchReportsUnsupportedSetup expects a failure to watch the bind
// event field to be returned, wrapped with the DCGM and driver requirements.
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

// TestGPUBindUnbindWatcher_WatchDoesNotStartAfterCancellation expects Watch to return
// context.Canceled without calling DCGM when the context is already cancelled.
func TestGPUBindUnbindWatcher_WatchDoesNotStartAfterCancellation(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	useMockDCGM(t, mockDCGM)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewGPUBindUnbindWatcher().Watch(ctx, func(dcgm.BindUnbindEventState) {})

	require.ErrorIs(t, err, context.Canceled)
}

// TestGPUBindUnbindWatcher_DoesNotDeliverEventAfterCancellationDuringRead cancels the context
// while a DCGM read is in progress and expects Watch to return context.Canceled without delivering the event.
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
