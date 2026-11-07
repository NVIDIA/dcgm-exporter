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

package dcgmprovider

import (
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nativeFieldKey struct {
	gpu   uint
	field dcgm.Short
}

type nativeWatchPolicy struct {
	interval int64
	age      float64
	samples  int
}

// nativeWatchFake models DCGM's connection-owned watches: two group handles
// covering the same entity and field replace one native owner, and unwatching
// either group removes that owner. A failed watch also unwatches its fields.
type nativeWatchFake struct {
	DCGM
	mu              sync.Mutex
	groups          map[uintptr][]uint
	fields          map[uintptr][]dcgm.Short
	watched         map[nativeFieldKey]nativeWatchPolicy
	failNext        error
	failGroup       uintptr
	keepFailedWatch bool
	clearOnUnwatch  bool
	watchCalls      int
	unwatchCalls    int
	refreshCalls    int
	unwatchErr      error
	onUnwatch       func()
}

// newNativeWatchFake returns a fake with two overlapping GPU groups (1 and 2)
// and their field groups.
func newNativeWatchFake() *nativeWatchFake {
	return &nativeWatchFake{
		groups:  map[uintptr][]uint{1: {0, 1}, 2: {1, 2}},
		fields:  map[uintptr][]dcgm.Short{1: {dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, dcgm.DCGM_FI_DEV_XID_ERRORS}, 2: {dcgm.DCGM_FI_DEV_XID_ERRORS, dcgm.DCGM_FI_DEV_GPU_TEMP}},
		watched: make(map[nativeFieldKey]nativeWatchPolicy),
	}
}

func watchTestHandles(id uintptr) (dcgm.FieldHandle, dcgm.GroupHandle) {
	var fields dcgm.FieldHandle
	var group dcgm.GroupHandle
	fields.SetHandle(id)
	group.SetHandle(id)
	return fields, group
}

func (n *nativeWatchFake) keys(fields dcgm.FieldHandle, group dcgm.GroupHandle) []nativeFieldKey {
	var keys []nativeFieldKey
	for _, gpu := range n.groups[group.GetHandle()] {
		for _, field := range n.fields[fields.GetHandle()] {
			key := nativeFieldKey{gpu: gpu, field: field}
			if field == dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT {
				key.gpu = 0
			}
			keys = append(keys, key)
		}
	}
	return keys
}

// WatchFieldsWithGroupExNoRefresh records the policy for every (entity,
// field) pair of the group and field group, replacing any earlier owner. It
// fails when failNext is set or group is failGroup, and then drops the pairs
// again unless keepFailedWatch is set.
func (n *nativeWatchFake) WatchFieldsWithGroupExNoRefresh(fields dcgm.FieldHandle, group dcgm.GroupHandle, interval int64, age float64, samples int32) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.watchCalls++
	for _, key := range n.keys(fields, group) {
		n.watched[key] = nativeWatchPolicy{interval, age, int(samples)}
	}

	if n.failNext != nil || n.failGroup == group.GetHandle() {
		if !n.keepFailedWatch {
			for _, key := range n.keys(fields, group) {
				delete(n.watched, key)
			}
		}
		err := n.failNext
		n.failNext = nil
		if err == nil {
			err = errors.New("native watch failed")
		}
		return err
	}
	return nil
}

func (n *nativeWatchFake) UnwatchFields(fields dcgm.FieldHandle, group dcgm.GroupHandle) error {
	n.mu.Lock()
	n.unwatchCalls++
	err := n.unwatchErr
	n.unwatchErr = nil
	if n.clearOnUnwatch {
		clear(n.watched)
	} else {
		for _, key := range n.keys(fields, group) {
			delete(n.watched, key)
		}
	}
	n.mu.Unlock()
	if n.onUnwatch != nil {
		n.onUnwatch()
	}
	return err
}

// UpdateAllFields counts the exporter's own UpdateAllFields calls; DCGM's
// sampling inside a watch call is not modeled.
func (n *nativeWatchFake) UpdateAllFields() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.refreshCalls++
	return nil
}

func (n *nativeWatchFake) GetValuesSince(group dcgm.GroupHandle, fields dcgm.FieldHandle, since time.Time) ([]dcgm.FieldValue_v2, time.Time, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, key := range n.keys(fields, group) {
		if _, ok := n.watched[key]; !ok {
			return nil, since, &dcgm.Error{Code: dcgm.DCGM_ST_NOT_WATCHED}
		}
	}
	return nil, since, nil
}

func (n *nativeWatchFake) WatchFieldValue(gpu uint, field dcgm.Short, interval, age time.Duration, samples int) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.watchCalls++
	n.watched[nativeFieldKey{gpu, field}] = nativeWatchPolicy{interval.Microseconds(), age.Seconds(), samples}
	return nil
}

func (n *nativeWatchFake) readFields(gpu uint, fields []dcgm.Short) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, field := range fields {
		if _, ok := n.watched[nativeFieldKey{gpu, field}]; !ok {
			return &dcgm.Error{Code: dcgm.DCGM_ST_NOT_WATCHED}
		}
	}
	return nil
}

func (n *nativeWatchFake) EntitiesGetLatestValues(entities []dcgm.GroupEntityPair, fields []dcgm.Short, _ uint) ([]dcgm.FieldValue_v2, error) {
	for _, entity := range entities {
		if err := n.readFields(entity.EntityId, fields); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (n *nativeWatchFake) EntityGetLatestValues(_ dcgm.Field_Entity_Group, gpu uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1, error) {
	return nil, n.readFields(gpu, fields)
}

func (n *nativeWatchFake) LinkGetLatestValues(_ uint, _ dcgm.Field_Entity_Group, parent uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1, error) {
	return nil, n.readFields(parent, fields)
}

func (n *nativeWatchFake) GetMultipleValuesForField(gpu uint, field dcgm.Short, _ int, _, _ time.Time) ([]dcgm.FieldValue_v1, error) {
	return nil, n.readFields(gpu, []dcgm.Short{field})
}

func TestWatchClientRetirementPreservesSharedFields(t *testing.T) {
	for _, sameGroups := range []bool{false, true} {
		name := "partial entity and field overlap"
		if sameGroups {
			name = "same fields and entities with different handles"
		}
		t.Run(name, func(t *testing.T) {
			native := newNativeWatchFake()
			if sameGroups {
				native.groups[2] = native.groups[1]
				native.fields[2] = native.fields[1]
			}
			client := newWatchClient(native)
			oldFields, oldGroup := watchTestHandles(1)
			newFields, newGroup := watchTestHandles(2)
			require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(oldFields, oldGroup, 1_000_000, 600, 0))
			require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(newFields, newGroup, 2_000_000, 30, 5))

			require.NoError(t, client.UnwatchFields(oldFields, oldGroup))

			_, _, err := client.GetValuesSince(newGroup, newFields, time.Now())
			require.NoError(t, err, "retiring the old registry must preserve the replacement's watches")
			assert.Equal(t, nativeWatchPolicy{2_000_000, 30, 5}, native.watched[nativeFieldKey{1, dcgm.DCGM_FI_DEV_XID_ERRORS}])
			if !sameGroups {
				assert.NotContains(t, native.watched, nativeFieldKey{0, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS})
			}
			require.NoError(t, client.UnwatchFields(newFields, newGroup))
			assert.Empty(t, native.watched, "the final consumer must release its native watches")
		})
	}
}

func TestWatchClientFailedCandidateRestoresActiveWatches(t *testing.T) {
	for _, keepFailedWatch := range []bool{false, true} {
		name := "native watch failure"
		if keepFailedWatch {
			name = "sample refresh failure after native watch setup"
		}
		t.Run(name, func(t *testing.T) {
			native := newNativeWatchFake()
			native.keepFailedWatch = keepFailedWatch
			client := newWatchClient(native)
			oldFields, oldGroup := watchTestHandles(1)
			newFields, newGroup := watchTestHandles(2)
			require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(oldFields, oldGroup, 1_000_000, 600, 0))
			wantErr := errors.New("candidate watch failed")
			native.failNext = wantErr

			err := client.WatchFieldsWithGroupExNoRefresh(newFields, newGroup, 2_000_000, 30, 5)

			require.ErrorIs(t, err, wantErr)
			_, _, err = client.GetValuesSince(oldGroup, oldFields, time.Now())
			require.NoError(t, err, "native rollback must not damage the last-good registry")
			assert.Equal(t, nativeWatchPolicy{1_000_000, 600, 0}, native.watched[nativeFieldKey{1, dcgm.DCGM_FI_DEV_XID_ERRORS}])
			assert.NotContains(t, native.watched, nativeFieldKey{2, dcgm.DCGM_FI_DEV_GPU_TEMP}, "failed candidates must not leave new native watches behind")
			assert.Zero(t, native.refreshCalls, "install, rollback and restore add no UpdateAllFields call of their own")
		})
	}
}

func TestWatchClientReadsWaitForWatchRestoration(t *testing.T) {
	fields := []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS}
	newFields, newGroup := watchTestHandles(2)
	for _, tt := range []struct {
		name string
		read func(DCGM) error
	}{
		{"GetValuesSince", func(client DCGM) error {
			_, _, err := client.GetValuesSince(newGroup, newFields, time.Now())
			return err
		}},
		{"EntitiesGetLatestValues", func(client DCGM) error {
			_, err := client.EntitiesGetLatestValues([]dcgm.GroupEntityPair{{EntityGroupId: dcgm.FE_GPU, EntityId: 1}}, fields, 0)
			return err
		}},
		{"EntityGetLatestValues", func(client DCGM) error {
			_, err := client.EntityGetLatestValues(dcgm.FE_GPU, 1, fields)
			return err
		}},
		{"LinkGetLatestValues", func(client DCGM) error {
			_, err := client.LinkGetLatestValues(0, dcgm.FE_GPU, 1, fields)
			return err
		}},
		{"GetMultipleValuesForField", func(client DCGM) error {
			_, err := client.GetMultipleValuesForField(1, fields[0], 2, time.Time{}, time.Time{})
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			native := newNativeWatchFake()
			client := newWatchClient(native)
			oldFields, oldGroup := watchTestHandles(1)
			require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(oldFields, oldGroup, 1_000_000, 600, 0))
			require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(newFields, newGroup, 2_000_000, 30, 5))
			unwatched := make(chan struct{})
			resume := make(chan struct{})
			native.onUnwatch = func() {
				close(unwatched)
				<-resume
			}
			var resumeOnce sync.Once
			resumeUnwatch := func() { resumeOnce.Do(func() { close(resume) }) }
			defer resumeUnwatch()
			unwatchDone := make(chan error, 1)
			go func() { unwatchDone <- client.UnwatchFields(oldFields, oldGroup) }()
			select {
			case <-unwatched:
			case <-time.After(time.Second):
				t.Fatal("native unwatch did not start")
			}
			readStarted := make(chan struct{})
			readDone := make(chan error, 1)
			go func() {
				close(readStarted)
				readDone <- tt.read(client)
			}()
			<-readStarted
			readEarly := false
			select {
			case err := <-readDone:
				readEarly = true
				t.Errorf("read entered the native unwatch interval: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			resumeUnwatch()
			select {
			case err := <-unwatchDone:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("native watch restoration did not complete")
			}
			if !readEarly {
				select {
				case err := <-readDone:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatal("read did not resume after watch restoration")
				}
			}
		})
	}
}

func TestWatchClientRestoresDisjointFieldsAfterModuleUnwatch(t *testing.T) {
	native := newNativeWatchFake()
	native.clearOnUnwatch = true
	native.fields[2] = []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}
	client := newWatchClient(native)
	oldFields, oldGroup := watchTestHandles(1)
	newFields, newGroup := watchTestHandles(2)
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(oldFields, oldGroup, 1_000_000, 600, 0))
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(newFields, newGroup, 2_000_000, 30, 5))

	require.NoError(t, client.UnwatchFields(oldFields, oldGroup))

	_, _, err := client.GetValuesSince(newGroup, newFields, time.Now())
	require.NoError(t, err, "module-wide unwatch must preserve requests with disjoint fields")
	assert.Equal(t, nativeWatchPolicy{2_000_000, 30, 5}, native.watched[nativeFieldKey{1, dcgm.DCGM_FI_DEV_GPU_TEMP}])
	assert.NotContains(t, native.watched, nativeFieldKey{0, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS})
	require.NoError(t, client.UnwatchFields(newFields, newGroup))
	assert.Empty(t, native.watched)
}

func TestWatchClientPreservesDirectLifecycleWatch(t *testing.T) {
	native := newNativeWatchFake()
	native.fields[1] = []dcgm.Short{dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT}
	client := newWatchClient(native)
	require.NoError(t, client.WatchFieldValue(0, dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, time.Second, 0, 2))
	fields, group := watchTestHandles(1)
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(fields, group, 2_000_000, 600, 0))

	require.NoError(t, client.UnwatchFields(fields, group))

	assert.Equal(t, nativeWatchPolicy{1_000_000, 0, 2}, native.watched[nativeFieldKey{0, dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT}])
}

func TestWatchClientReportsRestorationFailure(t *testing.T) {
	logBuffer := captureDefaultSlog(t)
	native := newNativeWatchFake()
	native.groups[3] = []uint{0}
	native.fields[3] = []dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS}
	client := newWatchClient(native)
	oldFields, oldGroup := watchTestHandles(1)
	newFields, newGroup := watchTestHandles(2)
	otherFields, otherGroup := watchTestHandles(3)
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(oldFields, oldGroup, 1_000_000, 600, 0))
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(newFields, newGroup, 2_000_000, 30, 5))
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(otherFields, otherGroup, 3_000_000, 15, 2))
	wantErr := errors.New("error watching fields: Setting not configured")
	native.failNext = wantErr

	err := client.UnwatchFields(oldFields, oldGroup)

	require.ErrorContains(t, err, "restore")
	require.ErrorContains(t, err, "Setting not configured")
	require.ErrorIs(t, err, wantErr)
	var restoreErr *RestoreError
	require.ErrorAs(t, err, &restoreErr, "callers recognize the restore failure the client already logged")
	assert.Contains(t, logBuffer.String(), "Failed to restore DCGM watch",
		"a surviving watch's failure must remain visible even when cleanup suppresses benign teardown statuses")
	_, _, err = client.GetValuesSince(otherGroup, otherFields, time.Now())
	require.NoError(t, err, "one failed restoration must not prevent restoring other surviving watches")
}

func TestWatchClientRepeatedSetupReplacesWatchPolicy(t *testing.T) {
	native := newNativeWatchFake()
	native.groups[3] = []uint{0}
	native.fields[3] = []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_UTIL}
	client := newWatchClient(native)
	fields, group := watchTestHandles(1)
	otherFields, otherGroup := watchTestHandles(2)
	retiringFields, retiringGroup := watchTestHandles(3)
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(fields, group, 1_000_000, 600, 0))
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(otherFields, otherGroup, 2_000_000, 30, 5))
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(retiringFields, retiringGroup, 4_000_000, 600, 0))
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(fields, group, 3_000_000, 15, 2))

	require.NoError(t, client.UnwatchFields(retiringFields, retiringGroup))
	assert.Equal(t, nativeWatchPolicy{3_000_000, 15, 2}, native.watched[nativeFieldKey{1, dcgm.DCGM_FI_DEV_XID_ERRORS}],
		"the latest setup must retain precedence over another overlapping request during restoration")

	require.NoError(t, client.UnwatchFields(fields, group))
	assert.Equal(t, nativeWatchPolicy{2_000_000, 30, 5}, native.watched[nativeFieldKey{1, dcgm.DCGM_FI_DEV_XID_ERRORS}])
	require.NoError(t, client.UnwatchFields(otherFields, otherGroup))

	assert.Empty(t, native.watched, "repeated setup must not retain an obsolete request")
}

func TestWatchClientShutdown(t *testing.T) {
	unwatchErr := errors.New("native unwatch failed")
	for _, tt := range []struct {
		name       string
		count      int
		direct     bool
		unwatchErr error
	}{
		{name: "no watches"},
		{name: "one watch", count: 1},
		{name: "many watches and lifecycle watch", count: 100, direct: true},
		{name: "unwatch failure", count: 2, unwatchErr: unwatchErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			native := newNativeWatchFake()
			native.clearOnUnwatch = true
			client := newWatchClient(native)
			original := Client()
			SetClient(client)
			t.Cleanup(func() { SetClient(original) })

			for i := range tt.count {
				id := uintptr(i + 1)
				native.groups[id] = []uint{0}
				native.fields[id] = []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}
				fields, group := watchTestHandles(id)
				require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(fields, group, 1_000_000, 600, 0))
			}
			if tt.direct {
				require.NoError(t, client.WatchFieldValue(0, dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, time.Second, 0, 2))
			}
			installed := native.watchCalls
			native.unwatchErr = tt.unwatchErr

			BeginShutdown()
			wantErr := tt.unwatchErr
			for i := range tt.count {
				fields, group := watchTestHandles(uintptr(i + 1))
				err := client.UnwatchFields(fields, group)
				require.ErrorIs(t, err, wantErr)
				wantErr = nil
			}

			assert.Equal(t, tt.count, native.unwatchCalls)
			assert.Equal(t, installed, native.watchCalls, "shutdown must not reinstall group or direct watches")
			assert.Empty(t, native.watched)
		})
	}
}

func TestBeginShutdown(t *testing.T) {
	for _, tt := range []struct {
		name         string
		wrap         func(DCGM) DCGM
		wantRejected bool
	}{
		{name: "missing client", wrap: func(DCGM) DCGM { return nil }},
		{name: "unwrapped client", wrap: func(client DCGM) DCGM { return client }},
		{name: "watch client", wrap: func(client DCGM) DCGM { return newWatchClient(client) }, wantRejected: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			native := newNativeWatchFake()
			client := tt.wrap(native)
			original := Client()
			SetClient(client)
			t.Cleanup(func() { SetClient(original) })

			BeginShutdown()
			BeginShutdown()
			fields, group := watchTestHandles(1)
			if client != nil {
				groupErr := client.WatchFieldsWithGroupExNoRefresh(fields, group, 1_000_000, 600, 0)
				directErr := client.WatchFieldValue(0, dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, time.Second, 0, 2)
				if tt.wantRejected {
					assert.ErrorContains(t, groupErr, "shutdown")
					assert.ErrorContains(t, directErr, "shutdown")
					assert.Zero(t, native.watchCalls)
				} else {
					require.NoError(t, groupErr)
					require.NoError(t, directErr)
				}
			}

			fresh := newWatchClient(newNativeWatchFake())
			require.NoError(t, fresh.WatchFieldsWithGroupExNoRefresh(fields, group, 1_000_000, 600, 0))
		})
	}
}

func TestWatchClientShutdownDuringUnwatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		native := newNativeWatchFake()
		client := newWatchClient(native)
		original := Client()
		SetClient(client)
		t.Cleanup(func() { SetClient(original) })
		oldFields, oldGroup := watchTestHandles(1)
		newFields, newGroup := watchTestHandles(2)
		require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(oldFields, oldGroup, 1_000_000, 600, 0))
		require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(newFields, newGroup, 1_000_000, 600, 0))

		started := make(chan struct{})
		resume := make(chan struct{})
		native.onUnwatch = func() {
			close(started)
			<-resume
		}
		unwatchDone := make(chan error, 1)
		go func() { unwatchDone <- client.UnwatchFields(oldFields, oldGroup) }()
		<-started
		shutdownDone := make(chan struct{})
		go func() {
			BeginShutdown()
			close(shutdownDone)
		}()
		synctest.Wait()
		select {
		case <-shutdownDone:
		default:
			t.Error("shutdown marker waited for the native unwatch")
		}
		close(resume)
		require.NoError(t, <-unwatchDone)
		<-shutdownDone
		assert.Equal(t, 2, native.watchCalls, "unwatch must skip restoration after observing shutdown")
	})
}

// addSingleGPUWatches gives the fake count single-GPU groups with handles
// 1..count, each watching GPU temperature on GPU id-1, and installs them all
// through client.
func addSingleGPUWatches(t *testing.T, native *nativeWatchFake, client DCGM, count int) {
	t.Helper()

	for i := range count {
		id := uintptr(i + 1)
		native.groups[id] = []uint{uint(i)}
		native.fields[id] = []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}
		fields, group := watchTestHandles(id)
		require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(fields, group, 1_000_000, 600, 0))
	}
}

// singleGPUWatches returns the watches with handles first..last that
// addSingleGPUWatches installed.
func singleGPUWatches(first, last int) []FieldGroupWatch {
	var watches []FieldGroupWatch
	for id := first; id <= last; id++ {
		fields, group := watchTestHandles(uintptr(id))
		watches = append(watches, FieldGroupWatch{FieldGroup: fields, Group: group})
	}
	return watches
}

// TestWatchClientRestoreReplaysEachSurvivorOnce records 8 watches, unwatches
// one, and expects restore to install each of the 7 survivors again once,
// with no UpdateAllFields call of the client's own, whether DCGM dropped only
// the unwatched watch or every watch of the connection.
func TestWatchClientRestoreReplaysEachSurvivorOnce(t *testing.T) {
	for _, tt := range []struct {
		name           string
		clearOnUnwatch bool
	}{
		{name: "unwatch removes only its own watches"},
		{name: "unwatch removes every watch of the connection", clearOnUnwatch: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			native := newNativeWatchFake()
			native.clearOnUnwatch = tt.clearOnUnwatch
			client := newWatchClient(native)
			addSingleGPUWatches(t, native, client, 8)
			installed := native.watchCalls

			fields, group := watchTestHandles(1)
			require.NoError(t, client.UnwatchFields(fields, group))

			assert.Zero(t, native.refreshCalls, "restore adds no UpdateAllFields call of its own")
			assert.Equal(t, 7, native.watchCalls-installed, "restore replays each of the 7 survivors once")
			assert.NotContains(t, native.watched, nativeFieldKey{0, dcgm.DCGM_FI_DEV_GPU_TEMP})
			for gpu := uint(1); gpu < 8; gpu++ {
				assert.Contains(t, native.watched, nativeFieldKey{gpu, dcgm.DCGM_FI_DEV_GPU_TEMP})
			}
		})
	}
}

// TestWatchClientUnwatchFieldGroupsRestoresSurvivorsOnce records 32 watches,
// unwatches 16 in one call, and expects 16 native unwatches, one restore pass
// that installs each of the 16 survivors once, no UpdateAllFields call of the
// client's own, and
// removed watches that never come back, also when one unwatch fails.
func TestWatchClientUnwatchFieldGroupsRestoresSurvivorsOnce(t *testing.T) {
	unwatchErr := errors.New("native unwatch failed")
	for _, tt := range []struct {
		name           string
		clearOnUnwatch bool
		unwatchErr     error
	}{
		{name: "unwatch removes only its own watches"},
		{name: "unwatch removes every watch of the connection", clearOnUnwatch: true},
		{name: "one unwatch fails", clearOnUnwatch: true, unwatchErr: unwatchErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			native := newNativeWatchFake()
			native.clearOnUnwatch = tt.clearOnUnwatch
			client := newWatchClient(native)
			addSingleGPUWatches(t, native, client, 32)
			installed := native.watchCalls
			native.unwatchErr = tt.unwatchErr

			err := client.UnwatchFieldGroups(singleGPUWatches(1, 16))

			if tt.unwatchErr != nil {
				require.ErrorIs(t, err, tt.unwatchErr)
				assert.ErrorContains(t, err, "unwatch field group 1 on group 1")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, 16, native.unwatchCalls, "every watch is unwatched, even after a failure")
			assert.Equal(t, 16, native.watchCalls-installed, "one restore pass replays each of the 16 survivors once")
			assert.Zero(t, native.refreshCalls, "teardown adds no UpdateAllFields call of its own")
			for gpu := range uint(32) {
				key := nativeFieldKey{gpu, dcgm.DCGM_FI_DEV_GPU_TEMP}
				if gpu < 16 {
					assert.NotContains(t, native.watched, key)
				} else {
					assert.Contains(t, native.watched, key)
				}
			}

			require.NoError(t, client.UnwatchFieldGroups(singleGPUWatches(17, 32)))
			assert.Empty(t, native.watched, "removed requests must not come back in a later restore")
		})
	}
}

// TestDeferRestoresRestoresOnceAndHoldsReads records 8 watches, unwatches 4 of
// them one at a time inside one DeferRestores scope, and expects 4 native
// unwatches but no restore until the scope ends, then one restore pass that
// replays each of the 4 survivors once. A read made inside the scope waits
// for that restore.
func TestDeferRestoresRestoresOnceAndHoldsReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		native := newNativeWatchFake()
		native.clearOnUnwatch = true
		client := newWatchClient(native)
		addSingleGPUWatches(t, native, client, 8)
		installed := native.watchCalls
		original := Client()
		SetClient(client)
		t.Cleanup(func() { SetClient(original) })

		end := DeferRestores()
		for _, watch := range singleGPUWatches(1, 4) {
			require.NoError(t, client.UnwatchFields(watch.FieldGroup, watch.Group))
		}

		readDone := make(chan error, 1)
		go func() {
			fields, group := watchTestHandles(8)
			_, _, err := client.GetValuesSince(group, fields, time.Now())
			readDone <- err
		}()
		synctest.Wait()

		assert.Equal(t, 4, native.unwatchCalls)
		assert.Equal(t, installed, native.watchCalls, "no restore runs inside the scope")
		require.Empty(t, readDone, "a read inside the scope waits for the restore")

		end()

		assert.Equal(t, 4, native.watchCalls-installed, "one restore pass replays each of the 4 survivors once")
		require.NoError(t, <-readDone, "the read sees the restored watch")
	})
}
