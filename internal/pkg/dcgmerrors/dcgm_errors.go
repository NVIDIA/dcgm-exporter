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

package dcgmerrors

import (
	"errors"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// Diagnostic describes a known DCGM error for operator-facing diagnostics.
type Diagnostic struct {
	Code    int
	Message string
	Status  string
	Hint    string
}

// Classify maps typed DCGM errors to actionable operator guidance.
func Classify(err error) (Diagnostic, bool) {
	var derr *dcgm.Error
	if !errors.As(err, &derr) {
		return Diagnostic{}, false
	}

	return ClassifyCode(int(derr.Code))
}

// ClassifyCode maps DCGM status codes to actionable operator guidance.
func ClassifyCode(code int) (Diagnostic, bool) {
	switch code {
	case dcgm.DCGM_ST_NO_PERMISSION:
		return Diagnostic{
			Code:    code,
			Message: "DCGM permission denied",
			Status:  "DCGM_ST_NO_PERMISSION",
			Hint:    "DCGM does not have permission to perform the requested operation. Verify container/device permissions, root/euid, and required capabilities such as CAP_SYS_ADMIN.",
		}, true
	case dcgm.DCGM_ST_REQUIRES_ROOT:
		return Diagnostic{
			Code:    code,
			Message: "DCGM operation requires root",
			Status:  "DCGM_ST_REQUIRES_ROOT",
			Hint:    "This DCGM operation requires root. Run dcgm-exporter or nv-hostengine with the required root privileges.",
		}, true
	case dcgm.DCGM_ST_CONNECTION_NOT_VALID:
		return Diagnostic{
			Code:    code,
			Message: "Could not retrieve metrics from DCGM",
			Status:  "DCGM_ST_CONNECTION_NOT_VALID",
			Hint:    "Verify nv-hostengine is running and restart dcgm-exporter after the DCGM connection recovers.",
		}, true
	case dcgm.DCGM_ST_PAUSED:
		return Diagnostic{
			Code:    code,
			Message: "DCGM is paused",
			Status:  "DCGM_ST_PAUSED",
			Hint:    "Resume DCGM or restart nv-hostengine before scraping metrics.",
		}, true
	case dcgm.DCGM_ST_UNINITIALIZED:
		return Diagnostic{
			Code:    code,
			Message: "DCGM is uninitialized",
			Status:  "DCGM_ST_UNINITIALIZED",
			Hint:    "Verify the installed DCGM library matches the go-dcgm bindings used to build dcgm-exporter.",
		}, true
	case dcgm.DCGM_ST_VER_MISMATCH:
		return Diagnostic{
			Code:    code,
			Message: "DCGM version mismatch",
			Status:  "DCGM_ST_VER_MISMATCH",
			Hint:    "Verify dcgm-exporter, go-dcgm, and the runtime DCGM library are from compatible DCGM versions.",
		}, true
	case dcgm.DCGM_ST_FUNCTION_NOT_FOUND:
		return Diagnostic{
			Code:    code,
			Message: "Required DCGM function was not found",
			Status:  "DCGM_ST_FUNCTION_NOT_FOUND",
			Hint:    "Install a DCGM library version that provides the required API, or rebuild dcgm-exporter against the target DCGM version.",
		}, true
	case dcgm.DCGM_ST_LIBRARY_NOT_FOUND:
		return Diagnostic{
			Code:    code,
			Message: "DCGM library was not found",
			Status:  "DCGM_ST_LIBRARY_NOT_FOUND",
			Hint:    "Install the DCGM package and ensure libdcgm.so is available on the runtime library path.",
		}, true
	case dcgm.DCGM_ST_INIT_ERROR:
		return Diagnostic{
			Code:    code,
			Message: "DCGM initialization failed",
			Status:  "DCGM_ST_INIT_ERROR",
			Hint:    "Check the DCGM installation, NVIDIA driver, and hostengine logs.",
		}, true
	case dcgm.DCGM_ST_NVML_ERROR:
		return Diagnostic{
			Code:    code,
			Message: "DCGM received an NVML error",
			Status:  "DCGM_ST_NVML_ERROR",
			Hint:    "Verify the NVIDIA driver and NVML are installed, loaded, and compatible with DCGM.",
		}, true
	case dcgm.DCGM_ST_NVML_NOT_LOADED:
		return Diagnostic{
			Code:    code,
			Message: "NVML is not loaded",
			Status:  "DCGM_ST_NVML_NOT_LOADED",
			Hint:    "Verify the NVIDIA driver stack is loaded before starting dcgm-exporter.",
		}, true
	case dcgm.DCGM_ST_INSUFFICIENT_DRIVER_VERSION:
		return Diagnostic{
			Code:    code,
			Message: "NVIDIA driver version is insufficient for DCGM",
			Status:  "DCGM_ST_INSUFFICIENT_DRIVER_VERSION",
			Hint:    "The installed NVIDIA driver is too old for the requested DCGM API. Upgrade the driver or use a compatible DCGM version.",
		}, true
	default:
		return Diagnostic{}, false
	}
}
