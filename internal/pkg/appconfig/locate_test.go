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
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestParseErrorsCarryTheYAMLPathAndPosition checks that parse errors carry the YAML path and position.
func TestParseErrorsCarryTheYAMLPathAndPosition(t *testing.T) {
	const prefix = "version: 2\nmetrics:\n  - name: gpu\n    file: /a.csv\n"
	for _, tc := range []struct {
		name, yaml, path string
		line, column     int
		message          string
	}{
		{
			name: "bad value is reported at the value", path: "metrics[0].every", line: 5, column: 12,
			yaml:    prefix + "    every: 0s\noutputs: {prometheus: {type: prometheus, schema: dcgm/v1}}\n",
			message: "must be greater than 0",
		},
		{
			name: "unknown key is reported at the key", path: "server.unknown", line: 7, column: 3,
			yaml:    prefix + "outputs: {prometheus: {type: prometheus, schema: dcgm/v1}}\nserver:\n  unknown: 1\n",
			message: "unknown key",
		},
		{
			name: "wrong version", path: "version", line: 1, column: 10,
			yaml: "version: 9\n", message: "must be 1 or 2",
		},
		{
			name: "version 1 path", path: "collection.interval", line: 3, column: 13,
			yaml:    "version: 1\ncollection:\n  interval: 0s\n",
			message: "must be greater than 0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseYAMLConfig([]byte(tc.yaml))
			require.Error(t, err)
			var located *ConfigError
			require.True(t, errors.As(err, &located))
			assert.Equal(t, tc.path, located.Path)
			assert.Equal(t, tc.line, located.Line, "line")
			assert.Equal(t, tc.column, located.Column, "column")
			assert.Contains(t, located.Message(), tc.message)
			assert.NotContains(t, located.Message(), tc.path+":")
		})
	}
}

// TestParseSyntaxErrorsCarryALine checks that parse syntax errors carry a line.
func TestParseSyntaxErrorsCarryALine(t *testing.T) {
	_, err := ParseYAMLConfig([]byte("version: 2\nmetrics: [\n"))
	var located *ConfigError
	require.True(t, errors.As(err, &located))
	assert.Positive(t, located.Line)
	assert.Empty(t, located.Path)
}

// TestLocateFindsKeysAndFallsBackToTheEnclosingKey checks that locate finds keys and falls back to the
// enclosing key.
func TestLocateFindsKeysAndFallsBackToTheEnclosingKey(t *testing.T) {
	parsed, err := ParseYAMLConfig([]byte("version: 2\nserver:\n  read_timeout: 5s\n"))
	require.NoError(t, err)
	line, column, found := parsed.Locate("server.read_timeout")
	assert.True(t, found)
	assert.Equal(t, []int{3, 17}, []int{line, column})
	line, column, found = parsed.Locate("server.write_timeout")
	assert.True(t, found, "the closest enclosing key is used")
	assert.Equal(t, []int{2, 1}, []int{line, column})
	_, _, found = parsed.Locate("nothing.here")
	assert.False(t, found)
	var none *YAMLConfig
	_, _, found = none.Locate("server")
	assert.False(t, found)
}

// TestParseTypeErrorsAreLocatedAndLoseTheDecoderWrapper checks that parse type errors are located and lose the
// decoder wrapper.
func TestParseTypeErrorsAreLocatedAndLoseTheDecoderWrapper(t *testing.T) {
	_, err := ParseYAMLConfig([]byte("version: 2\nserver:\n  max_concurrent_scrapes: many\n  read_timeout: [1]\n"))
	require.Error(t, err)
	var all []*ConfigError
	for _, leaf := range flattenForTest(err) {
		var located *ConfigError
		require.True(t, errors.As(leaf, &located))
		all = append(all, located)
	}

	require.Len(t, all, 2)
	assert.Equal(t, "server.max_concurrent_scrapes", all[0].Path)
	assert.Equal(t, 3, all[0].Line)
	assert.Equal(t, 27, all[0].Column)
	assert.Contains(t, all[0].Message(), "cannot unmarshal")
	assert.NotContains(t, err.Error(), "unmarshal errors:")
	assert.Equal(t, "server.read_timeout", all[1].Path)
}

// flattenForTest splits a joined error into its parts.
func flattenForTest(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}

	return []error{err}
}

// TestReadErrorKeepsTheSystemCause checks that read error keeps the system cause.
func TestReadErrorKeepsTheSystemCause(t *testing.T) {
	_, err := LoadYAMLConfigFile(filepath.Join(t.TempDir(), "missing.yaml"))
	var read *ReadError
	require.True(t, errors.As(err, &read))
	assert.ErrorIs(t, err, fs.ErrNotExist)
	assert.Contains(t, err.Error(), "read config file")
}

// TestKeyErrorMessageHasNoPath checks that key error message has no path.
func TestKeyErrorMessageHasNoPath(t *testing.T) {
	_, err := ParseYAMLConfig([]byte("version: 1\nserver: {maxConcurrentScrapes: 0}\n"))
	var located *ConfigError
	require.True(t, errors.As(err, &located))
	assert.Equal(t, "server.maxConcurrentScrapes", located.Path)
	assert.Equal(t, "must be greater than 0", located.Message(), "a message with a space before the problem loses the path too")
}

// TestLookupValueFollowsKeysAndIndexes checks that lookup value follows keys and indexes.
func TestLookupValueFollowsKeysAndIndexes(t *testing.T) {
	document := map[string]any{"a": map[string]any{"b": []any{"x", map[string]any{"c": 5}}}}
	for path, want := range map[string]any{"a.b[0]": "x", "a.b[1].c": 5} {
		got, found := LookupValue(document, path)
		assert.True(t, found, path)
		assert.Equal(t, want, got, path)
	}

	for _, path := range []string{"a.x", "a.b[2]", "a.b[x]", "z", "a.b.c"} {
		_, found := LookupValue(document, path)
		assert.False(t, found, path)
	}
}

// TestTypeErrorOnALineWithSeveralKeysNamesNoKey checks that type error on a line with several keys names no
// key.
func TestTypeErrorOnALineWithSeveralKeysNamesNoKey(t *testing.T) {
	_, err := ParseYAMLConfig([]byte("version: 2\nserver: {max_concurrent_scrapes: many, systemd_socket: true}\n"))
	var located *ConfigError
	require.True(t, errors.As(err, &located))
	assert.Equal(t, 2, located.Line)
	assert.Empty(t, located.Path, "the decoder gives only a line, so no key can be blamed")
	assert.Contains(t, err.Error(), "line 2: cannot unmarshal", "the text keeps the line")
}

// TestMissingVersionIsReportedAtTheStartOfTheFile checks that missing version is reported at the start of the
// file.
func TestMissingVersionIsReportedAtTheStartOfTheFile(t *testing.T) {
	_, err := ParseYAMLConfig([]byte("server: {read_timeout: 1s}\n"))
	var located *ConfigError
	require.True(t, errors.As(err, &located))
	assert.Equal(t, "version", located.Path)
	assert.Equal(t, 1, located.Line)
	assert.Equal(t, 1, located.Column)
	assert.Equal(t, "required; must be 1 or 2", located.Message())
}

// TestEmptyVersionIsReportedAtItsKey checks that empty version is reported at its key.
func TestEmptyVersionIsReportedAtItsKey(t *testing.T) {
	for _, document := range []string{"server: {read_timeout: 1s}\nversion:\n", "server: {read_timeout: 1s}\nversion: ~\n"} {
		_, err := ParseYAMLConfig([]byte(document))
		var located *ConfigError
		require.True(t, errors.As(err, &located), document)
		assert.Equal(t, "version", located.Path)
		assert.Equal(t, 2, located.Line, document)
		assert.Equal(t, 1, located.Column)
		assert.Equal(t, "must be 1 or 2", located.Message())
	}
}

// TestKeyThatOnlyAMergeSuppliesIsLocatedAtItsKey checks that key that only a merge supplies is located at its
// key.
func TestKeyThatOnlyAMergeSuppliesIsLocatedAtItsKey(t *testing.T) {
	_, err := ParseYAMLConfig([]byte(`version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
    overrides:
      - &base {name: c, match: [X], every: 1s}
      - {<<: *base, match: [Y]}
` + v2Outputs))
	var located *ConfigError
	require.True(t, errors.As(err, &located))
	assert.Equal(t, "metrics[0].overrides[1].name", located.Path)
	assert.Equal(t, 6, located.Line, "the name is in the anchored mapping, not at the merge item")
	assert.Equal(t, 22, located.Column, "the value of name")
}

// TestMergeThatNamesItsOwnMappingDoesNotLoopWhenLocating checks that merge that names its own mapping does not
// loop when locating.
func TestMergeThatNamesItsOwnMappingDoesNotLoopWhenLocating(t *testing.T) {
	var root yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("a: &a\n  <<: *a\n  b: 1\n"), &root))
	_, _, found := locatePath(&root, "a.missing", false)
	assert.True(t, found, "the walk reaches a and stops at the key it cannot find")
}

// TestDuplicateKeyIsLocatedByItsNodeNotByItsText checks that duplicate key is located by its node not by its
// text.
func TestDuplicateKeyIsLocatedByItsNodeNotByItsText(t *testing.T) {
	_, err := ParseYAMLConfig([]byte("version: 2\nserver:\n  read_timeout: 1s\n  read_timeout: 2s\n"))
	var located *ConfigError
	require.True(t, errors.As(err, &located))
	assert.Equal(t, []int{4, 3}, []int{located.Line, located.Column})
	assert.Contains(t, err.Error(), "duplicate YAML key")

	_, err = ParseYAMLConfig([]byte("version: 2\nserver:\n  \"line 9: a\": 1\n  \"line 9: a\": 2\n"))
	require.True(t, errors.As(err, &located))
	assert.Equal(t, 4, located.Line, "a key whose text says line 9 must not decide the line")
}

// TestDuplicateKeyMessageNamesTheFirstDefinition checks that the message names the first definition, on the same line or an earlier one.
func TestDuplicateKeyMessageNamesTheFirstDefinition(t *testing.T) {
	_, err := ParseYAMLConfig([]byte("version: 2\nserver: {read_timeout: 1s, read_timeout: 2s}\n"))
	var located *ConfigError
	require.True(t, errors.As(err, &located))
	assert.Equal(t, 2, located.Line)
	assert.Equal(t, `duplicate YAML key "read_timeout"; first defined on line 2`, located.Message())

	_, err = ParseYAMLConfig([]byte("version: 2\nserver:\n  read_timeout: 1s\n  read_timeout: 2s\n"))
	require.True(t, errors.As(err, &located))
	assert.Equal(t, 4, located.Line)
	assert.Equal(t, `duplicate YAML key "read_timeout"; first defined on line 3`, located.Message())
}

// TestPathsWithQuotedKeysAreSplitAndLocated checks that paths with quoted keys are split and located.
func TestPathsWithQuotedKeysAreSplitAndLocated(t *testing.T) {
	parsed, err := ParseYAMLConfig([]byte("version: 2\noutputs:\n  scrape.prod: {type: prometheus, schema: dcgm/v1, listen: \":9500\"}\n"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"Address": `outputs["scrape.prod"].listen`}, pickOrigins(parsed.SettingOrigins(), "Address"))
	line, column, found := parsed.Locate(`outputs["scrape.prod"].listen`)
	assert.True(t, found)
	assert.Equal(t, []int{3, 60}, []int{line, column})
	assert.Equal(t, `outputs["a.b"]`, joinKey("outputs", "a.b"))
	assert.Equal(t, "outputs.plain", joinKey("outputs", "plain"))
	assert.Equal(t, `server[""]`, joinKey("server", ""))
	assert.Equal(t, []pathStep{{name: "server", index: -1}, {name: "", index: -1}}, splitPath(joinKey("server", "")))
	assert.Nil(t, splitPath(`outputs["unterminated`))
}

// TestTypeErrorUnderADottedKeyNamesAQuotedPath checks that type error under a dotted key names a quoted path.
func TestTypeErrorUnderADottedKeyNamesAQuotedPath(t *testing.T) {
	_, err := ParseYAMLConfig([]byte("version: 2\noutputs:\n  scrape.prod:\n    type: prometheus\n    schema: dcgm/v1\n    listen: [1]\n"))
	require.Error(t, err)
	var located *ConfigError
	require.True(t, errors.As(flattenForTest(err)[0], &located))
	assert.Equal(t, `outputs["scrape.prod"].listen`, located.Path)
}

// pickOrigins keeps only the named fields of an origin map.
func pickOrigins(origins map[string]string, fields ...string) map[string]string {
	picked := map[string]string{}
	for _, field := range fields {
		picked[field] = origins[field]
	}

	return picked
}

// TestTypeErrorLocationEndsOnAValueThatContainsItself checks that type error location ends on a value that
// contains itself.
func TestTypeErrorLocationEndsOnAValueThatContainsItself(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := ParseYAMLConfig([]byte("version: 2\nserver: {max_concurrent_scrapes: &a [*a]}\n"))
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("locating a type error did not finish")
	}
}

// TestPathAtLineCountsAnAnchoredKeyOnceAndNamesMergedKeysInPlace checks that path at line counts an anchored
// key once and names merged keys in place.
func TestPathAtLineCountsAnAnchoredKeyOnceAndNamesMergedKeysInPlace(t *testing.T) {
	parse := func(text string) *yaml.Node {
		var root yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(text), &root))
		return &root
	}

	for name, tc := range map[string]struct {
		text string
		line int
		path string
	}{
		"key reached through an alias":  {"base: &o\n  every: 1s\nuse: *o\n", 2, "base.every"},
		"merged key sits in the holder": {"server:\n  <<: &base\n    max_concurrent_scrapes: many\n", 3, "server.max_concurrent_scrapes"},
		"merged list of anchors":        {"x: &a\n  k: 1\nserver:\n  <<: [*a]\n", 2, "x.k"},
	} {
		t.Run(name, func(t *testing.T) {
			path, _, found := pathAtLine(parse(tc.text), tc.line)
			require.True(t, found)
			assert.Equal(t, tc.path, path)
		})
	}
}
