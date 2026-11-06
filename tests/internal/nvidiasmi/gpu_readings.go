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
package nvidiasmi

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Reading is one numeric nvidia-smi value. Reported is false when nvidia-smi
// printed a placeholder such as [N/A]; Raw keeps the printed text.
type Reading struct {
	Value    float64
	Reported bool
	Raw      string
}

// Readings holds the values of one readings query, by GPU UUID and then by
// nvidia-smi query field name. With nounits, memory is in MiB, temperature in
// °C, power in W and clocks in MHz.
type Readings map[string]map[string]Reading

// QueryGPUReadings runs one nvidia-smi query for the named --query-gpu fields
// of every GPU and parses it. It runs nothing when fields is empty. Readings
// go stale, so callers query again for each comparison. Without a deadline on
// ctx, the query gets 30 seconds.
func QueryGPUReadings(ctx context.Context, run Runner, fields []string) (Readings, error) {
	fields = slices.Compact(slices.Sorted(slices.Values(fields)))
	if len(fields) == 0 {
		return Readings{}, nil
	}

	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()

	args := []string{"--query-gpu=uuid," + strings.Join(fields, ","), "--format=csv,noheader,nounits"}
	output, err := run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("query GPU readings with nvidia-smi: %w", err)
	}

	readings, err := ParseGPUReadings(output, fields)
	if err != nil {
		return nil, fmt.Errorf("parse nvidia-smi GPU readings: %w", err)
	}

	return readings, nil
}

// ParseGPUReadings parses the output of a readings query for fields, in the
// order the query listed them after the leading uuid column. Placeholders are
// stored as not reported; any other non-number is an error.
func ParseGPUReadings(output []byte, fields []string) (Readings, error) {
	reader := csv.NewReader(bytes.NewReader(output))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = len(fields) + 1

	readings := Readings{}
	for line := 1; ; line++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row %d: %w", line, err)
		}

		uuid := strings.TrimSpace(record[0])
		if notReported(uuid) {
			return nil, fmt.Errorf("row %d: GPU has no UUID (%q)", line, record[0])
		}

		row := make(map[string]Reading, len(fields))
		for i, field := range fields {
			reading, err := parseReading(strings.TrimSpace(record[i+1]))
			if err != nil {
				return nil, fmt.Errorf("row %d: %s: %w", line, field, err)
			}
			row[field] = reading
		}
		readings[uuid] = row
	}

	if len(readings) == 0 {
		return nil, errors.New("nvidia-smi listed no GPUs")
	}

	return readings, nil
}

// parseReading converts one printed value into a Reading. A placeholder is
// stored as not reported; any other non-number is an error.
func parseReading(raw string) (Reading, error) {
	if notReported(raw) {
		return Reading{Raw: raw}, nil
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return Reading{}, fmt.Errorf("value %q is not a number", raw)
	}

	return Reading{Value: value, Reported: true, Raw: raw}, nil
}
