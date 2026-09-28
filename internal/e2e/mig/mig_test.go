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
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

func TestPrepareHostBeforeClusterMutatesAndRestores(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
		"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader":             []byte("0, Disabled\n"),
		"nvidia-smi --query-compute-apps=pid,gpu_uuid,process_name --format=csv,noheader": []byte(""),
		"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU (UUID: GPU-test)\n"),
	}}
	var stdout bytes.Buffer

	cleanup, err := PrepareHostBeforeCluster(context.Background(), &stdout, runner, config.Tests{
		MIGConfigure: "true",
		Scenarios:    []string{"k8s/mig"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArg("-cgi") || !runner.hasCommandArg("19") {
		t.Fatalf("MIG create command missing; commands = %#v", runner.commands)
	}
	cleanup(context.Background())
	for _, want := range []string{"-dci", "-dgi", "-mig"} {
		if !runner.hasCommandArg(want) {
			t.Fatalf("MIG cleanup command missing %q; commands = %#v", want, runner.commands)
		}
	}
	if !strings.Contains(stdout.String(), "Configuring MIG on GPU 0 with profile 19") {
		t.Fatalf("MIG log missing:\n%s", stdout.String())
	}
}

func TestPrepareHostBeforeClusterSkipsUnsupportedProfiles(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"nvidia-smi mig -i 0 -lgip": {ExitCode: 6, Stderr: []byte("No MIG-supported devices found\n")},
	}}
	var stdout bytes.Buffer

	cleanup, err := PrepareHostBeforeCluster(context.Background(), &stdout, runner, config.Tests{
		MIGConfigure: "true",
		Scenarios:    []string{"k8s/mig"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanup(context.Background())
	if runner.hasCommandArg("-mig") {
		t.Fatalf("MIG mutation ran on unsupported host: %#v", runner.commands)
	}
	if !strings.Contains(stdout.String(), "no MIG profiles were detected") {
		t.Fatalf("MIG unsupported log missing:\n%s", stdout.String())
	}
}

func TestPrepareHostBeforeClusterFailsWhenSafetyProbeFails(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string][]byte{
			"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
			"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader": []byte("0, Disabled\n"),
		},
		results: map[string]e2eexec.Result{
			"nvidia-smi --query-compute-apps=pid,gpu_uuid,process_name --format=csv,noheader": {
				ExitCode: 1,
				Stderr:   []byte("nvidia-smi failed"),
			},
		},
	}
	_, err := PrepareHostBeforeCluster(context.Background(), io.Discard, runner, config.Tests{
		MIGConfigure: "true",
		Scenarios:    []string{"k8s/mig"},
	})
	if err == nil || !strings.Contains(err.Error(), "query active GPU compute processes") {
		t.Fatalf("PrepareHostBeforeCluster() error = %v, want safety probe failure", err)
	}
	if runner.hasCommandArg("-mig") {
		t.Fatalf("MIG mutation ran after failed safety probe: %#v", runner.commands)
	}
}

func TestPrepareHostBeforeClusterFailsWhenCurrentModeUnreadable(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string][]byte{
			"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
		},
		results: map[string]e2eexec.Result{
			"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader": {
				ExitCode: 1,
				Stderr:   []byte("nvidia-smi failed"),
			},
		},
	}
	_, err := PrepareHostBeforeCluster(context.Background(), io.Discard, runner, config.Tests{
		MIGConfigure: "true",
		Scenarios:    []string{"k8s/mig"},
	})
	if err == nil || !strings.Contains(err.Error(), "query current MIG mode") {
		t.Fatalf("PrepareHostBeforeCluster() error = %v, want mode query failure", err)
	}
	if runner.hasCommandArg("-mig") {
		t.Fatalf("MIG mutation ran after unreadable current mode: %#v", runner.commands)
	}
}

type fakeRunner struct {
	outputs  map[string][]byte
	results  map[string]e2eexec.Result
	commands []e2eexec.Command
}

func (f *fakeRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	f.commands = append(f.commands, command)
	key := commandKey(command)
	if f.results != nil {
		if result, ok := f.results[key]; ok {
			return streamFakeResult(command, result)
		}
	}
	if f.outputs != nil {
		return streamFakeResult(command, e2eexec.Result{Stdout: f.outputs[key]})
	}
	return streamFakeResult(command, e2eexec.Result{Stdout: []byte("ok\n")})
}

func commandKey(command e2eexec.Command) string {
	return strings.TrimSpace(command.Name + " " + strings.Join(command.Args, " "))
}

func streamFakeResult(command e2eexec.Command, result e2eexec.Result) e2eexec.Result {
	if command.Stdout != nil && len(result.Stdout) != 0 {
		_, _ = command.Stdout.Write(result.Stdout)
	}
	if command.Stderr != nil && len(result.Stderr) != 0 {
		_, _ = command.Stderr.Write(result.Stderr)
	}
	return result
}

func (f *fakeRunner) hasCommandArg(value string) bool {
	for _, command := range f.commands {
		for _, arg := range command.Args {
			if arg == value {
				return true
			}
		}
	}
	return false
}
