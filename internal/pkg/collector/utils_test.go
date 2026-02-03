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

package collector

import (
	"testing"
	"unsafe"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
)

func Test_isInt64Blank(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		want  bool
	}{
		{
			name:  "DCGM_FT_INT32_BLANK",
			value: dcgm.DCGM_FT_INT32_BLANK,
			want:  true,
		},
		{
			name:  "DCGM_FT_INT32_NOT_FOUND",
			value: dcgm.DCGM_FT_INT32_NOT_FOUND,
			want:  true,
		},
		{
			name:  "DCGM_FT_INT32_NOT_SUPPORTED",
			value: dcgm.DCGM_FT_INT32_NOT_SUPPORTED,
			want:  true,
		},
		{
			name:  "DCGM_FT_INT32_NOT_PERMISSIONED",
			value: dcgm.DCGM_FT_INT32_NOT_PERMISSIONED,
			want:  true,
		},
		{
			name:  "DCGM_FT_INT64_BLANK",
			value: dcgm.DCGM_FT_INT64_BLANK,
			want:  true,
		},
		{
			name:  "DCGM_FT_INT64_NOT_FOUND",
			value: dcgm.DCGM_FT_INT64_NOT_FOUND,
			want:  true,
		},
		{
			name:  "DCGM_FT_INT64_NOT_SUPPORTED",
			value: dcgm.DCGM_FT_INT64_NOT_SUPPORTED,
			want:  true,
		},
		{
			name:  "DCGM_FT_INT64_NOT_PERMISSIONED",
			value: dcgm.DCGM_FT_INT64_NOT_PERMISSIONED,
			want:  true,
		},
		{
			name:  "Valid value 0",
			value: 0,
			want:  false,
		},
		{
			name:  "Valid value 42",
			value: 42,
			want:  false,
		},
		{
			name:  "Valid negative value",
			value: -100,
			want:  false,
		},
		{
			name:  "Valid large value",
			value: 1000000000,
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isInt64Blank(tt.value)
			assert.Equal(t, tt.want, got, "isInt64Blank(%v)", tt.value)
		})
	}
}

func Test_isFloat64Blank(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  bool
	}{
		{
			name:  "DCGM_FT_FP64_BLANK",
			value: dcgm.DCGM_FT_FP64_BLANK,
			want:  true,
		},
		{
			name:  "DCGM_FT_FP64_NOT_FOUND",
			value: dcgm.DCGM_FT_FP64_NOT_FOUND,
			want:  true,
		},
		{
			name:  "DCGM_FT_FP64_NOT_SUPPORTED",
			value: dcgm.DCGM_FT_FP64_NOT_SUPPORTED,
			want:  true,
		},
		{
			name:  "DCGM_FT_FP64_NOT_PERMISSIONED",
			value: dcgm.DCGM_FT_FP64_NOT_PERMISSIONED,
			want:  true,
		},
		{
			name:  "Valid value 0.0",
			value: 0.0,
			want:  false,
		},
		{
			name:  "Valid value 3.14",
			value: 3.14,
			want:  false,
		},
		{
			name:  "Valid negative value",
			value: -100.5,
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isFloat64Blank(tt.value)
			assert.Equal(t, tt.want, got, "isFloat64Blank(%v)", tt.value)
		})
	}
}

func Test_isStringBlank(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{
			name:  "DCGM_FT_STR_BLANK",
			value: dcgm.DCGM_FT_STR_BLANK,
			want:  true,
		},
		{
			name:  "DCGM_FT_STR_NOT_FOUND",
			value: dcgm.DCGM_FT_STR_NOT_FOUND,
			want:  true,
		},
		{
			name:  "DCGM_FT_STR_NOT_SUPPORTED",
			value: dcgm.DCGM_FT_STR_NOT_SUPPORTED,
			want:  true,
		},
		{
			name:  "DCGM_FT_STR_NOT_PERMISSIONED",
			value: dcgm.DCGM_FT_STR_NOT_PERMISSIONED,
			want:  true,
		},
		{
			name:  "Valid empty string",
			value: "",
			want:  false,
		},
		{
			name:  "Valid string",
			value: "Hello World",
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isStringBlank(tt.value)
			assert.Equal(t, tt.want, got, "isStringBlank(%q)", tt.value)
		})
	}
}

func Test_isBlankValue(t *testing.T) {
	tests := []struct {
		name  string
		value dcgm.FieldValue_v2
		want  bool
	}{
		{
			name: "INT64 BLANK value",
			value: dcgm.FieldValue_v2{
				FieldType: dcgm.DCGM_FT_INT64,
				Value:     createInt64ByteArray(dcgm.DCGM_FT_INT64_BLANK),
			},
			want: true,
		},
		{
			name: "INT64 NOT_FOUND value",
			value: dcgm.FieldValue_v2{
				FieldType: dcgm.DCGM_FT_INT64,
				Value:     createInt64ByteArray(dcgm.DCGM_FT_INT64_NOT_FOUND),
			},
			want: true,
		},
		{
			name: "INT64 valid value",
			value: dcgm.FieldValue_v2{
				FieldType: dcgm.DCGM_FT_INT64,
				Value:     createInt64ByteArray(42),
			},
			want: false,
		},
		{
			name: "DOUBLE BLANK value",
			value: dcgm.FieldValue_v2{
				FieldType: dcgm.DCGM_FT_DOUBLE,
				Value:     createFloat64ByteArray(dcgm.DCGM_FT_FP64_BLANK),
			},
			want: true,
		},
		{
			name: "DOUBLE valid value",
			value: dcgm.FieldValue_v2{
				FieldType: dcgm.DCGM_FT_DOUBLE,
				Value:     createFloat64ByteArray(3.14),
			},
			want: false,
		},
		{
			name: "STRING BLANK value",
			value: dcgm.FieldValue_v2{
				FieldType: dcgm.DCGM_FT_STRING,
				Value:     createStringByteArray(dcgm.DCGM_FT_STR_BLANK),
			},
			want: true,
		},
		{
			name: "STRING valid value",
			value: dcgm.FieldValue_v2{
				FieldType: dcgm.DCGM_FT_STRING,
				Value:     createStringByteArray("Valid String"),
			},
			want: false,
		},
		{
			name: "Unknown field type",
			value: dcgm.FieldValue_v2{
				FieldType: dcgm.DCGM_FT_BINARY,
				Value:     [4096]byte{},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBlankValue(tt.value)
			assert.Equal(t, tt.want, got, "isBlankValue()")
		})
	}
}

// Helper functions to create byte arrays for testing

func createInt64ByteArray(value int64) [4096]byte {
	var arr [4096]byte
	for i := 0; i < 8; i++ {
		arr[i] = byte(value >> (i * 8))
	}
	return arr
}

func createFloat64ByteArray(value float64) [4096]byte {
	var arr [4096]byte
	bits := *(*uint64)(unsafe.Pointer(&value))
	for i := 0; i < 8; i++ {
		arr[i] = byte(bits >> (i * 8))
	}
	return arr
}

func createStringByteArray(value string) [4096]byte {
	var arr [4096]byte
	copy(arr[:], value)
	return arr
}
