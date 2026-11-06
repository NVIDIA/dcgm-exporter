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

// Package clioutput keeps error rendering consistent across e2e urfave applications.
package clioutput

import (
	"errors"
	"fmt"
	"io"

	"github.com/urfave/cli/v2"
)

type renderedUsageError struct {
	err error
}

func (e renderedUsageError) Error() string { return e.err.Error() }

func (e renderedUsageError) Unwrap() error { return e.err }

func (e renderedUsageError) usageRendered() {}

// ConfigureUsageErrors preserves urfave's help output and marks parser errors as rendered.
func ConfigureUsageErrors(app *cli.App) {
	handler := func(c *cli.Context, err error, isSubcommand bool) error {
		_, _ = fmt.Fprintf(c.App.Writer, "Incorrect Usage: %s\n\n", err)
		if isSubcommand {
			_ = cli.ShowSubcommandHelp(c)
		} else {
			_ = cli.ShowAppHelp(c)
		}
		return renderedUsageError{err: err}
	}

	app.OnUsageError = handler
	var configureCommands func([]*cli.Command)
	configureCommands = func(commands []*cli.Command) {
		for _, command := range commands {
			command.OnUsageError = handler
			configureCommands(command.Subcommands)
		}
	}
	configureCommands(app.Commands)
}

// WriteError writes errors that urfave has not already included in usage output.
func WriteError(w io.Writer, err error) {
	var rendered interface{ usageRendered() }
	if !errors.As(err, &rendered) {
		fmt.Fprintln(w, err)
	}
}
