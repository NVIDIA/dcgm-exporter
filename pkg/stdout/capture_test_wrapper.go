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
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func testCaptureWithCGO(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	buf := &bytes.Buffer{}
	logrus.SetOutput(buf)

	err := Capture(ctx, func() error {
		C.printBoom()
		return nil
	})
	assert.NoError(t, err)

	time.Sleep(10 * time.Millisecond)
	require.Equal(t, "Boom", strings.TrimSpace(buf.String()))

	cancel()
}
