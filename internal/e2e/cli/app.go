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
	"time"

	urfavecli "github.com/urfave/cli/v2"

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

// RunWithRunnerContext runs the e2e CLI command tree with the supplied runner.
func RunWithRunnerContext(ctx context.Context, args []string, stdout, stderr io.Writer, runner e2eexec.Runner) error {
	return runWithRunnerContext(ctx, args, stdout, stderr, runner)
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
	app.Usage = "run dcgm-exporter validation"
	app.HideHelp = true
	app.HideVersion = true
	app.SkipFlagParsing = true
	app.Writer = opts.Stdout
	app.ErrWriter = opts.Stderr
	app.Action = func(c *urfavecli.Context) error {
		args := c.Args().Slice()
		if len(args) == 0 || helpRequested(args) {
			writeRootHelp(opts.Stdout)
			return nil
		}
		return fmt.Errorf("unknown e2e command %q", args[0])
	}
	app.Commands = []*urfavecli.Command{
		testsCommand(opts),
		clusterCommand(opts),
		{
			Name:            "help",
			SkipFlagParsing: true,
			Action: func(c *urfavecli.Context) error {
				writeRootHelp(opts.Stdout)
				return nil
			},
		},
	}
	return app
}

// testsCommand wires the tests subcommand while keeping flag parsing in tests.go.
func testsCommand(opts Options) *urfavecli.Command {
	return &urfavecli.Command{
		Name:            "tests",
		Usage:           "Run e2e validation suites",
		HideHelp:        true,
		SkipFlagParsing: true,
		Action: func(c *urfavecli.Context) error {
			return runTests(c.Context, c.Args().Slice(), opts.Stdout, opts.Stderr, opts.Root, opts.Runner)
		},
	}
}

// clusterCommand wires the cluster subcommand while keeping operation parsing in cluster.go.
func clusterCommand(opts Options) *urfavecli.Command {
	return &urfavecli.Command{
		Name:            "cluster",
		Usage:           "Manage a local or external e2e Kubernetes target",
		HideHelp:        true,
		SkipFlagParsing: true,
		Action: func(c *urfavecli.Context) error {
			return runCluster(c.Context, c.Args().Slice(), opts.Stdout, opts.Stderr, opts.Root, opts.Runner)
		},
	}
}

// writeRootHelp prints the hand-authored top-level help used by humans and fixtures.
func writeRootHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: e2e <command> [options]

Commands:
  tests     Run e2e validation suites
  cluster   Manage a local or external e2e Kubernetes target
  help      Show this help

Examples:
  e2e tests --list-scenarios
  e2e tests --suite k8s --exporter-image registry.example/dcgm-exporter:dev
  e2e cluster up
  e2e cluster deploy --exporter-image registry.example/dcgm-exporter:dev

Run "e2e tests --help" or "e2e cluster --help" for command options.
`)
}

// helpRequested recognizes the help forms accepted before urfave/cli flag parsing runs.
func helpRequested(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")
}
