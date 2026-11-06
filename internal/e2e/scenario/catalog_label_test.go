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

package scenario

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCatalogResultNamesMatchGinkgoLabels keeps direct catalog commands connected to suite specs.
func TestCatalogResultNamesMatchGinkgoLabels(t *testing.T) {
	labelsBySuite := map[Suite]map[string]struct{}{}
	for suite, testDir := range map[Suite]string{
		SuiteHost:      "tests/host",
		SuiteContainer: "tests/container",
		SuiteK8s:       "tests/k8s",
	} {
		labelsBySuite[suite] = ginkgoLabels(t, filepath.Join(catalogRepositoryRoot(t), testDir))
	}

	for _, entry := range Catalog {
		if entry.Suite == SuiteStatic {
			continue
		}
		for _, resultName := range strings.Split(entry.ResultName, ",") {
			label := strings.TrimSpace(resultName)
			if _, ok := labelsBySuite[entry.Suite][label]; !ok {
				t.Errorf("%s result name %q is not a Ginkgo label in %s", entry.Selector(), label, entry.Suite)
			}
		}
	}
}

func catalogRepositoryRoot(t *testing.T) string {
	t.Helper()

	if testSrcDir, workspace := os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"); testSrcDir != "" && workspace != "" {
		return filepath.Join(testSrcDir, workspace)
	}

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func ginkgoLabels(t *testing.T, testDir string) map[string]struct{} {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(testDir, "*_test.go"))
	if err != nil {
		t.Fatalf("find test files in %s: %v", testDir, err)
	}

	labels := make(map[string]struct{})
	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isGinkgoLabelCall(call) {
				return true
			}
			for _, arg := range call.Args {
				literal, ok := arg.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				label, err := strconv.Unquote(literal.Value)
				if err == nil {
					labels[label] = struct{}{}
				}
			}
			return true
		})
	}
	return labels
}

func isGinkgoLabelCall(call *ast.CallExpr) bool {
	identifier, ok := call.Fun.(*ast.Ident)
	return ok && identifier.Name == "Label"
}
