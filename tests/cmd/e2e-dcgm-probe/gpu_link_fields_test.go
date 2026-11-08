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
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// TestFirstActiveGPULinkKeepsPhysicalIndex verifies selection retains the GPU link's hardware index.
func TestFirstActiveGPULinkKeepsPhysicalIndex(t *testing.T) {
	links := []dcgm.NvLinkStatus{
		{ParentType: dcgm.FE_SWITCH, ParentId: 0, Index: 0, State: dcgm.LS_UP},
		{ParentType: dcgm.FE_GPU, ParentId: 1, Index: 35, State: dcgm.LS_UP},
		{ParentType: dcgm.FE_GPU, ParentId: 0, Index: 17, State: dcgm.LS_UP},
		{ParentType: dcgm.FE_GPU, ParentId: 0, Index: 0, State: dcgm.LS_UP},
	}
	selected, found := firstActiveGPULink(links)
	if !found || selected.ParentId != 0 || selected.Index != 0 {
		t.Fatalf("selected link = %+v, found=%t; want GPU 0 physical link 0", selected, found)
	}
}

// TestGPULinkCandidatesIncludeGPUParentFields checks that GPU-level NVLink counters are probe candidates.
func TestGPULinkCandidatesIncludeGPUParentFields(t *testing.T) {
	names := map[dcgm.Short]string{
		dcgm.DCGM_FI_PROF_NVLINK_TX_BYTES: "DCGM_FI_PROF_NVLINK_TX_BYTES",
		dcgm.DCGM_FI_PROF_NVLINK_RX_BYTES: "DCGM_FI_PROF_NVLINK_RX_BYTES",
	}
	metadata := map[dcgm.Short]dcgm.FieldMeta{
		dcgm.DCGM_FI_PROF_NVLINK_TX_BYTES: {FieldType: byte(dcgm.DCGM_FT_INT64), EntityLevel: dcgm.FE_GPU},
		dcgm.DCGM_FI_PROF_NVLINK_RX_BYTES: {FieldType: byte(dcgm.DCGM_FT_INT64), EntityLevel: dcgm.FE_GPU},
	}
	fields, unmapped := gpuLinkCandidates(names, metadata)
	if len(unmapped) != 0 || len(fields) != 2 || fields[0].Name != names[dcgm.DCGM_FI_PROF_NVLINK_TX_BYTES] ||
		fields[1].Name != names[dcgm.DCGM_FI_PROF_NVLINK_RX_BYTES] {
		t.Fatalf("GPU-parent link fields = %+v, unmapped=%v; want both NVLink directions", fields, unmapped)
	}
}

// TestGPULinkCandidatesExcludeGlobalFields omits global counters and keeps GPU- and link-level candidates.
func TestGPULinkCandidatesExcludeGlobalFields(t *testing.T) {
	names := map[dcgm.Short]string{
		dcgm.DCGM_FI_CUDA_DRIVER_VERSION:    "DCGM_FI_CUDA_DRIVER_VERSION",
		dcgm.DCGM_FI_PROF_NVLINK_TX_BYTES:   "DCGM_FI_PROF_NVLINK_TX_BYTES",
		dcgm.DCGM_FI_DEV_NVLINK_LINK_STATUS: "DCGM_FI_DEV_NVLINK_LINK_STATUS",
	}
	metadata := map[dcgm.Short]dcgm.FieldMeta{
		dcgm.DCGM_FI_CUDA_DRIVER_VERSION:    {FieldType: byte(dcgm.DCGM_FT_INT64), EntityLevel: dcgm.FE_NONE},
		dcgm.DCGM_FI_DEV_DIAG_MEMORY_RESULT: {FieldType: byte(dcgm.DCGM_FT_INT64), EntityLevel: dcgm.FE_NONE},
		dcgm.DCGM_FI_PROF_NVLINK_TX_BYTES:   {FieldType: byte(dcgm.DCGM_FT_INT64), EntityLevel: dcgm.FE_GPU},
		dcgm.DCGM_FI_DEV_NVLINK_LINK_STATUS: {FieldType: byte(dcgm.DCGM_FT_INT64), EntityLevel: dcgm.FE_LINK},
	}

	fields, unmapped := gpuLinkCandidates(names, metadata)
	if len(unmapped) != 0 {
		t.Fatalf("global fields without names were reported as unmapped: %v", unmapped)
	}
	if len(fields) != 2 || fields[0].ID != dcgm.DCGM_FI_PROF_NVLINK_TX_BYTES ||
		fields[1].ID != dcgm.DCGM_FI_DEV_NVLINK_LINK_STATUS {
		t.Fatalf("GPU-link fields = %+v; want GPU-parent NVLink and declared link fields", fields)
	}
}
