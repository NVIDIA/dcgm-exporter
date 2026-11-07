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
	"fmt"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
)

const switchListingFallbackLog = `level=WARN msg="DCGM rejected the NVSwitch list request with an API version ` +
	`mismatch; finding NVSwitches from NVLink link status instead"`

// TestGetEntityGroupEntitiesFallsBackToLinkStatusForSwitches checks that a
// version mismatch on NVSwitch listing returns the switches from NVLink link
// status and logs a warning, for both forms go-dcgm can return.
func TestGetEntityGroupEntitiesFallsBackToLinkStatusForSwitches(t *testing.T) {
	switchLinks := []dcgm.NvLinkStatus{
		{ParentId: 3, ParentType: dcgm.FE_SWITCH, Index: 0},
		{ParentId: 0, ParentType: dcgm.FE_GPU, Index: 0},
		{ParentId: 1, ParentType: dcgm.FE_SWITCH, Index: 0},
		{ParentId: 3, ParentType: dcgm.FE_SWITCH, Index: 1},
		{ParentId: 1, ParentType: dcgm.FE_SWITCH, Index: 1, State: dcgm.LS_DOWN},
	}
	textErr := errors.New("error getting entity count: API version mismatch")

	for _, tt := range []struct {
		name    string
		err     error
		links   []dcgm.NvLinkStatus
		wantIDs []uint
		wantLog string
	}{
		{
			name:    "go-dcgm status text",
			err:     textErr,
			links:   switchLinks,
			wantIDs: []uint{1, 3},
			wantLog: `error="error getting entity count: API version mismatch"`,
		},
		{
			name:    "typed version mismatch",
			err:     fmt.Errorf("error getting entity count: %w", &dcgm.Error{Code: dcgm.DCGM_ST_VER_MISMATCH}),
			links:   switchLinks,
			wantIDs: []uint{1, 3},
			wantLog: `error="error getting entity count: "`,
		},
		{
			name:    "link status has only GPU links",
			err:     textErr,
			links:   []dcgm.NvLinkStatus{{ParentId: 0, ParentType: dcgm.FE_GPU}},
			wantIDs: []uint{},
			wantLog: `error="error getting entity count: API version mismatch"`,
		},
		{
			// go-dcgm returns no links and no error when DCGM answers
			// DCGM_ST_NOT_SUPPORTED for link status.
			name:    "link status not supported",
			err:     textErr,
			wantIDs: []uint{},
			wantLog: `error="error getting entity count: API version mismatch"`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			restoreEntityListingSeams(t)
			logBuffer := captureDefaultSlog(t)
			getEntityGroupEntitiesFunc = func(dcgm.Field_Entity_Group) ([]uint, error) {
				return nil, tt.err
			}
			getNvLinkLinkStatusFunc = func() ([]dcgm.NvLinkStatus, error) {
				return tt.links, nil
			}

			got, err := dcgmProvider{}.GetEntityGroupEntities(dcgm.FE_SWITCH)

			require.NoError(t, err)
			assert.Equal(t, tt.wantIDs, got)
			assert.Equal(t, 1, strings.Count(logBuffer.String(), switchListingFallbackLog))
			assert.Contains(t, logBuffer.String(), tt.wantLog)
			assert.Contains(t, logBuffer.String(), `hint="`+dcgmerrors.SupportedVersionHint+`"`)
		})
	}
}

// TestGetEntityGroupEntitiesDoesNotFallBack checks that successful listings,
// other errors and other entity groups never use link status.
func TestGetEntityGroupEntitiesDoesNotFallBack(t *testing.T) {
	mismatch := errors.New("error getting entity count: API version mismatch")
	otherErr := errors.New("error getting entity count: Host engine connection invalid/disconnected")

	for _, tt := range []struct {
		name    string
		group   dcgm.Field_Entity_Group
		ids     []uint
		err     error
		wantIDs []uint
	}{
		{name: "switch listing succeeds", group: dcgm.FE_SWITCH, ids: []uint{0, 1}, wantIDs: []uint{0, 1}},
		{name: "other switch listing error", group: dcgm.FE_SWITCH, err: otherErr},
		{name: "version mismatch on another group", group: dcgm.FE_GPU, err: mismatch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			restoreEntityListingSeams(t)
			logBuffer := captureDefaultSlog(t)
			getEntityGroupEntitiesFunc = func(dcgm.Field_Entity_Group) ([]uint, error) {
				return tt.ids, tt.err
			}
			getNvLinkLinkStatusFunc = func() ([]dcgm.NvLinkStatus, error) {
				t.Fatal("link status fallback must not run")
				return nil, nil
			}

			got, err := dcgmProvider{}.GetEntityGroupEntities(tt.group)

			assert.Equal(t, tt.wantIDs, got)
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
			} else {
				require.NoError(t, err)
			}
			assert.NotContains(t, logBuffer.String(), "level=WARN")
		})
	}
}

// TestGetEntityGroupEntitiesReturnsBothErrorsWhenFallbackFails checks that a
// failed link-status fallback returns the listing and link-status errors.
func TestGetEntityGroupEntitiesReturnsBothErrorsWhenFallbackFails(t *testing.T) {
	restoreEntityListingSeams(t)
	captureDefaultSlog(t)
	listErr := &dcgm.Error{Code: dcgm.DCGM_ST_VER_MISMATCH}
	linkErr := errors.New("link status failed")
	getEntityGroupEntitiesFunc = func(dcgm.Field_Entity_Group) ([]uint, error) {
		return nil, listErr
	}
	getNvLinkLinkStatusFunc = func() ([]dcgm.NvLinkStatus, error) {
		return nil, linkErr
	}

	got, err := dcgmProvider{}.GetEntityGroupEntities(dcgm.FE_SWITCH)

	assert.Nil(t, got)
	require.ErrorIs(t, err, listErr)
	require.ErrorIs(t, err, linkErr)
	assert.ErrorContains(t, err, "find NVSwitches from NVLink link status after DCGM rejected the NVSwitch list request")
}

// restoreEntityListingSeams puts the entity-listing test seams back after a test.
func restoreEntityListingSeams(t *testing.T) {
	t.Helper()
	prevGetEntityGroupEntities := getEntityGroupEntitiesFunc
	prevGetNvLinkLinkStatus := getNvLinkLinkStatusFunc
	t.Cleanup(func() {
		getEntityGroupEntitiesFunc = prevGetEntityGroupEntities
		getNvLinkLinkStatusFunc = prevGetNvLinkLinkStatus
	})
}
