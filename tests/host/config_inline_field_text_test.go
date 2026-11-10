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

package host

import (
	"fmt"
	"strings"
	"testing"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
)

const (
	// inlineFieldType is the Prometheus type the inline field sets. The
	// exporter's own counter file gives this field a gauge, so a file that has
	// no effect shows as a different type.
	inlineFieldType = "counter"
	// inlineFieldHelp is the help text the inline field sets. It differs from
	// the counter file's text for the same field.
	inlineFieldHelp = "Inline field help from the config file."
)

// inlineFieldTextProblems lists how family name differs from the wanted type
// (inlineFieldType) and help text (inlineFieldHelp). A missing family is a
// problem.
func inlineFieldTextProblems(families map[string]*io_prometheus_client.MetricFamily, name string) []string {
	family, ok := families[name]
	if !ok {
		return []string{fmt.Sprintf("%s is not served", name)}
	}

	var problems []string
	wantType := io_prometheus_client.MetricType(io_prometheus_client.MetricType_value[strings.ToUpper(inlineFieldType)])
	if family.GetType() != wantType {
		problems = append(problems, fmt.Sprintf("%s # TYPE is %s, want %s", name, family.GetType(), wantType))
	}

	if family.GetHelp() != inlineFieldHelp {
		problems = append(problems, fmt.Sprintf("%s # HELP is %q, want %q", name, family.GetHelp(), inlineFieldHelp))
	}
	return problems
}

// inlineFieldExposition builds scrape text for one GPU sample of name with the
// given type and help text.
func inlineFieldExposition(name, promType, help string) string {
	return fmt.Sprintf("# HELP %[1]s %[3]s\n# TYPE %[1]s %[2]s\n%[1]s{gpu=\"0\"} 1\n", name, promType, help)
}

// TestInlineFieldTextProblems checks the comparison fails on a different type
// or help text and passes on the configured ones.
func TestInlineFieldTextProblems(t *testing.T) {
	const family = "DCGM_FI_DEV_GPU_TEMP_CELSIUS"
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"as configured", inlineFieldExposition(family, inlineFieldType, inlineFieldHelp), nil},
		{
			"type ignored", inlineFieldExposition(family, "gauge", inlineFieldHelp),
			[]string{family + " # TYPE is GAUGE, want COUNTER"},
		},
		{
			"help ignored", inlineFieldExposition(family, inlineFieldType, "GPU temperature."),
			[]string{family + ` # HELP is "GPU temperature.", want "` + inlineFieldHelp + `"`},
		},
		{"family missing", inlineFieldExposition("OTHER", "gauge", "Other."), []string{family + " is not served"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, inlineFieldTextProblems(mustParseFamilies(t, tt.text), family))
		})
	}
}
