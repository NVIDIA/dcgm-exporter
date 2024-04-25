/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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
	"bytes"
	"encoding/gob"
	"fmt"
	"sync"
	"time"
)

func WaitWithTimeout(wg *sync.WaitGroup, timeout time.Duration) error {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for WaitGroup")
	}
}

func deepCopy[T any](src T) (dst T, err error) {
	var buf bytes.Buffer

	defer func() {
		if r := recover(); r != nil {
			// If there was a panic, return the zero value of T and the error.
			dst = *new(T)
			err = fmt.Errorf("panic occurred: %v", r)
		}
	}()

	// Create an encoder and send a value.
	err = gob.NewEncoder(&buf).Encode(src)
	if err != nil {
		return *new(T), err
	}

	// Create a new instance of the type T and decode into that.
	err = gob.NewDecoder(&buf).Decode(&dst)
	if err != nil {
		return *new(T), err
	}

	return dst, nil
}
