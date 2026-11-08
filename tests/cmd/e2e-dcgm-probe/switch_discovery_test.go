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

package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
)

// TestDiscoverSwitchIDs checks that NVSwitch discovery falls back to NVLink
// link status only on a DCGM version mismatch, reports the fallback on the
// warnings writer, and returns both errors when link status also fails.
func TestDiscoverSwitchIDs(t *testing.T) {
	mismatchErr := errors.New("error getting entity count: API version mismatch")
	otherErr := errors.New("error getting entity count: Host engine is not valid any longer")
	linkErr := errors.New("error getting NvLink status")
	links := []dcgm.NvLinkStatus{
		{ParentId: 3, ParentType: dcgm.FE_SWITCH, Index: 0},
		{ParentId: 0, ParentType: dcgm.FE_GPU, Index: 0},
		{ParentId: 1, ParentType: dcgm.FE_SWITCH, Index: 0},
		{ParentId: 3, ParentType: dcgm.FE_SWITCH, Index: 1},
	}

	for _, tt := range []struct {
		name         string
		listIDs      []uint
		listErr      error
		linkErr      error
		wantIDs      []uint
		wantErrs     []error
		wantWarning  bool
		wantLinkCall bool
	}{
		{
			name:    "listing succeeds",
			listIDs: []uint{0, 1},
			wantIDs: []uint{0, 1},
		},
		{
			name:         "go-dcgm status text mismatch uses link status",
			listErr:      mismatchErr,
			wantIDs:      []uint{1, 3},
			wantWarning:  true,
			wantLinkCall: true,
		},
		{
			name:         "typed mismatch uses link status",
			listErr:      &dcgm.Error{Code: dcgm.DCGM_ST_VER_MISMATCH},
			wantIDs:      []uint{1, 3},
			wantWarning:  true,
			wantLinkCall: true,
		},
		{
			name:     "other listing error is returned",
			listErr:  otherErr,
			wantErrs: []error{otherErr},
		},
		{
			name:         "mismatch and link status failure return both errors",
			listErr:      mismatchErr,
			linkErr:      linkErr,
			wantErrs:     []error{mismatchErr, linkErr},
			wantLinkCall: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			linkCalled := stubSwitchDiscovery(t, tt.listIDs, tt.listErr, links, tt.linkErr)

			var warnings bytes.Buffer
			ids, err := discoverSwitchIDs(&warnings)

			require.Equal(t, tt.wantLinkCall, *linkCalled)
			if tt.wantWarning {
				require.Equal(t, switchListingFallbackWarning+"\n", warnings.String())
			} else {
				require.Empty(t, warnings.String())
			}
			if len(tt.wantErrs) > 0 {
				for _, want := range tt.wantErrs {
					require.ErrorIs(t, err, want)
				}
				require.Nil(t, ids)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantIDs, ids)
		})
	}
}

// stubSwitchDiscovery replaces the NVSwitch listing and NVLink status calls
// for one test and restores them on cleanup. The returned flag records
// whether link status was read.
func stubSwitchDiscovery(t *testing.T, ids []uint, listErr error, links []dcgm.NvLinkStatus, linkErr error) *bool {
	t.Helper()

	listEntities, linkStatus := probeEntityGroupEntities, probeNvLinkLinkStatus
	t.Cleanup(func() {
		probeEntityGroupEntities, probeNvLinkLinkStatus = listEntities, linkStatus
	})

	linkCalled := false
	probeEntityGroupEntities = func(group dcgm.Field_Entity_Group) ([]uint, error) {
		require.Equal(t, dcgm.FE_SWITCH, group)
		return ids, listErr
	}
	probeNvLinkLinkStatus = func() ([]dcgm.NvLinkStatus, error) {
		linkCalled = true
		return links, linkErr
	}
	return &linkCalled
}
