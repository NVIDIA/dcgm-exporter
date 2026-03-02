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

import "github.com/NVIDIA/go-dcgm/pkg/dcgm"

// isBlankValue checks if a FieldValue_v2 contains a DCGM blank/sentinel value
// that should be filtered out. These values indicate no valid data is available.
func isBlankValue(val dcgm.FieldValue_v2) bool {
	switch val.FieldType {
	case dcgm.DCGM_FT_INT64:
		return isInt64Blank(val.Int64())
	case dcgm.DCGM_FT_DOUBLE:
		return isFloat64Blank(val.Float64())
	case dcgm.DCGM_FT_STRING:
		return isStringBlank(val.String())
	}
	return false
}

// isInt64Blank checks if an int64 value is a DCGM blank/sentinel value.
func isInt64Blank(v int64) bool {
	return v == dcgm.DCGM_FT_INT32_BLANK ||
		v == dcgm.DCGM_FT_INT32_NOT_FOUND ||
		v == dcgm.DCGM_FT_INT32_NOT_SUPPORTED ||
		v == dcgm.DCGM_FT_INT32_NOT_PERMISSIONED ||
		v == dcgm.DCGM_FT_INT64_BLANK ||
		v == dcgm.DCGM_FT_INT64_NOT_FOUND ||
		v == dcgm.DCGM_FT_INT64_NOT_SUPPORTED ||
		v == dcgm.DCGM_FT_INT64_NOT_PERMISSIONED
}

// isFloat64Blank checks if a float64 value is a DCGM blank/sentinel value.
func isFloat64Blank(v float64) bool {
	return v == dcgm.DCGM_FT_FP64_BLANK ||
		v == dcgm.DCGM_FT_FP64_NOT_FOUND ||
		v == dcgm.DCGM_FT_FP64_NOT_SUPPORTED ||
		v == dcgm.DCGM_FT_FP64_NOT_PERMISSIONED
}

// isStringBlank checks if a string value is a DCGM blank/sentinel value.
func isStringBlank(v string) bool {
	return v == dcgm.DCGM_FT_STR_BLANK ||
		v == dcgm.DCGM_FT_STR_NOT_FOUND ||
		v == dcgm.DCGM_FT_STR_NOT_SUPPORTED ||
		v == dcgm.DCGM_FT_STR_NOT_PERMISSIONED
}
