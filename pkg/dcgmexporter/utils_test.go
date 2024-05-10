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

package dcgmexporter

import (
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitWithTimeout(t *testing.T) {
	t.Run("Returns error by timeout", func(t *testing.T) {
		wg := &sync.WaitGroup{}
		defer wg.Done()
		wg.Add(1)
		timeout := 500 * time.Millisecond
		err := WaitWithTimeout(wg, timeout)
		require.Error(t, err)
		assert.ErrorContains(t, err, "timeout waiting for WaitGroup")
	})

	t.Run("Returns no error", func(t *testing.T) {
		wg := &sync.WaitGroup{}
		wg.Add(1)
		timeout := 500 * time.Millisecond
		wg.Done()
		err := WaitWithTimeout(wg, timeout)
		require.NoError(t, err)
	})
}

func TestRandUint64_Success(t *testing.T) {
	num, err := RandUint64()
	assert.Nil(t, err, "Unexpected error: %v", err)
	assert.NotZero(t, num, "Expected a non-zero uint64, but got 0")
}

// mockReader is a mock implementation of rand.Reader that always returns an error
type mockReader struct {
	err error
}

func (r *mockReader) Read(_ []byte) (n int, err error) {
	return 0, r.err
}

func TestRandUint64_Failure(t *testing.T) {
	// Simulate a failure in rand.Reader using mock rand.Reader
	mockReader := &mockReader{err: fmt.Errorf("mock error")}

	originalReader := rand.Reader
	rand.Reader = mockReader
	defer func() {
		rand.Reader = originalReader
	}()

	num, err := RandUint64()
	assert.NotNil(t, err, "Expected an error")
	assert.Zero(t, num, fmt.Sprintf("Expected a uint64, but got %d", num))
}

func TestDeepCopy(t *testing.T) {
	t.Run("Return error when pointer value is nil", func(t *testing.T) {
		got, err := deepCopy[*struct{}](nil)
		assert.Nil(t, got)
		assert.Error(t, err)
	})

	t.Run("Return error when src is unsupported type", func(t *testing.T) {
		ch := make(chan int)
		got, err := deepCopy(ch)
		assert.Nil(t, got)
		assert.Error(t, err)
	})
}
