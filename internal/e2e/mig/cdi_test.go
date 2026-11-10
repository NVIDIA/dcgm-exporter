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
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// Command keys of the CDI save and restore steps.
var (
	testDirKey     = commandKey(sudoCommand("test", "-d", cdiSpecDir))
	testSpecKey    = commandKey(sudoCommand("test", "-e", cdiSpecPath))
	testLinkKey    = commandKey(sudoCommand("test", "-L", cdiSpecPath))
	statSpecKey    = commandKey(sudoCommand("stat", "-c", "%a %u:%g", cdiSpecPath))
	catSpecKey     = commandKey(sudoCommand("cat", cdiSpecPath))
	installSpecKey = commandKey(sudoCommand("install", "-m", "640", "-o", "0", "-g", "44", "/dev/stdin", cdiSpecPath))
	removeSpecKey  = commandKey(sudoCommand("rm", "-f", cdiSpecPath))
	removeDirKey   = commandKey(sudoCommand("rmdir", "--ignore-fail-on-non-empty", cdiSpecDir))
	generateKey    = commandKey(sudoCommand("nvidia-ctk", "cdi", "generate", "--output="+cdiSpecPath))
)

// missing is what "test" returns for a path that does not exist.
var missing = e2eexec.Result{ExitCode: 1}

// TestSaveCDISpec checks what is recorded for each state of the host's CDI spec.
func TestSaveCDISpec(t *testing.T) {
	tests := []struct {
		name    string
		results map[string]e2eexec.Result
		want    cdiSpecState
		wantErr string
	}{
		{
			name: "spec present",
			results: map[string]e2eexec.Result{
				statSpecKey: {Stdout: []byte("640 0:44\n")},
				catSpecKey:  {Stdout: []byte("cdiVersion: 0.5.0\n")},
			},
			want: cdiSpecState{dirExisted: true, exists: true, content: []byte("cdiVersion: 0.5.0\n"), mode: "640", uid: "0", gid: "44"},
		},
		{
			name:    "directory without a spec",
			results: map[string]e2eexec.Result{testSpecKey: missing},
			want:    cdiSpecState{dirExisted: true},
		},
		{
			name:    "no directory",
			results: map[string]e2eexec.Result{testDirKey: missing, testSpecKey: missing},
			want:    cdiSpecState{},
		},
		{
			name:    "symbolic link",
			results: map[string]e2eexec.Result{testLinkKey: {}},
			wantErr: "/etc/cdi/nvidia.yaml is a symbolic link",
		},
		{
			name:    "unreadable path",
			results: map[string]e2eexec.Result{testSpecKey: {ExitCode: 2, Stderr: []byte("permission denied\n")}},
			wantErr: "check /etc/cdi/nvidia.yaml",
		},
		{
			name:    "unexpected stat output",
			results: map[string]e2eexec.Result{statSpecKey: {Stdout: []byte("rw-r--r-- root\n")}},
			wantErr: "unexpected stat output",
		},
		{
			name: "unreadable spec",
			results: map[string]e2eexec.Result{
				statSpecKey: {Stdout: []byte("644 0:0\n")},
				catSpecKey:  {ExitCode: 1, Err: errors.New("exit status 1")},
			},
			wantErr: "read /etc/cdi/nvidia.yaml before MIG configuration",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := saveCDISpec(context.Background(), &fakeRunner{results: test.results})
			assertErrorContains(t, err, test.wantErr)
			if test.wantErr == "" && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("state = %+v, want %+v", got, test.want)
			}
		})
	}
}

// assertErrorContains fails unless err contains want, or is nil when want is empty.
func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" && err != nil {
		t.Fatal(err)
	}
	if want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// TestRestoreCDISpec checks that the saved spec is written back with its mode
// and owner, or that what the run created is removed.
func TestRestoreCDISpec(t *testing.T) {
	tests := []struct {
		name      string
		state     cdiSpecState
		results   map[string]e2eexec.Result
		want      []string
		wantStdin string
	}{
		{
			name:      "spec present",
			state:     cdiSpecState{dirExisted: true, exists: true, content: []byte("cdiVersion: 0.5.0\n"), mode: "640", uid: "0", gid: "44"},
			want:      []string{installSpecKey},
			wantStdin: "cdiVersion: 0.5.0\n",
		},
		{name: "directory without a spec", state: cdiSpecState{dirExisted: true}, want: []string{removeSpecKey}},
		{name: "directory created by the run", state: cdiSpecState{}, want: []string{removeSpecKey, testDirKey, removeDirKey}},
		{
			name:    "directory never created",
			state:   cdiSpecState{},
			results: map[string]e2eexec.Result{testDirKey: missing},
			want:    []string{removeSpecKey, testDirKey},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{results: test.results}
			if err := restoreCDISpec(context.Background(), io.Discard, runner, test.state); err != nil {
				t.Fatal(err)
			}

			var got []string
			for _, command := range runner.commands {
				got = append(got, commandKey(command))
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("commands = %v, want %v", got, test.want)
			}
			if string(runner.commands[0].Stdin) != test.wantStdin {
				t.Fatalf("stdin = %q, want %q", runner.commands[0].Stdin, test.wantStdin)
			}
		})
	}
}

// TestPrepareHostBeforeClusterRestoresCDISpec checks the whole MIG step on a
// host without a CDI directory: with nvidia-ctk the spec is generated after MIG
// is configured, and cleanup removes the spec and the directory it created
// after MIG is restored; without nvidia-ctk cleanup leaves the directory alone.
func TestPrepareHostBeforeClusterRestoresCDISpec(t *testing.T) {
	disable := commandKey(sudoCommand("nvidia-smi", "-i", defaultMIGGPUIndex, "-mig", "0"))
	mkdir := commandKey(sudoCommand("mkdir", "-p", cdiSpecDir))
	ctkLookup := commandKey(e2eexec.Command{Name: "sh", Args: []string{"-c", "command -v nvidia-ctk"}})
	tests := []struct {
		name      string
		ctkResult e2eexec.Result
		wantOrder []string
		wantNone  []string
	}{
		{name: "with nvidia-ctk", wantOrder: []string{generateKey, disable, removeSpecKey, removeDirKey}},
		{name: "without nvidia-ctk", ctkResult: missing, wantOrder: []string{disable, removeSpecKey}, wantNone: []string{generateKey, removeDirKey}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{
				outputs: map[string][]byte{
					"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
					"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader":             []byte("0, Disabled\n"),
					"nvidia-smi --query-compute-apps=pid,gpu_uuid,process_name --format=csv,noheader": []byte(""),
					"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU (UUID: GPU-test)\n"),
				},
				results: map[string]e2eexec.Result{testDirKey: missing, testSpecKey: missing, ctkLookup: test.ctkResult},
			}
			// The directory exists once the run creates it.
			runner.beforeRun = func(_ context.Context, command e2eexec.Command) {
				if commandKey(command) == mkdir {
					delete(runner.results, testDirKey)
				}
			}

			cleanup, err := prepareMIGForTest(t, context.Background(), io.Discard, runner, config.TestOptions{
				MIGConfigure: "true",
				Scenarios:    []string{"k8s/mig"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := cleanup(context.Background()); err != nil {
				t.Fatal(err)
			}

			assertCommandOrder(t, runner, test.wantOrder, test.wantNone)
		})
	}
}

// assertCommandOrder fails unless runner ran each command in want, in that order, and none in
// unwanted.
func assertCommandOrder(t *testing.T, runner *fakeRunner, want, unwanted []string) {
	t.Helper()
	var got []string
	for _, command := range runner.commands {
		key := commandKey(command)
		if slices.Contains(want, key) || slices.Contains(unwanted, key) {
			got = append(got, key)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("commands = %v, want %v in this order", got, want)
	}
}

// TestCDIRegenerationFailureRollsBackMIG checks that a failed CDI spec
// regeneration fails MIG configuration, turns MIG off on every GPU it turned
// on, and puts the saved CDI spec back, for the one-GPU Kubernetes step and
// for the two-instances-per-GPU layout.
func TestCDIRegenerationFailureRollsBackMIG(t *testing.T) {
	mkdir := commandKey(sudoCommand("mkdir", "-p", cdiSpecDir))
	failure := e2eexec.Result{ExitCode: 1, Stderr: []byte("cdi failure")}
	paths := map[string]struct {
		configure    func(*testing.T, *fakeRunner) error
		wantRollback []string
	}{
		"one GPU": {
			configure: func(t *testing.T, runner *fakeRunner) error {
				_, err := prepareMIGForTest(t, context.Background(), io.Discard, runner, config.TestOptions{
					MIGConfigure: "true",
					Scenarios:    []string{"k8s/mig"},
				})
				return err
			},
			wantRollback: []string{"nvidia-smi -i 0 -mig 0"},
		},
		"two instances per GPU": {
			configure: func(t *testing.T, runner *fakeRunner) error {
				_, err := applyLayoutForTest(t, runner)
				return err
			},
			wantRollback: []string{"nvidia-smi -i 0 -mig 0", "nvidia-smi -i 1 -mig 0"},
		},
	}
	for _, step := range []string{mkdir, generateKey} {
		for name, path := range paths {
			t.Run(name+"/"+step, func(t *testing.T) {
				runner := layoutRunner("0, Disabled\n1, Disabled\n")
				runner.outputs["nvidia-smi mig -i 0 -lgip"] = []byte(h100Profiles + "MIG 1g.10gb 19\n")
				runner.results = map[string]e2eexec.Result{step: failure}

				err := path.configure(t, runner)

				assertErrorContains(t, err, "regenerate the NVIDIA CDI spec after MIG configuration")
				assertRolledBack(t, runner, path.wantRollback)
			})
		}
	}
}

// assertRolledBack fails unless runner turned MIG off with each command in
// wantMIGOff and wrote back layoutRunner's saved CDI spec, root-owned with
// mode 644.
func assertRolledBack(t *testing.T, runner *fakeRunner, wantMIGOff []string) {
	t.Helper()
	for _, want := range wantMIGOff {
		if !slices.Contains(mutatingCommands(runner), want) {
			t.Fatalf("MIG was not rolled back with %q: %v", want, mutatingCommands(runner))
		}
	}

	restoreKey := commandKey(sudoCommand("install", "-m", "644", "-o", "0", "-g", "0", "/dev/stdin", cdiSpecPath))
	if !slices.ContainsFunc(runner.commands, func(command e2eexec.Command) bool {
		return commandKey(command) == restoreKey && string(command.Stdin) == "spec"
	}) {
		t.Fatalf("the saved CDI spec was not restored: %v", runner.commands)
	}
}
