/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package container

import "testing"

// TestLocalHTTPURLUsesIPv4Loopback verifies local container checks avoid hostname resolution.
func TestLocalHTTPURLUsesIPv4Loopback(t *testing.T) {
	if got := localHTTPURL(9400, "/metrics"); got != "http://127.0.0.1:9400/metrics" {
		t.Fatalf("localHTTPURL() = %q", got)
	}
}

// TestImagePullUnavailableForPlatform distinguishes missing platforms from registry failures.
func TestImagePullUnavailableForPlatform(t *testing.T) {
	if !imagePullUnavailableForPlatform([]byte("no matching manifest for linux/arm64/v8 in the manifest list entries")) {
		t.Fatal("arm64 manifest mismatch was not detected")
	}
	if imagePullUnavailableForPlatform([]byte("unauthorized: authentication required")) {
		t.Fatal("registry authentication failure was classified as a missing platform")
	}
}

// TestLocalDockerEndpoint checks which Docker endpoints run containers on
// this host.
func TestLocalDockerEndpoint(t *testing.T) {
	tests := []struct {
		endpoint string
		want     bool
	}{
		{endpoint: "unix:///var/run/docker.sock", want: true},
		{endpoint: "tcp://127.0.0.1:2375", want: false},
		{endpoint: "tcp://localhost:2375", want: false},
		{endpoint: "tcp://[::1]:2375", want: false},
		{endpoint: "tcp://10.0.0.5:2376", want: false},
		{endpoint: "ssh://user@gpu-host", want: false},
		{endpoint: "", want: false},
	}

	for _, test := range tests {
		if got := localDockerEndpoint(test.endpoint); got != test.want {
			t.Errorf("localDockerEndpoint(%q) = %t, want %t", test.endpoint, got, test.want)
		}
	}
}
