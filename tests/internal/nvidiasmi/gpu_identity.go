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
	"strconv"
	"strings"
	"time"
)

// QueryTimeout bounds one nvidia-smi query when the caller's context has no
// deadline of its own.
const QueryTimeout = 30 * time.Second

// GPU is the identity of one GPU as nvidia-smi lists it. These values do not
// change while a test runs, so Inventory reads them once.
type GPU struct {
	Index         int
	UUID          string
	Name          string
	PCIBusID      string
	DriverVersion string
	MIGEnabled    bool
}

// queryField is one --query-gpu column and how its text fills a GPU.
type queryField struct {
	name  string
	parse func(gpu *GPU, raw string) error
}

// gpuQueryFields lists the identity columns in output order. The query and the
// parser both read this table, so the column order cannot drift.
var gpuQueryFields = []queryField{
	{"index", parseIndex},
	{"uuid", textField(func(g *GPU) *string { return &g.UUID })},
	{"name", textField(func(g *GPU) *string { return &g.Name })},
	{"pci.bus_id", textField(func(g *GPU) *string { return &g.PCIBusID })},
	{"driver_version", textField(func(g *GPU) *string { return &g.DriverVersion })},
	{"mig.mode.current", parseMIGMode},
}

// gpuQueryArgs returns the nvidia-smi arguments for the GPU inventory query.
func gpuQueryArgs() []string {
	names := make([]string, 0, len(gpuQueryFields))
	for _, field := range gpuQueryFields {
		names = append(names, field.name)
	}

	return []string{"--query-gpu=" + strings.Join(names, ","), "--format=csv,noheader,nounits"}
}

// QueryGPUIdentity runs the GPU identity query through run and parses it.
// Without a deadline on ctx, the query gets 30 seconds.
func QueryGPUIdentity(ctx context.Context, run Runner) ([]GPU, error) {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()

	output, err := run(ctx, gpuQueryArgs()...)
	if err != nil {
		return nil, fmt.Errorf("query GPU identity with nvidia-smi: %w", err)
	}

	gpus, err := ParseGPUIdentity(output)
	if err != nil {
		return nil, fmt.Errorf("parse nvidia-smi GPU identity: %w", err)
	}

	return gpus, nil
}

// withQueryTimeout gives ctx the query timeout when it has no deadline of its
// own. The caller must call the returned cancel function.
func withQueryTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}

	return context.WithTimeout(ctx, QueryTimeout)
}

// ParseGPUIdentity parses the output of the GPU identity query. It requires
// an index and a UUID on every row; other fields may be placeholders.
func ParseGPUIdentity(output []byte) ([]GPU, error) {
	reader := csv.NewReader(bytes.NewReader(output))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = len(gpuQueryFields)

	var gpus []GPU
	seen := map[string]bool{}
	for line := 1; ; line++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row %d: %w", line, err)
		}

		gpu, err := parseGPURow(record)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", line, err)
		}
		if seen[gpu.UUID] {
			return nil, fmt.Errorf("row %d: GPU UUID %s is listed twice", line, gpu.UUID)
		}

		seen[gpu.UUID] = true
		gpus = append(gpus, gpu)
	}

	if len(gpus) == 0 {
		return nil, errors.New("nvidia-smi listed no GPUs")
	}

	return gpus, nil
}

// parseGPURow converts one CSV record in gpuQueryFields order into a GPU and
// requires a UUID.
func parseGPURow(record []string) (GPU, error) {
	var gpu GPU
	for i, field := range gpuQueryFields {
		if err := field.parse(&gpu, strings.TrimSpace(record[i])); err != nil {
			return GPU{}, fmt.Errorf("%s: %w", field.name, err)
		}
	}

	if gpu.UUID == "" {
		return GPU{}, fmt.Errorf("GPU %d has no UUID (%q)", gpu.Index, record[1])
	}

	return gpu, nil
}

// parseIndex reads the GPU index, which must be an integer.
func parseIndex(gpu *GPU, raw string) error {
	index, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("index %q is not an integer", raw)
	}

	gpu.Index = index
	return nil
}

// parseMIGMode reads mig.mode.current. A placeholder means the GPU has no MIG
// support, so MIG is off.
func parseMIGMode(gpu *GPU, raw string) error {
	switch {
	case notReported(raw), strings.EqualFold(raw, "Disabled"):
		gpu.MIGEnabled = false
	case strings.EqualFold(raw, "Enabled"):
		gpu.MIGEnabled = true
	default:
		return fmt.Errorf("MIG mode %q is neither Enabled nor Disabled", raw)
	}

	return nil
}

// textField returns a parser that stores raw in the chosen string, or "" for a
// placeholder.
func textField(target func(*GPU) *string) func(*GPU, string) error {
	return func(gpu *GPU, raw string) error {
		if !notReported(raw) {
			*target(gpu) = raw
		}
		return nil
	}
}

// notReported reports whether raw is a placeholder nvidia-smi prints instead
// of a value: empty, N/A, Not Supported, or any bracketed text such as
// [Unknown Error] or [GPU requires reset].
func notReported(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		return true
	}

	switch strings.ToLower(raw) {
	case "", "n/a", "not supported":
		return true
	default:
		return false
	}
}
