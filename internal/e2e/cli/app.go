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

// Package cli provides the e2e validation command tree.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	urfavecli "github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/clioutput"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const (
	exitSetupFailure    = 10
	exitScenarioFailure = 20
	clusterInfoTimeout  = 2 * time.Minute
)

// artifactMode records whether the command is running from source or a prebuilt test root.
type artifactMode string

const (
	artifactModeSource  artifactMode = "source-tree"
	artifactModePackage artifactMode = "package"
)

var errSuiteSkipped = errors.New("suite skipped")

// Options configures the e2e CLI command tree.
type Options struct {
	Root   string
	Stdout io.Writer
	Stderr io.Writer
	Runner e2eexec.Runner
}

// resolveE2ERoot finds the source or packaged artifact root from a supported
// working directory. Source runs are rooted at go.mod; package runs are rooted
// at the directory that supplies bin/dcgm-exporter-*.test binaries.
func resolveE2ERoot(start string) (string, error) {
	path, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(path, "go.mod")) || packageTestBinariesPresent(filepath.Join(path, "bin")) {
			return path, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("could not locate repo root with go.mod or a package root with bin/ suite test binaries from %s", start)
		}
		path = parent
	}
}

// packageTestBinariesPresent reports whether binDir contains a packaged suite binary.
func packageTestBinariesPresent(binDir string) bool {
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasPrefix(name, "dcgm-exporter-") && strings.HasSuffix(name, ".test") {
			return true
		}
	}
	return false
}

// exitError preserves the harness-specific process code while carrying the original failure.
type exitError struct {
	code int
	err  error
}

// Error returns the wrapped failure message.
func (e exitError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped failure for errors.Is and errors.As.
func (e exitError) Unwrap() error { return e.err }

// ExitCode returns the process exit code encoded by err.
func ExitCode(err error) (int, bool) {
	var ee exitError
	if errors.As(err, &ee) {
		return ee.code, true
	}
	return 0, false
}

// setupFailure marks prerequisite and orchestration failures with the setup exit code.
func setupFailure(err error) error {
	if err == nil {
		return nil
	}
	var ee exitError
	if errors.As(err, &ee) {
		return err
	}
	return exitError{code: exitSetupFailure, err: err}
}

// scenarioFailure marks validation failures with the scenario exit code.
func scenarioFailure(err error) error {
	if err == nil {
		return nil
	}
	var ee exitError
	if errors.As(err, &ee) {
		return err
	}
	return exitError{code: exitScenarioFailure, err: err}
}

// run executes the command tree with the real OS runner.
func run(args []string, stdout, stderr io.Writer) error {
	return runWithRunner(args, stdout, stderr, e2eexec.OSRunner{})
}

// runWithRunner executes the command tree with an injected runner for tests.
func runWithRunner(args []string, stdout, stderr io.Writer, runner e2eexec.Runner) error {
	return runWithRunnerContext(context.Background(), args, stdout, stderr, runner)
}

// runWithRunnerContext binds command execution to the caller's cancellation context.
func runWithRunnerContext(ctx context.Context, args []string, stdout, stderr io.Writer, runner e2eexec.Runner) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	return runWithRootRunnerContext(ctx, args, stdout, stderr, root, runner)
}

// runWithRootRunner executes from an explicit repository or package root.
func runWithRootRunner(args []string, stdout, stderr io.Writer, root string, runner e2eexec.Runner) error {
	return runWithRootRunnerContext(context.Background(), args, stdout, stderr, root, runner)
}

// runWithRootRunnerContext is the shared test hook for root, runner, and context injection.
func runWithRootRunnerContext(ctx context.Context, args []string, stdout, stderr io.Writer, root string, runner e2eexec.Runner) error {
	app := NewApp(Options{Root: root, Stdout: stdout, Stderr: stderr, Runner: runner})
	return app.RunContext(ctx, append([]string{"e2e"}, args...))
}

// NewApp builds the e2e CLI command tree.
func NewApp(opts Options) *urfavecli.App {
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.Runner == nil {
		opts.Runner = e2eexec.OSRunner{}
	}
	app := urfavecli.NewApp()
	app.Name = "e2e"
	app.Usage = "Run dcgm-exporter validation"
	app.UsageText = "e2e <command> [options]"
	app.Description = `Run dcgm-exporter validation suites or manage the Kubernetes target used by them.

Use "e2e tests --help" or "e2e cluster --help" for command options.`
	app.HideVersion = true
	app.Writer = opts.Stdout
	app.ErrWriter = opts.Stderr
	app.Action = func(c *urfavecli.Context) error {
		if c.NArg() == 0 {
			return urfavecli.ShowAppHelp(c)
		}
		return fmt.Errorf("unknown e2e command %q", c.Args().First())
	}
	app.Commands = []*urfavecli.Command{
		testsCommand(opts),
		clusterCommand(opts),
	}
	clioutput.ConfigureUsageErrors(app)
	return app
}

// testsCommand wires the parser-owned tests command to suite execution.
func testsCommand(opts Options) *urfavecli.Command {
	testOpts, envErr := testsFromEnv()
	command := newTestsCLICommand(&testOpts, true)
	parseAction := command.Action
	command.Before = func(_ *urfavecli.Context) error {
		return envErr
	}
	command.Action = func(c *urfavecli.Context) error {
		if err := parseAction(c); err != nil {
			return err
		}
		return runTests(c.Context, opts.Stdout, opts.Root, opts.Runner, testOpts)
	}
	return command
}
