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

package utils

import (
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
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

func TestRandUint64_Failure(t *testing.T) {
	// Simulate a failure in rand.Reader using mock rand.Reader
	mockReader := &testutils.MockReader{Err: fmt.Errorf("mock error")}

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
		got, err := DeepCopy[*struct{}](nil)
		assert.Nil(t, got)
		assert.Error(t, err)
	})

	t.Run("Return error when src is unsupported type", func(t *testing.T) {
		ch := make(chan int)
		got, err := DeepCopy(ch)
		assert.Nil(t, got)
		assert.Error(t, err)
	})
}

func TestCleanupOnError(t *testing.T) {
	tests := []struct {
		name     string
		cleanups []func()
		want     []func()
	}{
		{
			name:     "Nil cleanup functions",
			cleanups: nil,
			want:     nil,
		},
		{
			name:     "Empty cleanup functions",
			cleanups: []func(){},
			want:     nil,
		},
		{
			name: "One cleanup functions",
			cleanups: []func(){
				func() {},
			},
			want: nil,
		},
		{
			name: "Multiple cleanup functions",
			cleanups: []func(){
				func() {},
				func() {
					func() {
						// This function is intentionally left blank
					}()
				},
				func() {},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, CleanupOnError(tt.cleanups), "expected output to be the same.")
		})
	}
}

func TestSanitizeLabelName(t *testing.T) {
	t.Run("Sanitize label with invalid characters", func(t *testing.T) {
		input := "label.with.dots/and-slashes"
		expected := "label_with_dots_and_slashes"
		got := SanitizeLabelName(input)
		assert.Equal(t, expected, got)
	})

	t.Run("Sanitize label with special characters", func(t *testing.T) {
		input := "label@with#special!chars"
		expected := "label_with_special_chars"
		got := SanitizeLabelName(input)
		assert.Equal(t, expected, got)
	})

	t.Run("Keep valid label unchanged", func(t *testing.T) {
		input := "valid_label_name"
		expected := "valid_label_name"
		got := SanitizeLabelName(input)
		assert.Equal(t, expected, got)
	})
}
