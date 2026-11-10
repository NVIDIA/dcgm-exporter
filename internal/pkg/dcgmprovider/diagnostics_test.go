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

// TestParseDCGMStatusCode expects negative DCGM status codes to be read from startup
// error text, and addresses, versions, positive numbers and empty text to give no code.
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

// TestClassifyDCGMStartupError checks that a wrapped typed DCGM error is read by its code with a known status
// name, that a code parsed from error text is returned even when unknown, and that a typed error with a code
// that has no diagnostic is marked unknown.
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

// TestLogDCGMInitFailureEmbeddedDiagnostics expects an embedded start failure to log the
// error, the status code with its name and hint, the log settings, and the process user and capabilities.
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

// TestLogDCGMInitFailureRemoteDiagnostics expects a remote hostengine connection failure to
// log the address, the IPv6 hint, and the status code with its name and hint.
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

// libraryLoadError returns the error dcgm.Init reports when the dynamic loader
// cannot find libdcgm.so.4.
func libraryLoadError() error {
	return &dcgm.LibraryLoadError{
		Library: "libdcgm.so.4",
		Reason:  "libdcgm.so.4: cannot open shared object file: No such file or directory",
	}
}

// TestLogDCGMInitFailureLibraryGuidance expects a libdcgm load error, embedded or remote, to log the
// library name and LD_LIBRARY_PATH advice instead of the remote hostengine hint, and other errors not to.
func TestLogDCGMInitFailureLibraryGuidance(t *testing.T) {
	for _, tt := range []struct {
		name        string
		config      *appconfig.Config
		mode        string
		err         error
		extra       []slog.Attr
		wantLibrary bool
	}{
		{
			name:        "embedded",
			config:      &appconfig.Config{},
			mode:        dcgmInitModeEmbedded,
			err:         libraryLoadError(),
			wantLibrary: true,
		},
		{
			name: "remote hostengine",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "127.0.0.1:5555",
			},
			mode:        dcgmInitModeStandalone,
			err:         fmt.Errorf("initialize DCGM: %w", libraryLoadError()),
			extra:       []slog.Attr{slog.String("hint", remoteHostengineHint)},
			wantLibrary: true,
		},
		{
			name: "message text alone",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "127.0.0.1:5555",
			},
			mode:  dcgmInitModeStandalone,
			err:   errors.New("libdcgm.so.4 not found"),
			extra: []slog.Attr{slog.String("hint", remoteHostengineHint)},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logBuffer := captureDefaultSlog(t)

			logDCGMInitFailure(tt.config, tt.mode, tt.err, tt.extra...)

			logs := logBuffer.String()
			if !tt.wantLibrary {
				assert.NotContains(t, logs, "library=libdcgm.so.4")
				assert.NotContains(t, logs, "LD_LIBRARY_PATH")
				assert.Contains(t, logs, "For IPv6")
				return
			}

			assert.Contains(t, logs, "library=libdcgm.so.4")
			assert.Contains(t, logs, "compatible DCGM library")
			assert.Contains(t, logs, "LD_LIBRARY_PATH")
			assert.NotContains(t, logs, remoteHostengineHint)
		})
	}
}

// TestIsDCGMLibraryLoadError checks that the missing libdcgm error is recognised directly and when wrapped,
// and that an unrelated error or nil is not.
func TestIsDCGMLibraryLoadError(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "go-dcgm library load error",
			err:  libraryLoadError(),
			want: true,
		},
		{
			name: "wrapped go-dcgm library load error",
			err:  fmt.Errorf("initialize DCGM: %w", libraryLoadError()),
			want: true,
		},
		{
			name: "message text without the go-dcgm type",
			err:  errors.New("libdcgm.so.4 not found or could not be loaded: no such file; go-dcgm requires DCGM 4"),
		},
		{
			name: "unrelated error",
			err:  errors.New("failed to connect to nv-hostengine"),
		},
		{
			name: "nil error",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isDCGMLibraryLoadError(tt.err))
		})
	}
}

// TestLogDCGMInitFailureUnknownStatusDiagnostics expects an unknown status code to be logged
// with the unknown-status hint and no status name, both with and without an existing hint.
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

// TestLogDCGMFieldsInitFailureDiagnostics expects a fields module start failure to log the
// error, the status code with its name, and the driver-too-old hint.
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

// captureDefaultSlog redirects the default slog logger to a buffer for assertions.
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
