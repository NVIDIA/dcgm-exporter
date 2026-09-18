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
	"encoding/binary"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// invalidLabelCharRE matches any character that is not a letter, digit, or underscore.
var invalidLabelCharRE = regexp.MustCompile(`[^a-zA-Z0-9_]`)

func WaitWithTimeout(wg *sync.WaitGroup, timeout time.Duration) error {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c:
		return nil
	case <-timer.C:
		return fmt.Errorf("timeout waiting for WaitGroup")
	}
}

func RandUint64() (uint64, error) {
	var num uint64
	err := binary.Read(rand.Reader, binary.BigEndian, &num)
	if err != nil {
		return 0, fmt.Errorf("failed to generate random 64-bit number; err: %w", err)
	}

	return num, nil
}

func CleanupOnError(cleanups []func()) []func() {
	for _, cleanup := range cleanups {
		cleanup()
	}

	return nil
}

// SanitizeLabelName converts arbitrary input into a Prometheus-safe label name.
func SanitizeLabelName(s string) string {
	sanitized := invalidLabelCharRE.ReplaceAllString(s, "_")
	if sanitized == "" {
		return "_"
	}

	if sanitized[0] >= '0' && sanitized[0] <= '9' {
		sanitized = "_" + sanitized
	}

	// Double-underscore labels are reserved by Prometheus internals.
	if strings.HasPrefix(sanitized, "__") {
		sanitized = "_" + strings.TrimLeft(sanitized, "_")
		if sanitized == "_" {
			return "_"
		}
	}

	return sanitized
}
