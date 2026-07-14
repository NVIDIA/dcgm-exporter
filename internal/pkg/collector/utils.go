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
	"context"
	"log/slog"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

const (
	blankValueSkippedMessage  = "Skipping blank DCGM field value"
	nonOKStatusSkippedMessage = "Skipping DCGM field value with non-OK status"

	unknownFieldName = "unknown"
)

func isDebugLoggingEnabled() bool {
	return slog.Default().Enabled(context.Background(), slog.LevelDebug)
}

func counterFieldNameOrUnknown(c []counters.Counter, fieldID dcgm.Short) string {
	counter, err := findCounterField(c, fieldID)
	if err != nil {
		return unknownFieldName
	}
	return counter.FieldName
}

func logBlankValueSkipped(
	fieldID dcgm.Short,
	fieldName string,
	entityType dcgm.Field_Entity_Group,
	entityID uint,
) {
	if !isDebugLoggingEnabled() {
		return
	}

	slog.Debug(
		blankValueSkippedMessage,
		slog.Int("fieldID", int(fieldID)),
		slog.String("fieldName", fieldName),
		slog.String("entityType", entityType.String()),
		slog.Uint64("entityID", uint64(entityID)),
	)
}

func logFieldValueSkipped(
	value dcgm.FieldValue_v1,
	fieldName string,
	entityType dcgm.Field_Entity_Group,
	entityID uint,
) {
	if value.Status == dcgm.DCGM_ST_OK {
		logBlankValueSkipped(value.FieldID, fieldName, entityType, entityID)
		return
	}

	if !isDebugLoggingEnabled() {
		return
	}

	slog.Debug(
		nonOKStatusSkippedMessage,
		slog.Int("fieldID", int(value.FieldID)),
		slog.String("fieldName", fieldName),
		slog.String("entityType", entityType.String()),
		slog.Uint64("entityID", uint64(entityID)),
		slog.Int("status", value.Status),
	)
}

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
