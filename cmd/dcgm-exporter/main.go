/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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
	"fmt"
	"log/slog"
	"os"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/configinspect"
	"github.com/NVIDIA/dcgm-exporter/pkg/cmd"
)

var BuildVersion = "Filled by the build system"

// main runs the command and exits with the status that cmd.ExitStatus gives for
// the error it returned.
func main() {
	app := cmd.NewApp(BuildVersion)
	if err := app.Run(os.Args); err != nil {
		switch {
		case cmd.AlreadyReported(err):
		case cmd.IsUsageError(err):
			// A usage error is a plain line, like the diagnostics of the config commands.
			fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
		default:
			logStartupError(err)
		}

		os.Exit(cmd.ExitStatus(err))
	}
}

// logStartupError logs each problem that err joins as its own record. Before
// the app sets up logging, Go's default handler writes a message unchanged,
// and the message can quote what the user typed, for example a pattern that
// does not compile, so the text is escaped here.
func logStartupError(err error) {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, problem := range joined.Unwrap() {
			logStartupError(problem)
		}

		return
	}

	slog.Error(configinspect.EscapeText(err.Error())) //nolint:gosec // G706: the text is escaped
}
