/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package collector

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
)

// TestCreateHealthGroup checks that createHealthGroup sets the exact watch
// mask, destroys a group it created when a later step fails, and returns a
// cleanup function that destroys the group once however often it is called.
func TestCreateHealthGroup(t *testing.T) {
	requestedMask := dcgm.DCGM_HEALTH_WATCH_IMEX
	groupID := dcgm.GroupHandle{}

	tests := []struct {
		name        string
		setUp       func(*mockdcgm.MockDCGM)
		wantErr     string
		wantCleanup bool
		callCleanup bool
	}{
		{
			name: "supported device query failure creates no group",
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().GetSupportedDevices().Return(nil, errors.New("query failed"))
			},
			wantErr: "get supported GPU devices",
		},
		{
			name: "zero supported GPUs creates no group",
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().GetSupportedDevices().Return([]uint{}, nil)
			},
			wantErr: "no supported GPU devices found",
		},
		{
			name: "create failure performs no destroy",
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
				client.EXPECT().CreateGroup(gomock.Any()).Return(dcgm.GroupHandle{}, errors.New("create failed"))
			},
			wantErr: "create health group",
		},
		{
			name: "add failure destroys group",
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
				client.EXPECT().CreateGroup(gomock.Cond(func(value any) bool {
					name, ok := value.(string)
					return ok && strings.HasPrefix(name, "test_health_")
				})).Return(groupID, nil)
				client.EXPECT().AddEntityToGroup(groupID, dcgm.FE_GPU, uint(0)).Return(errors.New("add failed"))
				client.EXPECT().DestroyGroup(groupID).Return(nil)
			},
			wantErr: "add GPU 0 to health group",
		},
		{
			name: "health set failure destroys group",
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
				client.EXPECT().CreateGroup(gomock.Any()).Return(groupID, nil)
				client.EXPECT().AddEntityToGroup(groupID, dcgm.FE_GPU, uint(0)).Return(nil)
				client.EXPECT().HealthSet(groupID, requestedMask).Return(errors.New("health set failed"))
				client.EXPECT().DestroyGroup(groupID).Return(nil)
			},
			wantErr: "set health watches",
		},
		{
			name: "success passes exact mask and cleanup destroys once",
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().GetSupportedDevices().Return([]uint{0, 1}, nil)
				client.EXPECT().CreateGroup(gomock.Any()).Return(groupID, nil)
				client.EXPECT().AddEntityToGroup(groupID, dcgm.FE_GPU, uint(0)).Return(nil)
				client.EXPECT().AddEntityToGroup(groupID, dcgm.FE_GPU, uint(1)).Return(nil)
				client.EXPECT().HealthSet(groupID, requestedMask).Return(nil)
				client.EXPECT().DestroyGroup(groupID).Return(nil).Times(1)
			},
			wantCleanup: true,
			callCleanup: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mockdcgm.NewMockDCGM(ctrl)
			tt.setUp(client)

			gotGroup, cleanup, err := createHealthGroup(client, "test_health", requestedMask)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, cleanup)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, groupID, gotGroup)
			if tt.wantCleanup {
				require.NotNil(t, cleanup)
			}
			if tt.callCleanup {
				cleanup()
				cleanup()
			}
		})
	}
}

// TestCreateHealthGroupCleanupLogsDestroyFailure checks that a DestroyGroup
// failure during cleanup is logged as a warning instead of panicking.
func TestCreateHealthGroupCleanupLogsDestroyFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mockdcgm.NewMockDCGM(ctrl)
	groupID := systemHealthTestGroupHandle()
	client.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
	client.EXPECT().CreateGroup(gomock.Any()).Return(groupID, nil)
	client.EXPECT().AddEntityToGroup(groupID, dcgm.FE_GPU, uint(0)).Return(nil)
	client.EXPECT().HealthSet(groupID, dcgm.DCGM_HEALTH_WATCH_IMEX).Return(nil)
	client.EXPECT().DestroyGroup(groupID).Return(errors.New("destroy failed")).Times(1)

	_, cleanup, err := createHealthGroup(client, "test_health", dcgm.DCGM_HEALTH_WATCH_IMEX)
	require.NoError(t, err)

	var logs bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))

	assert.NotPanics(t, cleanup)
	assert.Contains(t, logs.String(), "level=WARN")
	assert.Contains(t, logs.String(), "cannot destroy health group")
	assert.Contains(t, logs.String(), "destroy failed")
}
