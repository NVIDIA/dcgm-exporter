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

package testutils

import (
	"reflect"
	"runtime"
	"testing"
	"unsafe"
)

// RequireLinux checks if
func RequireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("Test is not supported on %q", runtime.GOOS)
	}
}

// GetStructPrivateFieldValue returns private field value
func GetStructPrivateFieldValue[T any](t *testing.T, v any, fieldName string) T {
	t.Helper()
	var result T
	value := reflect.ValueOf(v)
	if value.Kind() == reflect.Ptr {
		value = value.Elem()
	}

	if value.Kind() != reflect.Struct {
		t.Errorf("The type %s is not stuct", value.Type())
		return result
	}

	fieldVal := value.FieldByName(fieldName)

	if !fieldVal.IsValid() {
		t.Errorf("The field %s is invalid for the %s type", fieldName, value.Type())
		return result
	}

	fieldPtr := unsafe.Pointer(fieldVal.UnsafeAddr())

	// Cast the field pointer to a pointer of the correct type
	realPtr := (*T)(fieldPtr)

	return *realPtr
}
