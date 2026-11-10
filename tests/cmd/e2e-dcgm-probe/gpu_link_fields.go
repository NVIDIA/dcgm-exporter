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
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// newGPULinkFieldsCommand reports fields DCGM can read from a physical GPU link.
func newGPULinkFieldsCommand() *cli.Command {
	var address, fieldsSource, output string
	return &cli.Command{
		Name:  "gpu-link-fields",
		Usage: "Write the direct-DCGM field contract for one active GPU-owned link",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "address", Destination: &address, Usage: "private standalone DCGM hostengine address"},
			&cli.StringFlag{Name: "field-names", Destination: &fieldsSource, Usage: "path to go-dcgm const_fields.go"},
			&cli.StringFlag{Name: "output", Destination: &output, Usage: "private path for the temporary JSON contract"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 0 {
				return fmt.Errorf("unexpected arguments: %s", strings.Join(c.Args().Slice(), " "))
			}
			if strings.TrimSpace(address) == "" || strings.TrimSpace(fieldsSource) == "" || strings.TrimSpace(output) == "" {
				return errors.New("gpu-link-fields requires --address, --field-names, and --output")
			}
			previous := stageWriter
			stageWriter = c.App.ErrWriter
			defer func() { stageWriter = previous }()
			return runGPULinkFieldsProbe(address, fieldsSource, output)
		},
	}
}

// runGPULinkFieldsProbe uses DCGM field metadata and live samples to choose
// fields that the exporter can reasonably be expected to expose on one link.
func runGPULinkFieldsProbe(address, fieldsSource, output string) error {
	names, err := fieldNames(fieldsSource)
	if err != nil {
		return err
	}
	startStage("DCGM initialization")
	shutdown, err := dcgm.Init(dcgm.Standalone, address, "0")
	if err != nil {
		return probeStage("DCGM initialization", err)
	}
	defer func() {
		startStage("DCGM shutdown")
		shutdown()
	}()

	startStage("entity discovery")
	links, err := dcgm.GetNvLinkLinkStatus()
	if err != nil {
		return probeStage("entity discovery", err)
	}
	link, found := firstActiveGPULink(links)
	if !found {
		return errors.New("DCGM has no active GPU-owned link")
	}
	device, err := dcgm.GetDeviceInfo(link.ParentId)
	if err != nil {
		return probeStage("entity discovery", err)
	}
	fields, unmapped := gpuLinkCandidates(names, dcgm.GetAllSupportedFieldsMetadata())
	if len(unmapped) != 0 {
		return fmt.Errorf("GPU-link field IDs missing from go-dcgm field names: %v", unmapped)
	}
	fields, _ = scalarExportableFields(fields)
	entity := monitoredEntity{
		Pair:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: link.Index},
		ParentID:   link.ParentId,
		ParentType: dcgm.FE_GPU,
	}
	startStage("field availability discovery")
	available, _, err := probeNumericFields(fields, []monitoredEntity{entity})
	if err != nil {
		return probeStage("field availability discovery", err)
	}
	contract := metriccontract.GPULinkContract{GPU: link.ParentId, GPUUUID: device.UUID, Link: link.Index}
	for _, field := range available {
		contract.Fields = append(contract.Fields, metriccontract.GPULinkField{ID: int(field.Field.ID), Name: field.Field.Name})
	}
	if err := contract.Validate(); err != nil {
		return err
	}
	return writePrivateJSON(output, contract)
}

// firstActiveGPULink chooses the lowest physical index on the lowest GPU.
func firstActiveGPULink(links []dcgm.NvLinkStatus) (dcgm.NvLinkStatus, bool) {
	var selected dcgm.NvLinkStatus
	found := false
	for _, link := range links {
		if link.ParentType != dcgm.FE_GPU || link.State != dcgm.LS_UP {
			continue
		}
		if !found || link.ParentId < selected.ParentId || (link.ParentId == selected.ParentId && link.Index < selected.Index) {
			selected = link
			found = true
		}
	}
	return selected, found
}

// gpuLinkCandidates finds numeric fields DCGM says can be read through FE_LINK.
func gpuLinkCandidates(names map[dcgm.Short]string, metadata map[dcgm.Short]dcgm.FieldMeta) ([]candidateField, []int) {
	var fields []candidateField
	var unmapped []int
	for id, field := range metadata {
		if uint(field.FieldType) != dcgm.DCGM_FT_INT64 && uint(field.FieldType) != dcgm.DCGM_FT_DOUBLE {
			continue
		}
		// Global fields can be read from any group, but have no link identity.
		if field.EntityLevel == dcgm.FE_NONE {
			continue
		}
		if !dcgm.FieldSupportsEntityGroup(id, field.EntityLevel, dcgm.FE_LINK) {
			continue
		}
		name := names[id]
		if name == "" {
			unmapped = append(unmapped, int(id))
			continue
		}
		fields = append(fields, candidateField{ID: id, Name: name, Profiling: strings.HasPrefix(name, "DCGM_FI_PROF_")})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].ID < fields[j].ID })
	sort.Ints(unmapped)
	return fields, unmapped
}
