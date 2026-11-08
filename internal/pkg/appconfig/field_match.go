package appconfig

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// FieldPattern selects field names. Every place that tests a field name
// against a watch group or override item goes through it.
type FieldPattern struct {
	item     string
	regex    *regexp.Regexp
	wildcard bool
}

// ParseFieldPattern reads one item by its shape, in this order:
//  1. starts and ends with "/" and is longer than two characters: a regular
//     expression that must match the whole field name (extended only);
//  2. contains "*", "?" or "[": a path.Match wildcard;
//  3. otherwise: an exact field name.
//
// Matching is case-sensitive except where a regular expression says (?i).
// An item that starts with "@" is reserved for groups and rejected (extended
// only). Without extended, every item is a path.Match pattern, which is how
// the older watch group format reads its fields.
func ParseFieldPattern(item string, extended bool) (FieldPattern, error) {
	item = strings.TrimSpace(item)
	if item == "" {
		return FieldPattern{}, errors.New("must not be empty")
	}

	// Without extended matching every item is a path.Match pattern.
	if !extended {
		if _, err := path.Match(item, ""); err != nil {
			return FieldPattern{}, fmt.Errorf("invalid pattern %q: %w", item, err)
		}
		return FieldPattern{item: item, wildcard: true}, nil
	}

	if strings.HasPrefix(item, "@") {
		return FieldPattern{}, fmt.Errorf("%q: reserved for groups; not supported", item)
	}

	// A leading and trailing "/" marks a regular expression.
	if len(item) > 2 && strings.HasPrefix(item, "/") && strings.HasSuffix(item, "/") {
		re, err := regexp.Compile("^(?:" + item[1:len(item)-1] + ")$")
		if err != nil {
			return FieldPattern{}, fmt.Errorf("invalid regular expression %q: %w", item, err)
		}
		return FieldPattern{item: item, regex: re}, nil
	}

	// Any wildcard character makes it a path.Match pattern.
	if strings.ContainsAny(item, "*?[") {
		if _, err := path.Match(item, ""); err != nil {
			return FieldPattern{}, fmt.Errorf("invalid pattern %q: %w", item, err)
		}
		return FieldPattern{item: item, wildcard: true}, nil
	}

	return FieldPattern{item: item}, nil
}

// versionTwoFieldItem writes a version 1 watch-group item as a version 2 item
// that selects the same field names. Version 1 reads every item as a path.Match
// pattern, where a backslash escapes the next character; version 2 reads an item
// as a wildcard only when it holds "*", "?" or "[", and otherwise as an exact
// name. So an item with no unescaped wildcard character becomes its plain
// name, or an anchored regular expression when that name would be misread;
// an item with a wildcard stays as written, because version 2 matches it with
// path.Match too.
func versionTwoFieldItem(item string) string {
	item = strings.TrimSpace(item)

	var literal strings.Builder
	wildcard := false

	// Unescape the item and note whether it holds an unescaped wildcard.
	for i := 0; i < len(item); i++ {
		switch c := item[i]; c {
		case '*', '?', '[':
			wildcard = true
		case '\\':
			if i+1 < len(item) {
				i++
			}

			literal.WriteByte(item[i])
		default:
			literal.WriteByte(c)
		}
	}

	switch {
	case wildcard && !misreadByVersionTwo(item):
		return item
	case wildcard:
		// Only a name that starts with "@" or holds slashes could match, and no field has one.
		return "/(?:\\b\\B)/"
	case misreadByVersionTwo(literal.String()) || strings.ContainsAny(literal.String(), "*?["):
		return "/" + regexp.QuoteMeta(literal.String()) + "/"
	}

	return literal.String()
}

// misreadByVersionTwo reports whether version 2 would read the item as a group
// reference or a regular expression instead of a name or wildcard.
func misreadByVersionTwo(item string) bool {
	return strings.HasPrefix(item, "@") || (len(item) > 2 && strings.HasPrefix(item, "/") && strings.HasSuffix(item, "/"))
}

// Matches reports whether the pattern selects the whole field name.
func (p FieldPattern) Matches(name string) bool {
	switch {
	case p.regex != nil:
		return p.regex.MatchString(name)
	case p.wildcard:
		ok, err := path.Match(p.item, name)
		return err == nil && ok
	default:
		return p.item == name
	}
}

// IsExact reports whether the pattern is a plain field name.
func (p FieldPattern) IsExact() bool {
	return p.regex == nil && !p.wildcard
}

// String returns the trimmed pattern as it was parsed.
func (p FieldPattern) String() string {
	return p.item
}

// FieldMatcher is the parsed form of a list of items; it selects a name when
// any item does.
type FieldMatcher []FieldPattern

// Matches reports whether any item selects the field name.
func (m FieldMatcher) Matches(name string) bool {
	for _, pattern := range m {
		if pattern.Matches(name) {
			return true
		}
	}
	return false
}

// ParseFieldMatcher parses items and reports the first bad one by index.
func ParseFieldMatcher(items []string, extended bool) (FieldMatcher, error) {
	matcher := make(FieldMatcher, len(items))
	for i, item := range items {
		pattern, err := ParseFieldPattern(item, extended)
		if err != nil {
			return nil, &FieldPatternError{Index: i, Err: err}
		}
		matcher[i] = pattern
	}
	return matcher, nil
}

// FieldPatternError says which item of a list could not be read.
type FieldPatternError struct {
	Index int
	Err   error
}

// Error reports the item index followed by the underlying problem.
func (e *FieldPatternError) Error() string { return fmt.Sprintf("[%d]: %v", e.Index, e.Err) }

// Unwrap returns the underlying problem so callers can use errors.Is and errors.As.
func (e *FieldPatternError) Unwrap() error { return e.Err }

// FieldMatcher parses the group's field items. Groups from the config file
// read items with regular expressions; groups from the older format keep
// names and wildcards.
func (g WatchGroup) FieldMatcher() (FieldMatcher, error) {
	return ParseFieldMatcher(g.Fields, g.ExtendedMatch)
}

// ValidateWatchGroupPatterns checks field patterns without metric definitions
// or device discovery. Startup and inspection use it for every metric source.
func ValidateWatchGroupPatterns(watchGroups []WatchGroup) error {
	_, err := WatchGroupMatchers(watchGroups)
	return err
}

// invalidPattern is the phrase for a bad pattern of the group: a match pattern
// of an override, or a field pattern of a watch group.
func invalidPattern(watchGroup WatchGroup) string {
	if watchGroup.Path != "" {
		return "an invalid match pattern"
	}

	return "invalid field pattern"
}

// WatchGroupMatchers parses each group's field items once.
func WatchGroupMatchers(watchGroups []WatchGroup) ([]FieldMatcher, error) {
	matchers := make([]FieldMatcher, len(watchGroups))

	for i, watchGroup := range watchGroups {
		matcher, err := watchGroup.FieldMatcher()
		if err != nil {
			var itemErr *FieldPatternError
			if errors.As(err, &itemErr) {
				return nil, fmt.Errorf("%s has %s at index %d: %w", watchGroup.Describe(), invalidPattern(watchGroup), itemErr.Index, itemErr.Err)
			}

			return nil, fmt.Errorf("%s has %s: %w", watchGroup.Describe(), invalidPattern(watchGroup), err)
		}

		matchers[i] = matcher
	}

	return matchers, nil
}
