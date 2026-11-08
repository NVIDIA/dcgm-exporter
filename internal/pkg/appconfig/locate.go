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
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigError is a problem found in a config file. Path is the YAML path the
// problem is about and Line and Column say where it is in the file; each is
// empty (or zero) when it could not be worked out.
type ConfigError struct {
	Path   string
	Line   int
	Column int
	// Msg is the problem without the YAML path.
	Msg string
	Err error
}

// Error returns the underlying message unchanged.
func (e *ConfigError) Error() string { return e.Err.Error() }

// Unwrap returns the underlying error.
func (e *ConfigError) Unwrap() error { return e.Err }

// Message returns the problem without the YAML path, for callers that show
// the path as a separate field.
func (e *ConfigError) Message() string { return e.Msg }

// errorLinePattern finds a line number in a YAML syntax error, which names no key.
var errorLinePattern = regexp.MustCompile(`line ([0-9]+)`)

// locateConfigError adds the YAML path and position to err. root is nil when
// the file did not parse as YAML. A problem with a key (*keyError) is located
// by its path; a type error from the YAML decoder is located by its line; any
// other error, such as a syntax error, only gets a line number if its message
// has one.
func locateConfigError(root *yaml.Node, err error) error {
	var keyErr *keyError
	var typeErr *yaml.TypeError
	switch {
	case errors.As(err, &keyErr):
		located := &ConfigError{Err: err, Msg: strings.TrimPrefix(err.Error(), keyErr.prefix()), Path: keyErr.Path}
		switch {
		case keyErr.Line > 0:
			located.Line, located.Column = keyErr.Line, keyErr.Column
			// The position follows any key name in the message, so strip the last
			// copy; the first may sit inside a key name that contains the same text.
			needle := fmt.Sprintf(" at line %d", keyErr.Line)
			if at := strings.LastIndex(located.Msg, needle); at >= 0 {
				located.Msg = located.Msg[:at] + located.Msg[at+len(needle):]
			}
		default:
			at := keyErr.At
			if at == "" {
				at = keyErr.Path
			}

			if line, column, ok := locatePath(root, at, keyErr.AtKey); ok {
				located.Line, located.Column = line, column
			}
		}

		return located
	case errors.As(err, &typeErr):
		var all []error
		for _, entry := range typeErr.Errors {
			all = append(all, locateTypeError(root, entry))
		}

		return errors.Join(all...)
	}

	located := &ConfigError{Err: err, Msg: err.Error()}
	if match := errorLinePattern.FindStringSubmatch(err.Error()); match != nil {
		located.Line, _ = strconv.Atoi(match[1])
	}

	return located
}

// typeEntryPattern splits one yaml.TypeError entry, "line 9: cannot unmarshal ...".
var typeEntryPattern = regexp.MustCompile(`^line ([0-9]+): (.*)$`)

// locateTypeError turns one entry of a YAML type error into a located error
// with the decoder's wrapper text removed.
func locateTypeError(root *yaml.Node, entry string) error {
	match := typeEntryPattern.FindStringSubmatch(entry)
	if match == nil {
		return &ConfigError{Err: errors.New(entry), Msg: entry}
	}

	line, _ := strconv.Atoi(match[1])
	// With no key to name, the text keeps the line so startup logs still say where.
	located := &ConfigError{Line: line, Msg: match[2], Err: fmt.Errorf("line %d: %s", line, match[2])}
	if path, column, ok := pathAtLine(root, line); ok {
		located.Path, located.Column = path, column
		located.Err = errors.New(path + ": " + match[2])
	}

	return located
}

// pathAtLine finds the key that starts on line and returns its path and column.
// When several keys start on that line, as in a flow mapping, the decoder's line
// number cannot say which one is wrong, so no path is returned.
//
// Each node is visited once, so an anchor that is used again by an alias counts
// as one key and a value that contains itself ends the walk. A merge key (<<)
// adds the entries of the mappings it names to the mapping that holds it.
func pathAtLine(root *yaml.Node, line int) (path string, column int, found bool) {
	finder := lineFinder{line: line, visited: map[*yaml.Node]bool{}}
	if root != nil {
		finder.walk(root, "")
	}

	if finder.candidates != 1 {
		return "", 0, false
	}

	return finder.path, finder.column, true
}

// lineFinder collects the keys that start on one line of a YAML tree.
type lineFinder struct {
	line       int
	candidates int
	path       string
	column     int
	visited    map[*yaml.Node]bool
}

// walk visits every key under node and notes the ones that start on the line.
func (f *lineFinder) walk(node *yaml.Node, prefix string) {
	node = resolveAlias(node)
	if f.visited[node] {
		return
	}

	f.visited[node] = true
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			f.walk(child, prefix)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Tag == "!!merge" {
				f.walkMerged(value, prefix)
				continue
			}

			child := joinKey(prefix, key.Value)
			f.note(key, value, child)
			f.walk(value, child)
		}
	case yaml.SequenceNode:
		for i, item := range node.Content {
			f.walk(item, fmt.Sprintf("%s[%d]", prefix, i))
		}
	}
}

// walkMerged walks the mapping, or each mapping of a list, that a merge key
// names, under the path of the mapping that holds the merge key.
func (f *lineFinder) walkMerged(value *yaml.Node, prefix string) {
	value = resolveAlias(value)
	if value.Kind != yaml.SequenceNode {
		f.walk(value, prefix)
		return
	}

	for _, item := range value.Content {
		f.walk(item, prefix)
	}
}

// note records a key that starts on the line. A scalar value is reported at
// the value; any other node is reported at its key.
func (f *lineFinder) note(key, value *yaml.Node, path string) {
	if key.Line != f.line {
		return
	}

	f.candidates++
	f.path, f.column = path, value.Column
	if resolveAlias(value).Kind != yaml.ScalarNode {
		f.column = key.Column
	}
}

// Locate returns the line and column of a YAML path in the parsed file. When
// the path is not in the file it returns the closest enclosing key that is.
func (y *YAMLConfig) Locate(path string) (line, column int, found bool) {
	if y == nil {
		return 0, 0, false
	}

	return locatePath(y.root, path, false)
}

// pathStep is one name or index in a YAML path.
type pathStep struct {
	name  string
	index int // -1 for a name
}

// splitPath breaks "metrics[0].every" into its steps. A key whose name contains
// a dot or a bracket is written in quotes, as in outputs["a.b"].listen; joinKey
// builds such paths.
func splitPath(path string) []pathStep {
	var steps []pathStep
	for i := 0; i < len(path); {
		var step pathStep
		var width int
		var ok bool
		switch path[i] {
		case '.':
			i++
			continue
		case '[':
			step, width, ok = splitBracket(path[i:])
		default:
			end := strings.IndexAny(path[i:], ".[")
			if end < 0 {
				end = len(path) - i
			}

			step, width, ok = pathStep{name: path[i : i+end], index: -1}, end, true
		}

		if !ok {
			return nil
		}

		steps = append(steps, step)
		i += width
	}

	return steps
}

// splitBracket reads one bracketed step at the start of text, a quoted name
// such as ["a.b"] or an index such as [0], and says how many bytes it used.
func splitBracket(text string) (step pathStep, width int, ok bool) {
	if strings.HasPrefix(text, `["`) {
		quoted, err := strconv.QuotedPrefix(text[1:])
		if err != nil {
			return pathStep{}, 0, false
		}

		name, err := strconv.Unquote(quoted)
		end := 1 + len(quoted)
		if err != nil || end >= len(text) || text[end] != ']' {
			return pathStep{}, 0, false
		}

		return pathStep{name: name, index: -1}, end + 1, true
	}

	end := strings.IndexByte(text, ']')
	if end < 0 {
		return pathStep{}, 0, false
	}

	index, err := strconv.Atoi(text[1:end])
	if err != nil {
		return pathStep{}, 0, false
	}

	return pathStep{index: index}, end + 1, true
}

// joinKey appends a key to a YAML path, quoting an empty name or a name that
// contains a dot, a bracket or a quote so the path can still be split.
func joinKey(parent, name string) string {
	if name == "" || strings.ContainsAny(name, `.[]"`) {
		return parent + "[" + strconv.Quote(name) + "]"
	}

	if parent == "" {
		return name
	}

	return parent + "." + name
}

// locatePath walks root along path. A scalar value is reported at the value, so
// the position points at what is wrong; any other node is reported at its key.
// With atKey the last key is always the reported position.
func locatePath(root *yaml.Node, path string, atKey bool) (line, column int, found bool) {
	steps := splitPath(path)
	if root == nil || len(steps) == 0 {
		return 0, 0, false
	}

	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}

	for _, step := range steps {
		node = resolveAlias(node)
		var next, key *yaml.Node
		switch {
		case step.index < 0 && node.Kind == yaml.MappingNode:
			key, next = findMappingEntry(node, step.name)
		case step.index >= 0 && node.Kind == yaml.SequenceNode && step.index < len(node.Content):
			next = node.Content[step.index]
		}

		if next == nil {
			return line, column, found
		}

		found = true
		position := next
		if key != nil && (atKey || resolveAlias(next).Kind != yaml.ScalarNode) {
			position = key
		}

		line, column = position.Line, position.Column
		node = next
	}

	return line, column, found
}

// findMappingEntry returns the key and value nodes for name in a mapping. A key
// the mapping holds itself wins; otherwise the mappings its merge keys (<<) name
// are searched, so a key that exists only through a merge is still found.
func findMappingEntry(mapping *yaml.Node, name string) (key, value *yaml.Node) {
	return findMappingEntryOnce(mapping, name, map[*yaml.Node]bool{})
}

// findMappingEntryOnce is findMappingEntry with the mappings already searched,
// so a merge that names its own mapping ends the search.
func findMappingEntryOnce(mapping *yaml.Node, name string, searched map[*yaml.Node]bool) (key, value *yaml.Node) {
	if searched[mapping] {
		return nil, nil
	}

	searched[mapping] = true
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == name {
			return mapping.Content[i], mapping.Content[i+1]
		}
	}

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Tag != "!!merge" {
			continue
		}

		for _, source := range mergedMappings(mapping.Content[i+1]) {
			if key, value = findMappingEntryOnce(source, name, searched); key != nil {
				return key, value
			}
		}
	}

	return nil, nil
}

// mergedMappings returns the mappings a merge key (<<) names: the one mapping
// its value is, or each mapping of a list. Anything that is not a mapping is left out.
func mergedMappings(value *yaml.Node) []*yaml.Node {
	merged := resolveAlias(value)
	sources := []*yaml.Node{merged}
	if merged.Kind == yaml.SequenceNode {
		sources = merged.Content
	}

	var mappings []*yaml.Node
	for _, source := range sources {
		if source = resolveAlias(source); source.Kind == yaml.MappingNode {
			mappings = append(mappings, source)
		}
	}

	return mappings
}

// resolveAlias follows an alias to the node it names.
func resolveAlias(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return node.Alias
	}

	return node
}

// LookupValue follows a key path such as "metrics[0].every" through plain
// decoded values (maps, lists and scalars) and returns what it finds there.
func LookupValue(root any, path string) (any, bool) {
	steps := splitPath(path)
	if len(steps) == 0 {
		return nil, false
	}

	current := root
	for _, step := range steps {
		if step.index < 0 {
			object, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}

			if current, ok = object[step.name]; !ok {
				return nil, false
			}

			continue
		}

		list, ok := current.([]any)
		if !ok || step.index >= len(list) {
			return nil, false
		}

		current = list[step.index]
	}

	return current, true
}
