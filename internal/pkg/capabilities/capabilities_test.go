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
	"testing"
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
