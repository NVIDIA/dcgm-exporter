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

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/mig"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/suite"
)

// TestContainerGroupNeedsVSOCK covers when the container suite must fail, not skip, VSOCK specs.
func TestContainerGroupNeedsVSOCK(t *testing.T) {
	withVSOCK := scenario.Scenario{Group: scenario.GroupContainerRemoteDCGM, Requirements: []scenario.Requirement{{Kind: scenario.RequirementVSOCK}}}
	without := scenario.Scenario{Group: scenario.GroupContainerBaseline}
	check := config.TestOptions{BootstrapMode: config.BootstrapCheck}
	skip := config.TestOptions{BootstrapMode: config.BootstrapCheck, BootstrapComponents: map[config.BootstrapComponent]config.BootstrapMode{config.BootstrapVSOCK: config.BootstrapSkip}}

	tests := []struct {
		name  string
		group string
		opts  config.TestOptions
		want  bool
	}{
		{name: "required and check", group: scenario.GroupContainerRemoteDCGM, opts: check, want: true},
		{name: "required and skip", group: scenario.GroupContainerRemoteDCGM, opts: skip},
		{name: "group without VSOCK", group: scenario.GroupContainerBaseline, opts: check},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := containerGroupNeedsVSOCK([]scenario.Scenario{withVSOCK, without}, test.group, test.opts); got != test.want {
				t.Fatalf("containerGroupNeedsVSOCK() = %v, want %v", got, test.want)
			}
		})
	}
}

// TestHostVSOCKGroupRequiresVSOCKUnlessSkipped covers the host group's VSOCK environment.
func TestHostVSOCKGroupRequiresVSOCKUnlessSkipped(t *testing.T) {
	skip := map[config.BootstrapComponent]config.BootstrapMode{config.BootstrapVSOCK: config.BootstrapSkip}
	tests := []struct {
		name string
		opts config.TestOptions
		want bool
	}{
		{name: "check", opts: config.TestOptions{BootstrapMode: config.BootstrapCheck}, want: true},
		{name: "install", opts: config.TestOptions{BootstrapMode: config.BootstrapInstall}, want: true},
		{name: "vsock skipped", opts: config.TestOptions{BootstrapMode: config.BootstrapCheck, BootstrapComponents: skip}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, env, err := prepareHostGroupRuntime(context.Background(), io.Discard, suite.TestBinaryManager{}, &fakeRunner{}, test.opts, scenario.GroupHostVSOCK)
			if err != nil {
				t.Fatal(err)
			}
			if got := hasArg(env, "E2E_REQUIRE_VSOCK=1"); got != test.want {
				t.Fatalf("E2E_REQUIRE_VSOCK set = %v, want %v: %#v", got, test.want, env)
			}
			if !hasArg(env, "E2E_REQUIRE_DCGM=1") {
				t.Fatalf("E2E_REQUIRE_DCGM=1 missing: %#v", env)
			}
		})
	}
}

// TestPrepareHostMIGLayout checks that an unsupported host skips the host
// suite and that the layout's cleanup and test environment pass through.
func TestPrepareHostMIGLayout(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		env      []string
		wantSkip bool
		wantErr  bool
	}{
		{name: "layout applied", env: []string{"E2E_MIG_LAYOUT=two-instances-per-gpu"}},
		{name: "unsupported host", err: fmt.Errorf("%w: 1 GPUs", mig.ErrLayoutUnsupported), wantSkip: true, wantErr: true},
		{name: "failed change", err: errors.New("enable MIG"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := prepareHostLayout
			t.Cleanup(func() { prepareHostLayout = previous })
			prepareHostLayout = func(context.Context, io.Writer, e2eexec.Runner, []scenario.Scenario) (func(context.Context) error, []string, error) {
				return func(context.Context) error { return nil }, test.env, test.err
			}

			cleanup, env, err := prepareHostMIGLayout(context.Background(), io.Discard, &fakeRunner{}, nil)

			require.Equal(t, test.wantSkip, errors.Is(err, errSuiteSkipped), "suite skipped: %v", err)
			require.Equal(t, test.wantErr, err != nil, "error: %v", err)
			require.Equal(t, test.env, env)
			require.NoError(t, cleanup(context.Background()))
		})
	}
}
