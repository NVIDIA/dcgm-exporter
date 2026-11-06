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

package stdout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils/childcover"
)

const captureTestScenarioEnv = "DCGM_EXPORTER_CAPTURE_TEST_SCENARIO"

// TestCapture runs stdout-mutating cases in a child process and checks their observable output.
// The child mode keeps the main test process from sharing its stdout descriptor with the capture.
func TestCapture(t *testing.T) {
	if scenario := os.Getenv(captureTestScenarioEnv); scenario != "" {
		runCaptureScenario(t, scenario)
		return
	}

	t.Run("raw and structured output", func(t *testing.T) {
		stdout, stderr := runCaptureSubprocess(t, "TestCapture", "raw-and-structured")

		assert.Contains(t, stdout, "raw output\n")
		assert.NotContains(t, stdout, "structured output")
		assert.Contains(t, stderr, "structured output")
	})

	t.Run("sustained output", func(t *testing.T) {
		stdout, _ := runCaptureSubprocess(t, "TestCapture", "sustained")

		assert.Equal(t, 128, strings.Count(stdout, "captured line "))
	})

	t.Run("oversized output", func(t *testing.T) {
		stdout, _ := runCaptureSubprocess(t, "TestCapture", "oversized")

		assert.Contains(t, stdout, strings.Repeat("x", maximumCaptureLineSize+1))
	})

	t.Run("canceled context still drains output", func(t *testing.T) {
		stdout, _ := runCaptureSubprocess(t, "TestCapture", "canceled")

		assert.Contains(t, stdout, "output after cancellation\n")
	})

	t.Run("repeated capture", func(t *testing.T) {
		stdout, _ := runCaptureSubprocess(t, "TestCapture", "repeated")

		assert.Equal(t, 1, strings.Count(stdout, "first capture\n"))
		assert.Equal(t, 1, strings.Count(stdout, "second capture\n"))
	})
}

// TestCaptureReturnsCallbackAndOverlapErrors checks error propagation without changing the main test process stdout.
func TestCaptureReturnsCallbackAndOverlapErrors(t *testing.T) {
	if scenario := os.Getenv(captureTestScenarioEnv); scenario != "" {
		runCaptureScenario(t, scenario)
		return
	}

	stdout, _ := runCaptureSubprocess(t, "TestCaptureReturnsCallbackAndOverlapErrors", "errors")
	assert.Contains(t, stdout, "callback error output\n")
}

// TestCaptureRestoresStdoutAfterPanic checks that deferred cleanup runs while Capture propagates a panic.
func TestCaptureRestoresStdoutAfterPanic(t *testing.T) {
	if scenario := os.Getenv(captureTestScenarioEnv); scenario != "" {
		runCaptureScenario(t, scenario)
		return
	}

	stdout, _ := runCaptureSubprocess(t, "TestCaptureRestoresStdoutAfterPanic", "panic")
	assert.Contains(t, stdout, "output before panic\n")
	assert.Contains(t, stdout, "output after panic\n")
}

// TestCaptureDoesNotLeakFileDescriptors checks repeated captures keep the child process descriptor count stable.
func TestCaptureDoesNotLeakFileDescriptors(t *testing.T) {
	if scenario := os.Getenv(captureTestScenarioEnv); scenario != "" {
		runCaptureScenario(t, scenario)
		return
	}

	runCaptureSubprocess(t, "TestCaptureDoesNotLeakFileDescriptors", "descriptor-count")
}

// TestCaptureFailurePaths checks setup, reader, and restore failures without leaking a changed stdout descriptor.
// Setup and reader checks run locally because they never replace stdout; restore failure stays in a child process.
func TestCaptureFailurePaths(t *testing.T) {
	if scenario := os.Getenv(captureTestScenarioEnv); scenario != "" {
		runCaptureScenario(t, scenario)
		return
	}

	t.Run("setup", func(t *testing.T) {
		t.Run("save stdout", func(t *testing.T) {
			setupErr := errors.New("duplicate stdout failed")
			operations := defaultCaptureOperations
			operations.duplicateDescriptor = func(int) (int, error) {
				return 0, setupErr
			}

			capture, err := newStdoutCapture(context.Background(), operations)
			require.Nil(t, capture)
			require.ErrorIs(t, err, setupErr)
		})

		t.Run("create pipe", func(t *testing.T) {
			setupErr := errors.New("create capture pipe failed")
			operations := defaultCaptureOperations
			operations.createPipe = func() (*os.File, *os.File, error) {
				return nil, nil, setupErr
			}

			capture, err := newStdoutCapture(context.Background(), operations)
			require.Nil(t, capture)
			require.ErrorIs(t, err, setupErr)
		})

		t.Run("redirect stdout", func(t *testing.T) {
			setupErr := errors.New("redirect stdout failed")
			operations := defaultCaptureOperations
			operations.replaceDescriptor = func(int, int, int) error {
				return setupErr
			}

			capture, err := newStdoutCapture(context.Background(), operations)
			require.Nil(t, capture)
			require.ErrorIs(t, err, setupErr)
		})
	})

	t.Run("reader", func(t *testing.T) {
		readerErr := errors.New("capture reader failed")
		err := forwardCapturedOutput(context.Background(), &bytes.Buffer{}, failingCaptureReader{err: readerErr})
		require.ErrorIs(t, err, readerErr)
	})

	runCaptureSubprocess(t, "TestCaptureFailurePaths", "restore-and-callback-errors")
	runCaptureSubprocess(t, "TestCaptureFailurePaths", "restore-close-and-callback-errors")
}

// TestWriteAll verifies that capture output is complete even when the destination writes a partial buffer.
// The capture reader relies on this helper to avoid silently dropping raw stdout after a short write.
func TestWriteAll(t *testing.T) {
	var captured bytes.Buffer
	partialWriter := captureWriterFunc(func(data []byte) (int, error) {
		return captured.Write(data[:1])
	})
	require.NoError(t, writeAll(partialWriter, []byte("raw output")))
	assert.Equal(t, "raw output", captured.String())

	assert.ErrorIs(t, writeAll(captureWriterFunc(func([]byte) (int, error) {
		return 0, nil
	}), []byte("raw output")), io.ErrShortWrite)

	writeErr := errors.New("destination failed")
	assert.ErrorIs(t, writeAll(captureWriterFunc(func([]byte) (int, error) {
		return 0, writeErr
	}), []byte("raw output")), writeErr)
}

// runCaptureSubprocess starts one named test in a child process with an isolated stdout descriptor.
// Capture tests use it because changing process stdout is unsafe to run alongside other package tests.
func runCaptureSubprocess(t *testing.T, testName, scenario string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	arguments := []string{"-test.run", "^" + testName + "$"}
	coverDir, err := childcover.Dir()
	require.NoError(t, err)
	if coverDir != "" {
		arguments = append(arguments, "-test.gocoverdir="+coverDir)
	}

	testBinary, err := os.Executable()
	require.NoError(t, err)
	command := exec.CommandContext(ctx, testBinary, arguments...)
	command.Env = append(os.Environ(), captureTestScenarioEnv+"="+scenario)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	require.NoError(t, command.Run(), "stdout: %s\nstderr: %s", stdout.String(), stderr.String())
	return stdout.String(), stderr.String()
}

// runCaptureScenario exercises one child-process case selected by runCaptureSubprocess.
// Each case asserts the behavior that cannot be safely observed after the parent restores its own stdout.
func runCaptureScenario(t *testing.T, scenario string) {
	t.Helper()
	scenarios := map[string]func(*testing.T){
		"raw-and-structured":                captureRawAndStructured,
		"sustained":                         captureSustainedOutput,
		"oversized":                         captureOversizedOutput,
		"canceled":                          captureCanceledOutput,
		"repeated":                          captureRepeatedOutput,
		"errors":                            captureCallbackErrors,
		"panic":                             capturePanic,
		"descriptor-count":                  captureDescriptorCount,
		"restore-and-callback-errors":       captureRestoreErrors,
		"restore-close-and-callback-errors": captureRestoreCloseErrors,
	}
	run, ok := scenarios[scenario]
	if !ok {
		t.Fatalf("unknown capture test scenario %q", scenario)
	}
	run(t)
}

func captureRawAndStructured(t *testing.T) {
	t.Helper()
	require.NoError(t, Capture(context.Background(), func() error {
		fmt.Fprint(os.Stdout, "raw output\n")
		fmt.Fprint(os.Stdout, "2024-02-07 18:01:05.641 INFO [1:1] structured output\n")
		return nil
	}))
}

func captureSustainedOutput(t *testing.T) {
	t.Helper()
	require.NoError(t, Capture(context.Background(), func() error {
		for index := range 128 {
			_, err := fmt.Fprintf(os.Stdout, "captured line %d\n", index)
			if err != nil {
				return err
			}
		}
		return nil
	}))
}

func captureOversizedOutput(t *testing.T) {
	t.Helper()
	require.NoError(t, Capture(context.Background(), func() error {
		_, err := fmt.Fprintln(os.Stdout, strings.Repeat("x", maximumCaptureLineSize+1))
		return err
	}))
}

func captureCanceledOutput(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, Capture(ctx, func() error {
		_, err := fmt.Fprint(os.Stdout, "output after cancellation\n")
		return err
	}))
}

func captureRepeatedOutput(t *testing.T) {
	t.Helper()
	for _, message := range []string{"first capture\n", "second capture\n"} {
		message := message
		require.NoError(t, Capture(context.Background(), func() error {
			_, err := fmt.Fprint(os.Stdout, message)
			return err
		}))
	}
}

func captureCallbackErrors(t *testing.T) {
	t.Helper()
	callbackErr := errors.New("callback failed")
	var nestedErr error
	err := Capture(context.Background(), func() error {
		nestedErr = Capture(context.Background(), func() error { return nil })
		_, writeErr := fmt.Fprint(os.Stdout, "callback error output\n")
		if writeErr != nil {
			return writeErr
		}
		return callbackErr
	})
	require.ErrorIs(t, err, callbackErr)
	require.ErrorIs(t, nestedErr, errCaptureInProgress)
}

func capturePanic(t *testing.T) {
	t.Helper()
	func() {
		defer func() {
			require.NotNil(t, recover())
		}()
		_ = Capture(context.Background(), func() error {
			_, err := fmt.Fprint(os.Stdout, "output before panic\n")
			require.NoError(t, err)
			panic("capture callback panic")
		})
	}()
	_, err := fmt.Fprint(os.Stdout, "output after panic\n")
	require.NoError(t, err)
}

func captureDescriptorCount(t *testing.T) {
	t.Helper()
	require.NoError(t, Capture(context.Background(), func() error { return nil }))
	before := countOpenFileDescriptors(t)
	for range 16 {
		require.NoError(t, Capture(context.Background(), func() error { return nil }))
	}
	assert.Equal(t, before, countOpenFileDescriptors(t))
}

func captureRestoreErrors(t *testing.T) {
	t.Helper()
	callbackErr := errors.New("callback failed")
	restoreErr := errors.New("restore stdout failed")
	operations := defaultCaptureOperations
	savedStdout := -1
	operations.duplicateDescriptor = func(descriptor int) (int, error) {
		duplicatedDescriptor, err := syscall.Dup(descriptor)
		savedStdout = duplicatedDescriptor
		return duplicatedDescriptor, err
	}
	redirectCalls := 0
	operations.replaceDescriptor = func(source, destination, flags int) error {
		redirectCalls++
		if redirectCalls == 1 {
			return syscall.Dup3(source, destination, flags)
		}
		return restoreErr
	}
	stdoutDescriptor := int(os.Stdout.Fd())
	closedDescriptors := make([]int, 0, 1)
	operations.closeDescriptor = func(descriptor int) error {
		closedDescriptors = append(closedDescriptors, descriptor)
		if descriptor == stdoutDescriptor {
			return syscall.Dup3(savedStdout, descriptor, 0)
		}
		return syscall.Close(descriptor)
	}

	err := captureWithOperations(context.Background(), func() error {
		return callbackErr
	}, operations)
	require.ErrorIs(t, err, callbackErr)
	require.ErrorIs(t, err, restoreErr)
	require.Contains(t, closedDescriptors, stdoutDescriptor)
}

func captureRestoreCloseErrors(t *testing.T) {
	t.Helper()
	callbackErr := errors.New("callback failed")
	restoreErr := errors.New("restore stdout failed")
	closeErr := errors.New("close redirected stdout failed")
	originalStdout, err := syscall.Dup(syscall.Stdout)
	require.NoError(t, err)
	defer syscall.Close(originalStdout)

	operations := defaultCaptureOperations
	redirectCalls := 0
	operations.replaceDescriptor = func(source, destination, flags int) error {
		redirectCalls++
		if redirectCalls == 2 {
			return restoreErr
		}
		return syscall.Dup3(source, destination, flags)
	}
	operations.closeDescriptor = func(int) error {
		return closeErr
	}

	err = captureWithOperations(context.Background(), func() error {
		return callbackErr
	}, operations)
	require.ErrorIs(t, err, callbackErr)
	require.ErrorIs(t, err, restoreErr)
	require.ErrorIs(t, err, closeErr)
	require.NoError(t, syscall.Dup3(originalStdout, syscall.Stdout, 0))
}

// failingCaptureReader returns its configured error when the capture reader tries to read it.
// TestCaptureFailurePaths uses it to confirm reader errors reach Capture cleanup instead of being discarded.
type failingCaptureReader struct {
	err error
}

// Read implements io.Reader by returning the configured test error.
// TestCaptureFailurePaths passes this reader to forwardCapturedOutput to exercise the read-error path.
func (reader failingCaptureReader) Read([]byte) (int, error) {
	return 0, reader.err
}

// captureWriterFunc adapts a function into an io.Writer for focused forwarding tests.
// TestWriteAll uses it to model partial, stalled, and failed destination writes without a package-level fake.
type captureWriterFunc func([]byte) (int, error)

// Write calls the test function with the bytes that the capture reader wants to forward.
// TestWriteAll supplies the function to verify writeAll reacts correctly to its result.
func (writer captureWriterFunc) Write(data []byte) (int, error) {
	return writer(data)
}

// countOpenFileDescriptors returns the Linux descriptor count used by the child-process leak test.
// The exporter supports Linux, where /proc/self/fd provides the count without opening a test-only pipe.
func countOpenFileDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	return len(entries)
}
