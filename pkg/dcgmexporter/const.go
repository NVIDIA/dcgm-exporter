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

import "fmt"

type DCGMExporterMetric uint16

const (
	DCGMFIUnknown      DCGMExporterMetric = 0
	DCGMXIDErrorsCount DCGMExporterMetric = iota + 9000
)

// DCGMFields maps DCGMExporterMetric String to enum
var DCGMFields = map[string]DCGMExporterMetric{
	DCGMXIDErrorsCount.String(): DCGMXIDErrorsCount,
	DCGMFIUnknown.String():      DCGMFIUnknown,
}

// String method to convert the enum value to a string
func (d DCGMExporterMetric) String() string {
	switch d {
	case DCGMXIDErrorsCount:
		return "DCGM_EXP_XID_ERRORS_COUNT"
	default:
		return "DCGM_FI_UNKNOWN"
	}
}

func IdentifyMetricType(s string) (DCGMExporterMetric, error) {
	mv, ok := DCGMFields[s]
	if !ok {
		return mv, fmt.Errorf("unknown DCGMExporterMetric field '%s'", s)
	}
	return mv, nil
}

// Constants for logging fields
const (
	LoggerGroupIDKey = "groupID"
	LoggerDumpKey    = "dump"
	LoggerStackTrace = "stacktrace"
)

const (
	PARENT_ID_IGNORED      = 0
	DCGM_ST_NOT_CONFIGURED = "Setting not configured"
)
