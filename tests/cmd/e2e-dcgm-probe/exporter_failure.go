/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package main

import (
	"errors"
	"os"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

func writeExporterAcceptanceResult(options exporterAcceptanceOptions, report dcgmprobe.ExporterAcceptance, failure dcgmprobe.ExporterFailure, runErr error) error {
	if runErr == nil {
		runErr = writePrivateJSON(options.outputPath, report)
		if runErr == nil {
			return nil
		}
		failure.Stage = "report write"
	}
	failure.Version, failure.Outcome, failure.Error = 1, "failed", runErr.Error()
	failure.Report = report
	for _, history := range []*[]dcgmprobe.HistorySample{&failure.Report.Injected, &failure.Report.History, &failure.Report.SecondHistory} {
		if len(*history) > 64 {
			*history = (*history)[:64]
			failure.HistoryTruncated = true
		}
	}
	return errors.Join(runErr, writePrivateJSON(options.outputPath+".failed.json", failure))
}

func finishExporterAttempt(owned *acceptanceClient, app *acceptanceExporter, scratch string, failure *dcgmprobe.ExporterFailure, runErr error) error {
	cleanupErr := owned.finish(app)
	if scratch != "" {
		cleanupErr = errors.Join(cleanupErr, os.RemoveAll(scratch))
	}
	failure.Cleanup = "complete"
	if app != nil {
		failure.Stage, failure.Response = app.stage, app.lastResponse
		failure.HistoryVerified, failure.SecondHistoryVerified = app.historyVerified, app.secondHistoryVerified
		failure.CleanupErrors = app.cleanupErrors
	}
	if cleanupErr != nil {
		failure.CleanupErrors = append(failure.CleanupErrors, cleanupErr.Error())
		if runErr == nil {
			failure.Stage = "cleanup"
		}
	}
	if !owned.released || len(failure.CleanupErrors) != 0 {
		failure.Cleanup = "failed"
	}
	return errors.Join(runErr, cleanupErr)
}
