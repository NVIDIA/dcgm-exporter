/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package collector

const WindowSizeInMSLabel = "window_size_in_ms"

// Source of the const values: https://github.com/NVIDIA/DCGM/blob/master/dcgmlib/dcgm_fields.h
const (
	// DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE Nothing is running on the GPU and the clocks are dropping to Idle state
	DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE clockEventBitmask = 0x0000000000000001
	// DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING GPU clocks are limited by current setting of applications clocks
	DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING clockEventBitmask = 0x0000000000000002
	// DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP SW Power Scaling algorithm is reducing the clocks below requested clocks
	DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP clockEventBitmask = 0x0000000000000004
	// DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN HW Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN clockEventBitmask = 0x0000000000000008
	// DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST Sync Boost
	DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST clockEventBitmask = 0x0000000000000010
	// SW Thermal Slowdown
	DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL clockEventBitmask = 0x0000000000000020
	// DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL HW Thermal Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL clockEventBitmask = 0x0000000000000040
	// DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE HW Power Brake Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE clockEventBitmask = 0x0000000000000080
	// DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS GPU clocks are limited by current setting of Display clocks
	DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS clockEventBitmask = 0x0000000000000100
)
