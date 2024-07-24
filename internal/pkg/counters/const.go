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

const (
	undefinedConfigMapData = "none"

	cpuFieldsStart = 1100
	dcpFieldsStart = 1000

	DCGMExpClockEventsCount = "DCGM_EXP_CLOCK_EVENTS_COUNT"
	DCGMExpXIDErrorsCount   = "DCGM_EXP_XID_ERRORS_COUNT"
	DCGMExpGPUHealthStatus  = "DCGM_EXP_GPU_HEALTH_STATUS"
)
