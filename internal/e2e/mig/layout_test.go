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

package mig

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// h100Profiles is "nvidia-smi mig -i 0 -lgip" output on an H100 80GB.
const h100Profiles = `+-----------------------------------------------------------------------------+
| GPU instance profiles:                                                      |
| GPU   Name             ID    Instances   Memory     P2P    SM    DEC   ENC  |
|                              Free/Total   GiB              CE    JPEG  OFA  |
|=============================================================================|
|   0  MIG 1g.10gb       19     7/7        9.75       No     16     1     0   |
|                                                             1     1     0   |
+-----------------------------------------------------------------------------+
|   0  MIG 1g.10gb+me    20     1/1        9.75       No     16     1     0   |
|                                                             1     1     1   |
+-----------------------------------------------------------------------------+
|   0  MIG 1g.20gb       15     4/4        19.62      No     26     1     0   |
|                                                             1     1     0   |
+-----------------------------------------------------------------------------+
|   0  MIG 2g.20gb       14     3/3        19.62      No     32     2     0   |
|                                                             2     2     0   |
+-----------------------------------------------------------------------------+
|   0  MIG 3g.40gb        9     2/2        39.50      No     60     3     0   |
|                                                             3     3     0   |
+-----------------------------------------------------------------------------+
|   0  MIG 4g.40gb        5     1/1        39.50      No     64     4     0   |
|                                                             4     4     0   |
+-----------------------------------------------------------------------------+
|   0  MIG 7g.80gb        0     1/1        79.25      No     132    7     0   |
|                                                             8     7     1   |
+-----------------------------------------------------------------------------+`

// b200Profiles is the profile list of a B200, cut to the rows that matter.
const b200Profiles = `|   1  MIG 1g.23gb       19     7/7        21.75      No     16     1     0   |
|   1  MIG 2g.45gb       14     3/3        44.25      No     36     2     0   |
|   1  MIG 3g.90gb        9     2/2        89.50      No     72     3     0   |
|   1  MIG 4g.90gb        5     1/1        89.50      No     72     4     0   |
|   1  MIG 7g.180gb       0     1/1        178.50     No     148    7     0   |`

// TestLargestProfileFittingTwice checks that the largest profile with two free
// instances is chosen, and that output without one chooses nothing.
func TestLargestProfileFittingTwice(t *testing.T) {
	for _, tt := range []struct {
		name   string
		output string
		want   string
		wantOK bool
	}{
		{name: "H100", output: h100Profiles, want: "9", wantOK: true},
		{name: "B200", output: b200Profiles, want: "9", wantOK: true},
		{name: "one instance left of each", output: strings.ReplaceAll(b200Profiles, "2/2", "1/2"), want: "14", wantOK: true},
		{name: "no profile rows", output: "No MIG-enabled devices found.\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := largestProfileFittingTwice(tt.output)

			if ok != tt.wantOK || got.id != tt.want {
				t.Fatalf("largestProfileFittingTwice() = %+v, %t; want ID %q, %t", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// layoutRunner returns a fake host with the given "index, mode" lines, no
// compute processes, no MIG instances and the H100 profiles on every GPU.
func layoutRunner(modes string) *fakeRunner {
	return &fakeRunner{outputs: map[string][]byte{
		"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader":             []byte(modes),
		"nvidia-smi --query-compute-apps=pid,gpu_uuid,process_name --format=csv,noheader": []byte(""),
		"nvidia-smi -L":                             []byte("GPU 0: NVIDIA H100 (UUID: GPU-0)\nGPU 1: NVIDIA H100 (UUID: GPU-1)\n"),
		"nvidia-smi mig -i 0 -lgip":                 []byte(h100Profiles),
		"nvidia-smi mig -i 1 -lgip":                 []byte(h100Profiles),
		commandKey(sudoCommand("cat", cdiSpecPath)): []byte("spec"),
	}}
}

// mutatingCommands returns the nvidia-smi commands of runner that change MIG.
func mutatingCommands(runner *fakeRunner) []string {
	var got []string
	for _, command := range runner.commands {
		key := commandKey(command)
		if slices.ContainsFunc([]string{"-mig", "-cgi", "-dci", "-dgi"}, func(flag string) bool {
			return slices.Contains(command.Args, flag)
		}) {
			got = append(got, strings.TrimPrefix(key, "sudo "))
		}
	}
	return got
}

// applyLayoutForTest runs applyTwoInstancesPerGPU with a private lock.
func applyLayoutForTest(t *testing.T, runner e2eexec.Runner) (func(context.Context) error, error) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("MIG host configuration requires Linux")
	}
	return applyTwoInstancesPerGPU(context.Background(), io.Discard, runner, testMIGLockPath(t), os.Geteuid())
}

// TestApplyTwoInstancesPerGPUCreatesAndRestores checks that every GPU gets MIG
// and two instances of the largest profile that fits twice, and that restore
// removes them and turns MIG off only where it was off.
func TestApplyTwoInstancesPerGPUCreatesAndRestores(t *testing.T) {
	runner := layoutRunner("0, Disabled\n1, Enabled\n")

	restore, err := applyLayoutForTest(t, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(context.Background()); err != nil {
		t.Fatalf("restore error = %v", err)
	}

	want := []string{
		"nvidia-smi -i 0 -mig 1",
		"nvidia-smi -i 1 -mig 1",
		"nvidia-smi mig -i 0 -cgi 9,9 -C",
		"nvidia-smi mig -i 1 -cgi 9,9 -C",
		"nvidia-smi mig -i 0 -dci",
		"nvidia-smi mig -i 0 -dgi",
		"nvidia-smi -i 0 -mig 0",
		"nvidia-smi mig -i 1 -dci",
		"nvidia-smi mig -i 1 -dgi",
	}
	if got := mutatingCommands(runner); !slices.Equal(got, want) {
		t.Fatalf("MIG commands = %v, want %v", got, want)
	}
}

// TestApplyTwoInstancesPerGPURollsBackFailedCreate checks that a failed
// instance creation restores every GPU before the error returns.
func TestApplyTwoInstancesPerGPURollsBackFailedCreate(t *testing.T) {
	runner := layoutRunner("0, Disabled\n1, Disabled\n")
	runner.results = map[string]e2eexec.Result{
		commandKey(sudoCommand("nvidia-smi", "mig", "-i", "1", "-cgi", "9,9", "-C")): {ExitCode: 1, Stderr: []byte("setup failure")},
	}

	_, err := applyLayoutForTest(t, runner)

	if err == nil || !strings.Contains(err.Error(), "setup failure") {
		t.Fatalf("apply error = %v, want the create failure", err)
	}
	got := mutatingCommands(runner)
	for _, want := range []string{
		"nvidia-smi mig -i 0 -dci",
		"nvidia-smi mig -i 0 -dgi",
		"nvidia-smi -i 0 -mig 0",
		"nvidia-smi mig -i 1 -dci",
		"nvidia-smi mig -i 1 -dgi",
		"nvidia-smi -i 1 -mig 0",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("rollback commands %v miss %q", got, want)
		}
	}
}

// TestApplyTwoInstancesPerGPURefusesUnsupportedHosts checks that a host with
// one GPU, or a GPU without MIG, is reported as unsupported and left alone,
// and that existing MIG instances are refused.
func TestApplyTwoInstancesPerGPURefusesUnsupportedHosts(t *testing.T) {
	for _, tt := range []struct {
		name            string
		modes           string
		existing        string
		wantUnsupported bool
	}{
		{name: "one GPU", modes: "0, Disabled\n", wantUnsupported: true},
		{name: "GPU without MIG", modes: "0, Disabled\n1, [N/A]\n", wantUnsupported: true},
		{name: "existing MIG instances", modes: "0, Enabled\n1, Enabled\n", existing: "  MIG 3g.40gb Device 0: (UUID: MIG-1)\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := layoutRunner(tt.modes)
			if tt.existing != "" {
				runner.outputs["nvidia-smi -L"] = []byte(tt.existing)
			}

			_, err := applyLayoutForTest(t, runner)

			if err == nil || errors.Is(err, ErrLayoutUnsupported) != tt.wantUnsupported {
				t.Fatalf("apply error = %v, want unsupported %t", err, tt.wantUnsupported)
			}
			if got := mutatingCommands(runner); len(got) != 0 {
				t.Fatalf("MIG changed on a refused host: %v", got)
			}
		})
	}
}

// TestDeclaredLayout checks that the selection's declared layout is found and
// that a layout cannot share a run with scenarios that leave MIG alone.
func TestDeclaredLayout(t *testing.T) {
	withLayout := scenario.Scenario{Suite: scenario.SuiteHost, Name: "withLayout", MIGLayout: scenario.MIGLayoutTwoInstancesPerGPU}
	without := scenario.Scenario{Suite: scenario.SuiteHost, Name: "without"}
	for _, tt := range []struct {
		name     string
		selected []scenario.Scenario
		want     scenario.MIGLayout
		wantErr  string
	}{
		{name: "no layout", selected: []scenario.Scenario{without}},
		{name: "layout", selected: []scenario.Scenario{withLayout}, want: scenario.MIGLayoutTwoInstancesPerGPU},
		{name: "mixed", selected: []scenario.Scenario{withLayout, without}, want: scenario.MIGLayoutTwoInstancesPerGPU, wantErr: "run it without host/without"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := declaredLayout(tt.selected)

			if got != tt.want || (err == nil) != (tt.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("declaredLayout() = %q, %v; want %q, %q", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

// TestPrepareHostLayoutWithoutLayoutChangesNothing checks that a selection
// without a declared layout runs no command and needs no cleanup.
func TestPrepareHostLayoutWithoutLayoutChangesNothing(t *testing.T) {
	runner := &fakeRunner{}

	cleanup, env, err := PrepareHostLayout(context.Background(), io.Discard, runner, []scenario.Scenario{{Suite: scenario.SuiteHost, Name: "startupMetrics"}})

	if err != nil || env != nil || len(runner.commands) != 0 {
		t.Fatalf("PrepareHostLayout() env %v, error %v, commands %v; want nothing", env, err, runner.commands)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}
