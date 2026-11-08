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

package capability

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// fieldSupportSamples is how many dmon samples each field gets. A field is
// supported when any sample on any GPU has a value, so a rate field that is
// blank on its first sample is not reported unsupported.
const fieldSupportSamples = 3

// profilingFieldPrefix starts the name of every DCGM profiling field.
const profilingFieldPrefix = "DCGM_FI_PROF_"

// DCGMField is one CSV row and the DCGM field ID the exporter reads for it.
type DCGMField struct {
	Name string
	ID   int
}

// FieldSupport is what dcgmi dmon proved about CSV rows on the GPUs it sees.
// Its JSON form is the document the suites read from E2E_DCGM_FIELD_SUPPORT.
type FieldSupport struct {
	// Supported lists rows with a value on at least one GPU.
	Supported []string `json:"supported"`
	// Unsupported maps rows DCGM refused to watch, or returned no value for on
	// every GPU during the probe, to why. No value is not proof that DCGM lacks
	// the field: event fields such as XID errors stay N/A until an event
	// happens. The suites treat these rows only as not required.
	Unsupported map[string]string `json:"unsupported"`
	// Unclassified maps rows the probe could not classify to why. They are
	// left out of the document, so the suites report them as unchecked.
	Unclassified map[string]string `json:"-"`
}

// ProbeFieldSupport watches fields with dcgmi dmon on every GPU the DCGM
// backend sees and classifies each one. A blank or N/A value on every GPU in
// every sample means unsupported (no value now, which includes event fields
// that have had no event); any other value means supported. Fields
// whose probe failed are unclassified, never supported or unsupported.
func ProbeFieldSupport(ctx context.Context, runner e2eexec.Runner, opts ProbeOptions, fields []DCGMField) FieldSupport {
	support := FieldSupport{Unsupported: map[string]string{}, Unclassified: map[string]string{}}
	if len(fields) == 0 {
		return support
	}

	if opts.DryRun {
		markUnclassified(&support, fields, "the DCGM field support probe does not run in dry-run mode")
		return support
	}

	// Profiling fields need the DCGM profiling module, so a failure to watch
	// them must not leave the device fields unclassified.
	session := newDCGMProbeSession(runner, opts)
	for _, batch := range splitProfilingFields(fields) {
		probeFieldBatch(ctx, session, batch, &support)
	}
	sort.Strings(support.Supported)
	return support
}

// WithoutProfiling moves every supported profiling (DCGM_FI_PROF_*) row to
// Unclassified with reason, so suites report it as unchecked instead of
// requiring it. Unsupported profiling rows stay as they are.
func (s *FieldSupport) WithoutProfiling(reason string) {
	kept := s.Supported[:0]
	for _, name := range s.Supported {
		if strings.HasPrefix(name, profilingFieldPrefix) {
			s.Unclassified[name] = reason
			continue
		}
		kept = append(kept, name)
	}
	s.Supported = kept
}

// splitProfilingFields returns the non-empty batches of non-profiling and
// profiling (DCGM_FI_PROF_*) fields, in that order.
func splitProfilingFields(fields []DCGMField) [][]DCGMField {
	var device, profiling []DCGMField
	for _, field := range fields {
		if strings.HasPrefix(field.Name, profilingFieldPrefix) {
			profiling = append(profiling, field)
			continue
		}
		device = append(device, field)
	}

	var batches [][]DCGMField
	for _, batch := range [][]DCGMField{device, profiling} {
		if len(batch) != 0 {
			batches = append(batches, batch)
		}
	}
	return batches
}

// probeFieldBatch runs one dmon command for fields. When DCGM refuses the
// whole batch, as it does when one field cannot be watched, each half is
// probed again, so one refusal does not hide the others and a single refused
// field costs few extra runs.
func probeFieldBatch(ctx context.Context, session *dcgmProbeSession, fields []DCGMField, support *FieldSupport) {
	ids := make([]string, 0, len(fields))
	for _, field := range fields {
		ids = append(ids, strconv.Itoa(field.ID))
	}
	command := "dcgmi dmon -e " + strings.Join(ids, ",")
	output := session.run(ctx, "dmon", "-e", strings.Join(ids, ","), "-c", strconv.Itoa(fieldSupportSamples))

	// A table means DCGM watched every field; classify each column.
	rows, err := parseDmonRows(output, len(fields))
	if err != nil {
		markUnclassified(support, fields, fmt.Sprintf("could not read %s output: %v", command, err))
		return
	}

	if len(rows) != 0 {
		classifyDmonColumns(fields, rows, command, support)
		return
	}

	// Without a table, a DCGM refusal is evidence and anything else is a probe failure.
	if !dmonReportsUnsupported(output) {
		reason := dcgmProbeUnavailableReason("field support", output) + ": " + dcgmProbeFailureDetail(output)
		markUnclassified(support, fields, reason)
		return
	}

	if len(fields) > 1 {
		half := len(fields) / 2
		probeFieldBatch(ctx, session, fields[:half], support)
		probeFieldBatch(ctx, session, fields[half:], support)
		return
	}

	support.Unsupported[fields[0].Name] = fmt.Sprintf("DCGM refused to watch field %d (%s): %s",
		fields[0].ID, command, dcgmProbeFailureDetail(output))
}

// parseDmonRows returns the values of each "GPU <n>" line of dcgmi dmon
// output, one value per field. A failed command has no rows. A GPU line with
// the wrong number of values is an error, because the columns cannot be
// matched to fields.
func parseDmonRows(output string, fieldCount int) ([][]string, error) {
	if strings.HasPrefix(strings.TrimSpace(output), "command failed:") {
		return nil, nil
	}

	var rows [][]string
	for _, line := range strings.Split(output, "\n") {
		words := strings.Fields(line)
		if len(words) < 2 || words[0] != "GPU" {
			continue
		}
		if _, err := strconv.Atoi(words[1]); err != nil {
			continue
		}

		values := words[2:]
		if len(values) != fieldCount {
			return nil, fmt.Errorf("line %q has %d values for %d fields", strings.TrimSpace(line), len(values), fieldCount)
		}
		rows = append(rows, values)
	}
	return rows, nil
}

// classifyDmonColumns marks a field supported when any GPU sample has a value
// and unsupported when every sample is blank or N/A. The reason says only
// that DCGM returned no value, because event fields are N/A until an event.
func classifyDmonColumns(fields []DCGMField, rows [][]string, command string, support *FieldSupport) {
	for column, field := range fields {
		hasValue := false
		for _, row := range rows {
			if !dmonValueMissing(row[column]) {
				hasValue = true
				break
			}
		}

		if hasValue {
			support.Supported = append(support.Supported, field.Name)
			continue
		}
		support.Unsupported[field.Name] = fmt.Sprintf(
			"DCGM returned no value (N/A) on every GPU during the probe (%s, field %d, %d samples, %d GPU sample lines)",
			command, field.ID, fieldSupportSamples, len(rows))
	}
}

// dmonValueMissing reports whether a dmon value is N/A or one of DCGM's
// blank, not-found, not-supported or not-permissioned sentinels.
func dmonValueMissing(value string) bool {
	if strings.EqualFold(value, "N/A") || strings.EqualFold(value, "blank") || dcgmSentinelEvidence(value) != "" {
		return true
	}

	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false
	}

	// The int32 and fp64 sentinel ranges; the int64 ones are matched by text above.
	const (
		int32BlankSentinel = 2147483632
		fp64BlankSentinel  = 140737488355328
		sentinelCount      = 4
	)
	inRange := func(start float64) bool {
		return number >= start && number < start+sentinelCount && number == math.Trunc(number)
	}

	return inRange(int32BlankSentinel) || inRange(fp64BlankSentinel)
}

// dmonReportsUnsupported reports whether failed dmon output is DCGM refusing
// a field, rather than a failure to run the probe at all.
func dmonReportsUnsupported(output string) bool {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "operation not supported") {
		return false
	}

	return dcgmTextualUnsupportedEvidence(output) ||
		strings.Contains(lower, "is not supported") ||
		strings.Contains(lower, "not_supported")
}

// markUnclassified records the same reason for every field.
func markUnclassified(support *FieldSupport, fields []DCGMField, reason string) {
	for _, field := range fields {
		support.Unclassified[field.Name] = reason
	}
}
