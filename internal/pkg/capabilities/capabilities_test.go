/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

package capabilities

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetCurrentCapabilities(t *testing.T) {
	caps := GetCurrentCapabilities()
	if caps == "" {
		t.Error("Expected non-empty capability string")
	}
	t.Logf("Current capabilities: %s", caps)
}

func TestCheckSysAdmin(t *testing.T) {
	// This test will vary depending on how it's run
	// Just verify it doesn't panic
	hasCap := CheckSysAdmin()
	t.Logf("Has CAP_SYS_ADMIN: %v", hasCap)
}

func TestIsRunningAsRoot(t *testing.T) {
	// Just verify it doesn't panic
	isRoot := IsRunningAsRoot()
	t.Logf("Running as root: %v", isRoot)
}

func TestLogCapabilityInfo(t *testing.T) {
	// Just verify it doesn't panic
	LogCapabilityInfo()
}

func withProcStatus(t *testing.T, status string, err error) {
	t.Helper()
	prev := readProcStatus
	readProcStatus = func() ([]byte, error) {
		if err != nil {
			return nil, err
		}
		return []byte(status), nil
	}
	t.Cleanup(func() { readProcStatus = prev })
}

func TestHasCapabilityTable(t *testing.T) {
	tests := []struct {
		name    string
		cap     int
		status  string
		readErr error
		want    bool
		wantErr string
	}{
		{name: "invalid negative", cap: -1, wantErr: "invalid capability"},
		{name: "invalid high", cap: 64, wantErr: "invalid capability"},
		{name: "read error", cap: CAP_SYS_ADMIN, readErr: errors.New("read failed"), wantErr: "failed to read"},
		{name: "missing CapEff", cap: CAP_SYS_ADMIN, status: "Name:\ttest\n", wantErr: "could not find CapEff"},
		{name: "bad hex", cap: CAP_SYS_ADMIN, status: "CapEff:\tnot-hex\n", wantErr: "failed to parse"},
		{name: "cap present", cap: 1, status: "CapEff:\t0000000000000002\n", want: true},
		{name: "cap absent", cap: 2, status: "CapEff:\t0000000000000002\n", want: false},
		{name: "malformed CapEff line is ignored", cap: 1, status: "CapEff:\n", wantErr: "could not find CapEff"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withProcStatus(t, tt.status, tt.readErr)

			got, err := HasCapability(tt.cap)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCapabilityReportingBranches(t *testing.T) {
	withProcStatus(t, "CapEff:\t0000000000200002\n", nil)
	assert.True(t, CheckSysAdmin())
	assert.Contains(t, GetCurrentCapabilities(), "CAP_SYS_ADMIN")

	withProcStatus(t, "CapEff:\t0000000000000000\n", nil)
	assert.False(t, CheckSysAdmin())
	assert.Equal(t, "no capabilities", GetCurrentCapabilities())

	withProcStatus(t, "CapEff:\tnot-hex\n", nil)
	assert.Contains(t, GetCurrentCapabilities(), "error parsing")

	withProcStatus(t, "", errors.New("read failed"))
	assert.Contains(t, GetCurrentCapabilities(), "error reading capabilities")

	withProcStatus(t, "Name:\ttest\n", nil)
	assert.Equal(t, "unknown", GetCurrentCapabilities())
}

func TestWarnAndDropCapabilities(t *testing.T) {
	withProcStatus(t, "CapEff:\t0000000000000000\n", nil)
	assert.NotPanics(t, func() {
		WarnIfMissingProfilingCapabilities(false)
		WarnIfMissingProfilingCapabilities(true)
	})
	require.NoError(t, DropAllCapabilitiesExcept([]int{CAP_SYS_ADMIN}))
}
