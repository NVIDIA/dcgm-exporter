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

// Package main provides the e2e validation CLI entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	e2ecli "github.com/NVIDIA/dcgm-exporter/internal/e2e/cli"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// main runs the e2e CLI and exits with the harness-specific status code.
func main() {
	os.Exit(run())
}

// run wires signal cancellation into the internal CLI command tree.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	app := e2ecli.NewApp(e2ecli.Options{
		Root:   root,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Runner: e2eexec.OSRunner{},
	})
	if err := app.RunContext(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code, ok := e2ecli.ExitCode(err); ok {
			return code
		}
		return 2
	}
	return 0
}
