/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

type entityHistoryOptions struct {
	mode            string
	ownedHostengine string
	outputPath      string
}

func newEntityHistoryCommand() *cli.Command {
	options := entityHistoryOptions{}
	return &cli.Command{
		Name:  "field-entity-history",
		Usage: "Verify fake GPU/GI/CI native XID injection and history",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "mode", Value: "embedded", Destination: &options.mode, Usage: "embedded or standalone"},
			&cli.StringFlag{Name: "owned-hostengine", Destination: &options.ownedHostengine, Usage: "standalone caller-owned isolated unix:// socket; caller must stop backend afterward"},
			&cli.StringFlag{Name: "output", Destination: &options.outputPath, Usage: "private path for native feasibility report"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 0 {
				return fmt.Errorf("unexpected positional arguments: %s", strings.Join(c.Args().Slice(), " "))
			}
			socket, err := validateEntityHistoryOptions(options)
			if err != nil {
				return err
			}
			report, err := runEntityHistory(options, socket)
			if err != nil {
				return err
			}
			return writePrivateJSON(options.outputPath, report)
		},
	}
}

func validateEntityHistoryOptions(options entityHistoryOptions) (string, error) {
	if options.mode != "embedded" && options.mode != "standalone" {
		return "", errors.New("--mode must be embedded or standalone")
	}
	if strings.TrimSpace(options.outputPath) == "" {
		return "", errors.New("--output is required")
	}
	if options.mode == "embedded" {
		if options.ownedHostengine != "" {
			return "", errors.New("--owned-hostengine requires standalone mode")
		}
		return "", nil
	}
	if !strings.HasPrefix(options.ownedHostengine, "unix:///") {
		return "", errors.New("standalone mode requires --owned-hostengine with an isolated absolute unix:// socket")
	}
	endpoint, err := url.Parse(options.ownedHostengine)
	if err != nil || endpoint.Host != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		!filepath.IsAbs(endpoint.Path) || filepath.Clean(endpoint.Path) != endpoint.Path || strings.ContainsRune(endpoint.Path, 0) {
		return "", errors.New("owned hostengine socket must be an absolute clean path without query, fragment or NUL")
	}
	return endpoint.Path, nil
}

func runEntityHistory(options entityHistoryOptions, socket string) (report dcgmprobe.EntityHistory, resultErr error) {
	var shutdown func()
	var err error
	if options.mode == "embedded" {
		shutdown, err = dcgm.Init(dcgm.Embedded)
	} else {
		// Use the same decoded socket path for watch/read and injection clients.
		shutdown, err = dcgm.Init(dcgm.Standalone, socket, "1")
	}
	if err != nil {
		return report, probeStage("DCGM initialization", err)
	}
	defer shutdown()

	entities, giID, ciID, err := createHistoryHierarchy()
	if err != nil {
		return report, err
	}
	report = dcgmprobe.EntityHistory{Version: 1, Mode: options.mode, Topology: "fake", Entities: entities, GPUInstanceID: giID, ComputeInstanceID: ciID}
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	group, err := dcgm.CreateGroup("fec-native-entities-" + suffix)
	if err != nil {
		return report, probeStage("entity group creation", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, probeStage("entity group destruction", dcgm.DestroyGroup(group)))
	}()
	for _, e := range entities {
		if err := dcgm.AddEntityToGroup(group, dcgm.Field_Entity_Group(e.Group), e.ID); err != nil {
			return report, probeStage("entity group population", err)
		}
	}
	fields, err := dcgm.FieldGroupCreate("fec-native-fields-"+suffix, []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS})
	if err != nil {
		return report, probeStage("field group creation", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, probeStage("field group destruction", dcgm.FieldGroupDestroy(fields)))
	}()
	if err := dcgm.WatchFieldsWithGroupEx(fields, group, 1_000_000, 120, 100); err != nil {
		return report, probeStage("field watch", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, probeStage("field unwatch", dcgm.UnwatchFields(fields, group)))
	}()

	err = injectAndReadEntityHistory(&report, group, fields, socket)
	return report, err
}

// injectAndReadEntityHistory proves exact source history and the independent cutoff window.
func injectAndReadEntityHistory(report *dcgmprobe.EntityHistory, group dcgm.GroupHandle, fields dcgm.FieldHandle, socket string) error {
	var err error
	// Historical timestamps prove the cutoff; exact comparison rejects any normalization.
	now := time.Now().UnixMicro()
	report.SinceUsec = now - 120_000_000
	report.WindowSinceUsec = now - 10_000_000
	for _, e := range report.Entities {
		for _, sample := range []struct{ offset, value int64 }{{-90_000_000, 31}, {-3_000_000, 45}, {-2_000_000, 45}, {-1_000_000, 46}} {
			value := dcgmprobe.HistorySample{Entity: e, FieldID: uint(dcgm.DCGM_FI_DEV_XID_ERRORS), FieldType: dcgm.DCGM_FT_INT64, TimestampUsec: now + sample.offset, Value: sample.value}
			if err := injectEntitySample(socket, value); err != nil {
				return probeStage("entity field injection", err)
			}
			report.Expected = append(report.Expected, value)
		}
	}
	report.History, err = readEntityHistory(group, fields, report.SinceUsec)
	if err != nil {
		return err
	}
	if !sameHistory(report.Expected, report.History) {
		return errors.New("native source history differs from injected samples")
	}
	report.WindowHistory, err = readEntityHistory(group, fields, report.WindowSinceUsec)
	if err != nil {
		return err
	}
	var windowExpected []dcgmprobe.HistorySample
	for _, sample := range report.Expected {
		if sample.TimestampUsec >= report.WindowSinceUsec {
			windowExpected = append(windowExpected, sample)
		}
	}
	if !sameHistory(windowExpected, report.WindowHistory) {
		return errors.New("native window history differs from injected in-window samples")
	}
	return nil
}

func createHistoryHierarchy() ([]dcgmprobe.Entity, uint, uint, error) {
	var entities []dcgmprobe.Entity
	parent := dcgm.GroupEntityPair{}
	for _, eg := range []dcgm.Field_Entity_Group{dcgm.FE_GPU, dcgm.FE_GPU_I, dcgm.FE_GPU_CI} {
		ids, err := dcgm.CreateFakeEntities([]dcgm.MigHierarchyInfo{{Entity: dcgm.GroupEntityPair{EntityGroupId: eg}, Parent: parent}})
		if err != nil {
			return nil, 0, 0, probeStage("fake entity creation", err)
		}
		if len(ids) != 1 {
			return nil, 0, 0, errors.New("fake entity creation returned unexpected entity count")
		}
		parent = dcgm.GroupEntityPair{EntityGroupId: eg, EntityId: ids[0]}
		entities = append(entities, dcgmprobe.Entity{Group: uint(eg), ID: ids[0]})
	}
	hierarchy, err := dcgm.GetGPUInstanceHierarchy()
	if err != nil {
		return nil, 0, 0, probeStage("MIG hierarchy discovery", err)
	}
	var foundGI, foundCI bool
	var giID, ciID uint
	for i := uint(0); i < hierarchy.Count; i++ {
		e := hierarchy.EntityList[i]
		if e.Entity.EntityGroupId == dcgm.FE_GPU_I && e.Entity.EntityId == entities[1].ID && e.Parent.EntityGroupId == dcgm.FE_GPU && e.Parent.EntityId == entities[0].ID {
			foundGI = true
			giID = e.Info.NvmlInstanceId
		}
		if e.Entity.EntityGroupId == dcgm.FE_GPU_CI && e.Entity.EntityId == entities[2].ID && e.Parent.EntityGroupId == dcgm.FE_GPU_I && e.Parent.EntityId == entities[1].ID {
			foundCI = true
			ciID = e.Info.NvmlComputeInstanceId
		}
	}
	if !foundGI || !foundCI {
		return nil, 0, 0, errors.New("native discovery omitted the created GPU/GI/CI hierarchy")
	}
	return entities, giID, ciID, nil
}

func readEntityHistory(group dcgm.GroupHandle, fields dcgm.FieldHandle, since int64) ([]dcgmprobe.HistorySample, error) {
	// The second return is the cursor for a subsequent read, unused for this one-shot window.
	values, _, err := dcgm.GetValuesSince(group, fields, time.UnixMicro(since))
	if err != nil {
		return nil, probeStage("entity history read", err)
	}
	var samples []dcgmprobe.HistorySample
	for _, v := range values {
		samples = append(samples, dcgmprobe.HistorySample{Entity: dcgmprobe.Entity{Group: uint(v.EntityGroupId), ID: v.EntityID}, FieldID: uint(v.FieldID), FieldType: v.FieldType, Status: v.Status, TimestampUsec: v.TS, Value: v.Int64()})
	}
	return samples, nil
}

// sameHistory checks typed membership and sample multiplicity; it never merges
// equal values or timestamps to hide overlapping reads.
func sameHistory(expected, actual []dcgmprobe.HistorySample) bool {
	if len(expected) != len(actual) {
		return false
	}
	counts := make(map[dcgmprobe.HistorySample]int, len(expected))
	for _, sample := range expected {
		counts[sample]++
	}
	for _, sample := range actual {
		if counts[sample] == 0 {
			return false
		}
		counts[sample]--
	}
	return true
}
