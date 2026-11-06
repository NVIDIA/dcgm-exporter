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

package cli

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// dcgmFieldsSource finds the go-dcgm field definitions: the copy packaged under
// etc/ in the package root or in the bundle that holds the tools directory, then
// the module cache of a source tree.
func dcgmFieldsSource(ctx context.Context, root, toolsDir string, runner e2eexec.Runner) (string, error) {
	candidates := []string{filepath.Join(root, "etc", "go-dcgm-const-fields.go")}
	if toolsDir != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(filepath.Clean(toolsDir)), "etc", "go-dcgm-const-fields.go"))
	}
	for _, packaged := range candidates {
		if info, err := os.Stat(packaged); err == nil && info.Mode().IsRegular() {
			return packaged, nil
		}
	}
	result := runner.Run(ctx, e2eexec.Command{
		Name: "go",
		Args: []string{"list", "-m", "-f", "{{.Dir}}", "github.com/NVIDIA/go-dcgm"},
		Dir:  root,
	})
	if result.ExitCode != 0 {
		return "", fmt.Errorf("locate go-dcgm field definitions: %s%s", result.Stdout, result.Stderr)
	}

	path := filepath.Join(strings.TrimSpace(string(result.Stdout)), "pkg", "dcgm", "const_fields.go")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read go-dcgm field definitions: %w", err)
	}

	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("go-dcgm field definitions must be a regular file: %s", path)
	}

	return path, nil
}

// dcgmFieldIDs reads the name-to-ID tables of go-dcgm's const_fields.go, the
// tables dcgm.GetFieldID looks names up in. A current name wins over a legacy
// name, as in GetFieldID. The e2e CLI is built without cgo, so it reads the
// source instead of importing go-dcgm. tests/cmd/e2e-dcgm-probe reads the same
// file with its own parser (fieldNames), for the constant names.
func dcgmFieldIDs(path string) (map[string]int, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse go-dcgm field definitions: %w", err)
	}

	tables := map[string]map[string]int{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			if err := readFieldIDTable(spec.(*ast.ValueSpec), tables); err != nil {
				return nil, fmt.Errorf("read go-dcgm field definitions %s: %w", path, err)
			}
		}
	}

	if len(tables["dcgmFields"]) == 0 {
		return nil, fmt.Errorf("go-dcgm field definitions %s have no dcgmFields table", path)
	}

	ids := map[string]int{}
	for _, table := range []string{"legacyDCGMFields", "dcgmFields"} {
		for name, id := range tables[table] {
			ids[name] = id
		}
	}
	return ids, nil
}

// readFieldIDTable stores the dcgmFields or legacyDCGMFields map literal of a
// var declaration in tables. Other declarations are ignored.
func readFieldIDTable(spec *ast.ValueSpec, tables map[string]map[string]int) error {
	for i, ident := range spec.Names {
		if (ident.Name != "dcgmFields" && ident.Name != "legacyDCGMFields") || i >= len(spec.Values) {
			continue
		}
		literal, ok := spec.Values[i].(*ast.CompositeLit)
		if !ok {
			return fmt.Errorf("%s is not a map literal", ident.Name)
		}

		table := make(map[string]int, len(literal.Elts))
		for _, element := range literal.Elts {
			name, id, err := fieldIDEntry(element)
			if err != nil {
				return fmt.Errorf("%s: %w", ident.Name, err)
			}
			table[name] = id
		}
		tables[ident.Name] = table
	}
	return nil
}

// fieldIDEntry reads one `"NAME": ID` entry of a field ID map literal.
func fieldIDEntry(element ast.Expr) (string, int, error) {
	pair, ok := element.(*ast.KeyValueExpr)
	if !ok {
		return "", 0, errors.New("entry is not a key-value pair")
	}

	key, keyOK := pair.Key.(*ast.BasicLit)
	value, valueOK := pair.Value.(*ast.BasicLit)
	if !keyOK || !valueOK || key.Kind != token.STRING || value.Kind != token.INT {
		return "", 0, errors.New("entry is not a string name and an integer ID")
	}

	name, err := strconv.Unquote(key.Value)
	if err != nil {
		return "", 0, fmt.Errorf("field name %s: %w", key.Value, err)
	}

	id, err := strconv.Atoi(value.Value)
	if err != nil {
		return "", 0, fmt.Errorf("field %s ID %s: %w", name, value.Value, err)
	}

	return name, id, nil
}
