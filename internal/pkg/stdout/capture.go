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
	"io"
	"log/slog"
	"os"
	"strings"
	"syscall"
)

// Capture redirects Go and C stdout while inner runs, forwarding DCGM log
// entries to slog and other output to the original stdout.
func Capture(ctx context.Context, inner func() error) (retErr error) {
	fd, err := syscall.Dup(syscall.Stdout)
	if err != nil {
		return err
	}
	stdout := os.NewFile(uintptr(fd), "original stdout")
	defer func() {
		retErr = errors.Join(retErr, stdout.Close())
	}()

	r, w, err := os.Pipe()
	if err != nil {
		return err
	}

	err = syscall.Dup3(int(w.Fd()), syscall.Stdout, 0)
	if err != nil {
		return errors.Join(err, r.Close(), w.Close())
	}

	done := make(chan error, 1)
	go func() {
		err := forwardOutput(ctx, r, stdout)
		// Close the reader even on a forwarding error so writers cannot remain
		// blocked on a pipe that no longer has a consumer.
		done <- errors.Join(err, r.Close())
	}()

	defer func() {
		// Restoring fd 1 and closing w removes both pipe writers. Drain the
		// reader before closing the saved stdout or returning to the caller.
		restoreErr := syscall.Dup3(fd, syscall.Stdout, 0)
		if restoreErr != nil {
			// There may still be a writer at fd 1; unblock the reader explicitly.
			retErr = errors.Join(retErr, r.Close())
		}
		retErr = errors.Join(retErr, restoreErr, w.Close(), <-done)
	}()

	return inner()
}

func forwardOutput(ctx context.Context, r io.Reader, stdout io.Writer) error {
	reader := bufio.NewReader(r)
	for {
		// DCGM can emit entire lshw documents, including lines larger than
		// Scanner's token limit. Keep consuming until the pipe is closed,
		// including when ctx is canceled, so native writers can finish.
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			logEntry := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			parsedLogEntry := parseOutputEntry(logEntry)
			if parsedLogEntry.IsRawString {
				// fd 1 points at our input pipe. Write to the saved descriptor
				// instead, preserving raw output without feeding it back.
				if _, writeErr := io.WriteString(stdout, line); writeErr != nil {
					return writeErr
				}
			} else {
				slog.LogAttrs(ctx, slog.LevelInfo, parsedLogEntry.Message, slog.String("dcgm_level", parsedLogEntry.Level))
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
