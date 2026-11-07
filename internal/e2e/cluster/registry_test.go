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

package cluster

import (
	"context"
	"io"
	"strings"
	"testing"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

func TestEnsureImagePullSecretRejectsPartialInputs(t *testing.T) {
	err := EnsureImagePullSecret(context.Background(), &recordingRunner{}, io.Discard, DefaultConfig(), "dcgm-exporter", "pull-secret", "")
	if err == nil || !strings.Contains(err.Error(), "requires both secret name and docker config path") {
		t.Fatalf("EnsureImagePullSecret() error = %v, want partial input error", err)
	}
}

func TestRequireImagePullSecretRejectsMissingSecret(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		`kubectl -n dcgm-exporter get secret pull-secret -o jsonpath={.type}{"\t"}{.data.\.dockerconfigjson}`: {ExitCode: 1, Stderr: []byte("not found")},
	}}
	err := RequireImagePullSecret(context.Background(), runner, DefaultConfig(), "dcgm-exporter", "pull-secret")
	if err == nil || !strings.Contains(err.Error(), "required image pull secret") {
		t.Fatalf("RequireImagePullSecret() error = %v, want missing Secret error", err)
	}
}

func TestRequireImagePullSecretValidatesTypeAndPayload(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantError bool
	}{
		{name: "wrong type", output: "Opaque\te30=", wantError: true},
		{name: "empty docker config", output: "kubernetes.io/dockerconfigjson\t", wantError: true},
		{name: "valid", output: "kubernetes.io/dockerconfigjson\te30="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{results: map[string]e2eexec.Result{
				`kubectl -n dcgm-exporter get secret pull-secret -o jsonpath={.type}{"\t"}{.data.\.dockerconfigjson}`: {
					Stdout: []byte(tt.output),
				},
			}}
			err := RequireImagePullSecret(context.Background(), runner, DefaultConfig(), "dcgm-exporter", "pull-secret")
			if tt.wantError && (err == nil || !strings.Contains(err.Error(), "kubernetes.io/dockerconfigjson")) {
				t.Fatalf("RequireImagePullSecret() error = %v, want invalid Secret error", err)
			}
			if !tt.wantError && err != nil {
				t.Fatalf("RequireImagePullSecret() error = %v, want nil", err)
			}
		})
	}
}
