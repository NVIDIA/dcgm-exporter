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

package cmd

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/configinspect"
)

// parserInputMetadataKey identifies presence observations shared by root and tail parsers.
const parserInputMetadataKey = "parser-input"

// inspectionDelimiterMetadataKey records whether -- came before PATH in a
// config command, which the leaf parser consumes before the command runs.
const inspectionDelimiterMetadataKey = "inspection-delimiter-before-path"

// The config commands.
const (
	commandShow     = "show"
	commandValidate = "validate"
	commandExplain  = "explain"
)

// inspectionCommands lists the commands that inspect settings, in help order.
var inspectionCommands = []string{commandShow, commandValidate, commandExplain}

// configureInspectionCLI registers inspection commands and application-local help.
func configureInspectionCLI(app *cli.App) {
	if app.Metadata == nil {
		app.Metadata = map[string]any{}
	}

	// Application-level settings: help name, environment check and the help flag.
	app.HelpName = "dcgm-exporter"
	app.Before = rejectBadEnvironment
	app.OnUsageError = rootOnUsageError
	app.Flags = append(app.Flags, &cli.BoolFlag{
		Name: "help", Aliases: []string{"h"}, Usage: "Show help",
		Category: "General", DisableDefaultText: true,
	})

	app.Flags, app.Metadata[parserInputMetadataKey] = observeFlags(app.Flags)

	// The config command and its show, validate and explain subcommands.
	group := &cli.Command{
		Name: "config", HideHelpCommand: true,
		Before:       rememberInspectionDelimiter,
		Usage:        "Inspect settings without starting the exporter",
		Action:       runConfigGroup,
		OnUsageError: inspectionOnUsageError,
	}

	for _, name := range inspectionCommands {
		argsUsage := "[PATH] [-o " + strings.Join(inspectionFormats(name), "|") + "]"
		if name == commandExplain {
			argsUsage += " [--verbose]"
		}

		group.Subcommands = append(group.Subcommands, &cli.Command{
			Name: name, HideHelpCommand: true, Usage: inspectionUsage(name),
			ArgsUsage: argsUsage + " [-- GLOBAL OPTIONS]",
			Flags:     inspectionFlags(name), OnUsageError: inspectionOnUsageError,
			Action: runConfigInspection,
		})
	}

	// Register the commands and the help topics.
	app.Commands = []*cli.Command{
		group,
		{
			Name: "help", Aliases: []string{"h"},
			Usage:     "Show help for a command or topic",
			ArgsUsage: "[config [show|validate|explain]]", Action: contextualHelp,
		},
	}

	configureHelp(app)
}

// inspectionOnUsageError leaves inspection diagnostics to main without usage
// output. Wrong command usage ends the run with status 2.
func inspectionOnUsageError(_ *cli.Context, err error, _ bool) error {
	if name, found := strings.CutPrefix(err.Error(), "flag provided but not defined: -"); found {
		if name == CLIConfigFile || name == "-"+CLIConfigFile {
			message := "--%s is not accepted here; give the file as PATH (for example config validate PATH) " +
				"or before the command (dcgm-exporter --%s FILE config ...)"
			return usageError(fmt.Errorf(message, CLIConfigFile, CLIConfigFile))
		}

		return usageError(fmt.Errorf("unknown option --%s", strings.TrimPrefix(name, "-")))
	}

	return usageError(err)
}

// rootOnUsageError handles a global flag that does not parse. When the command
// line goes on to run show, validate or explain, it is a usage error like any
// other in the config commands: status 2, a one-line diagnostic and an empty
// stdout. Otherwise the usage text goes to stdout and the exit status stays 1,
// as it always was for the exporter itself.
func rootOnUsageError(c *cli.Context, err error, _ bool) error {
	input, _ := c.App.Metadata[parserInputMetadataKey].(*parserInput)
	if input != nil && input.set != nil && namesInspectionLeaf(input.set.Args()) {
		return usageError(err)
	}

	fmt.Fprintf(c.App.Writer, "Incorrect Usage: %s\n\n", err.Error())
	_ = cli.ShowAppHelp(c)

	return err
}

// namesInspectionLeaf says the arguments left after a failed parse of the
// global flags, which the root flag set keeps, include the config command with show, validate or explain.
func namesInspectionLeaf(args []string) bool {
	for i := range args {
		if runsInspectionLeaf(args[i:]) {
			return true
		}
	}

	return false
}

// runConfigGroup shows group help or rejects an unknown subcommand.
func runConfigGroup(c *cli.Context) error {
	if c.NArg() != 0 {
		return usageError(fmt.Errorf("unknown config command %q; expected show, validate or explain", c.Args().First()))
	}

	return writeCommandHelp(c, c.Command)
}

// inspectionUsage describes what each command does.
func inspectionUsage(name string) string {
	switch name {
	case commandShow:
		return "Print the effective settings as a config file; does not read the files they name"
	case commandValidate:
		return "Check the settings and the local files they name; exit 1 when invalid"
	default:
		return "Explain where each setting came from and what will be collected"
	}
}

// inspectionFormats lists the output formats a command accepts, the default first.
func inspectionFormats(command string) []string {
	if command == commandShow {
		return []string{string(configinspect.FormatYAML), string(configinspect.FormatJSON)}
	}

	return []string{string(configinspect.FormatText), string(configinspect.FormatJSON)}
}

// outputFlag keeps the leaf and post-PATH parsers consistent. Format membership
// and duplicate selections are checked by inspectionOperands, not this flag.
func outputFlag(command string) *cli.StringFlag {
	formats := inspectionFormats(command)
	return &cli.StringFlag{
		Name: "output", Aliases: []string{"o"}, Usage: "Result `FORMAT`: " + strings.Join(formats, " or "),
		Value: formats[0], Category: "Result",
	}
}

// inspectionOptions separates leaf presentation from delimited launch overrides.
type inspectionOptions struct {
	path, output string
	verbose      bool
	tail         []string
}

// inspectionOperands identifies PATH and the override boundary; urfave parses
// both presentation options independently on either side of PATH.
func inspectionOperands(c *cli.Context) (inspectionOptions, error) {
	options := inspectionOptions{output: c.String("output"), verbose: c.Bool("verbose")}
	command := c.Command.Name
	args := c.Args().Slice()
	if len(args) > 0 && args[0] == "" {
		return options, fmt.Errorf("PATH must not be empty")
	}

	if len(args) > 0 && args[0] == "-" {
		return options, fmt.Errorf("PATH %q (standard input) is not supported; give a file path", "-")
	}

	delimiterBeforePath, _ := c.App.Metadata[inspectionDelimiterMetadataKey].(bool)
	if !delimiterBeforePath && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		options.path, args = args[0], args[1:]
	}

	var local []string
	if options.path == "" {
		// The leaf parser consumed --; remaining flags belong to the launch tail.
		options.tail = args
	} else if boundary := slices.Index(args, "--"); boundary >= 0 {
		local, options.tail = args[:boundary], args[boundary+1:]
	} else {
		local = args
	}

	if outputOccurrences(c) > 1 {
		return options, duplicateOutputError(command)
	}

	if err := parseInspectionLocalOptions(c, local, &options); err != nil {
		return options, err
	}

	formats := inspectionFormats(command)
	if !slices.Contains(formats, options.output) {
		return options, fmt.Errorf("invalid --output %q; expected %s", options.output, strings.Join(formats, " or "))
	}

	return options, nil
}

// duplicateOutputError is the usage error for a result format selected twice.
func duplicateOutputError(command string) error {
	return fmt.Errorf("duplicate --output; select %s once", strings.Join(inspectionFormats(command), " or "))
}

// parseInspectionLocalOptions merges only explicitly supplied post-PATH options,
// preserving a preceding output or verbose selection when the other follows PATH.
func parseInspectionLocalOptions(c *cli.Context, args []string, options *inspectionOptions) error {
	if len(args) == 0 {
		return nil
	}

	parser := &cli.App{
		Flags: inspectionFlags(c.Command.Name), HideHelp: true, HideHelpCommand: true,
		Writer: io.Discard, ErrWriter: c.App.ErrWriter, OnUsageError: inspectionOnUsageError,
	}

	parser.Action = func(parsed *cli.Context) error {
		if outputOccurrences(c)+outputOccurrences(parsed) > 1 {
			return duplicateOutputError(c.Command.Name)
		}

		if parsed.NArg() != 0 {
			return fmt.Errorf("extra operands; config %s accepts one PATH", c.Command.Name)
		}

		if parsed.IsSet("output") {
			options.output = parsed.String("output")
		}

		if parsed.IsSet("verbose") {
			options.verbose = parsed.Bool("verbose")
		}

		return nil
	}

	return parser.Run(append([]string{"inspection-output"}, args...))
}

// parseInspectionTail parses launch overrides without reapplying environment inputs.
func parseInspectionTail(c *cli.Context, args []string) (*cli.Context, error) {
	if len(args) == 0 {
		return nil, nil
	}

	// The tail is parsed by a copy of the root app that has only the exporter
	// options: no commands, and none of the selectors and actions.
	parser := NewApp()
	parser.Commands = nil
	parser.HideHelp, parser.HideHelpCommand, parser.HideVersion = true, true, true
	parser.Writer, parser.ErrWriter = io.Discard, c.App.ErrWriter
	parser.OnUsageError = inspectionOnUsageError
	parser.Flags, parser.Metadata[parserInputMetadataKey] = observeFlags(inspectionTailFlags(parser.Flags))

	var result *cli.Context
	parser.Action = func(parsed *cli.Context) error {
		if parsed.NArg() != 0 {
			return fmt.Errorf("only exporter options may follow --; unexpected %q", parsed.Args().First())
		}

		// A flag set both before the command and in the tail must use one spelling.
		root, _ := c.App.Metadata[parserInputMetadataKey].(*parserInput)
		tail, _ := parser.Metadata[parserInputMetadataKey].(*parserInput)
		for canonical, name := range tail.spelling {
			if previous, found := root.spelling[canonical]; found && previous != name {
				return fmt.Errorf("%s and %s are the same flag; use one spelling", typedFlag(name), typedFlag(previous))
			}
		}

		result = parsed
		return nil
	}

	if err := parser.Run(append([]string{"inspection-overrides"}, args...)); err != nil {
		return nil, err
	}

	return result, nil
}

// typedFlag writes a flag name the way it is typed: -n or --no-hostname.
func typedFlag(name string) string {
	if len(name) == 1 {
		return "-" + name
	}

	return "--" + name
}

// inspectionTailFlags removes selectors and actions and clears environment
// sources; tail presence must mean argv rather than reapplying the root environment.
func inspectionTailFlags(observedFlags []cli.Flag) []cli.Flag {
	var flags []cli.Flag
	for _, observed := range observedFlags {
		f := observed.(*inspectionFlag).DocGenerationFlag
		if f.Names()[0] == CLIConfigFile || f.Names()[0] == "help" || f.Names()[0] == "version" {
			continue
		}

		swapEnvVars(f, nil)
		flags = append(flags, f)
	}

	return flags
}

// sameConfigFile reports whether two config paths select the same file. It
// compares cleaned absolute paths, and also treats two existing paths as the
// same when they are the same file on disk, such as through a symlink.
func sameConfigFile(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil && absA == absB {
		return true
	}

	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)

	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

// runConfigInspection resolves the settings the way startup does and runs the
// selected command on them. It never opens DCGM, a listener or any service.
func runConfigInspection(c *cli.Context) error {
	options, err := inspectionOperands(c)
	if err != nil {
		return usageError(err)
	}

	// PATH and --config-file may both be given only when they name the same file.
	input, _ := c.App.Metadata[parserInputMetadataKey].(*parserInput)
	if options.path != "" && input.argv(CLIConfigFile) && !sameConfigFile(c.String(CLIConfigFile), options.path) {
		return usageError(errors.New("conflicting configuration files: --config-file and PATH must select the same file"))
	}

	tail, err := parseInspectionTail(c, options.tail)
	if err != nil {
		return usageError(err)
	}

	resolved, resolveErr := resolveConfig(c, options.path, tail, false)
	addContainerIgnoredWarning(resolved)
	return inspect(c, options, resolved, resolveErr)
}

// inspectionFlags shares presentation definitions between the leaf and post-PATH
// parser without exposing them as launch overrides.
func inspectionFlags(command string) []cli.Flag {
	flags := []cli.Flag{&outputSelectionFlag{StringFlag: outputFlag(command)}}
	if command == commandExplain {
		flags = append(flags, &cli.BoolFlag{
			Name: "verbose", Usage: "Show every setting and every collected field", Category: "Result",
		})
	}

	return flags
}

// outputSelectionFlag is the --output flag; it counts how many times the user
// selected a format, so a second selection can be refused.
type outputSelectionFlag struct {
	*cli.StringFlag
	// occurrences is the most times any one spelling (--output or -o) was set.
	// After parsing, urfave copies the value to the other spelling, which counts
	// once there, so the larger count is the number of times the user typed it.
	occurrences int
	perName     map[string]int
}

// Apply resets the output occurrence counter for each parser run.
func (f *outputSelectionFlag) Apply(set *flag.FlagSet) error {
	if err := f.StringFlag.Apply(set); err != nil {
		return err
	}

	f.occurrences = 0
	f.perName = map[string]int{}
	for _, name := range f.Names() {
		if value := set.Lookup(name); value != nil {
			value.Value = &outputSelectionValue{Value: value.Value, name: name, flag: f}
		}
	}

	return nil
}

// outputSelectionValue wraps the value of one spelling of --output and counts
// each time it is set.
type outputSelectionValue struct {
	flag.Value
	name string
	flag *outputSelectionFlag
}

// Set counts successful output selections before command admission.
func (v *outputSelectionValue) Set(value string) error {
	if err := v.Value.Set(value); err != nil {
		return err
	}

	v.flag.perName[v.name]++
	v.flag.occurrences = max(v.flag.occurrences, v.flag.perName[v.name])
	return nil
}

// Get preserves the wrapped flag value’s typed getter.
func (v *outputSelectionValue) Get() any {
	return v.Value.(flag.Getter).Get()
}

// outputOccurrences returns the number of explicit result-format selections.
func outputOccurrences(c *cli.Context) int {
	for _, f := range c.Command.Flags {
		if output, ok := f.(*outputSelectionFlag); ok {
			return output.occurrences
		}
	}
	return 0
}

// rememberInspectionDelimiter records whether urfave consumed -- before the
// first operand. The group still sees leaf input before parsing; it keeps only
// this boundary fact, not raw launch values.
func rememberInspectionDelimiter(c *cli.Context) error {
	beforePath := false
	args := c.Args().Tail()
	for i := 0; i < len(args); i++ {
		token := args[i]
		if token == "--" {
			beforePath = true
			break
		}

		if !strings.HasPrefix(token, "-") {
			break
		}

		name, _, assigned := strings.Cut(strings.TrimLeft(token, "-"), "=")
		if name == "output" || name == "o" {
			if !assigned {
				i++
			}

			continue
		}

		if name == "verbose" || name == "help" || name == "h" {
			continue
		}

		break
	}

	c.App.Metadata[inspectionDelimiterMetadataKey] = beforePath
	return nil
}

// runsInspectionLeaf says the arguments select show, validate or explain. Those
// commands report an unparsable environment value themselves, or show their own
// help. Help for the config group resolves nothing, so it is not one.
func runsInspectionLeaf(args []string) bool {
	return len(args) >= 2 && args[0] == "config" && slices.Contains(inspectionCommands, args[1])
}

// rejectBadEnvironment fails startup on an environment variable that its flag
// cannot parse. The error goes to stderr through the caller and nothing is
// written to stdout. A config command instead reports the problem itself, as a
// diagnostic.
func rejectBadEnvironment(c *cli.Context) error {
	input, _ := c.App.Metadata[parserInputMetadataKey].(*parserInput)
	if input == nil || len(input.envProblems) == 0 || runsInspectionLeaf(c.Args().Slice()) {
		return nil
	}

	return input.envProblems[0].Err
}

// inspect runs the config command that c names on the resolved settings and
// writes its result.
func inspect(c *cli.Context, options inspectionOptions, r *appconfig.Resolved, resolveErr error) error {
	var result configinspect.Result
	var err error
	format := configinspect.OutputFormat(options.output)
	switch c.Command.Name {
	case commandShow:
		result, err = configinspect.Show(r, resolveErr, format)
	case commandValidate:
		result, err = configinspect.Validate(r, resolveErr, format)
	case commandExplain:
		result, err = configinspect.Explain(r, resolveErr, format, options.verbose)
	default:
		return fmt.Errorf("%q is not a config inspection command", c.Command.Name)
	}

	if err != nil {
		return err
	}

	return writeResult(c, result)
}

// writeResult writes stderr and then stdout, and ends the run with the
// invalid-configuration status when the result says the configuration is invalid.
func writeResult(c *cli.Context, result configinspect.Result) error {
	if len(result.Stderr) > 0 {
		if err := writeOutput(c.App.ErrWriter, result.Stderr); err != nil {
			return err
		}
	}

	if len(result.Stdout) > 0 {
		if err := writeOutput(c.App.Writer, result.Stdout); err != nil {
			return err
		}
	}

	if result.Failure != nil {
		return reportedFailure(exitFailure, result.Failure)
	}

	return nil
}

// writeOutput writes a complete result in one call and reports a failed or short write.
func writeOutput(writer io.Writer, output []byte) error {
	written, err := writer.Write(output)
	if err == nil && written != len(output) {
		return io.ErrShortWrite
	}

	return err
}
