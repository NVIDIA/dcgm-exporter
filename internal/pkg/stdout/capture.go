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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
)

const (
	captureReadBufferSize  = 4 * 1024
	maximumCaptureLineSize = 64 * 1024
)

var (
	captureMu            sync.Mutex
	errCaptureInProgress = errors.New("stdout capture is already active")
)

// captureOperations groups the operating-system calls that create and restore stdout capture.
// Capture uses the default operations in production, while tests supply a failing operation to prove error cleanup.
type captureOperations struct {
	duplicateDescriptor func(int) (int, error)
	createPipe          func() (*os.File, *os.File, error)
	replaceDescriptor   func(int, int, int) error
	closeDescriptor     func(int) error
}

var defaultCaptureOperations = captureOperations{
	duplicateDescriptor: syscall.Dup,
	createPipe:          os.Pipe,
	replaceDescriptor:   syscall.Dup3,
	closeDescriptor:     syscall.Close,
}

// stdoutCapture owns the descriptors and reader used by one Capture call.
// Capture restores stdout and waits for its reader through close before it releases this owner.
type stdoutCapture struct {
	savedStdout *os.File
	reader      *os.File
	writer      *os.File
	readerDone  <-chan error
	operations  captureOperations
}

// Capture redirects process stdout while inner runs, then forwards raw output to its original sink.
// It does not capture stderr and returns an error when another capture is already active.
func Capture(ctx context.Context, inner func() error) (err error) {
	return captureWithOperations(ctx, inner, defaultCaptureOperations)
}

// captureWithOperations runs Capture with explicit descriptor operations.
// Capture uses the default operations in production.
// Package tests use this helper to verify failures without changing global state.
func captureWithOperations(ctx context.Context, inner func() error, operations captureOperations) (err error) {
	if !captureMu.TryLock() {
		return errCaptureInProgress
	}
	defer captureMu.Unlock()

	capture, err := newStdoutCapture(ctx, operations)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, capture.close())
	}()

	return inner()
}

// newStdoutCapture redirects stdout to a pipe and starts the reader that drains the pipe.
// Capture calls it once after acquiring captureMu so its descriptor changes cannot overlap.
func newStdoutCapture(ctx context.Context, operations captureOperations) (*stdoutCapture, error) {
	savedStdoutFD, err := operations.duplicateDescriptor(syscall.Stdout)
	if err != nil {
		return nil, fmt.Errorf("failed to save stdout: %w", err)
	}
	savedStdout := os.NewFile(uintptr(savedStdoutFD), "saved stdout")

	reader, writer, err := operations.createPipe()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to create stdout capture pipe: %w", err),
			savedStdout.Close(),
		)
	}

	if err := operations.replaceDescriptor(int(writer.Fd()), syscall.Stdout, 0); err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to redirect stdout: %w", err),
			writer.Close(),
			reader.Close(),
			savedStdout.Close(),
		)
	}

	readerDone := make(chan error, 1)
	go func() {
		readerDone <- forwardCapturedOutput(ctx, savedStdout, reader)
	}()

	return &stdoutCapture{
		savedStdout: savedStdout,
		reader:      reader,
		writer:      writer,
		readerDone:  readerDone,
		operations:  operations,
	}, nil
}

// close restores stdout, closes the pipe writer, drains the reader, and closes owned descriptors.
// If stdout cannot be restored or closed, it stops the reader before waiting so cleanup still returns.
func (capture *stdoutCapture) close() error {
	var cleanupErr error
	readerClosed := false
	if err := capture.operations.replaceDescriptor(int(capture.savedStdout.Fd()), syscall.Stdout, 0); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("failed to restore stdout: %w", err))
		if closeErr := capture.operations.closeDescriptor(syscall.Stdout); closeErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("failed to close redirected stdout: %w", closeErr))
		}
		if closeErr := capture.reader.Close(); closeErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("failed to stop stdout capture reader: %w", closeErr))
		}
		readerClosed = true
	}

	if err := capture.writer.Close(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("failed to close stdout capture writer: %w", err))
	}
	if err := <-capture.readerDone; err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if !readerClosed {
		if err := capture.reader.Close(); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("failed to close stdout capture reader: %w", err))
		}
	}
	if err := capture.savedStdout.Close(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("failed to close saved stdout: %w", err))
	}

	return cleanupErr
}

// forwardCapturedOutput drains captured stdout until the writer is closed.
// It buffers ordinary lines for parsing and forwards oversized lines in bounded raw chunks.
func forwardCapturedOutput(ctx context.Context, destination io.Writer, reader io.Reader) error {
	bufferedReader := bufio.NewReaderSize(reader, captureReadBufferSize)
	line := make([]byte, 0, maximumCaptureLineSize)
	oversizedLine := false
	var forwardErr error

	forwardRaw := func(data []byte) {
		if forwardErr == nil {
			forwardErr = writeAll(destination, data)
		}
	}
	forwardLine := func(data []byte) {
		if forwardErr == nil {
			forwardErr = forwardCapturedLine(ctx, destination, data)
		}
	}

	for {
		fragment, readErr := bufferedReader.ReadSlice('\n')
		switch {
		case oversizedLine:
			forwardRaw(fragment)
			if !errors.Is(readErr, bufio.ErrBufferFull) {
				oversizedLine = false
			}
		case len(line)+len(fragment) > maximumCaptureLineSize:
			forwardRaw(line)
			forwardRaw(fragment)
			line = line[:0]
			oversizedLine = errors.Is(readErr, bufio.ErrBufferFull)
		default:
			line = append(line, fragment...)
			if !errors.Is(readErr, bufio.ErrBufferFull) {
				forwardLine(line)
				line = line[:0]
			}
		}

		switch {
		case readErr == nil, errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			return forwardErr
		default:
			return errors.Join(forwardErr, fmt.Errorf("failed to read captured stdout: %w", readErr))
		}
	}
}

// forwardCapturedLine sends raw text to the saved stdout descriptor or sends parsed logs to slog.
// Capture keeps structured logs on the configured logger destination instead of routing them into the pipe.
func forwardCapturedLine(ctx context.Context, destination io.Writer, line []byte) error {
	entry := strings.TrimSuffix(string(line), "\n")
	entry = strings.TrimSuffix(entry, "\r")
	parsedEntry := parseOutputEntry(entry)
	if parsedEntry.IsRawString {
		return writeAll(destination, line)
	}

	slog.LogAttrs(ctx, slog.LevelInfo, parsedEntry.Message, slog.String("dcgm_level", parsedEntry.Level))
	return nil
}

// writeAll writes every byte in data or returns the first write error.
// The capture reader uses it so a short write cannot silently drop stdout text.
func writeAll(destination io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := destination.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}

	return nil
}
