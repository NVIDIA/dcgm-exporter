//go:build container

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

package container

import (
	"os"
	"testing"
)

// TestGetEnvOrDefaultUsesDefaultWhenUnset verifies unset image configuration uses the fallback value.
func TestGetEnvOrDefaultUsesDefaultWhenUnset(t *testing.T) {
	const key = "E2E_TEST_MISSING_IMAGE"
	previous, wasSet := os.LookupEnv(key)
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(key, previous)
		} else {
			_ = os.Unsetenv(key)
		}
	})
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset test env var: %v", err)
	}

	if got := getEnvOrDefault(key, "default-image"); got != "default-image" {
		t.Fatalf("expected unset env var to use default, got %q", got)
	}
}

// TestGetEnvOrDefaultPreservesExplicitEmpty verifies an explicitly empty image variable disables that image.
func TestGetEnvOrDefaultPreservesExplicitEmpty(t *testing.T) {
	t.Setenv("E2E_TEST_EMPTY_IMAGE", "")

	if got := getEnvOrDefault("E2E_TEST_EMPTY_IMAGE", "default-image"); got != "" {
		t.Fatalf("expected explicitly empty env var to be preserved, got %q", got)
	}
}
