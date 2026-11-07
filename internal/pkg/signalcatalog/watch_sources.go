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

package signalcatalog

import (
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// WatchSource identifies a selected source and its hardware scope.
type WatchSource = appconfig.WatchSource

// WatchSourcesFor resolves the profile's source and the metadata needed by its GPU points.
func WatchSourcesFor(id SignalID, profile ProfileID) ([]WatchSource, error) {
	binding, err := BindingFor(id, profile)
	if err != nil {
		return nil, err
	}

	signal, _ := Lookup(id) // BindingFor already validated the immutable catalog entry.
	if binding.OmitReason != "" || binding.OutputKind == OutputAttribute || signal.Entity == EntityExporter {
		return nil, nil
	}

	source := signal.Source
	if binding.SourceOverride.Kind != "" {
		source = binding.SourceOverride
	}
	return []WatchSource{
		{Source: source, Entity: signal.Entity},
		{Source: dcgmSource("DCGM_FI_DEV_PCIE_MAX_LINK_GEN", dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_GEN), Entity: EntityGPU, Metadata: true},
		{Source: dcgmSource("DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH", dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH), Entity: EntityGPU, Metadata: true},
		{Source: dcgmSource("DCGM_FI_DEV_PCIE_LINK_GEN", dcgm.DCGM_FI_DEV_PCIE_LINK_GEN), Entity: EntityGPU, Metadata: true},
		{Source: dcgmSource("DCGM_FI_DEV_PCIE_LINK_WIDTH", dcgm.DCGM_FI_DEV_PCIE_LINK_WIDTH), Entity: EntityGPU, Metadata: true},
	}, nil
}
