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

package dcgmerrors

import (
	"errors"
	"fmt"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
)

// TestClassify classifies supported typed DCGM errors, including wrapped errors, and rejects unknown statuses and non-DCGM errors.
func TestClassify(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantOK     bool
		wantCode   int
		wantStatus string
	}{
		{
			name:       "no permission",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_NO_PERMISSION},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_NO_PERMISSION,
			wantStatus: "DCGM_ST_NO_PERMISSION",
		},
		{
			name:       "requires root",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_REQUIRES_ROOT},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_REQUIRES_ROOT,
			wantStatus: "DCGM_ST_REQUIRES_ROOT",
		},
		{
			name:       "connection not valid",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_CONNECTION_NOT_VALID,
			wantStatus: "DCGM_ST_CONNECTION_NOT_VALID",
		},
		{
			name:       "uninitialized status",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_UNINITIALIZED},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_UNINITIALIZED,
			wantStatus: "DCGM_ST_UNINITIALIZED",
		},
		{
			name:       "wrapped uninitialized error",
			err:        fmt.Errorf("wrapped: %w", &dcgm.Error{Code: dcgm.DCGM_ST_UNINITIALIZED}),
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_UNINITIALIZED,
			wantStatus: "DCGM_ST_UNINITIALIZED",
		},
		{
			name:       "version mismatch",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_VER_MISMATCH},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_VER_MISMATCH,
			wantStatus: "DCGM_ST_VER_MISMATCH",
		},
		{
			name:       "paused",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_PAUSED},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_PAUSED,
			wantStatus: "DCGM_ST_PAUSED",
		},
		{
			name:       "function not found",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_FUNCTION_NOT_FOUND},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_FUNCTION_NOT_FOUND,
			wantStatus: "DCGM_ST_FUNCTION_NOT_FOUND",
		},
		{
			name:       "library not found",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_LIBRARY_NOT_FOUND},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_LIBRARY_NOT_FOUND,
			wantStatus: "DCGM_ST_LIBRARY_NOT_FOUND",
		},
		{
			name:       "init error",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_INIT_ERROR},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_INIT_ERROR,
			wantStatus: "DCGM_ST_INIT_ERROR",
		},
		{
			name:       "nvml error",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_NVML_ERROR},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_NVML_ERROR,
			wantStatus: "DCGM_ST_NVML_ERROR",
		},
		{
			name:       "nvml not loaded",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_NVML_NOT_LOADED},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_NVML_NOT_LOADED,
			wantStatus: "DCGM_ST_NVML_NOT_LOADED",
		},
		{
			name:       "insufficient driver version",
			err:        &dcgm.Error{Code: dcgm.DCGM_ST_INSUFFICIENT_DRIVER_VERSION},
			wantOK:     true,
			wantCode:   dcgm.DCGM_ST_INSUFFICIENT_DRIVER_VERSION,
			wantStatus: "DCGM_ST_INSUFFICIENT_DRIVER_VERSION",
		},
		{
			name:   "unknown dcgm status",
			err:    &dcgm.Error{Code: dcgm.DCGM_ST_NOT_CONFIGURED},
			wantOK: false,
		},
		{
			name:   "non dcgm error",
			err:    assert.AnError,
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Classify(tc.err)

			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}
			assert.Equal(t, tc.wantCode, got.Code)
			assert.Equal(t, tc.wantStatus, got.Status)
			assert.NotEmpty(t, got.Message)
			assert.NotEmpty(t, got.Hint)
		})
	}
}

// TestClassifyCode returns the status name and permission hint for a recognized no-permission code.
func TestClassifyCode(t *testing.T) {
	got, ok := ClassifyCode(dcgm.DCGM_ST_NO_PERMISSION)

	assert.True(t, ok)
	assert.Equal(t, dcgm.DCGM_ST_NO_PERMISSION, got.Code)
	assert.Equal(t, "DCGM_ST_NO_PERMISSION", got.Status)
	assert.Contains(t, got.Hint, "CAP_SYS_ADMIN")
}

// TestIsVersionMismatch checks typed and text-only DCGM version-mismatch
// errors, including the text go-dcgm returns from entity listing.
func TestIsVersionMismatch(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "wrapped typed mismatch",
			err:  fmt.Errorf("list: %w", &dcgm.Error{Code: dcgm.DCGM_ST_VER_MISMATCH}),
			want: true,
		},
		{
			name: "go-dcgm entity listing text",
			err:  errors.New("error getting entity count: API version mismatch"),
			want: true,
		},
		{
			name: "other typed error",
			err:  &dcgm.Error{Code: dcgm.DCGM_ST_NOT_SUPPORTED},
			want: false,
		},
		{
			name: "other text error",
			err:  assert.AnError,
			want: false,
		},
		{
			name: "nil",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsVersionMismatch(tc.err))
		})
	}
}

// TestIsFatalScrapeError verifies that only terminal DCGM states cause the
// server to end the exporter rather than keep responding with failed scrapes.
func TestIsFatalScrapeError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "wrapped connection failure",
			err:  fmt.Errorf("scrape: %w", &dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID}),
			want: true,
		},
		{
			name: "permission failure stays per scrape",
			err:  &dcgm.Error{Code: dcgm.DCGM_ST_NO_PERMISSION},
			want: false,
		},
		{
			name: "joined fatal error follows nonfatal error",
			err: errors.Join(
				&dcgm.Error{Code: dcgm.DCGM_ST_NO_PERMISSION},
				&dcgm.Error{Code: dcgm.DCGM_ST_CONNECTION_NOT_VALID},
			),
			want: true,
		},
		{
			name: "non DCGM error",
			err:  assert.AnError,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsFatalScrapeError(tc.err))
		})
	}
}
