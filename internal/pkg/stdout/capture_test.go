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
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCapture(t *testing.T) {
	type testCase struct {
		name       string
		logMessage string
		assert     func(t *testing.T, str string)
	}

	testCases := []testCase{
		{
			name:       "function writes an arbitrary string into /dev/stdout",
			logMessage: "hello from dcgm",
			assert: func(t *testing.T, str string) {
				assert.Equal(t, "hello from dcgm", strings.TrimSpace(str))
			},
		},
		{
			name:       "function writes an DCGM log entry string into /dev/stdout",
			logMessage: "2024-02-07 18:01:05.641 INFO  [517155:517155] Linux 4.15.0-180-generic [{anonymous}::StartEmbeddedV2]",
			assert: func(t *testing.T, str string) {
				assert.Contains(t, strings.TrimSpace(str), "Linux 4.15.0-180-generic")
			},
		},
		{
			name:       "function writes an DCGM log entry string with a valid date only",
			logMessage: "2024-02-07 18:01:05.641",
			assert: func(t *testing.T, str string) {
				assert.Equal(t, "2024-02-07 18:01:05.641", strings.TrimSpace(str))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a buffer to capture stdout output
			var buf bytes.Buffer

			// Save the original stdout
			stdout := os.Stdout

			// Create a pipe to redirect stdout
			r, w, err := os.Pipe()
			assert.NoError(t, err)

			os.Stdout = w // Redirect stdout to the write end of the pipe

			ctx, cancel := context.WithCancel(context.Background())
			err = Capture(ctx, func() error {
				fmt.Println(tc.logMessage)
				return nil
			})

			assert.NoError(t, err)

			// Close the write end of the pipe to allow reading all data
			_ = w.Close()
			os.Stdout = stdout // Restore original stdout

			// Read from the pipe directly into the buffer
			_, err = buf.ReadFrom(r)
			assert.NoError(t, err)
			if tc.assert != nil {
				tc.assert(t, buf.String())
			}
			cancel()
		})
	}
}

func TestCaptureWithCGO(t *testing.T) {
	testCaptureWithCGO(t)
}
