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

package appconfig

import (
	"fmt"
	"regexp"
)

// keyError is a problem with one key of a config file. It names the YAML path
// itself, so locating it in the file never depends on its message text.
type keyError struct {
	// Path is the YAML path of the key, such as "metrics[0].every".
	Path string
	// Msg is what is wrong, without the path.
	Msg string
	// AtKey says the position to report is the key, not its value.
	AtKey bool
	// Line and Column are the position of the key when the reader already knows
	// it. They replace looking the path up, which cannot work for a key whose
	// name contains a dot or a bracket.
	Line, Column int
	// At is the path to report the position of, when it is not Path itself.
	At string
	// Cause is the error that Msg was taken from, if any.
	Cause error
	// space joins Path and Msg with a space instead of ": ".
	space bool
}

// Error writes the path and the problem, for example "server.read_timeout: must be greater than 0".
func (e *keyError) Error() string { return e.prefix() + e.Msg }

// prefix is the start of Error that comes before the message.
func (e *keyError) prefix() string {
	if e.Path == "" {
		return ""
	}

	if e.space {
		return e.Path + " "
	}

	return e.Path + ": "
}

// Unwrap returns the error the message came from.
func (e *keyError) Unwrap() error { return e.Cause }

// keyErrorf reports a problem with the key at path.
func keyErrorf(path, format string, args ...any) error {
	return &keyError{Path: path, Msg: fmt.Sprintf(format, args...)}
}

// keyErrorSpacef is keyErrorf for messages that read "path message" with no colon.
func keyErrorSpacef(path, format string, args ...any) error {
	return &keyError{Path: path, Msg: fmt.Sprintf(format, args...), space: true}
}

// keyWrap reports cause as a problem with the key at path.
func keyWrap(path string, cause error) error {
	return &keyError{Path: path, Msg: friendlyMessage(cause), Cause: cause}
}

// durationParseError matches the text of the errors that time.ParseDuration returns.
var durationParseError = regexp.MustCompile(`^time: (?:invalid|missing unit in|unknown unit "[^"]*" in) duration (".*")$`)

// friendlyMessage is the text of err, with the Go text of a duration that did
// not parse replaced by one that says what to write.
func friendlyMessage(err error) string {
	if match := durationParseError.FindStringSubmatch(err.Error()); match != nil {
		return fmt.Sprintf("invalid duration %s (use a number with a unit, such as 30s)", match[1])
	}

	return err.Error()
}
