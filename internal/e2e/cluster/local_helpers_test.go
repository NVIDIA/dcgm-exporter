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
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// TestRunRequiredOmitsSensitiveFailureDetailsWithoutOutputWrapper preserves safe command context and cancellation while suppressing secret output.
func TestRunRequiredOmitsSensitiveFailureDetailsWithoutOutputWrapper(t *testing.T) {
	const marker = "SYNTHETIC-SECRET-DO-NOT-LOG"
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"kubectl apply -f -": {
			ExitCode: 1,
			Stdout:   []byte(marker + " stdout"),
			Stderr:   []byte(marker + " stderr"),
			Err:      errors.Join(errors.New(marker+" runner error"), context.Canceled),
		},
	}}
	var output bytes.Buffer
	err := runRequired(context.Background(), runner, &output, "apply pull secret", e2eexec.Command{
		Name:            "kubectl",
		Args:            []string{"apply", "-f", "-"},
		SensitiveOutput: true,
	})
	if err == nil || !strings.Contains(err.Error(), "kubectl apply -f -") || !strings.Contains(err.Error(), "exit code 1") {
		t.Fatalf("runRequired() error = %v, want safe command and exit context", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runRequired() error lost cancellation: %v", err)
	}
	if strings.Contains(err.Error(), marker) || strings.Contains(output.String(), marker) {
		t.Fatalf("sensitive failure data escaped: error=%q output=%q", err, output.String())
	}
	if runner.commands[0].Stdout != nil || runner.commands[0].Stderr != nil {
		t.Fatalf("sensitive command writers were not suppressed: %#v", runner.commands[0])
	}
}
