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
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// versionMismatchText is DCGM's status text for DCGM_ST_VER_MISMATCH, in lower case.
const versionMismatchText = "api version mismatch"

// SupportedVersionHint is the "hint" log attribute for warnings that a DCGM
// version fallback is in use.
const SupportedVersionHint = "Make sure the DCGM library and the DCGM hostengine both run the DCGM version " +
	"supported by this version of dcgm-exporter."

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

// IsVersionMismatch reports whether err is DCGM_ST_VER_MISMATCH. Some go-dcgm
// calls return only DCGM's status text, so the text is checked as well.
func IsVersionMismatch(err error) bool {
	if err == nil {
		return false
	}

	if diagnostic, ok := Classify(err); ok {
		return diagnostic.Code == dcgm.DCGM_ST_VER_MISMATCH
	}

	return strings.Contains(strings.ToLower(err.Error()), versionMismatchText)
}

// IsFatalScrapeError reports whether err means DCGM cannot safely serve
// metrics. The HTTP server uses this shared taxonomy to end the application
// instead of treating a fatal DCGM failure as an empty successful scrape.
func IsFatalScrapeError(err error) bool {
	if err == nil {
		return false
	}
	if dcgmError, ok := err.(*dcgm.Error); ok && isFatalScrapeCode(int(dcgmError.Code)) {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, joinedErr := range joined.Unwrap() {
			if IsFatalScrapeError(joinedErr) {
				return true
			}
		}
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return IsFatalScrapeError(wrapped.Unwrap())
	}
	return false
}

// isFatalScrapeCode reports whether a DCGM status leaves the exporter unable
// to safely serve metrics. IsFatalScrapeError applies it to every branch of a
// wrapped or joined error.
func isFatalScrapeCode(code int) bool {
	switch code {
	case dcgm.DCGM_ST_CONNECTION_NOT_VALID,
		dcgm.DCGM_ST_UNINITIALIZED,
		dcgm.DCGM_ST_LIBRARY_NOT_FOUND,
		dcgm.DCGM_ST_INIT_ERROR,
		dcgm.DCGM_ST_NVML_NOT_LOADED:
		return true
	default:
		return false
	}
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
