/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package stdout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const captureTestEnv = "DCGM_EXPORTER_TEST_CAPTURE"

func captureTestMessage(name string) string {
	switch name {
	case "dcgm":
		return "2024-02-07 18:01:05.641 INFO  [517155:517155] Linux 4.15.0-180-generic [{anonymous}::StartEmbeddedV2]\n"
	case "date":
		return "2024-02-07 18:01:05.641\n"
	case "multiline":
		return "2024-02-07 18:01:05.641 ERROR [1:1] Couldn't parse json: '[\n" +
			strings.Repeat("{\"id\":\"core\"}\n", 17000) + "]'\n"
	case "long-line", "canceled":
		return strings.Repeat("x", 218000) + "\n"
	case "partial-line":
		return "unterminated output"
	case "cgo":
		return "Boom\n"
	default:
		return "hello from dcgm\n"
	}
}

func TestCapture(t *testing.T) {
	for _, name := range []string{"raw", "date", "dcgm", "multiline", "long-line", "partial-line", "cgo", "canceled", "inner-error", "repeated"} {
		t.Run(name, func(t *testing.T) {
			// Capture changes process-wide fd 1. A subprocess exercises both Go
			// and native writes without redirecting the test runner's output.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// #nosec G204,G702 -- Re-execute this test binary with a fixed test selector.
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCaptureProcess$")
			cmd.Env = append(os.Environ(), captureTestEnv+"="+name)
			var out, logs bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &logs
			err := cmd.Run()
			require.NoError(t, ctx.Err(), "capture blocked: %s", logs.String())
			require.NoError(t, err, "%s", logs.String())

			expected := captureTestMessage(name)
			switch name {
			case "dcgm":
				expected = ""
				require.Contains(t, logs.String(), "Linux 4.15.0-180-generic")
				require.Contains(t, logs.String(), "dcgm_level=INFO")
			case "multiline":
				_, expected, _ = strings.Cut(expected, "\n")
				require.Contains(t, logs.String(), "Couldn't parse json")
				require.Contains(t, logs.String(), "dcgm_level=ERROR")
			case "repeated":
				expected = ""
			}
			require.Equal(t, expected+"restored\n", out.String())
		})
	}
}

func TestCaptureProcess(t *testing.T) {
	name := os.Getenv(captureTestEnv)
	if name == "" {
		return
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	if name == "canceled" {
		cancel()
	}
	innerErr := errors.New("inner failed")
	err := Capture(ctx, func() error {
		if name == "cgo" {
			printCGO()
		} else if name != "repeated" {
			if _, err := fmt.Print(captureTestMessage(name)); err != nil {
				return err
			}
		}
		if name == "inner-error" {
			return innerErr
		}
		return nil
	})
	if name == "inner-error" {
		require.ErrorIs(t, err, innerErr)
	} else {
		require.NoError(t, err)
	}
	if name == "repeated" {
		// Warm up the runtime poller before checking descriptor ownership.
		before, err := os.ReadDir("/proc/self/fd")
		require.NoError(t, err)
		for range 20 {
			require.NoError(t, Capture(ctx, func() error { return nil }))
		}
		after, err := os.ReadDir("/proc/self/fd")
		require.NoError(t, err)
		require.Len(t, after, len(before))
	}
	_, err = fmt.Println("restored")
	require.NoError(t, err)
	cancel()
	// Avoid the test runner's PASS line in the captured stdout.
	os.Exit(0)
}
