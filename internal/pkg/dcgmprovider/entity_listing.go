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

package dcgmprovider

import (
	"fmt"
	"log/slog"
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
)

// DCGM version fallback for NVSwitch listing.
//
// DCGM 4.7.1 changed the version of the request that dcgmGetEntityGroupEntities
// sends from the DCGM library to the hostengine, and neither side accepts the
// other version. A DCGM library and a separate DCGM hostengine on opposite
// sides of 4.7.1 therefore fail dcgmGetEntityGroupEntities with
// DCGM_ST_VER_MISMATCH. The exporter lists entities only to find NVSwitches,
// and NVLink link status, whose request did not change, reports every
// NVSwitch with its links. Falling back to it keeps NVSwitch and NVLink
// metrics working across that version boundary.
//
// The e2e DCGM probe (tests/cmd/e2e-dcgm-probe) uses SwitchIDsFromLinks for
// the same fallback. This file can be deleted, GetEntityGroupEntities restored
// to a direct dcgm.GetEntityGroupEntities call, and the probe's fallback
// removed, once mixed library and hostengine versions across DCGM 4.7.1 no
// longer need to work.

// These unexported function variables are test seams for the fallback.
// Production code leaves them bound to the go-dcgm package functions.
var (
	getEntityGroupEntitiesFunc = dcgm.GetEntityGroupEntities
	getNvLinkLinkStatusFunc    = dcgm.GetNvLinkLinkStatus
)

// GetEntityGroupEntities returns the IDs of the DCGM entities in entityGroup.
// When DCGM rejects NVSwitch listing with a version mismatch, it logs a
// warning and returns the NVSwitches found in NVLink link status instead.
func (d dcgmProvider) GetEntityGroupEntities(entityGroup dcgm.Field_Entity_Group) ([]uint, error) {
	entities, err := getEntityGroupEntitiesFunc(entityGroup)
	if err == nil || entityGroup != dcgm.FE_SWITCH || !dcgmerrors.IsVersionMismatch(err) {
		return entities, err
	}

	slog.Warn("DCGM rejected the NVSwitch list request with an API version mismatch; "+
		"finding NVSwitches from NVLink link status instead",
		slog.String("error", err.Error()),
		slog.String("hint", dcgmerrors.SupportedVersionHint))

	links, linkErr := getNvLinkLinkStatusFunc()
	if linkErr != nil {
		return nil, fmt.Errorf("find NVSwitches from NVLink link status after DCGM rejected the NVSwitch list request (%w): %w",
			err, linkErr)
	}

	return SwitchIDsFromLinks(links), nil
}

// SwitchIDsFromLinks returns the sorted, unique NVSwitch IDs that own links in
// NVLink link status. It returns an empty, non-nil slice when no switch owns a
// link.
func SwitchIDsFromLinks(links []dcgm.NvLinkStatus) []uint {
	switches := make([]uint, 0)
	for _, link := range links {
		if link.ParentType == dcgm.FE_SWITCH && !slices.Contains(switches, link.ParentId) {
			switches = append(switches, link.ParentId)
		}
	}

	slices.Sort(switches)
	return switches
}
