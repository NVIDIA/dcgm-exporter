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

package devicewatcher

import (
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
)

const (
	testRefusedField dcgm.Short = 1603
	testRefusedName             = "sm_clock_hertz (field ID 1603)"
	// testRefusalText is the error go-dcgm's FieldGroupCreate returns for a field
	// the hostengine refuses.
	testRefusalText = "error creating DCGM fields group: " + dcgmBadParamText
)

// TestExplainRefusedFields checks that the message names each refused field,
// says what to do, and keeps the original DCGM error.
func TestExplainRefusedFields(t *testing.T) {
	refusal := errors.New(testRefusalText)
	cause := newFieldGroupCreateError("test-group", 1, 1, 1, 2, refusal)
	client := mockdcgm.NewMockDCGM(gomock.NewController(t))
	expectOnlyTestFieldRefused(client, refusal)

	groups := []FieldWatchGroup{{Name: "test-group", Fields: []dcgm.Short{testRefusedField, dcgm.DCGM_FI_DEV_GPU_TEMP}}}
	got := ExplainRefusedFields(client, groups, cause)

	require.ErrorIs(t, got, cause)
	assert.EqualError(t, got, "the DCGM hostengine refused these fields: watch group \"test-group\": "+testRefusedName+
		". Run the DCGM version this exporter supports, or remove these fields from the metric file. DCGM error: "+
		"create physical field group for logical group \"test-group\" chunk 1/1 (fields 1-2, capacity 127): "+
		testRefusalText)

	var refusedErr *RefusedFieldsError
	require.ErrorAs(t, got, &refusedErr)
	assert.Equal(t, error(cause), refusedErr.Unwrap())
}

// TestExplainRefusedFieldsKeepsCauseWhenNoFieldIsRefused checks that a refusal
// with no field refused on its own keeps the original error.
func TestExplainRefusedFieldsKeepsCauseWhenNoFieldIsRefused(t *testing.T) {
	cause := newFieldGroupCreateError("test-group", 1, 1, 1, 2, errors.New(testRefusalText))
	ctrl := gomock.NewController(t)
	client := mockdcgm.NewMockDCGM(ctrl)
	client.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(dcgm.FieldHandle{}, nil)
	client.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil)

	groups := []FieldWatchGroup{{Name: "test-group", Fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}}}
	assert.Equal(t, cause, ExplainRefusedFields(client, groups, cause))
}

// TestExplainRefusedFieldsNamesEveryWatchGroup checks that the message names the
// refused fields of every watch group, not only the group whose watch failed.
func TestExplainRefusedFieldsNamesEveryWatchGroup(t *testing.T) {
	const memoryClockHertz = dcgm.Short(1604)
	refusal := errors.New(testRefusalText)
	ctrl := gomock.NewController(t)
	client := mockdcgm.NewMockDCGM(ctrl)
	client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{testRefusedField}).Return(dcgm.FieldHandle{}, refusal)
	client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{memoryClockHertz}).Return(dcgm.FieldHandle{}, refusal)
	client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}).Return(dcgm.FieldHandle{}, nil)
	client.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil)
	client.EXPECT().FieldGetByID(testRefusedField).Return(dcgm.FieldMeta{Tag: "sm_clock_hertz"}, nil)
	client.EXPECT().FieldGetByID(memoryClockHertz).Return(dcgm.FieldMeta{Tag: "memory_clock_hertz"}, nil)

	got := ExplainRefusedFields(client, []FieldWatchGroup{
		{Name: "default", Fields: []dcgm.Short{testRefusedField, dcgm.DCGM_FI_DEV_GPU_TEMP}},
		{Name: "framebuffer", Fields: []dcgm.Short{memoryClockHertz}},
	}, newFieldGroupCreateError("default", 1, 1, 1, 2, refusal))

	require.ErrorIs(t, got, refusal)
	assert.ErrorContains(t, got, `refused these fields: watch group "default": `+testRefusedName+"; "+
		`watch group "framebuffer": memory_clock_hertz (field ID 1604). Run`)
}

// TestExplainRefusedFieldsKeepsCauseWhenAProbeFailsForAnotherReason checks that
// only DCGM's bad-parameter refusal marks a field as refused: any other probe or
// cleanup failure keeps the original error instead of blaming the fields.
func TestExplainRefusedFieldsKeepsCauseWhenAProbeFailsForAnotherReason(t *testing.T) {
	cause := newFieldGroupCreateError("test-group", 1, 1, 1, 2, errors.New(testRefusalText))
	tests := []struct {
		name       string
		probeErr   error
		destroyErr error
	}{
		{name: "another probe error", probeErr: errors.New("connection lost")},
		{name: "cleanup fails", destroyErr: errors.New("destroy failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mockdcgm.NewMockDCGM(ctrl)
			client.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(dcgm.FieldHandle{}, tt.probeErr)
			if tt.probeErr == nil {
				client.EXPECT().FieldGroupDestroy(gomock.Any()).Return(tt.destroyErr)
			}

			groups := []FieldWatchGroup{{Name: "test-group", Fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}}}
			assert.Equal(t, cause, ExplainRefusedFields(client, groups, cause))
		})
	}
}

// expectOnlyTestFieldRefused makes the fake hostengine accept every one-field
// group except the one holding testRefusedField.
func expectOnlyTestFieldRefused(client *mockdcgm.MockDCGM, refusal error) {
	client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{testRefusedField}).
		Return(dcgm.FieldHandle{}, refusal)
	client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}).
		Return(dcgm.FieldHandle{}, nil)
	client.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil)
	client.EXPECT().FieldGetByID(testRefusedField).Return(dcgm.FieldMeta{Tag: "sm_clock_hertz"}, nil)
}

// TestIsBadParam checks that both the typed DCGM status and go-dcgm's plain-text
// message count as a bad-parameter refusal, and nothing else does.
func TestIsBadParam(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "no error"},
		{name: "go-dcgm field group text", err: errors.New(testRefusalText), want: true},
		{name: "wrapped field group text", err: fmt.Errorf("probe: %w", errors.New(testRefusalText)), want: true},
		{name: "typed bad parameter", err: &dcgm.Error{Code: dcgm.DCGM_ST_BADPARAM}, want: true},
		{name: "typed other status", err: &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID}},
		{name: "other text", err: errors.New("error creating DCGM fields group: Host engine connection invalid/disconnected")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isBadParam(tt.err))
		})
	}
}

// TestExplainRefusedFieldsIgnoresRefusalsOutsideFieldGroupCreation checks that a
// bad-parameter status from another step, such as adding a GPU that detached,
// keeps its own error and probes no field.
func TestExplainRefusedFieldsIgnoresRefusalsOutsideFieldGroupCreation(t *testing.T) {
	client := mockdcgm.NewMockDCGM(gomock.NewController(t))
	cause := fmt.Errorf("add GPU 0 to group: %w", &dcgm.Error{Code: dcgm.DCGM_ST_BADPARAM})

	got := ExplainRefusedFields(client, []FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{testRefusedField}}}, cause)

	assert.Equal(t, cause, got)
}

// TestExplainRefusedFieldsKeepsNonRefusalCreateFailure checks that a field group
// creation failure other than a refusal keeps its own error without any lookup.
func TestExplainRefusedFieldsKeepsNonRefusalCreateFailure(t *testing.T) {
	client := mockdcgm.NewMockDCGM(gomock.NewController(t))
	cause := newFieldGroupCreateError("default", 1, 1, 1, 1,
		errors.New("error creating DCGM fields group: Host engine connection invalid/disconnected"))

	got := ExplainRefusedFields(client, []FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{testRefusedField}}}, cause)

	assert.Equal(t, cause, got)
}

// TestExplainRefusedFieldsKeepsCauseWhenALaterGroupProbeFails checks that a
// probe failing for another reason in a later group keeps the original error,
// even after an earlier group found a refused field.
func TestExplainRefusedFieldsKeepsCauseWhenALaterGroupProbeFails(t *testing.T) {
	refusal := errors.New(testRefusalText)
	cause := newFieldGroupCreateError("default", 1, 1, 1, 1, refusal)
	client := mockdcgm.NewMockDCGM(gomock.NewController(t))
	client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{testRefusedField}).Return(dcgm.FieldHandle{}, refusal)
	client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}).
		Return(dcgm.FieldHandle{}, &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID})
	client.EXPECT().FieldGetByID(testRefusedField).Return(dcgm.FieldMeta{Tag: "sm_clock_hertz"}, nil).AnyTimes()

	got := ExplainRefusedFields(client, []FieldWatchGroup{
		{Name: "default", Fields: []dcgm.Short{testRefusedField}},
		{Name: "other", Fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}},
	}, cause)

	assert.Equal(t, cause, got)
}

// TestExplainRefusedFieldsNamesRefusedFieldsBeyondFirstChunk checks that a group
// larger than one DCGM field group is probed in full.
func TestExplainRefusedFieldsNamesRefusedFieldsBeyondFirstChunk(t *testing.T) {
	fields := testFieldIDs(maxFieldIDsPerFieldGroup + 2)
	first, last := fields[5], fields[len(fields)-1]
	refusal := errors.New(testRefusalText)
	cause := newFieldGroupCreateError("large", 1, 2, 1, maxFieldIDsPerFieldGroup, refusal)
	client := mockdcgm.NewMockDCGM(gomock.NewController(t))
	client.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ string, probe []dcgm.Short) (dcgm.FieldHandle, error) {
			if probe[0] == first || probe[0] == last {
				return dcgm.FieldHandle{}, refusal
			}
			return dcgm.FieldHandle{}, nil
		}).Times(len(fields))
	client.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).Times(len(fields) - 2)
	client.EXPECT().FieldGetByID(gomock.Any()).Return(dcgm.FieldMeta{}, nil).Times(2)

	got := ExplainRefusedFields(client, []FieldWatchGroup{{Name: "large", Fields: fields}}, cause)

	require.ErrorIs(t, got, refusal)
	assert.ErrorContains(t, got, "field ID "+strconv.Itoa(int(first))+", field ID "+strconv.Itoa(int(last))+".")
	assert.NotContains(t, got.Error(), "field ID "+strconv.Itoa(int(fields[6]))+",")
}
