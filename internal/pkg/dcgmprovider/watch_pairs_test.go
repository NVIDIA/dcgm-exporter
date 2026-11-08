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
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// groupFake extends the native fake with group and field group creation, so
// the watch client learns the pairs of the watches it installs.
type groupFake struct {
	*nativeWatchFake
	nextHandle uintptr
}

// newGroupFake returns a native fake whose new groups and field groups start
// at handle 100, clear of the fake's fixed groups 1 and 2.
func newGroupFake() *groupFake {
	return &groupFake{nativeWatchFake: newNativeWatchFake(), nextHandle: 100}
}

// CreateGroup returns a new, empty group.
func (g *groupFake) CreateGroup(string) (dcgm.GroupHandle, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.nextHandle++
	g.groups[g.nextHandle] = nil

	group := dcgm.GroupHandle{}
	group.SetHandle(g.nextHandle)
	return group, nil
}

// AddEntityToGroup adds a GPU to a group.
func (g *groupFake) AddEntityToGroup(group dcgm.GroupHandle, _ dcgm.Field_Entity_Group, gpu uint) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.groups[group.GetHandle()] = append(g.groups[group.GetHandle()], gpu)
	return nil
}

// FieldGroupCreate returns a new field group with fields.
func (g *groupFake) FieldGroupCreate(_ string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.nextHandle++
	g.fields[g.nextHandle] = fields

	fieldGroup := dcgm.FieldHandle{}
	fieldGroup.SetHandle(g.nextHandle)
	return fieldGroup, nil
}

// DestroyGroup forgets a group.
func (g *groupFake) DestroyGroup(group dcgm.GroupHandle) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.groups, group.GetHandle())
	return nil
}

// pairWatch names a watch the test installs: GPUs and fields.
type pairWatch struct {
	gpus   []uint
	fields []dcgm.Short
}

// watchThroughClient creates a group of gpus and a field group of fields
// through client, watches them, and returns the watch.
func watchThroughClient(t *testing.T, client *watchClient, watch pairWatch) FieldGroupWatch {
	t.Helper()

	group, err := client.CreateGroup("group")
	require.NoError(t, err)
	for _, gpu := range watch.gpus {
		require.NoError(t, client.AddEntityToGroup(group, dcgm.FE_GPU, gpu))
	}

	fieldGroup, err := client.FieldGroupCreate("fields", watch.fields)
	require.NoError(t, err)

	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(fieldGroup, group, 1_000_000, 600, 0))
	return FieldGroupWatch{FieldGroup: fieldGroup, Group: group}
}

// TestWatchClientSkipsUnwatchOfCoveredWatches unwatches a watch while another
// one survives, and expects DCGM to be asked to unwatch, and the survivor to
// be restored, only when a pair of the removed watch has no newer surviving
// watch, or a module samples it.
func TestWatchClientSkipsUnwatchOfCoveredWatches(t *testing.T) {
	temp, xid := dcgm.Short(dcgm.DCGM_FI_DEV_GPU_TEMP), dcgm.Short(dcgm.DCGM_FI_DEV_XID_ERRORS)
	profiling := dcgm.Short(dcgm.DCGM_FI_PROF_SM_UTIL_RATIO)
	tests := []struct {
		name           string
		removed        pairWatch
		survivor       pairWatch
		survivorFirst  bool
		clearOnUnwatch bool
		wantNative     bool
	}{
		{
			name:     "covered by a newer watch",
			removed:  pairWatch{gpus: []uint{0, 1}, fields: []dcgm.Short{temp}},
			survivor: pairWatch{gpus: []uint{0, 1, 2}, fields: []dcgm.Short{temp, xid}},
		},
		{
			name:       "partly covered",
			removed:    pairWatch{gpus: []uint{0, 1}, fields: []dcgm.Short{temp}},
			survivor:   pairWatch{gpus: []uint{0}, fields: []dcgm.Short{temp}},
			wantNative: true,
		},
		{
			name:       "not covered",
			removed:    pairWatch{gpus: []uint{0}, fields: []dcgm.Short{temp}},
			survivor:   pairWatch{gpus: []uint{0}, fields: []dcgm.Short{xid}},
			wantNative: true,
		},
		{
			name:          "covered by an older watch, whose policy DCGM no longer has",
			removed:       pairWatch{gpus: []uint{0}, fields: []dcgm.Short{temp}},
			survivor:      pairWatch{gpus: []uint{0}, fields: []dcgm.Short{temp}},
			survivorFirst: true,
			wantNative:    true,
		},
		{
			name:           "covered, but a module samples the field and clears more",
			removed:        pairWatch{gpus: []uint{0}, fields: []dcgm.Short{profiling}},
			survivor:       pairWatch{gpus: []uint{0}, fields: []dcgm.Short{profiling}},
			clearOnUnwatch: true,
			wantNative:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			native := newGroupFake()
			native.clearOnUnwatch = tt.clearOnUnwatch
			client := newWatchClient(native)

			var removed FieldGroupWatch
			if tt.survivorFirst {
				watchThroughClient(t, client, tt.survivor)
				removed = watchThroughClient(t, client, tt.removed)
			} else {
				removed = watchThroughClient(t, client, tt.removed)
				watchThroughClient(t, client, tt.survivor)
			}
			installed := native.watchCalls

			require.NoError(t, client.UnwatchFieldGroups([]FieldGroupWatch{removed}))

			assertUnwatchOutcome(t, native.nativeWatchFake, installed, tt.wantNative)
			for _, gpu := range tt.survivor.gpus {
				for _, field := range tt.survivor.fields {
					assert.Contains(t, native.watched, nativeFieldKey{gpu, field}, "the survivor keeps its watches")
				}
			}
		})
	}
}

// assertUnwatchOutcome checks that DCGM was asked to unwatch once and the one
// survivor restored once when wantNative, and that neither happened otherwise.
func assertUnwatchOutcome(t *testing.T, native *nativeWatchFake, installed int, wantNative bool) {
	t.Helper()

	if wantNative {
		assert.Equal(t, 1, native.unwatchCalls, "the removed watch is unwatched in DCGM")
		assert.Equal(t, 1, native.watchCalls-installed, "the survivor is restored once")
		return
	}

	assert.Zero(t, native.unwatchCalls, "a covered watch is not unwatched in DCGM")
	assert.Equal(t, installed, native.watchCalls, "nothing is restored when nothing was unwatched")
}

// TestWatchClientUnwatchesWatchesItCannotSee checks that a watch on a group the
// client did not create, whose members it does not know, is always unwatched
// in DCGM, and that a destroyed group's handle starts unknown again.
func TestWatchClientUnwatchesWatchesItCannotSee(t *testing.T) {
	native := newGroupFake()
	client := newWatchClient(native)
	unknownFields, unknownGroup := watchTestHandles(2)
	require.NoError(t, client.WatchFieldsWithGroupExNoRefresh(unknownFields, unknownGroup, 1_000_000, 600, 0))
	watchThroughClient(t, client, pairWatch{gpus: []uint{1, 2}, fields: []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS, dcgm.DCGM_FI_DEV_GPU_TEMP}})

	require.NoError(t, client.UnwatchFields(unknownFields, unknownGroup))
	assert.Equal(t, 1, native.unwatchCalls)

	known := watchThroughClient(t, client, pairWatch{gpus: []uint{0}, fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}})
	require.NoError(t, client.DestroyGroup(known.Group))
	_, ok := client.topology.pairs(known.Group, known.FieldGroup)
	assert.False(t, ok, "a destroyed group's members are forgotten")
}

// TestWatchPairModuleSampled checks which pairs count as sampled by a DCGM
// module.
func TestWatchPairModuleSampled(t *testing.T) {
	gpu := groupEntity{entityGroup: dcgm.FE_GPU}
	gpuLink := groupEntity{entityGroup: dcgm.FE_LINK, link: true, parentGroup: dcgm.FE_GPU}
	tests := []struct {
		name string
		pair watchPair
		want bool
	}{
		{name: "GPU field on a GPU", pair: watchPair{gpu, dcgm.DCGM_FI_DEV_GPU_TEMP}},
		{name: "NVLink field on a GPU link", pair: watchPair{gpuLink, dcgm.DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL}},
		{name: "profiling field", pair: watchPair{gpu, dcgm.DCGM_FI_PROF_SM_UTIL_RATIO}, want: true},
		{name: "per-link profiling field", pair: watchPair{gpuLink, dcgm.DCGM_FI_PROF_NVLINK_TX_BYTES_PER_LINK}, want: true},
		{name: "NVSwitch field", pair: watchPair{gpu, dcgm.DCGM_FI_FIRST_NVSWITCH_FIELD_ID + 1}, want: true},
		{name: "switch link", pair: watchPair{groupEntity{entityGroup: dcgm.FE_LINK, link: true, parentGroup: dcgm.FE_SWITCH}, dcgm.DCGM_FI_DEV_GPU_TEMP}, want: true},
		{name: "switch", pair: watchPair{groupEntity{entityGroup: dcgm.FE_SWITCH}, dcgm.DCGM_FI_DEV_GPU_TEMP}, want: true},
		{name: "CPU core", pair: watchPair{groupEntity{entityGroup: dcgm.FE_CPU_CORE}, dcgm.DCGM_FI_DEV_GPU_TEMP}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.pair.moduleSampled())
		})
	}
}
