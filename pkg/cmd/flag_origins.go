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
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"

	"github.com/urfave/cli/v2"
)

// parserInput records parser presence, without retaining argv or environment values.
// The flag set visits only argv inputs; Flag.IsSet also includes environment inputs.
type parserInput struct {
	set      *flag.FlagSet
	spelling map[string]string
	// envProblems are environment variables whose value the flag could not
	// parse. The flag is applied without its environment variable, so parsing
	// goes on and the caller decides how to report the problem.
	envProblems []*appconfig.EnvValueError
}

// envErrorDetail picks the parser's reason out of its message, for example
// `strconv.ParseInt: parsing "abc": invalid syntax`. It is used for a flag
// type that envValueSummary does not know.
var envErrorDetail = regexp.MustCompile(`for flag [^:]+: (.*)$`)

// usageUnit finds the unit that a flag's usage text names in backquotes, such as MILLISECONDS.
var usageUnit = regexp.MustCompile("`(MILLISECONDS|HOURS)`")

// inspectionFlag wraps a root flag so the parser records which flags were set,
// and how, without changing how the flag parses.
type inspectionFlag struct {
	cli.DocGenerationFlag
	input *parserInput
}

// Apply observes each new flag set without altering typed flag parsing.
func (f *inspectionFlag) Apply(set *flag.FlagSet) error {
	if f.input.set != set {
		f.input.spelling = map[string]string{}
		f.input.envProblems = nil
	}

	f.input.set = set
	if err := f.DocGenerationFlag.Apply(set); err != nil {
		problem := f.envProblemOf(err)
		if problem == nil {
			return err
		}

		f.input.envProblems = append(f.input.envProblems, problem)
		restore := swapEnvVars(f.DocGenerationFlag, nil)
		err = f.DocGenerationFlag.Apply(set)
		restore()
		if err != nil {
			return err
		}
	}

	if f.IsSliceFlag() {
		return nil
	}

	canonical := f.Names()[0]
	for _, name := range f.Names() {
		value := set.Lookup(name)
		value.Value = &inputValue{Value: value.Value, input: f.input, canonical: canonical, name: name}
	}

	return nil
}

// envProblemOf recognizes a parse failure of one of the flag's environment variables.
func (f *inspectionFlag) envProblemOf(err error) *appconfig.EnvValueError {
	for _, name := range f.GetEnvVars() {
		if !strings.Contains(err.Error(), strconv.Quote(name)) {
			continue
		}

		summary := "invalid value for --" + f.Names()[0]
		value, _ := os.LookupEnv(name)
		if text, known := envValueSummary(f.DocGenerationFlag, value); known {
			summary = text
		} else if match := envErrorDetail.FindStringSubmatch(err.Error()); match != nil {
			summary = match[1]
		}

		return &appconfig.EnvValueError{Env: name, Flag: f.Names()[0], Summary: summary, Err: err}
	}

	return nil
}

// envValueSummary says in plain words why value is not valid for the flag's
// type. The second result is false for a flag type it does not know.
func envValueSummary(f cli.DocGenerationFlag, value string) (string, bool) {
	switch f.(type) {
	case *cli.IntFlag, *cli.Int64Flag:
		text := fmt.Sprintf("%q is not a whole number", value)
		if unit := usageUnit.FindStringSubmatch(f.GetUsage()); unit != nil {
			text += " of " + strings.ToLower(unit[1])
		}

		return text, true
	case *cli.DurationFlag:
		return fmt.Sprintf("%q is not a duration", value), true
	case *cli.BoolFlag:
		return fmt.Sprintf("%q is not true or false", value), true
	default:
		return "", false
	}
}

// swapEnvVars replaces the environment variables of a flag and returns a
// function that puts the old ones back.
func swapEnvVars(f cli.Flag, vars []string) func() {
	var previous []string
	switch typed := f.(type) {
	case *cli.StringFlag:
		previous, typed.EnvVars = typed.EnvVars, vars
	case *cli.IntFlag:
		previous, typed.EnvVars = typed.EnvVars, vars
	case *cli.Int64Flag:
		previous, typed.EnvVars = typed.EnvVars, vars
	case *cli.BoolFlag:
		previous, typed.EnvVars = typed.EnvVars, vars
	case *cli.DurationFlag:
		previous, typed.EnvVars = typed.EnvVars, vars
	case *cli.StringSliceFlag:
		previous, typed.EnvVars = typed.EnvVars, vars
	default:
		// The cases above are the flag types this application registers. A type
		// not listed keeps its environment variables, so nothing is swapped or
		// restored; add the type above when a flag of that type is added.
		return func() {}
	}

	return func() { swapEnvVars(f, previous) }
}

// IsVisible preserves the registered flag’s help visibility.
func (f *inspectionFlag) IsVisible() bool {
	return f.DocGenerationFlag.(cli.VisibleFlag).IsVisible()
}

// GetCategory preserves the registered flag’s help category.
func (f *inspectionFlag) GetCategory() string {
	return f.DocGenerationFlag.(cli.CategorizableFlag).GetCategory()
}

// IsSliceFlag preserves slice metadata and the library’s slice parser.
func (f *inspectionFlag) IsSliceFlag() bool {
	slice, ok := f.DocGenerationFlag.(cli.DocGenerationSliceFlag)
	return ok && slice.IsSliceFlag()
}

// inputValue wraps a flag's value and records the spelling the first time it is set.
type inputValue struct {
	flag.Value
	input           *parserInput
	canonical, name string
}

// Set records the first argv spelling before alias normalization.
func (v *inputValue) Set(value string) error {
	if err := v.Value.Set(value); err != nil {
		return err
	}
	// Alias normalization copies the value after parsing. The first spelling is
	// the original argv spelling, before those library-generated copies.
	if _, exists := v.input.spelling[v.canonical]; !exists {
		v.input.spelling[v.canonical] = v.name
	}
	return nil
}

// Get preserves the wrapped flag value’s typed getter.
func (v *inputValue) Get() any {
	return v.Value.(flag.Getter).Get()
}

// IsBoolFlag preserves boolean parsing without requiring a value operand.
func (v *inputValue) IsBoolFlag() bool {
	b, ok := v.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// Count preserves the library’s repeated-flag count.
func (v *inputValue) Count() int {
	c, ok := v.Value.(cli.Countable)
	if ok {
		return c.Count()
	}
	return 0
}

// argv checks argv presence separately from environment presence.
func (p *parserInput) argv(name string) bool {
	if p == nil || p.set == nil {
		return false
	}
	found := false
	p.set.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// origin identifies the winning input by presence rather than value equality.
func (p *parserInput) origin(f cli.DocGenerationFlag) appconfig.SettingOrigin {
	if p.argv(f.Names()[0]) {
		return appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--" + f.Names()[0]}
	}

	for _, name := range f.GetEnvVars() {
		if _, present := os.LookupEnv(name); present {
			return appconfig.SettingOrigin{Kind: appconfig.OriginEnv, Name: name}
		}
	}

	return appconfig.SettingOrigin{Kind: appconfig.OriginDefault}
}

// observeFlags adds per-application input observation to registered flags.
func observeFlags(flags []cli.Flag) ([]cli.Flag, *parserInput) {
	input := &parserInput{spelling: map[string]string{}}
	result := make([]cli.Flag, len(flags))
	for i, f := range flags {
		result[i] = &inspectionFlag{DocGenerationFlag: f.(cli.DocGenerationFlag), input: input}
	}
	return result, input
}
