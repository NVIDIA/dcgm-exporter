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
	"fmt"
	"io"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// switchListingFallbackWarning is the stderr line the probe writes when it
// finds NVSwitches from NVLink link status instead of the NVSwitch list.
const switchListingFallbackWarning = "warning: DCGM rejected the NVSwitch list request with an API version mismatch; " +
	"found NVSwitches from NVLink link status instead"

// NVSwitch discovery calls stay replaceable so the version fallback can be
// tested without a GPU.
var (
	probeEntityGroupEntities = dcgm.GetEntityGroupEntities
	probeNvLinkLinkStatus    = dcgm.GetNvLinkLinkStatus
)

// discoverSwitchIDs returns the NVSwitch entity IDs, finding them the same way
// the exporter does. When DCGM rejects the NVSwitch list request with a
// version mismatch (a DCGM library and hostengine on opposite sides of DCGM
// 4.7.1), it writes a warning to warnings and returns the switches that own
// links in NVLink link status. Other listing errors are returned unchanged.
func discoverSwitchIDs(warnings io.Writer) ([]uint, error) {
	switchIDs, err := probeEntityGroupEntities(dcgm.FE_SWITCH)
	if err == nil {
		return switchIDs, nil
	}

	if !dcgmerrors.IsVersionMismatch(err) {
		return nil, fmt.Errorf("get NVSwitch entities: %w", err)
	}

	links, linkErr := probeNvLinkLinkStatus()
	if linkErr != nil {
		return nil, fmt.Errorf("get NVSwitch entities: %w; find NVSwitches from NVLink status: %w", err, linkErr)
	}

	fmt.Fprintln(warnings, switchListingFallbackWarning)
	return dcgmprovider.SwitchIDsFromLinks(links), nil
}
