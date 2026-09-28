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

package exec

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	commands []Command
	results  []Result
}

func (f *fakeRunner) Run(_ context.Context, command Command) Result {
	f.commands = append(f.commands, command)
	if len(f.results) == 0 {
		return Result{ExitCode: -1}
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result
}

func TestFakeRunner(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: []byte("ok\n")}}}
	got := runner.Run(context.Background(), Command{Name: "echo", Args: []string{"ok"}})

	if string(got.Stdout) != "ok\n" {
		t.Fatalf("Stdout = %q, want ok", got.Stdout)
	}
	if len(runner.commands) != 1 || runner.commands[0].Name != "echo" {
		t.Fatalf("commands = %#v", runner.commands)
	}
}

func TestOSRunnerStreamsAndCapturesOutput(t *testing.T) {
	var streamed bytes.Buffer
	done := make(chan Result, 1)
	go func() {
		done <- OSRunner{}.Run(context.Background(), Command{
			Name:   "sh",
			Args:   []string{"-c", "printf start; sleep 0.2; printf end"},
			Stdout: &streamed,
		})
	}()

	deadline := time.After(time.Second)
	for !strings.Contains(streamed.String(), "start") {
		select {
		case <-deadline:
			t.Fatalf("output did not stream before command completed; streamed=%q", streamed.String())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	result := <-done
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", result.ExitCode, result.Stderr)
	}
	if string(result.Stdout) != "startend" || streamed.String() != "startend" {
		t.Fatalf("captured=%q streamed=%q, want startend", result.Stdout, streamed.String())
	}
}
