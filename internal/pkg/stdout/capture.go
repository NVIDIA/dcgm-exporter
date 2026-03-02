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
	"log/slog"
	"os"
	"syscall"
)

// Capture go and C stdout and stderr and writes to std output
func Capture(ctx context.Context, inner func() error) error {
	stdout, err := syscall.Dup(syscall.Stdout)
	if err != nil {
		return err
	}

	r, w, err := os.Pipe()
	if err != nil {
		return err
	}

	err = syscall.Dup3(int(w.Fd()), syscall.Stdout, 0)
	if err != nil {
		return err
	}

	defer func() {
		ierr := syscall.Close(syscall.Stdout)
		if ierr != nil {
			err = ierr
		}

		ierr = syscall.Dup3(stdout, syscall.Stdout, 0)
		if ierr != nil {
			err = ierr
		}
	}()

	scanner := bufio.NewScanner(r)
	go func() {
		for scanner.Scan() {
			if ctx.Err() != nil {
				return
			}
			logEntry := scanner.Text()
			parsedLogEntry := parseOutputEntry(logEntry)
			if parsedLogEntry.IsRawString {
				_, err := os.Stdout.Write([]byte(parsedLogEntry.Message + "\n"))
				if err != nil {
					return
				}
				continue
			}
			slog.LogAttrs(ctx, slog.LevelInfo, parsedLogEntry.Message, slog.String("dcgm_level", parsedLogEntry.Level))
		}
	}()

	// Call function here
	return inner()
}
