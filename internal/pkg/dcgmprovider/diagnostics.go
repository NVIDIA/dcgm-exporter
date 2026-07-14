/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmprovider

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/capabilities"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
)

const (
	dcgmInitModeEmbedded   = "embedded"
	dcgmInitModeStandalone = "standalone"
	dcgmInitModeFields     = "fields"

	remoteHostengineHint = "Verify nv-hostengine is running and listening on the expected address. " +
		"For IPv6, use bracket notation: [<IPv6_ADDR>]:<PORT> (e.g., \"[::1]:5555\")"
	unknownDCGMStatusHint = "Enable DCGM debug logs and check the DCGM hostengine logs for the status-specific failure reason."
)

var dcgmStatusCodePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bError:\s*(-\d+)\b`),
	regexp.MustCompile(`\berror code\s+(-\d+)\b`),
}

type dcgmStartupStatus struct {
	code       int
	diagnostic dcgmerrors.Diagnostic
	known      bool
}

func logDCGMInitFailure(config *appconfig.Config, mode string, err error, extraAttrs ...slog.Attr) {
	attrs, hasHint := dcgmStartupLogAttrs(config, mode, extraAttrs...)
	attrs = append(attrs, slog.String("error", err.Error()))

	status, hasStatus := classifyDCGMStartupError(err)
	if hasStatus {
		attrs = appendDCGMStatusAttrs(attrs, status, hasHint)
	}

	slog.Error(dcgmInitFailureMessage(mode), attrs...)
}

func logDCGMFieldsInitFailure(config *appconfig.Config, statusCode int) {
	status := classifyDCGMStatusCode(statusCode)
	attrs, _ := dcgmStartupLogAttrs(config, dcgmInitModeFields)
	attrs = append(attrs, slog.String("error", fmt.Sprintf("Failed to initialize DCGM Fields module; err: %d", statusCode)))
	attrs = appendDCGMStatusAttrs(attrs, status, false)

	slog.Error("Failed to initialize DCGM Fields module", attrs...)
}

func dcgmInitFailureMessage(mode string) string {
	switch mode {
	case dcgmInitModeEmbedded:
		return "Failed to initialize embedded DCGM"
	case dcgmInitModeStandalone:
		return "Failed to connect to remote hostengine"
	default:
		return "Failed to initialize DCGM"
	}
}

func dcgmStartupLogAttrs(config *appconfig.Config, mode string, extraAttrs ...slog.Attr) ([]any, bool) {
	attrs := []any{slog.String("mode", mode)}
	hasHint := false

	if config != nil {
		if config.UseRemoteHE && config.RemoteHEInfo != "" {
			attrs = append(attrs, slog.String("address", config.RemoteHEInfo))
		}
		attrs = append(
			attrs,
			slog.Bool("enable_dcgm_log", config.EnableDCGMLog),
			slog.String("dcgm_log_level", config.DCGMLogLevel),
		)
	}

	attrs = append(
		attrs,
		slog.Int("uid", os.Getuid()),
		slog.Int("euid", os.Geteuid()),
		slog.Bool("running_as_root", capabilities.IsRunningAsRoot()),
		slog.Bool("has_cap_sys_admin", capabilities.CheckSysAdmin()),
	)

	for _, attr := range extraAttrs {
		if attr.Key == "hint" {
			hasHint = true
		}
		attrs = append(attrs, attr)
	}

	return attrs, hasHint
}

func appendDCGMStatusAttrs(attrs []any, status dcgmStartupStatus, hasHint bool) []any {
	attrs = append(attrs, slog.Int("dcgm_status_code", status.code))
	if status.known {
		attrs = append(attrs, slog.String("dcgm_status_name", status.diagnostic.Status))
		if hasHint {
			return append(attrs, slog.String("dcgm_status_hint", status.diagnostic.Hint))
		}
		return append(attrs, slog.String("hint", status.diagnostic.Hint))
	}

	if hasHint {
		return append(attrs, slog.String("dcgm_status_hint", unknownDCGMStatusHint))
	}
	return append(attrs, slog.String("hint", unknownDCGMStatusHint))
}

func classifyDCGMStartupError(err error) (dcgmStartupStatus, bool) {
	if err == nil {
		return dcgmStartupStatus{}, false
	}

	if diagnostic, ok := dcgmerrors.Classify(err); ok {
		return dcgmStartupStatus{code: diagnostic.Code, diagnostic: diagnostic, known: true}, true
	}

	var derr *dcgm.Error
	if errors.As(err, &derr) {
		return classifyDCGMStatusCode(int(derr.Code)), true
	}

	code, ok := parseDCGMStatusCode(err.Error())
	if !ok {
		return dcgmStartupStatus{}, false
	}
	return classifyDCGMStatusCode(code), true
}

func classifyDCGMStatusCode(code int) dcgmStartupStatus {
	diagnostic, ok := dcgmerrors.ClassifyCode(code)
	return dcgmStartupStatus{code: code, diagnostic: diagnostic, known: ok}
}

func parseDCGMStatusCode(message string) (int, bool) {
	for _, pattern := range dcgmStatusCodePatterns {
		matches := pattern.FindStringSubmatch(message)
		if len(matches) != 2 {
			continue
		}
		code, err := strconv.Atoi(matches[1])
		if err != nil {
			return 0, false
		}
		return code, true
	}
	return 0, false
}
