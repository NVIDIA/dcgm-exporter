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

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
)

const fieldMapPath = "pkg/dcgm/const_fields.go"

// fieldMaps holds the generated current and legacy DCGM field-name maps from go-dcgm.
type fieldMaps struct {
	fields map[string]int
	legacy map[string]int
}

// readFieldMaps parses go-dcgm's generated field map file from a module directory.
func readFieldMaps(moduleDir string) (fieldMaps, error) {
	path := filepath.Join(moduleDir, fieldMapPath)

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return fieldMaps{}, fmt.Errorf("parse %s: %w", path, err)
	}

	fields, err := readMapLiteral(file, "dcgmFields")
	if err != nil {
		return fieldMaps{}, err
	}
	legacy, err := readMapLiteral(file, "legacyDCGMFields")
	if err != nil {
		return fieldMaps{}, err
	}

	return fieldMaps{fields: fields, legacy: legacy}, nil
}

// combined returns one lookup table containing both canonical and legacy field names.
func (fm fieldMaps) combined() (map[string]int, error) {
	combined := make(map[string]int, len(fm.fields)+len(fm.legacy))
	for name, id := range fm.fields {
		combined[name] = id
	}
	for name, id := range fm.legacy {
		if existingID, ok := combined[name]; ok && existingID != id {
			return nil, fmt.Errorf(
				"conflicting field ID for %q: canonical=%d legacy=%d",
				name,
				existingID,
				id,
			)
		}
		combined[name] = id
	}
	return combined, nil
}

// readMapLiteral extracts a string-to-integer map literal from a parsed Go file.
func readMapLiteral(file *ast.File, name string) (map[string]int, error) {
	var literal *ast.CompositeLit

	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.VAR {
			continue
		}

		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			for i, ident := range valueSpec.Names {
				if ident.Name != name {
					continue
				}
				if i >= len(valueSpec.Values) {
					return nil, fmt.Errorf("%s has no value", name)
				}
				var ok bool
				literal, ok = valueSpec.Values[i].(*ast.CompositeLit)
				if !ok {
					return nil, fmt.Errorf("%s is not a map literal", name)
				}
				break
			}
		}
	}

	if literal == nil {
		return nil, fmt.Errorf("%s map not found", name)
	}

	entries := make(map[string]int, len(literal.Elts))
	for _, elt := range literal.Elts {
		keyValue, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return nil, fmt.Errorf("%s contains a non key-value entry", name)
		}

		key, err := stringLiteral(keyValue.Key)
		if err != nil {
			return nil, fmt.Errorf("%s key: %w", name, err)
		}
		value, err := intLiteral(keyValue.Value)
		if err != nil {
			return nil, fmt.Errorf("%s[%q]: %w", name, key, err)
		}
		if _, exists := entries[key]; exists {
			return nil, fmt.Errorf("%s contains duplicate key %q", name, key)
		}

		entries[key] = value
	}

	return entries, nil
}

// stringLiteral returns the unquoted value of a Go string literal expression.
func stringLiteral(expr ast.Expr) (string, error) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", fmt.Errorf("expected string literal")
	}

	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", err
	}

	return value, nil
}

// intLiteral returns the integer value of a Go integer literal expression.
func intLiteral(expr ast.Expr) (int, error) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, fmt.Errorf("expected integer literal")
	}

	value, err := strconv.ParseInt(lit.Value, 0, 0)
	if err != nil {
		return 0, err
	}

	return int(value), nil
}

// fieldChange describes one added, removed, or changed field-name mapping.
type fieldChange struct {
	Name    string `json:"name"`
	Current *int   `json:"current,omitempty"`
	Target  *int   `json:"target,omitempty"`
}

// fieldDiff summarizes the current-to-target go-dcgm field-map comparison.
type fieldDiff struct {
	CurrentVersion string        `json:"currentVersion"`
	TargetVersion  string        `json:"targetVersion"`
	CurrentCount   int           `json:"currentCount"`
	TargetCount    int           `json:"targetCount"`
	Added          []fieldChange `json:"added"`
	Removed        []fieldChange `json:"removed"`
	IDChanges      []fieldChange `json:"idChanges"`
}

// diffFields compares two field maps and reports added, removed, and ID-changed names.
func diffFields(currentVersion, targetVersion string, current, target map[string]int) fieldDiff {
	diff := fieldDiff{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CurrentCount:   len(current),
		TargetCount:    len(target),
		Added:          []fieldChange{},
		Removed:        []fieldChange{},
		IDChanges:      []fieldChange{},
	}

	for name, currentID := range current {
		targetID, ok := target[name]
		if !ok {
			diff.Removed = append(diff.Removed, removedField(name, currentID))
			continue
		}
		if currentID != targetID {
			diff.IDChanges = append(diff.IDChanges, changedField(name, currentID, targetID))
		}
	}

	for name, targetID := range target {
		if _, ok := current[name]; !ok {
			diff.Added = append(diff.Added, addedField(name, targetID))
		}
	}

	sortFieldChanges(diff.Added)
	sortFieldChanges(diff.Removed)
	sortFieldChanges(diff.IDChanges)

	return diff
}

// addedField creates an added-field diff entry.
func addedField(name string, target int) fieldChange {
	return fieldChange{Name: name, Target: intPtr(target)}
}

// removedField creates a removed-field diff entry.
func removedField(name string, current int) fieldChange {
	return fieldChange{Name: name, Current: intPtr(current)}
}

// changedField creates an ID-changed field diff entry.
func changedField(name string, current, target int) fieldChange {
	return fieldChange{Name: name, Current: intPtr(current), Target: intPtr(target)}
}

// intPtr returns a pointer to value so JSON can distinguish zero from absent.
func intPtr(value int) *int {
	return &value
}

// sortFieldChanges orders diff entries by field name for stable output.
func sortFieldChanges(changes []fieldChange) {
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Name < changes[j].Name
	})
}

// hasBlockedChanges reports whether the diff should stop a bump from proceeding.
func (diff fieldDiff) hasBlockedChanges(allowRemovals bool) bool {
	if len(diff.IDChanges) > 0 {
		return true
	}

	return !allowRemovals && len(diff.Removed) > 0
}
