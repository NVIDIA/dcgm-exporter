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

/*
#include <stdio.h>
void printBoom() {
	printf("Boom\n");
	fflush(stdout);
}
*/
import "C"

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCaptureWithCGO(t *testing.T) {
	t.Helper()
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
		C.printBoom()
		return nil
	})
	assert.NoError(t, err)
	// It takes a time before CGO flushes logs to the std output
	// We need to wait until we start to receive the data
	// Create temporary buffer to detect data
	var tempBuf [1]byte
	// Read from the pipe to ensure data is available
	_, err = r.Read(tempBuf[:]) // Block until data is written
	assert.NoError(t, err)
	buf.Write(tempBuf[:]) // Start capturing the data
	// Close the write end of the pipe to allow reading all data
	_ = w.Close()
	_, err = buf.ReadFrom(r) // Read the remaining data
	assert.NoError(t, err)
	require.Equal(t, "Boom", strings.TrimSpace(buf.String()))
	os.Stdout = stdout // Restore original stdout
	cancel()
}
