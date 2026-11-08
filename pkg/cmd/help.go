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
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/urfave/cli/v2"
)

const selectorGuide = `DEVICE SELECTORS
  Use f alone, g or i, or g+i. Add :0,2-4 to g or i for indexed selection.
  GPU: g selects GPUs; i selects MIG GPU instances and requires MIG mode.
  GPU f selects each GPU's MIG instances when present, otherwise the GPU.
  CPU: g selects CPUs; i selects CPU cores; f selects both.
  Switch: g selects switches; i selects links; f selects both.
  f cannot be combined or indexed. Device indices are checked at runtime.`

// configureHelp installs application-local templates without changing global hooks.
func configureHelp(app *cli.App) {
	app.CustomAppHelpTemplate = "{{(ExtraInfo).Help}}"
	app.ExtraInfo = func() map[string]string { return map[string]string{"Help": appHelp(app)} }
	for _, command := range app.Commands {
		configureCommandHelp(app.HelpName, command)
	}
}

// configureCommandHelp prepares command paths before urfave adds the application prefix.
func configureCommandHelp(parent string, command *cli.Command) {
	command.HelpName = command.Name
	if parent != "dcgm-exporter" {
		command.HelpName = parent + " " + command.Name
	}
	command.CustomHelpTemplate = "{{.UsageText}}"
	view := *command
	view.HelpName = parent + " " + command.Name
	command.UsageText = commandHelp(&view)
	for _, child := range command.Subcommands {
		configureCommandHelp(parent+" "+command.Name, child)
	}
}

// appHelp renders root usage, registered options, version and selector guidance.
func appHelp(app *cli.App) string {
	var b strings.Builder
	fmt.Fprintf(&b, "NAME\n  %s - %s\n\nUSAGE\n  %s [GLOBAL OPTIONS]\n  %s [GLOBAL OPTIONS] COMMAND\n\n", app.HelpName, app.Usage, app.HelpName, app.HelpName)
	writeHelpCommands(&b, app.VisibleCommands())
	writeHelpFlags(&b, app.VisibleFlags())
	if app.Version != "" {
		fmt.Fprintf(&b, "VERSION\n  %s\n", app.Version)
	}
	b.WriteString("\n" + selectorGuide + "\n")
	return b.String()
}

// commandHelp renders contextual usage and the registered command options.
func commandHelp(command *cli.Command) string {
	// Name, usage and subcommands.
	var b strings.Builder
	fmt.Fprintf(&b, "NAME\n  %s\n", command.HelpName)
	writeWrapped(&b, command.Usage, "  ")
	fmt.Fprintf(&b, "\nUSAGE\n")
	writeWrapped(&b, command.HelpName+" [OPTIONS] "+command.ArgsUsage, "  ")
	b.WriteByte('\n')
	writeHelpCommands(&b, command.Subcommands)

	// Options, with a help option added when the command has none.
	flags := append([]cli.Flag{}, command.Flags...)
	hasHelp := false
	for _, flag := range flags {
		if flag.Names()[0] == "help" {
			hasHelp = true
		}
	}

	if !hasHelp {
		flags = append(flags, &cli.BoolFlag{Name: "help", Aliases: []string{"h"}, Usage: "Show command help", Category: "General", DisableDefaultText: true})
	}

	writeHelpFlags(&b, flags)

	// The inspection commands also explain the launch overrides and the exit status.
	if slices.Contains(inspectionCommands, command.Name) {
		resultOptions := "--output goes"
		if command.Name == commandExplain {
			resultOptions = "--output and --verbose go"
		}

		writeWrapped(&b, "Exporter options after -- are applied as at startup; "+
			"--config-file, --help and --version are not allowed there. "+
			"Without PATH the file is chosen as at startup: --config-file, then "+
			"DCGM_EXPORTER_CONFIG_FILE, then no file. "+
			resultOptions+" before --, on either side of PATH; give --output only once. "+
			"Use ./-name.yaml for a dash-leading filename.", "  ")

		writeWrapped(&b, "Inspection does not prove GPU, DCGM, API or socket availability; "+
			"validate and explain list what was not checked. Exit status: 0 success, "+
			"1 any other failure, including an invalid configuration, 2 wrong command usage.", "  ")

		b.WriteString("\nEXAMPLE\n")
		example := command.HelpName + " ./exporter.yaml -- --collect-interval 1000"
		if command.Name == commandShow {
			example = command.HelpName + " ./exporter.yaml > effective.yaml"
		}

		writeWrapped(&b, example, "  ")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// writeHelpCommands lists visible commands with descriptions beneath their names.
func writeHelpCommands(b *strings.Builder, commands []*cli.Command) {
	if len(commands) == 0 {
		return
	}
	b.WriteString("COMMANDS\n")
	for _, c := range commands {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(b, "  %s\n", strings.Join(c.Names(), ", "))
		writeWrapped(b, c.Usage, "    ")
	}
	b.WriteString("\n")
}

// writeHelpFlags groups visible registered flags by their metadata categories.
func writeHelpFlags(b *strings.Builder, flags []cli.Flag) {
	groups := map[string][]cli.DocGenerationFlag{}
	for _, f := range flags {
		visible, ok := f.(cli.VisibleFlag)
		if !ok || !visible.IsVisible() {
			continue
		}
		flag := f.(cli.DocGenerationFlag)
		category := "General"
		if categorized, ok := f.(cli.CategorizableFlag); ok && categorized.GetCategory() != "" {
			category = categorized.GetCategory()
		}
		groups[category] = append(groups[category], flag)
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, category := range names {
		fmt.Fprintf(b, "%s OPTIONS\n", strings.ToUpper(category))
		for _, flag := range groups[category] {
			writeHelpFlag(b, flag)
		}
	}
}

// writeHelpFlag renders one option's spellings, description and metadata.
func writeHelpFlag(b *strings.Builder, flag cli.DocGenerationFlag) {
	value, usage := helpUsage(flag.GetUsage())
	if flag.TakesValue() && value == "" {
		value = "VALUE"
	}
	var spellings []string
	for _, name := range flag.Names() {
		prefix := "--"
		if utf8.RuneCountInString(name) == 1 {
			prefix = "-"
		}
		spellings = append(spellings, prefix+name)
	}
	spelling := strings.Join(spellings, ", ")
	if flag.TakesValue() {
		spelling += " " + value
	}
	writeWrapped(b, spelling, "  ")
	writeWrapped(b, usage, "    ")
	if defaults := flag.GetDefaultText(); defaults != "" {
		writeWrapped(b, "Default: "+defaults, "    ")
	}
	if env := flag.GetEnvVars(); len(env) > 0 {
		writeWrapped(b, "Environment: "+strings.Join(env, ", "), "    ")
	}
	if slice, ok := flag.(cli.DocGenerationSliceFlag); ok && slice.IsSliceFlag() {
		writeWrapped(b, "May be repeated; comma-separated values are accepted.", "    ")
	}
	b.WriteString("\n")
}

// writeWrapped keeps indivisible tokens intact and wraps prose to 80 columns.
func writeWrapped(b *strings.Builder, text, indent string) {
	column := utf8.RuneCountInString(indent)
	b.WriteString(indent)
	for i, word := range strings.Fields(text) {
		width := utf8.RuneCountInString(word)
		if i > 0 {
			if column+1+width > 80 {
				b.WriteString("\n" + indent)
				column = utf8.RuneCountInString(indent)
			} else {
				b.WriteByte(' ')
				column++
			}
		}
		b.WriteString(word)
		column += width
	}
	b.WriteByte('\n')
}

// contextualHelp resolves every requested topic before writing command help.
func contextualHelp(c *cli.Context) error {
	if c.NArg() == 0 {
		_, err := io.WriteString(c.App.Writer, appHelp(c.App))
		return err
	}
	commands := c.App.Commands
	var selected *cli.Command
	for _, topic := range c.Args().Slice() {
		selected = nil
		for _, command := range commands {
			if command.HasName(topic) {
				selected = command
				break
			}
		}
		if selected == nil {
			return fmt.Errorf("unknown help topic %q", topic)
		}
		commands = selected.Subcommands
	}
	return writeCommandHelp(c, selected)
}

// writeCommandHelp writes contextual help and returns writer errors.
func writeCommandHelp(c *cli.Context, command *cli.Command) error {
	_, err := io.WriteString(c.App.Writer, commandHelp(command))
	return err
}

// helpUsage extracts the library’s backtick metavar from a usage description.
func helpUsage(usage string) (string, string) {
	before, rest, found := strings.Cut(usage, "`")
	if !found {
		return "", usage
	}
	placeholder, after, found := strings.Cut(rest, "`")
	if !found {
		return "", usage
	}
	return placeholder, before + placeholder + after
}
