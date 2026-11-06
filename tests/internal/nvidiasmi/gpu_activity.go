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
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// GPUUtilization is one row of the nvidia-smi GPU utilization query.
// UtilizationPercent is utilization.gpu in percent.
type GPUUtilization struct {
	Index              int
	UUID               string
	UtilizationPercent Reading
}

// QueryGPUUtilization runs the GPU utilization query through run and parses
// it. Without a deadline on ctx, the query gets 30 seconds.
func QueryGPUUtilization(ctx context.Context, run Runner) ([]GPUUtilization, error) {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()

	output, err := run(ctx, "--query-gpu=index,uuid,utilization.gpu", "--format=csv,noheader,nounits")
	if err != nil {
		return nil, fmt.Errorf("query GPU utilization with nvidia-smi: %w", err)
	}

	gpus, err := ParseGPUUtilization(output)
	if err != nil {
		return nil, fmt.Errorf("parse nvidia-smi GPU utilization: %w", err)
	}

	return gpus, nil
}

// ParseGPUUtilization parses index,uuid,utilization.gpu rows. It requires an
// index and a UUID on every row; the utilization may be a placeholder.
func ParseGPUUtilization(output []byte) ([]GPUUtilization, error) {
	reader := csv.NewReader(bytes.NewReader(output))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = 3

	var gpus []GPUUtilization
	for line := 1; ; line++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row %d: %w", line, err)
		}

		gpu, err := parseUtilizationRow(record)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", line, err)
		}

		gpus = append(gpus, gpu)
	}

	if len(gpus) == 0 {
		return nil, errors.New("nvidia-smi listed no GPUs")
	}

	return gpus, nil
}

// parseUtilizationRow converts one index,uuid,utilization.gpu record and
// requires an integer index and a UUID.
func parseUtilizationRow(record []string) (GPUUtilization, error) {
	index, err := strconv.Atoi(strings.TrimSpace(record[0]))
	if err != nil {
		return GPUUtilization{}, fmt.Errorf("index %q is not an integer", record[0])
	}

	uuid := strings.TrimSpace(record[1])
	if notReported(uuid) {
		return GPUUtilization{}, fmt.Errorf("GPU %d has no UUID (%q)", index, record[1])
	}

	util, err := parseReading(strings.TrimSpace(record[2]))
	if err != nil {
		return GPUUtilization{}, fmt.Errorf("utilization.gpu: %w", err)
	}

	return GPUUtilization{Index: index, UUID: uuid, UtilizationPercent: util}, nil
}

// QueryComputeAppUUIDs returns the UUIDs nvidia-smi reports for compute
// processes: GPU UUIDs, and MIG device UUIDs for processes on a MIG instance.
// Without a deadline on ctx, the query gets 30 seconds.
func QueryComputeAppUUIDs(ctx context.Context, run Runner) ([]string, error) {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()

	output, err := run(ctx, "--query-compute-apps=gpu_uuid", "--format=csv,noheader")
	if err != nil {
		return nil, fmt.Errorf("list GPU compute processes with nvidia-smi: %w", err)
	}

	return ParseComputeAppUUIDs(output), nil
}

// ParseComputeAppUUIDs reads GPU (GPU-...) and MIG device (MIG-...) UUIDs,
// one per line and each once, from --query-compute-apps=gpu_uuid output.
// Other lines, such as a "no running processes" message, are ignored.
func ParseComputeAppUUIDs(output []byte) []string {
	var uuids []string
	for _, line := range strings.Split(string(output), "\n") {
		uuid := strings.TrimSpace(line)
		if IsMIGDeviceUUID(uuid) || strings.HasPrefix(uuid, "GPU-") {
			if !slices.Contains(uuids, uuid) {
				uuids = append(uuids, uuid)
			}
		}
	}
	return uuids
}

// IsMIGDeviceUUID reports whether uuid names a MIG device rather than a GPU.
// nvidia-smi does not say which GPU a MIG device belongs to in this query.
func IsMIGDeviceUUID(uuid string) bool {
	return strings.HasPrefix(uuid, "MIG-")
}

// cudaVersionRE matches the CUDA version in the nvidia-smi summary header:
// "CUDA Version: 13.1", or "CUDA UMD Version: 13.5" from newer drivers.
var cudaVersionRE = regexp.MustCompile(`CUDA (?:UMD )?Version:\s*([0-9]+)\.([0-9]+)`)

// QueryCUDAVersion returns the major and minor CUDA version the driver
// supports, from the nvidia-smi summary. Without a deadline on ctx, the query
// gets 30 seconds.
func QueryCUDAVersion(ctx context.Context, run Runner) (int, int, error) {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()

	output, err := run(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("read the nvidia-smi summary: %w", err)
	}

	return ParseCUDAVersion(output)
}

// ParseCUDAVersion reads "CUDA Version: <major>.<minor>", or "CUDA UMD
// Version: <major>.<minor>", from the nvidia-smi summary.
func ParseCUDAVersion(output []byte) (int, int, error) {
	match := cudaVersionRE.FindSubmatch(output)
	if match == nil {
		return 0, 0, errors.New("the nvidia-smi summary names no CUDA version")
	}

	major, err := strconv.Atoi(string(match[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("CUDA major version %q: %w", match[1], err)
	}
	minor, err := strconv.Atoi(string(match[2]))
	if err != nil {
		return 0, 0, fmt.Errorf("CUDA minor version %q: %w", match[2], err)
	}

	return major, minor, nil
}
