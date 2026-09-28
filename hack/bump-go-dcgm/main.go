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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	goDCGMModule      = "github.com/NVIDIA/go-dcgm"
	counterTestTarget = "./internal/pkg/counters/..."
)

// options captures command-line settings for a go-dcgm bump attempt.
type options struct {
	version            string
	output             string
	dryRun             bool
	allowFieldRemovals bool
}

// moduleInfo contains the subset of go command module JSON this tool needs.
type moduleInfo struct {
	Path    string      `json:"Path"`
	Version string      `json:"Version"`
	Dir     string      `json:"Dir"`
	Error   string      `json:"Error"`
	Replace *moduleInfo `json:"Replace"`
}

// main runs the bump helper and reports user-facing errors.
func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// run compares the requested go-dcgm version and updates the module when allowed.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}

	currentModule, err := currentGoDCGM(ctx)
	if err != nil {
		return err
	}
	targetModule, err := downloadGoDCGM(ctx, opts.version)
	if err != nil {
		return err
	}

	currentMaps, err := readFieldMaps(currentModule.Dir)
	if err != nil {
		return fmt.Errorf("current go-dcgm field map: %w", err)
	}
	targetMaps, err := readFieldMaps(targetModule.Dir)
	if err != nil {
		return fmt.Errorf("target go-dcgm field map: %w", err)
	}

	currentCombined, err := currentMaps.combined()
	if err != nil {
		return fmt.Errorf("current go-dcgm field maps: %w", err)
	}
	targetCombined, err := targetMaps.combined()
	if err != nil {
		return fmt.Errorf("target go-dcgm field maps: %w", err)
	}

	diff := diffFields(
		displayVersion(currentModule),
		displayVersion(targetModule),
		currentCombined,
		targetCombined,
	)
	if err := writeDiff(stdout, opts.output, diff); err != nil {
		return err
	}

	if diff.hasBlockedChanges(opts.allowFieldRemovals) {
		return blockedChangesError(diff, opts.allowFieldRemovals)
	}

	if opts.dryRun {
		fmt.Fprintln(stderr, "dry run: go.mod and go.sum were not changed")
		return nil
	}

	if err := runCommand(ctx, stdout, stderr, "go", "get", goDCGMModule+"@"+opts.version); err != nil {
		return err
	}
	if err := runCommand(ctx, stdout, stderr, "go", "mod", "tidy"); err != nil {
		return err
	}
	if err := runCommand(ctx, stdout, stderr, "go", "test", counterTestTarget); err != nil {
		return err
	}

	return nil
}

// parseOptions validates command-line flags passed from the Makefile target.
func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options

	flags := flag.NewFlagSet("bump-go-dcgm", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.version, "version", "", "go-dcgm version, branch, tag, commit, or query")
	flags.StringVar(&opts.output, "output", "text", "diff output format: text or json")
	flags.BoolVar(&opts.dryRun, "dry-run", false, "compare field maps without updating go.mod/go.sum")
	flags.BoolVar(&opts.allowFieldRemovals, "allow-field-removals", false, "allow removed field names; ID changes still fail")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}

	opts.version = strings.TrimSpace(opts.version)
	if opts.version == "" {
		return options{}, errors.New("GO_DCGM_VERSION is required")
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	switch opts.output {
	case "text", "json":
	default:
		return options{}, fmt.Errorf("GO_DCGM_OUTPUT must be text or json, got %q", opts.output)
	}

	return opts, nil
}

// currentGoDCGM returns the go-dcgm module currently selected by this repository.
func currentGoDCGM(ctx context.Context) (moduleInfo, error) {
	out, err := runCaptured(ctx, "", "go", "list", "-m", "-json", goDCGMModule)
	if err != nil {
		return moduleInfo{}, err
	}

	info, err := parseModuleJSON(out)
	if err != nil {
		return moduleInfo{}, fmt.Errorf("parse current go-dcgm module info: %w", err)
	}
	if info.Replace != nil && info.Replace.Dir != "" {
		info.Dir = info.Replace.Dir
		if info.Replace.Version != "" {
			info.Version = info.Replace.Version
		}
	}
	if info.Dir == "" {
		return moduleInfo{}, errors.New("current go-dcgm module directory was not reported by go list")
	}

	return info, nil
}

// downloadGoDCGM downloads the requested go-dcgm module version into the module cache.
func downloadGoDCGM(ctx context.Context, version string) (moduleInfo, error) {
	tmpDir, err := os.MkdirTemp("", "go-dcgm-download-*")
	if err != nil {
		return moduleInfo{}, err
	}
	defer os.RemoveAll(tmpDir)

	out, err := runCaptured(ctx, tmpDir, "go", "mod", "download", "-json", goDCGMModule+"@"+version)
	if err != nil {
		return moduleInfo{}, err
	}

	info, err := parseModuleJSON(out)
	if err != nil {
		return moduleInfo{}, fmt.Errorf("parse target go-dcgm module info: %w", err)
	}
	if info.Error != "" {
		return moduleInfo{}, fmt.Errorf("download %s@%s: %s", goDCGMModule, version, info.Error)
	}
	if info.Dir == "" {
		return moduleInfo{}, errors.New("target go-dcgm module directory was not reported by go mod download")
	}

	return info, nil
}

// parseModuleJSON decodes module metadata emitted by go list or go mod download.
func parseModuleJSON(data []byte) (moduleInfo, error) {
	var info moduleInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return moduleInfo{}, err
	}

	return info, nil
}

// displayVersion returns a stable version label for reports.
func displayVersion(info moduleInfo) string {
	if info.Version != "" {
		return info.Version
	}
	if info.Dir != "" {
		return filepath.Clean(info.Dir)
	}

	return info.Path
}

// writeDiff renders a field-map diff in the requested output format.
func writeDiff(writer io.Writer, output string, diff fieldDiff) error {
	switch output {
	case "json":
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(diff)
	case "text":
		writeTextDiff(writer, diff)
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", output)
	}
}

// writeTextDiff renders a human-readable field-map diff.
func writeTextDiff(writer io.Writer, diff fieldDiff) {
	fmt.Fprintf(writer, "go-dcgm field diff\n")
	fmt.Fprintf(writer, "current: %s (%d fields)\n", diff.CurrentVersion, diff.CurrentCount)
	fmt.Fprintf(writer, "target:  %s (%d fields)\n", diff.TargetVersion, diff.TargetCount)
	fmt.Fprintln(writer)

	writeChangeSection(writer, "added", diff.Added, func(change fieldChange) string {
		return fmt.Sprintf("+ %s = %d", change.Name, valueOf(change.Target))
	})
	writeChangeSection(writer, "removed", diff.Removed, func(change fieldChange) string {
		return fmt.Sprintf("- %s = %d", change.Name, valueOf(change.Current))
	})
	writeChangeSection(writer, "id changes", diff.IDChanges, func(change fieldChange) string {
		return fmt.Sprintf("! %s: %d -> %d", change.Name, valueOf(change.Current), valueOf(change.Target))
	})
}

// writeChangeSection renders one text section of field-map changes.
func writeChangeSection(writer io.Writer, title string, changes []fieldChange, line func(fieldChange) string) {
	fmt.Fprintf(writer, "%s: %d\n", title, len(changes))
	if len(changes) == 0 {
		fmt.Fprintln(writer, "  (none)")
		fmt.Fprintln(writer)
		return
	}

	for _, change := range changes {
		fmt.Fprintf(writer, "  %s\n", line(change))
	}
	fmt.Fprintln(writer)
}

// blockedChangesError describes field-map changes that require manual review.
func blockedChangesError(diff fieldDiff, allowRemovals bool) error {
	var reasons []string
	if len(diff.IDChanges) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d field ID change(s)", len(diff.IDChanges)))
	}
	if len(diff.Removed) > 0 && !allowRemovals {
		reasons = append(reasons, fmt.Sprintf("%d removed field(s)", len(diff.Removed)))
	}

	message := "go-dcgm field diff contains " + strings.Join(reasons, " and ")
	if len(diff.Removed) > 0 && !allowRemovals {
		message += "; set GO_DCGM_ALLOW_FIELD_REMOVALS=true to allow removals after review"
	}
	if len(diff.IDChanges) > 0 {
		message += "; field ID changes require code review and are always blocked by this target"
	}

	return errors.New(message)
}

// valueOf unwraps an optional field ID for text output and tests.
func valueOf(value *int) int {
	if value == nil {
		return 0
	}

	return *value
}

// runCaptured runs a command and returns trimmed combined output.
func runCaptured(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s failed: %w\n%s", name, strings.Join(args, " "), err, string(out))
	}

	return bytes.TrimSpace(out), nil
}

// runCommand runs a command with stdout and stderr connected to the caller's writers.
func runCommand(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}

	return nil
}
