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
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestParseDCGMStatusCode(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    int
		wantOK  bool
	}{
		{
			name:    "startup error format",
			message: "CacheManager Init Failed. Error: -17",
			want:    dcgm.DCGM_ST_NO_PERMISSION,
			wantOK:  true,
		},
		{
			name:    "lowercase error code format",
			message: "dcgm failed with error code -44",
			want:    dcgm.DCGM_ST_INSUFFICIENT_DRIVER_VERSION,
			wantOK:  true,
		},
		{
			name:    "does not parse IPv4 address",
			message: "connect failed: 127.0.0.1:5555",
		},
		{
			name:    "does not parse IPv6 address",
			message: "address [::1]:5555 unreachable",
		},
		{
			name:    "does not parse version string",
			message: "version 4.5.3",
		},
		{
			name:    "does not parse positive number",
			message: "Error: 17",
		},
		{
			name: "does not parse empty message",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseDCGMStatusCode(tc.message)

			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestClassifyDCGMStartupError(t *testing.T) {
	t.Run("uses typed errors first", func(t *testing.T) {
		status, ok := classifyDCGMStartupError(fmt.Errorf("wrapped: %w", &dcgm.Error{Code: dcgm.DCGM_ST_NVML_ERROR}))

		require.True(t, ok)
		assert.True(t, status.known)
		assert.Equal(t, dcgm.DCGM_ST_NVML_ERROR, status.code)
		assert.Equal(t, "DCGM_ST_NVML_ERROR", status.diagnostic.Status)
	})

	t.Run("returns unknown parsed status code", func(t *testing.T) {
		status, ok := classifyDCGMStartupError(errors.New("CacheManager Init Failed. Error: -999"))

		require.True(t, ok)
		assert.False(t, status.known)
		assert.Equal(t, -999, status.code)
	})

	t.Run("typed dcgm error with unknown code", func(t *testing.T) {
		status, ok := classifyDCGMStartupError(&dcgm.Error{Code: dcgm.DCGM_ST_NOT_CONFIGURED})

		require.True(t, ok)
		assert.False(t, status.known)
		assert.Equal(t, dcgm.DCGM_ST_NOT_CONFIGURED, status.code)
	})
}

func TestLogDCGMInitFailureEmbeddedDiagnostics(t *testing.T) {
	logBuffer := captureDefaultSlog(t)
	config := &appconfig.Config{
		EnableDCGMLog: true,
		DCGMLogLevel:  "DEBUG",
	}

	logDCGMInitFailure(config, dcgmInitModeEmbedded, errors.New("error starting nv-hostengine: CacheManager Init Failed. Error: -17"))

	logs := logBuffer.String()
	assert.Contains(t, logs, `msg="Failed to initialize embedded DCGM"`)
	assert.Contains(t, logs, `error="error starting nv-hostengine: CacheManager Init Failed. Error: -17"`)
	assert.Contains(t, logs, "dcgm_status_code=-17")
	assert.Contains(t, logs, "dcgm_status_name=DCGM_ST_NO_PERMISSION")
	assert.Contains(t, logs, "DCGM does not have permission to perform the requested operation")
	assert.Contains(t, logs, "mode=embedded")
	assert.Contains(t, logs, "enable_dcgm_log=true")
	assert.Contains(t, logs, "dcgm_log_level=DEBUG")
	assert.Contains(t, logs, "uid=")
	assert.Contains(t, logs, "euid=")
	assert.Contains(t, logs, "has_cap_sys_admin=")
}

func TestLogDCGMInitFailureRemoteDiagnostics(t *testing.T) {
	logBuffer := captureDefaultSlog(t)
	config := &appconfig.Config{
		UseRemoteHE:  true,
		RemoteHEInfo: "127.0.0.1:1",
		DCGMLogLevel: "NONE",
	}

	logDCGMInitFailure(
		config,
		dcgmInitModeStandalone,
		errors.New("error connecting to nv-hostengine: Error: -21"),
		slog.String("hint", remoteHostengineHint),
	)

	logs := logBuffer.String()
	assert.Contains(t, logs, `msg="Failed to connect to remote hostengine"`)
	assert.Contains(t, logs, "address=127.0.0.1:1")
	assert.Contains(t, logs, "For IPv6")
	assert.Contains(t, logs, "dcgm_status_code=-21")
	assert.Contains(t, logs, "dcgm_status_name=DCGM_ST_CONNECTION_NOT_VALID")
	assert.Contains(t, logs, "dcgm_status_hint=")
	assert.Contains(t, logs, "Verify nv-hostengine is running and restart dcgm-exporter after the DCGM connection recovers.")
}

func TestLogDCGMInitFailureUnknownStatusDiagnostics(t *testing.T) {
	t.Run("without existing hint", func(t *testing.T) {
		logBuffer := captureDefaultSlog(t)

		logDCGMInitFailure(&appconfig.Config{}, dcgmInitModeEmbedded, errors.New("CacheManager Init Failed. Error: -999"))

		logs := logBuffer.String()
		assert.Contains(t, logs, "dcgm_status_code=-999")
		assert.NotContains(t, logs, "dcgm_status_name=")
		assert.Contains(t, logs, "hint=")
		assert.Contains(t, logs, unknownDCGMStatusHint)
	})

	t.Run("with existing hint", func(t *testing.T) {
		logBuffer := captureDefaultSlog(t)

		logDCGMInitFailure(
			&appconfig.Config{UseRemoteHE: true, RemoteHEInfo: "127.0.0.1:1"},
			dcgmInitModeStandalone,
			errors.New("error connecting to nv-hostengine: Error: -999"),
			slog.String("hint", remoteHostengineHint),
		)

		logs := logBuffer.String()
		assert.Contains(t, logs, "address=127.0.0.1:1")
		assert.Contains(t, logs, "For IPv6")
		assert.Contains(t, logs, "dcgm_status_code=-999")
		assert.NotContains(t, logs, "dcgm_status_name=")
		assert.Contains(t, logs, "dcgm_status_hint=")
		assert.Contains(t, logs, unknownDCGMStatusHint)
	})
}

func TestLogDCGMFieldsInitFailureDiagnostics(t *testing.T) {
	logBuffer := captureDefaultSlog(t)
	config := &appconfig.Config{DCGMLogLevel: "NONE"}

	logDCGMFieldsInitFailure(config, dcgm.DCGM_ST_INSUFFICIENT_DRIVER_VERSION)

	logs := logBuffer.String()
	assert.Contains(t, logs, `msg="Failed to initialize DCGM Fields module"`)
	assert.Contains(t, logs, "mode=fields")
	assert.Contains(t, logs, "error=\"Failed to initialize DCGM Fields module; err: -44\"")
	assert.Contains(t, logs, "dcgm_status_code=-44")
	assert.Contains(t, logs, "dcgm_status_name=DCGM_ST_INSUFFICIENT_DRIVER_VERSION")
	assert.Contains(t, logs, "The installed NVIDIA driver is too old for the requested DCGM API.")
}

func captureDefaultSlog(t *testing.T) *bytes.Buffer {
	t.Helper()

	originalLogger := slog.Default()

	var logBuffer bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuffer, nil)))

	t.Cleanup(func() {
		slog.SetDefault(originalLogger)
	})

	return &logBuffer
}
