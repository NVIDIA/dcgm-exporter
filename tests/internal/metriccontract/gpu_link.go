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

package metriccontract

import (
	"errors"
	"fmt"
	"strings"
)

// GPULinkContract records the fields DCGM could read from one physical GPU link.
// A host test uses the field names and link identity to check exporter output.
type GPULinkContract struct {
	GPU     uint           `json:"gpu"`
	GPUUUID string         `json:"gpu_uuid"`
	Link    uint           `json:"link"`
	Fields  []GPULinkField `json:"fields"`
}

// GPULinkField identifies one numeric field with a direct DCGM sample.
type GPULinkField struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Validate rejects an empty or ambiguous direct-DCGM link contract.
func (c GPULinkContract) Validate() error {
	var errs []error
	if strings.TrimSpace(c.GPUUUID) == "" {
		errs = append(errs, errors.New("GPU UUID is empty"))
	}
	if len(c.Fields) == 0 {
		errs = append(errs, errors.New("DCGM returned no numeric fields for the GPU link"))
	}
	seenIDs := map[int]bool{}
	seenNames := map[string]bool{}
	for _, field := range c.Fields {
		if field.ID <= 0 || !strings.HasPrefix(field.Name, "DCGM_FI_") {
			errs = append(errs, fmt.Errorf("invalid DCGM field %d %q", field.ID, field.Name))
		}
		if seenIDs[field.ID] || seenNames[field.Name] {
			errs = append(errs, fmt.Errorf("duplicate DCGM field %d %q", field.ID, field.Name))
		}
		seenIDs[field.ID] = true
		seenNames[field.Name] = true
	}
	return errors.Join(errs...)
}
