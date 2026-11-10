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

package counters

import (
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/signalcatalog"
)

const (
	cpuFieldsStart = 1100
	dcpFieldsStart = 1000

	DCGMExpClockEventsCount = appconfig.ExporterClockEventsCountField
	DCGMExpClockEventsTotal = signalcatalog.ExporterFieldClockEventsTotal
	DCGMExpXIDErrorsCount   = appconfig.ExporterXIDErrorsCountField
	DCGMExpXIDErrorsTotal   = signalcatalog.ExporterFieldXIDErrorsTotal
	DCGMExpGPUHealthStatus  = signalcatalog.ExporterFieldGPUHealthStatus
	DCGMExpP2PStatus        = signalcatalog.ExporterFieldP2PStatus

	DCGMExpSystemHealthStatus = "DCGM_EXP_SYSTEM_HEALTH_STATUS"

	DCGMExpFieldPolicyEnabled                     = "DCGM_EXP_FIELD_POLICY_ENABLED"
	DCGMExpFieldPolicyCurrentValue                = "DCGM_EXP_FIELD_POLICY_CURRENT_VALUE"
	DCGMExpFieldPolicyThreshold                   = "DCGM_EXP_FIELD_POLICY_THRESHOLD"
	DCGMExpFieldPolicyViolationCount              = "DCGM_EXP_FIELD_POLICY_VIOLATION_COUNT"
	DCGMExpFieldPolicyLastTriggerTimestampSeconds = "DCGM_EXP_FIELD_POLICY_LAST_TRIGGER_TIMESTAMP_SECONDS"
)
