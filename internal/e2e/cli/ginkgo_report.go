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

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/onsi/ginkgo/v2/types"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const (
	// stderrTailLines is how many trailing stderr lines a failure reason shows
	// when the Ginkgo report is unavailable.
	stderrTailLines = 20

	// maxMessageChars caps a failure or skip message in the summary.
	maxMessageChars = 300

	// ginkgoJSONReportFlag is the test binary flag that writes the Ginkgo JSON report.
	ginkgoJSONReportFlag = "--ginkgo.json-report="
)

// specOutcome describes one spec from a Ginkgo report: its full text, where it
// stopped, and the summarized failure or skip message.
type specOutcome struct {
	Name     string
	Location string
	Message  string
}

// ginkgoOutcome holds the specs of one group run that failed or that ran and called Skip.
type ginkgoOutcome struct {
	Failed  []specOutcome
	Skipped []specOutcome
	// SuiteReasons holds suite-level failure reasons, such as an interrupt or a suite timeout.
	SuiteReasons []string
}

// newGroupReportPath creates a private directory for one group run and returns
// the report file path inside it and a function that removes the directory.
// Every group gets its own directory, so reports never collide.
func newGroupReportPath(suiteName, groupName string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "dcgm-exporter-e2e-report-")
	if err != nil {
		return "", func() { /* no directory was created */ }, fmt.Errorf("create Ginkgo report directory: %w", err)
	}

	cleanup := func() {
		if err := os.RemoveAll(dir); err != nil {
			slog.Warn("remove Ginkgo report directory", "dir", dir, "error", err)
		}
	}

	return filepath.Join(dir, suiteName+"-"+groupName+".json"), cleanup, nil
}

// readGinkgoOutcome reads the Ginkgo JSON report at path. Specs that failed,
// panicked, were interrupted, aborted or timed out go to Failed. Specs that ran
// and called Skip go to Skipped; specs filtered out by labels or focus never
// ran, so they are left out.
func readGinkgoOutcome(path string) (ginkgoOutcome, error) {
	var outcome ginkgoOutcome

	data, err := os.ReadFile(path)
	if err != nil {
		return outcome, fmt.Errorf("read Ginkgo report: %w", err)
	}

	var reports []types.Report
	if err := json.Unmarshal(data, &reports); err != nil {
		return outcome, fmt.Errorf("parse Ginkgo report %s: %w", path, err)
	}

	for _, report := range reports {
		outcome.SuiteReasons = append(outcome.SuiteReasons, report.SpecialSuiteFailureReasons...)
		for _, spec := range report.SpecReports {
			switch {
			case spec.Failed():
				outcome.Failed = append(outcome.Failed, newSpecOutcome(spec))
			case spec.State.Is(types.SpecStateSkipped) && specRanAndSkipped(spec):
				outcome.Skipped = append(outcome.Skipped, newSpecOutcome(spec))
			}
		}
	}

	return outcome, nil
}

// specRanAndSkipped reports whether a skipped spec started and called Skip.
// A spec filtered out by labels or focus, or skipped because an earlier spec
// in its ordered container failed, has no attempts.
func specRanAndSkipped(spec types.SpecReport) bool {
	return spec.NumAttempts > 0
}

// newSpecOutcome builds a specOutcome from a spec's full name, failure
// location and shortened message.
func newSpecOutcome(spec types.SpecReport) specOutcome {
	name := spec.FullText()
	if name == "" {
		// Suite-level nodes such as BeforeSuite have no spec text.
		name = spec.LeafNodeType.String()
	}

	location := spec.Failure.Location
	if location.FileName == "" {
		location = spec.Failure.FailureNodeLocation
	}

	outcome := specOutcome{Name: name, Message: specMessage(spec)}
	if location.FileName != "" {
		outcome.Location = fmt.Sprintf("%s:%d", location.FileName, location.LineNumber)
	}

	return outcome
}

// specMessage shortens a spec's failure or skip message. A forwarded panic
// value and the messages of additional failures, such as a failing cleanup
// after the spec failed, are appended.
func specMessage(spec types.SpecReport) string {
	parts := []string{messageSummary(spec.Failure.Message)}
	if spec.Failure.ForwardedPanic != "" {
		parts = append(parts, "panic: "+messageSummary(spec.Failure.ForwardedPanic))
	}
	for _, additional := range spec.AdditionalFailures {
		if message := messageSummary(additional.Failure.Message); message != "" {
			parts = append(parts, "also: "+message)
		}
	}

	return strings.Join(slices.DeleteFunc(parts, func(part string) bool { return part == "" }), "; ")
}

// messageSummary joins the non-empty lines of a message with single spaces,
// collapsing runs of whitespace, and cuts the result at maxMessageChars
// characters with an ellipsis. Gomega puts the useful part of a failure on
// later lines, so the whole message is kept rather than its first line.
func messageSummary(text string) string {
	joined := strings.Join(strings.Fields(text), " ")

	runes := []rune(joined)
	if len(runes) <= maxMessageChars {
		return joined
	}

	return string(runes[:maxMessageChars]) + "…"
}

// finishGroup reads a finished group's Ginkgo report once. It records the
// group's skipped specs in skips, whether or not the group failed, and returns
// nil when the test binary exited 0. Otherwise it returns an error that lists
// each failed spec from the report; when the report cannot be used, the error
// says so and shows the exit status and the last stderr lines instead.
func finishGroup(kind, group, reportPath string, result e2eexec.Result, skips *skippedSpecs) error {
	outcome, readErr := readGinkgoOutcome(reportPath)
	if readErr == nil && len(outcome.Skipped) != 0 {
		*skips = append(*skips, groupSkips{Group: group, Specs: outcome.Skipped})
	}
	if result.ExitCode == 0 {
		return nil
	}

	return groupFailure(kind, group, outcome, readErr, result)
}

// groupFailure builds the error for a group whose test binary exited non-zero
// from its report outcome, or from readErr and stderr when the report is unusable.
func groupFailure(kind, group string, outcome ginkgoOutcome, readErr error, result e2eexec.Result) error {
	if readErr != nil {
		return fmt.Errorf("%s group %s failed: no Ginkgo report (%v); %s", kind, group, readErr, exitAndStderr(result))
	}

	var text strings.Builder
	fmt.Fprintf(&text, "%s group %s failed:", kind, group)
	for _, spec := range outcome.Failed {
		fmt.Fprintf(&text, "\n  FAILED %s", spec.Name)
		if spec.Location != "" {
			fmt.Fprintf(&text, " (%s)", spec.Location)
		}
		if spec.Message != "" {
			fmt.Fprintf(&text, ": %s", spec.Message)
		}
	}
	for _, reason := range outcome.SuiteReasons {
		fmt.Fprintf(&text, "\n  suite: %s", reason)
	}
	if len(outcome.Failed) == 0 {
		fmt.Fprintf(&text, "\n  the Ginkgo report lists no failed spec; %s", exitAndStderr(result))
	}

	return errors.New(text.String())
}

// ginkgoGroupArgs returns the test binary arguments that run one group's
// labeled specs verbosely and write the Ginkgo JSON report to reportPath.
func ginkgoGroupArgs(testName string, labels []string, reportPath string) []string {
	return []string{
		goTestVerboseFlag,
		goTestRunFlag, "^" + testName + "$",
		"--ginkgo.v",
		"--ginkgo.no-color",
		"--ginkgo.fail-on-empty",
		"--ginkgo.label-filter=" + strings.Join(labels, " || "),
		ginkgoJSONReportFlag + reportPath,
	}
}

// exitAndStderr describes a finished command by its exit status and the last
// stderr lines, labeled as stderr so they are not read as test output. The
// command's error is added only when it says more than the exit status, such
// as a command that could not start.
func exitAndStderr(result e2eexec.Result) string {
	text := fmt.Sprintf("exit status %d", result.ExitCode)
	var exitErr *exec.ExitError
	if result.Err != nil && !errors.As(result.Err, &exitErr) {
		text += fmt.Sprintf(" (%v)", result.Err)
	}

	lines := strings.Split(strings.TrimRight(string(result.Stderr), "\r\n"), "\n")
	if len(lines) > stderrTailLines {
		lines = lines[len(lines)-stderrTailLines:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return text + "; stderr was empty"
	}

	return fmt.Sprintf("%s; last stderr lines (%d):\n  stderr: %s", text, len(lines), strings.Join(lines, "\n  stderr: "))
}

// groupSkips lists the skipped specs of one group run.
type groupSkips struct {
	Group string
	Specs []specOutcome
}

// skippedSpecs collects the skipped specs of every group in one run so the
// run can list them together at its end.
type skippedSpecs []groupSkips

// skippedSpecsKey is the context key under which a run keeps its skippedSpecs.
type skippedSpecsKey struct{}

// withSkippedSpecs returns a context whose group runs record their skipped
// specs in skips, so the run can write them once at its end.
func withSkippedSpecs(ctx context.Context, skips *skippedSpecs) context.Context {
	return context.WithValue(ctx, skippedSpecsKey{}, skips)
}

// skippedSpecsFrom returns the collector a run attached to ctx. A context with
// none gets a collector nobody reads, so a group run outside a full run still works.
func skippedSpecsFrom(ctx context.Context) *skippedSpecs {
	if skips, ok := ctx.Value(skippedSpecsKey{}).(*skippedSpecs); ok {
		return skips
	}

	return new(skippedSpecs)
}

// write prints every group's skipped specs with their reasons, whether or not
// the group failed. It prints nothing when no spec was skipped.
func (s skippedSpecs) write(w io.Writer) {
	if len(s) == 0 {
		return
	}

	fmt.Fprintln(w, "[e2e] skipped specs:")
	for _, group := range s {
		fmt.Fprintf(w, "[e2e]   group %s:\n", group.Group)
		for _, spec := range group.Specs {
			if spec.Message == "" {
				fmt.Fprintf(w, "[e2e]     SKIPPED %s\n", spec.Name)
				continue
			}
			fmt.Fprintf(w, "[e2e]     SKIPPED %s: %s\n", spec.Name, spec.Message)
		}
	}
}
