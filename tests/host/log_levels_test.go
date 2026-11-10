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

package host

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// debugRecordText is how the exporter's text log format marks a debug record.
	debugRecordText = "level=DEBUG"
	// dcgmLogFile is the only file the embedded DCGM log can go to.
	dcgmLogFile = "stdout"
)

// dcgmLogLine matches the dcgm_level field the exporter adds to every line of
// the embedded DCGM log it routes through its own logger, and captures the DCGM
// level. The text format writes dcgm_level=DEBUG and the JSON format writes
// "dcgm_level":"DEBUG". The record's own level is always INFO, so only this
// field carries the DCGM severity.
var dcgmLogLine = regexp.MustCompile(`\bdcgm_level=(\w+)|"dcgm_level":"(\w+)"`)

// runDebugLogEffect runs the exporter with the default log level, with --debug
// and with observability.logs.level, and checks only the last two write debug
// records and every run serves the baseline families.
func runDebugLogEffect(t testing.TB) {
	base := startSettingBaseline(t)
	runSettingForms(t, base, []settingForm{
		{
			name: "default log level",
			args: func(_ testing.TB, base *settingBaseline, port int) []string {
				return baselineArgs(base.collectors, port)
			},
			check: checkNoDebugRecords,
		},
		{
			name: "--debug",
			args: func(_ testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--debug")
			},
			check: checkDebugRecords,
		},
		{
			name: "observability.logs.level",
			args: func(t testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--config-file", writeSettingConfig(t, map[string]any{
					"version":       2,
					"observability": map[string]any{"logs": map[string]any{"level": "debug"}},
				}))
			},
			check: checkDebugRecords,
		},
	})
}

// runEmbeddedDCGMLogEffect runs the exporter with the embedded DCGM log off
// and on at ERROR and DEBUG, as flags and as config keys, and checks the DCGM
// log lines in the output match the level and every run serves the baseline
// families.
func runEmbeddedDCGMLogEffect(t testing.TB) {
	base := startSettingBaseline(t)
	runSettingForms(t, base, []settingForm{
		{
			name: "DCGM log off",
			args: func(_ testing.TB, base *settingBaseline, port int) []string {
				return baselineArgs(base.collectors, port)
			},
			check: checkNoDCGMLog,
		},
		dcgmLogFlagForm("ERROR"),
		dcgmLogFlagForm("DEBUG"),
		dcgmLogKeyForm("error", "ERROR"),
		dcgmLogKeyForm("debug", "DEBUG"),
	})
}

// dcgmLogFlagForm is the form that turns the DCGM log on with the flags at
// level, a DCGM level name.
func dcgmLogFlagForm(level string) settingForm {
	return settingForm{
		name: "--enable-dcgm-log --dcgm-log-level " + level,
		args: func(_ testing.TB, base *settingBaseline, port int) []string {
			return append(baselineArgs(base.collectors, port), "--enable-dcgm-log", "--dcgm-log-level", level)
		},
		check: dcgmLogCheck(level),
	}
}

// dcgmLogKeyForm is the form that turns the DCGM log on with
// sources.dcgm.embedded_log at keyLevel, a YAML level name. level is the DCGM
// level name that keyLevel stands for.
func dcgmLogKeyForm(keyLevel, level string) settingForm {
	return settingForm{
		name: "sources.dcgm.embedded_log " + keyLevel,
		args: func(t testing.TB, base *settingBaseline, port int) []string {
			return append(baselineArgs(base.collectors, port), "--config-file", writeSettingConfig(t, map[string]any{
				"version": 2,
				"sources": map[string]any{"dcgm": map[string]any{
					"embedded_log": map[string]any{"file": dcgmLogFile, "level": keyLevel},
				}},
			}))
		},
		check: dcgmLogCheck(level),
	}
}

// checkNoDebugRecords checks the run has the baseline families and wrote no debug record.
func checkNoDebugRecords(t testing.TB, base *settingBaseline, run settingRun) {
	t.Helper()
	checkSameDCGMShapeAsBaseline(t, base, run)

	require.Zerof(t, countDebugRecords(run.log), "the default log level wrote debug records:\n%s", run.log)
}

// checkDebugRecords checks the run has the baseline families and wrote debug records.
func checkDebugRecords(t testing.TB, base *settingBaseline, run settingRun) {
	t.Helper()
	checkSameDCGMShapeAsBaseline(t, base, run)

	require.Positivef(t, countDebugRecords(run.log), "no debug record in the log:\n%s", run.log)
}

// countDebugRecords counts the debug records in the exporter's text log.
func countDebugRecords(log string) int {
	return strings.Count(log, debugRecordText)
}

// checkNoDCGMLog checks the run has the baseline families and its output holds no DCGM log line.
func checkNoDCGMLog(t testing.TB, base *settingBaseline, run settingRun) {
	t.Helper()
	checkSameDCGMShapeAsBaseline(t, base, run)

	require.Emptyf(t, dcgmLogLevels(run.log), "DCGM log lines appeared with the DCGM log off:\n%s", run.log)
}

// dcgmLogCheck returns a check that the run has the baseline families and its output holds the DCGM log lines
// that the DCGM log level level gives.
func dcgmLogCheck(level string) func(testing.TB, *settingBaseline, settingRun) {
	return func(t testing.TB, base *settingBaseline, run settingRun) {
		t.Helper()
		checkSameDCGMShapeAsBaseline(t, base, run)

		require.Empty(t, dcgmLogLevelProblems(dcgmLogLevels(run.log), level), "DCGM log at level %s\n%s", level, run.log)
	}
}

// dcgmLogLevels returns the level of every DCGM log line in output, in order.
func dcgmLogLevels(output string) []string {
	var levels []string
	for _, line := range strings.Split(output, "\n") {
		match := dcgmLogLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		// The text form fills the first group and the JSON form the second.
		levels = append(levels, match[1]+match[2])
	}
	return levels
}

// dcgmLogLevelProblems lists how the levels of the DCGM log lines differ from
// what the DCGM log level level gives. ERROR gives only FATAL and ERROR lines;
// an output with no DCGM line also passes, so an ERROR form proves only that
// nothing below ERROR appeared. DEBUG must give at least one INFO, DEBUG or VERB line, which ERROR never does.
func dcgmLogLevelProblems(levels []string, level string) []string {
	if level == "ERROR" {
		var problems []string
		for _, line := range levels {
			if line != "FATAL" && line != "ERROR" {
				problems = append(problems, fmt.Sprintf("%s line appeared with level ERROR", line))
			}
		}
		return problems
	}

	if slices.ContainsFunc(levels, func(line string) bool { return line == "INFO" || line == "DEBUG" || line == "VERB" }) {
		return nil
	}
	return []string{fmt.Sprintf("no INFO, DEBUG or VERB line appeared with level %s", level)}
}

// TestCountDebugRecords checks only the exporter's debug records count.
func TestCountDebugRecords(t *testing.T) {
	log := "time=x level=INFO msg=start\ntime=x level=DEBUG msg=a\ntime=x level=DEBUG msg=b\ntime=x level=WARN msg=DEBUG\n"

	assert.Equal(t, 2, countDebugRecords(log))
	assert.Zero(t, countDebugRecords("time=x level=INFO msg=start\n"))
}

// TestDCGMLogLevels checks DCGM log lines are found in the text and JSON log
// formats by their dcgm_level field, and the exporter's own records are not.
func TestDCGMLogLevels(t *testing.T) {
	output := `time=2026-10-29T14:10:09.000Z level=INFO msg="Attempting to initialize DCGM."
time=2026-10-29T14:10:10.430Z level=INFO msg="Initialized base logger [./dcgmlib/src/DcgmApi.cpp:6402] [{anonymous}::StartEmbeddedV2]" dcgm_level=DEBUG
time=2026-10-29T14:10:10.430Z level=INFO msg="Created thread named \"\" ID 3506427584 DcgmThread ptr 0x0x71c29403e1c0 [./common/DcgmThread/DcgmThread.cpp:119] [DcgmThread::Start]" dcgm_level=INFO
{"time":"2026-10-29T14:10:11.000Z","level":"INFO","msg":"module loaded [./x.cpp:2] [g]","dcgm_level":"ERROR"}
time=2026-10-29T14:10:12.000Z level=ERROR msg="ERROR [not a dcgm line]"
`

	assert.Equal(t, []string{"DEBUG", "INFO", "ERROR"}, dcgmLogLevels(output))
	assert.Empty(t, dcgmLogLevels("time=x level=DEBUG msg=hello\n"))
}

// TestDCGMLogLevelProblems checks ERROR allows only FATAL and ERROR lines and
// DEBUG needs a line below ERROR, which proves the level took effect.
func TestDCGMLogLevelProblems(t *testing.T) {
	assert.Empty(t, dcgmLogLevelProblems(nil, "ERROR"))
	assert.Empty(t, dcgmLogLevelProblems([]string{"FATAL", "ERROR"}, "ERROR"))
	assert.Equal(t, []string{"INFO line appeared with level ERROR"}, dcgmLogLevelProblems([]string{"ERROR", "INFO"}, "ERROR"))
	assert.Empty(t, dcgmLogLevelProblems([]string{"ERROR", "INFO"}, "DEBUG"))
	assert.Len(t, dcgmLogLevelProblems([]string{"ERROR", "WARN"}, "DEBUG"), 1, "only ERROR and WARN lines")
	assert.Len(t, dcgmLogLevelProblems(nil, "DEBUG"), 1, "no lines at all")
}
