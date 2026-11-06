package appconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fieldMatchNames = []string{
	"DCGM_FI_DEV_GPU_TEMP_CELSIUS",
	"DCGM_FI_DEV_GPU_TEMP_LIMIT",
	"DCGM_FI_DEV_BOARD_POWER_WATTS",
	"DCGM_EXP_XID_ERRORS_COUNT",
}

func TestFieldPatternSelectsByShape(t *testing.T) {
	tests := []struct {
		name  string
		item  string
		exact bool
		want  []string
	}{
		{
			name: "regex", item: "/^DCGM_FI_DEV_GPU_TEMP_CELSIUS$/",
			want: []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS"},
		},
		{
			name: "wildcard", item: "DCGM_FI_DEV_GPU_TEMP_*",
			want: []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_GPU_TEMP_LIMIT"},
		},
		{
			name: "exact name", item: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", exact: true,
			want: []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS"},
		},
		{
			name: "surrounding whitespace is ignored", item: "\n DCGM_FI_DEV_GPU_TEMP_CELSIUS\t", exact: true,
			want: []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS"},
		},
		{
			name: "slashes around a star are a regex, not a wildcard", item: "/DCGM_FI_*/",
			want: nil,
		},
		{
			name: "regex with alternatives", item: "/DCGM_FI_DEV_(GPU_TEMP_.*|BOARD_POWER_WATTS)/",
			want: []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_GPU_TEMP_LIMIT", "DCGM_FI_DEV_BOARD_POWER_WATTS"},
		},
		{
			name: "regex must match the whole name", item: "/GPU_TEMP/",
			want: nil,
		},
		{
			name: "regex alternation is anchored as a whole", item: "/DCGM_EXP_XID_ERRORS_COUNT|GPU_TEMP/",
			want: []string{"DCGM_EXP_XID_ERRORS_COUNT"},
		},
		{
			name: "case-insensitive regex", item: "/(?i)dcgm_fi_dev_board_power_watts/",
			want: []string{"DCGM_FI_DEV_BOARD_POWER_WATTS"},
		},
		{name: "lower-case name matches nothing", item: "dcgm_fi_dev_gpu_temp_celsius", exact: true},
		{name: "lower-case wildcard matches nothing", item: "dcgm_fi_dev_gpu_temp_*"},
		{name: "two slashes are an exact name", item: "//", exact: true},
		{name: "regex that matches nothing", item: "/NO_SUCH_FIELD/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern, err := ParseFieldPattern(tt.item, true)
			require.NoError(t, err)
			assert.Equal(t, tt.exact, pattern.IsExact())
			var got []string
			for _, name := range fieldMatchNames {
				if pattern.Matches(name) {
					got = append(got, name)
				}
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFieldPatternRejections(t *testing.T) {
	tests := []struct {
		name, item, wantErr string
	}{
		{"empty", "", "must not be empty"},
		{"blank", "  ", "must not be empty"},
		{"invalid regex", "/DCGM_(/", "invalid regular expression"},
		{"backreference", `/(a)\1/`, "invalid regular expression"},
		{"lookahead", "/(?=a)a/", "invalid regular expression"},
		{"group reference", "@thermals", "reserved for groups; not supported"},
		{"bad wildcard", "DCGM_[", "invalid pattern"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFieldPattern(tt.item, true)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// Version 1 watch groups keep names and wildcards: slashes and "@" are
// ordinary characters in a path.Match pattern.
func TestFieldPatternWithoutExtendedKeepsWildcardMeaning(t *testing.T) {
	slashed, err := ParseFieldPattern("/DCGM_FI_*/", false)
	require.NoError(t, err)
	assert.False(t, slashed.Matches("DCGM_FI_DEV_GPU_TEMP_CELSIUS"))
	assert.True(t, slashed.Matches("/DCGM_FI_DEV_GPU_TEMP_CELSIUS/"))

	at, err := ParseFieldPattern("@thermals", false)
	require.NoError(t, err)
	assert.True(t, at.Matches("@thermals"))

	wildcard, err := ParseFieldPattern("DCGM_FI_DEV_GPU_TEMP_*", false)
	require.NoError(t, err)
	assert.True(t, wildcard.Matches("DCGM_FI_DEV_GPU_TEMP_CELSIUS"))
}

func TestFieldMatcherReportsBadItemIndex(t *testing.T) {
	_, err := ParseFieldMatcher([]string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "/(/"}, true)
	var itemErr *FieldPatternError
	require.ErrorAs(t, err, &itemErr)
	assert.Equal(t, 1, itemErr.Index)
	assert.Contains(t, err.Error(), "[1]: invalid regular expression")

	matcher, err := ParseFieldMatcher([]string{"NO_SUCH", "/DCGM_EXP_.*/"}, true)
	require.NoError(t, err)
	assert.True(t, matcher.Matches("DCGM_EXP_XID_ERRORS_COUNT"))
	assert.False(t, matcher.Matches("DCGM_FI_DEV_GPU_TEMP_CELSIUS"))
}

func TestValidateWatchGroupPatterns(t *testing.T) {
	assert.NoError(t, ValidateWatchGroupPatterns(nil))
	valid := []WatchGroup{
		{Name: "exact", Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}},
		{Name: "regex", Fields: []string{"/^DCGM_FI_DEV_.*$/"}, ExtendedMatch: true},
	}

	assert.NoError(t, ValidateWatchGroupPatterns(valid))

	err := ValidateWatchGroupPatterns([]WatchGroup{valid[0], {Name: "bad", Fields: []string{"A", "/(/"}, ExtendedMatch: true}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `watch group "bad" has invalid field pattern at index 1`)

	err = ValidateWatchGroupPatterns([]WatchGroup{{Name: "legacy", Fields: []string{"[a"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `watch group "legacy" has invalid field pattern at index 0`)
}

func TestWatchGroupMatchersKeepsTheGroupOrder(t *testing.T) {
	matchers, err := WatchGroupMatchers([]WatchGroup{
		{Name: "temps", Fields: []string{"DCGM_FI_DEV_*_TEMP"}},
		{Name: "power", Fields: []string{"DCGM_FI_DEV_POWER_USAGE"}},
	})

	require.NoError(t, err)
	require.Len(t, matchers, 2)
	assert.True(t, matchers[0].Matches("DCGM_FI_DEV_GPU_TEMP"))
	assert.False(t, matchers[0].Matches("DCGM_FI_DEV_POWER_USAGE"))
	assert.True(t, matchers[1].Matches("DCGM_FI_DEV_POWER_USAGE"))
}

// A version 1 item is a path.Match pattern. Its version 2 form must select the
// same names, including the items that version 2 would otherwise misread.
func TestVersionTwoFieldItemSelectsTheSameNames(t *testing.T) {
	names := []string{
		"DCGM_FI_DEV_GPU_TEMP", "DCGM_FI_DEV_POWER_USAGE", "DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_*",
		"DCGM_FI_DEV_FB_*USED", "DCGM_FI_DEV_POWER_?", "DCGM_FI_DEV_A[B]", "DCGM_FI_DEV_AB", "a\\b", "[x]",
		"/DCGM_FI_DEV_FB_USED/", "@DCGM_FI_DEV_FB_USED", "/DCGM_FI_X/", "//",
	}
	tests := []struct {
		item string
		want string
	}{
		{`DCGM_FI_DEV_GPU_TEMP`, `DCGM_FI_DEV_GPU_TEMP`},
		{`DCGM_FI_DEV_GPU_TEM\P`, `DCGM_FI_DEV_GPU_TEMP`},
		{`  DCGM_FI_DEV_GPU_TEMP `, `DCGM_FI_DEV_GPU_TEMP`},
		{`a\\b`, `a\b`},
		{`DCGM_FI_DEV_POWER_*`, `DCGM_FI_DEV_POWER_*`},
		{`DCGM_FI_DEV_POWER_?`, `DCGM_FI_DEV_POWER_?`},
		{`DCGM_FI_DEV_FB_[UF]SED`, `DCGM_FI_DEV_FB_[UF]SED`},
		{`DCGM_FI_DEV_\*`, `/DCGM_FI_DEV_\*/`},
		{`DCGM_FI_DEV_FB_\*USED`, `/DCGM_FI_DEV_FB_\*USED/`},
		{`DCGM_FI_DEV_FB_\*U*`, `DCGM_FI_DEV_FB_\*U*`},
		{`DCGM_FI_DEV_A\[B]`, `/DCGM_FI_DEV_A\[B\]/`},
		{`\[x]`, `/\[x\]/`},
		{`/DCGM_FI_DEV_FB_USED/`, `//DCGM_FI_DEV_FB_USED//`},
		{`@DCGM_FI_DEV_FB_USED`, `/@DCGM_FI_DEV_FB_USED/`},
		{`//`, `//`},
	}
	for _, tt := range tests {
		t.Run(tt.item, func(t *testing.T) {
			got := versionTwoFieldItem(tt.item)
			assert.Equal(t, tt.want, got)
			was, err := ParseFieldMatcher([]string{tt.item}, false)
			require.NoError(t, err)
			now, err := ParseFieldMatcher([]string{got}, true)
			require.NoError(t, err)
			for _, name := range names {
				assert.Equal(t, was.Matches(name), now.Matches(name), "field %q", name)
			}
		})
	}
}

// A wildcard that version 2 would read as a group reference or a regular
// expression can only select a name that starts with "@" or holds slashes. No
// field has such a name, so the item is written as one that selects nothing.
func TestVersionTwoFieldItemWritesAMisreadWildcardAsMatchingNothing(t *testing.T) {
	for _, item := range []string{"/DCGM_FI_*/", "@*"} {
		got := versionTwoFieldItem(item)
		assert.Equal(t, `/(?:\b\B)/`, got)
		matcher, err := ParseFieldMatcher([]string{got}, true)
		require.NoError(t, err)
		assert.False(t, matcher.Matches("DCGM_FI_DEV_GPU_TEMP"))
	}
}
