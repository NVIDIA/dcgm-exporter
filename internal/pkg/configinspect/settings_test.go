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

package configinspect

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// fullConfig is a config that sets every part the version 2 document can hold.
func fullConfig() *appconfig.Config {
	config := appconfig.DefaultConfig()
	// The embedded log cannot be combined with the remote hostengine in a file.
	config.EnableDCGMLog = true
	config.XIDCountWindowSize = 600000
	config.ClockEventsCountWindowSize = 120000
	// Retention on a watch group needs both values to produce both rows.
	age := config.WatchRetention.MaxAge
	samples := int64(3)
	config.WatchGroups = []appconfig.WatchGroup{{
		Name: "temperatures", Interval: 1000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"},
		Retention: appconfig.WatchRetentionOverride{MaxAge: &age, MaxSamples: &samples},
	}}

	return config
}

// TestEverySettingPathIsAKeyOfTheDocument checks each row names a key that the
// document writes for a configuration that sets every part of it.
func TestEverySettingPathIsAKeyOfTheDocument(t *testing.T) {
	for _, view := range settingViews(fullConfig(), appconfig.ConfigNames{}) {
		if slices.Contains([]string{
			"UseOldNamespace", "NoHostname",
			"ReplaceBlanksInModelName", "UseFakeGPUs", "DisableStartupValidate",
		}, view.field) {
			assert.Empty(t, view.path, "%s has no version 2 key", view.field)
			continue
		}

		assert.NotEmpty(t, view.path, "%s must be found in the document", view.field)
		if view.field == "EnableDCGMLog" || view.field == "DCGMLogLevel" {
			assert.False(t, view.omitted, "%s is enabled, so the document must write its key", view.field)
		}
	}
}

// TestSettingViewsResolveEveryRowForADefaultConfig checks every row finds its
// key in the document of the default config. A row whose key the document does
// not write would panic here.
func TestSettingViewsResolveEveryRowForADefaultConfig(t *testing.T) {
	// The check is the panic in settingViews when a row's key is missing.
	for _, view := range settingViews(appconfig.DefaultConfig(), appconfig.ConfigNames{}) {
		assert.NotEmpty(t, view.label())
	}
}

// viewOf returns the view for field and fails the test when there is none.
func viewOf(t *testing.T, views []settingView, field string) settingView {
	t.Helper()
	for _, view := range views {
		if view.field == field {
			return view
		}
	}

	require.Failf(t, "missing view", "no view of %s", field)
	return settingView{}
}

func TestSettingViewsLeaveOutKeysTheDocumentDoesNotWrite(t *testing.T) {
	views := settingViews(appconfig.DefaultConfig(), appconfig.ConfigNames{})

	level := viewOf(t, views, "DCGMLogLevel")
	assert.True(t, level.omitted)
	assert.True(t, level.hidden, "an omitted setting nothing set has nothing to list")
	assert.Equal(t, "the embedded DCGM log is off", level.omittedBecause)
	assert.Equal(t, "none", level.value)

	window := viewOf(t, views, "XIDCountWindowSize")
	assert.True(t, window.omitted)
	assert.False(t, window.hidden, "a count window always has a value to show")
	assert.Equal(t, "5m", window.value)
	assert.Equal(t, "metrics[0].overrides[DCGM_EXP_XID_ERRORS_COUNT].window", window.label())
}

func TestSettingViewsWriteACountWindowAtItsOverrideWhenItIsNotTheDefault(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.XIDCountWindowSize = 600000
	window := viewOf(t, settingViews(config, appconfig.ConfigNames{}), "XIDCountWindowSize")
	assert.False(t, window.omitted)
	assert.Equal(t, "10m", window.value)
	assert.Equal(t, "metrics[0].overrides[0].window", window.path)
}

func TestSettingViewsSayWhatTheDocumentWritesInsteadOfAZeroPollInterval(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.GPUBindUnbindPollInterval = 0
	view := viewOf(t, settingViews(config, appconfig.ConfigNames{}), "GPUBindUnbindPollInterval")
	assert.Contains(t, view.substituted, "written as 1s")
	assert.Equal(t, "0s", view.value)
}

func TestSettingViewsShowAConfigMapSourceUnderItsFlag(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.MetricSource = appconfig.MetricSource{
		Kind: appconfig.MetricSourceConfigMap, ConfigMap: appconfig.ConfigMapMetricSource{Namespace: "ns", Name: "cm"},
	}

	view := viewOf(t, settingViews(config, appconfig.ConfigNames{}), "MetricSource")
	assert.Equal(t, "ns:cm", view.value)
	assert.Empty(t, view.path)
	assert.Equal(t, "--configmap-data", view.label())
}

func TestSettingViewsReadWatchGroupValuesFromTheDocument(t *testing.T) {
	age := appconfig.DefaultWatchRetention().MaxAge
	config := appconfig.DefaultConfig()
	config.WatchGroups = []appconfig.WatchGroup{{
		Name: "temps", Interval: 2000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"},
		Retention: appconfig.WatchRetentionOverride{MaxAge: &age},
	}}

	views := settingViews(config, appconfig.ConfigNames{})
	assert.Equal(t, "temps", viewOf(t, views, "WatchGroups[0].Name").value)
	assert.Equal(t, "2s", viewOf(t, views, "WatchGroups[0].Interval").value)
	assert.Equal(t, []any{"DCGM_FI_DEV_GPU_TEMP"}, viewOf(t, views, "WatchGroups[0].Fields").value)
	assert.Equal(t, "10m", viewOf(t, views, "WatchGroups[0].Retention.MaxAge").value)
}

func TestSettingViewsKeepAnOutputNameWithADotWhole(t *testing.T) {
	views := settingViews(appconfig.DefaultConfig(), appconfig.ConfigNames{Output: "a.b"})
	address := viewOf(t, views, "Address")
	assert.Equal(t, ":9400", address.value)
	assert.Equal(t, "outputs.a.b.listen", address.path)
}

func TestSettingLeavesSplitObjectsIntoKeys(t *testing.T) {
	leaves := settingLeaves(map[string]any{"b": 2, "a": map[string]any{"c": 3}})
	assert.Equal(t, []settingLeaf{{suffix: ".a.c", value: 3}, {suffix: ".b", value: 2}}, leaves)
	assert.Equal(t, []settingLeaf{{value: []any{"x"}}}, settingLeaves([]any{"x"}))
}

// fieldNamesIn lists the setting field names that the named source files spell
// out: the cases of a switch on a field, comparisons with a field, and the keys
// of the tables named in tables.
func fieldNamesIn(t *testing.T, files []string, tables ...string) []string {
	t.Helper()
	var names []string

	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		require.NoError(t, err)
		ast.Inspect(parsed, func(node ast.Node) bool {
			for _, expr := range fieldExprsIn(node, tables) {
				names = append(names, stringLiteralValue(t, expr)...)
			}

			return true
		})
	}

	return names
}

// stringLiteralValue returns the text of a string literal as a one-item list,
// or nothing when the expression is not a string literal.
func stringLiteralValue(t *testing.T, expr ast.Expr) []string {
	t.Helper()
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return nil
	}

	name, err := strconv.Unquote(literal.Value)
	require.NoError(t, err)

	return []string{name}
}

// isFieldReference reports whether the expression is a plain name or a selector
// called "field".
func isFieldReference(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "field"
	case *ast.SelectorExpr:
		return e.Sel.Name == "field"
	}

	return false
}

// fieldExprsIn returns the expressions of one syntax node that spell out a field
// name: the cases of a switch on a field, the value compared with a field, or the
// keys of a table named in tables.
func fieldExprsIn(node ast.Node, tables []string) []ast.Expr {
	switch n := node.(type) {
	case *ast.SwitchStmt:
		return switchCaseExprs(n)
	case *ast.BinaryExpr:
		if (n.Op == token.EQL || n.Op == token.NEQ) && isFieldReference(n.X) {
			return []ast.Expr{n.Y}
		}
	case *ast.ValueSpec:
		return tableKeyExprs(n, tables)
	}

	return nil
}

// switchCaseExprs returns every case expression of a switch whose tag is a field.
func switchCaseExprs(stmt *ast.SwitchStmt) []ast.Expr {
	if stmt.Tag == nil || !isFieldReference(stmt.Tag) {
		return nil
	}

	var exprs []ast.Expr
	for _, clause := range stmt.Body.List {
		exprs = append(exprs, clause.(*ast.CaseClause).List...)
	}

	return exprs
}

// tableKeyExprs returns the keys of a table declaration whose name is in tables.
func tableKeyExprs(spec *ast.ValueSpec, tables []string) []ast.Expr {
	if len(spec.Names) != 1 || !slices.Contains(tables, spec.Names[0].Name) || len(spec.Values) != 1 {
		return nil
	}

	var keys []ast.Expr
	for _, element := range spec.Values[0].(*ast.CompositeLit).Elts {
		keys = append(keys, element.(*ast.KeyValueExpr).Key)
	}

	return keys
}

// TestFieldNamesTheReportsSpellOutAreSettings checks that every setting field
// name written in the inspection rules is a field of the settings table, so a
// renamed setting cannot silently turn a rule off.
func TestFieldNamesTheReportsSpellOutAreSettings(t *testing.T) {
	known := map[string]bool{}
	for _, setting := range appconfig.SettingKeys(fullConfig(), appconfig.ConfigNames{}) {
		known[setting.Field] = true
	}

	names := fieldNamesIn(t, []string{"inactive.go", "settings.go", "explain.go"}, "unsetWorkloadFields", "countWindowCounters")
	require.NotEmpty(t, names)
	for _, name := range names {
		assert.True(t, known[name], "%q is not a field of appconfig.Settings", name)
	}
}
