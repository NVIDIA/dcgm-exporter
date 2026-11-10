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

// Command e2e-dcgm-probe provides direct DCGM observations, including the
// numeric field/entity contract exposed by a configured DCGM instance, for the
// e2e harness. Its JSON output is temporary test input and must never be
// published as a CI artifact.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/clioutput"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/fieldexport"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvmlinjection"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvsdmmock"
)

const (
	watchFrequencyUsec = 100_000
)

// Native resource operations stay replaceable so cleanup failures can be tested without a GPU.
var (
	probeCreateEntityGroup  = dcgm.CreateGroup
	probeDestroyEntityGroup = dcgm.DestroyGroup
	probeAddEntity          = addEntityToGroup
	probeCreateFieldGroup   = dcgm.FieldGroupCreate
	probeDestroyFieldGroup  = dcgm.FieldGroupDestroy
	probeWatchFields        = dcgm.WatchFieldsWithGroupEx
	probeUnwatchFields      = dcgm.UnwatchFields
	probeUpdateFields       = dcgm.UpdateAllFields
)

// probeGroupMaxEntities is the most entities the probe puts in one DCGM group.
// DCGM rejects more, and a large NVSwitch fabric reports more links than that,
// so probeNumericFields splits entities across several groups. Tests lower it.
var probeGroupMaxEntities = dcgm.DCGM_GROUP_MAX_ENTITIES

// candidateField pairs field metadata with its stable name and profiling classification.
type candidateField struct {
	ID        dcgm.Short
	Name      string
	Profiling bool
}

// monitoredEntity carries native entity/parent identity and the labels used in expected metrics.
type monitoredEntity struct {
	Pair       dcgm.GroupEntityPair
	ParentID   uint
	ParentType dcgm.Field_Entity_Group
	Labels     map[string]string
}

// probedSample pairs one entity with its exported scalar value.
type probedSample struct {
	Entity monitoredEntity
	Value  float64
}

// probedMetric collects available entity samples for one field.
type probedMetric struct {
	Field   candidateField
	Samples []probedSample
}

// probedUnavailable records a field with no usable samples and its availability reason.
type probedUnavailable struct {
	Field  candidateField
	Reason string
}

// probeOptions selects the field-name source and exclusive private contract
// report path, and where the command writes warnings.
type probeOptions struct {
	fieldsSource string
	outputPath   string
	warnings     io.Writer
}

// probeStageError retains the cause while exposing the first failed stage without sensitive native details.
type probeStageError struct {
	stage string
	err   error
}

// Error reports the stage without exposing the underlying native error text.
func (e probeStageError) Error() string { return "DCGM probe failed during " + e.stage }

// Unwrap preserves the cause for error classification and diagnostic handling.
func (e probeStageError) Unwrap() error { return e.err }

// stageWriter receives "stage: <name>" lines. It discards them unless a command
// sets it, so only that command reports its steps.
var stageWriter io.Writer = io.Discard

// startStage tells the caller which probe step is about to start, so a hang can
// be traced to the last stage line.
func startStage(stage string) {
	fmt.Fprintf(stageWriter, "stage: %s\n", stage)
}

// probeStage adds the first failed probe stage while retaining the underlying error.
func probeStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	var staged probeStageError
	if errors.As(err, &staged) {
		return err
	}
	return probeStageError{stage: stage, err: err}
}

// main runs the probe CLI and returns a nonzero process result for command failures.
func main() {
	app := newApp(os.Stdout, os.Stderr)
	if err := app.Run(os.Args); err != nil {
		clioutput.WriteError(os.Stderr, err)
		os.Exit(1)
	}
}

// run runs a command without writing output, for CPU-only argument validation.
func run(args []string) error {
	return newApp(io.Discard, io.Discard).Run(append([]string{"e2e-dcgm-probe"}, args...))
}

// newApp constructs the shared command tree and configures usage-error output.
func newApp(stdout, stderr io.Writer) *cli.App {
	app := cli.NewApp()
	app.Name = "e2e-dcgm-probe"
	app.Usage = "Collect direct DCGM observations for the e2e harness"
	app.UsageText = "e2e-dcgm-probe <command> [options]"
	app.HideVersion = true
	app.Writer = stdout
	app.ErrWriter = stderr
	// Empty invocation prints help; positional commands must match the command tree.
	app.Action = func(c *cli.Context) error {
		if c.NArg() == 0 {
			return cli.ShowAppHelp(c)
		}
		return fmt.Errorf("unknown e2e-dcgm-probe command %q", c.Args().First())
	}
	app.Commands = []*cli.Command{
		newContractCommand("nvml-injection", "Write the NVML injection contract", runNVMLInjection),
		newContractCommand("nvsdm-mock", "Write the NVSDM mock contract", runNVSDMMock),
		newCacheCountsCommand(),
		newEntityHistoryCommand(),
		newExporterAcceptanceCommand(),
		newGPULinkFieldsCommand(),
	}
	clioutput.ConfigureUsageErrors(app)
	return app
}

// newContractCommand binds required source/output options to one contract observation callback.
func newContractCommand(name, usage string, action func(probeOptions) error) *cli.Command {
	options := probeOptions{}
	return &cli.Command{
		Name:      name,
		Usage:     usage,
		UsageText: "e2e-dcgm-probe " + name + " [options]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "field-names", Destination: &options.fieldsSource, Usage: "path to go-dcgm const_fields.go"},
			&cli.StringFlag{Name: "output", Destination: &options.outputPath, Usage: "private path for the temporary JSON contract"},
		},
		// Validate options before invoking the contract-specific native observer.
		Action: func(c *cli.Context) error {
			if c.NArg() != 0 {
				return fmt.Errorf("unexpected arguments: %s", strings.Join(c.Args().Slice(), " "))
			}
			if strings.TrimSpace(options.fieldsSource) == "" {
				return errors.New("--field-names is required")
			}
			if strings.TrimSpace(options.outputPath) == "" {
				return errors.New("--output is required")
			}

			options.warnings = c.App.ErrWriter
			return action(options)
		},
	}
}

// runNVMLInjection records scalar GPU/MIG observations and unavailable fields from an embedded DCGM session.
func runNVMLInjection(options probeOptions) error {
	names, err := fieldNames(options.fieldsSource)
	if err != nil {
		return err
	}
	cleanup, err := dcgm.Init(dcgm.Embedded)
	if err != nil {
		return probeStage("DCGM initialization", err)
	}
	defer cleanup()

	entities, computeInstances, multiScopeEntityList, deviceCount, err := monitoredEntities()
	if err != nil {
		return probeStage("entity discovery", err)
	}
	contract := newNVMLInjectionContract(deviceCount, computeInstances)
	if deviceCount == 0 {
		return writePrivateJSON(options.outputPath, contract)
	}

	metadata := dcgm.GetAllSupportedFieldsMetadata()
	fields, unmapped := numericGPUFields(names, metadata)
	fields, injectionExcluded := nvmlInjectionFields(fields)
	fields, scalarExcluded := scalarExportableFields(fields)
	excluded := injectionExcluded
	excluded = append(excluded, scalarExcluded...)
	fields, multiScopeFields := splitMultiScopeFields(fields)
	computeInstanceFields, computeInstanceUnmapped := numericEntityFields(
		names,
		metadata,
		dcgm.FE_GPU_CI,
	)
	computeInstanceFields, computeInstanceExcluded := scalarExportableFields(computeInstanceFields)
	excluded = append(excluded, computeInstanceExcluded...)
	unmapped = append(unmapped, computeInstanceUnmapped...)
	sort.Ints(unmapped)
	if len(unmapped) != 0 {
		return fmt.Errorf("numeric GPU field IDs missing from go-dcgm field names: %v", unmapped)
	}
	metrics, unavailable, err := probeFields(fields, gpuLevelFieldEntities(multiScopeEntityList))
	if err != nil {
		return probeStage("field availability discovery", err)
	}
	unavailable = append(unavailable, excluded...)
	computeFieldEntities := computeInstanceFieldEntities(entities, computeInstances)
	if len(computeFieldEntities) > 0 && len(computeInstanceFields) > 0 {
		computeInstanceMetrics, computeInstanceUnavailable, err := probeFields(computeInstanceFields, computeFieldEntities)
		if err != nil {
			return probeStage("field availability discovery", err)
		}
		metrics = append(metrics, computeInstanceMetrics...)
		unavailable = append(unavailable, computeInstanceUnavailable...)
	}
	if len(multiScopeEntityList) > 0 && len(multiScopeFields) > 0 {
		multiScopeMetrics, multiScopeUnavailable, err := probeFields(multiScopeFields, multiScopeEntityList)
		if err != nil {
			return probeStage("field availability discovery", err)
		}
		metrics = append(metrics, multiScopeMetrics...)
		unavailable = append(unavailable, multiScopeUnavailable...)
	}
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].ID < metrics[j].ID })
	contract.Metrics = metrics
	contract.Unavailable = unavailable
	if err := contract.Validate(); err != nil {
		return err
	}
	return writePrivateJSON(options.outputPath, contract)
}

// newNVMLInjectionContract records the topology discovered before the probe
// adds field samples. Contract validation later checks those samples against
// these counts.
func newNVMLInjectionContract(deviceCount int, computeInstances []monitoredEntity) nvmlinjection.Contract {
	return nvmlinjection.Contract{
		Version:              nvmlinjection.CurrentVersion,
		DeviceCount:          deviceCount,
		ComputeInstanceCount: len(computeInstances),
	}
}

// runNVSDMMock records switch/link observations with the identities used by the NVSDM contract.
//
// DCGM workaround: DCGM's NVSwitch module drops a whole update cycle when one
// watched field has no NVSDM mapping, and DCGM does not release a link's
// NVSwitch watches when that link is unwatched. Fields probed with or after
// such a field then return values for only some links. The first pass finds
// the fields that return values; the second pass restarts DCGM and probes only
// those fields, which are the fields the exporter watches. Remove the second
// pass once DCGM keeps the other fields' samples and releases link watches.
func runNVSDMMock(options probeOptions) error {
	names, err := fieldNames(options.fieldsSource)
	if err != nil {
		return err
	}

	first, err := probeNVSDM(options, names, nil)
	if err != nil {
		return err
	}
	second, err := probeNVSDM(options, names, first.sampledFieldIDs())
	if err != nil {
		return err
	}

	contract := second.contract(first.Unavailable)
	if err := contract.Validate(); err != nil {
		return err
	}
	return writePrivateJSON(options.outputPath, contract)
}

// nvsdmProbe holds one DCGM session's switch and link inventory and its field results.
type nvsdmProbe struct {
	SwitchCount int
	LinkCount   int
	MetricsByID map[dcgm.Short]*nvsdmmock.Metric
	Unavailable []nvsdmmock.Unavailable
}

// probeNVSDM starts embedded DCGM, discovers switches and links, probes their
// numeric fields, and shuts DCGM down. A non-nil onlyFields limits the probe to
// those field IDs. Discovery warnings go to options.warnings.
func probeNVSDM(options probeOptions, names map[dcgm.Short]string, onlyFields map[dcgm.Short]struct{}) (nvsdmProbe, error) {
	cleanup, err := dcgm.Init(dcgm.Embedded)
	if err != nil {
		return nvsdmProbe{}, probeStage("DCGM initialization", err)
	}
	defer cleanup()

	switchIDs, err := discoverSwitchIDs(options.warnings)
	if err != nil {
		return nvsdmProbe{}, probeStage("entity discovery", err)
	}
	linkStatuses, err := dcgm.GetNvLinkLinkStatus()
	if err != nil {
		return nvsdmProbe{}, probeStage("entity discovery", fmt.Errorf("get NVLink status: %w", err))
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nvsdmProbe{}, probeStage("entity discovery", fmt.Errorf("get hostname: %w", err))
	}
	switches, links := nvsdmEntities(switchIDs, linkStatuses, hostname)

	metadata := dcgm.GetAllSupportedFieldsMetadata()
	switchFields, switchUnmapped := numericEntityFields(names, metadata, dcgm.FE_SWITCH)
	linkFields, linkUnmapped := numericEntityFields(names, metadata, dcgm.FE_LINK)
	unmapped := append([]int(nil), switchUnmapped...)
	unmapped = append(unmapped, linkUnmapped...)
	sort.Ints(unmapped)
	if len(unmapped) != 0 {
		return nvsdmProbe{}, fmt.Errorf("numeric NVSDM field IDs missing from go-dcgm field names: %v", unmapped)
	}

	result := nvsdmProbe{
		SwitchCount: len(switches),
		LinkCount:   len(links),
		MetricsByID: make(map[dcgm.Short]*nvsdmmock.Metric),
	}
	// Keep switch and link metadata paired with their matching entity inventories.
	for _, entityGroup := range []struct {
		name     string
		fields   []candidateField
		entities []monitoredEntity
	}{
		{name: nvsdmmock.EntityGroupSwitch, fields: selectFields(switchFields, onlyFields), entities: switches},
		{name: nvsdmmock.EntityGroupLink, fields: selectFields(linkFields, onlyFields), entities: links},
	} {
		metrics, unavailable, err := probeNumericFields(entityGroup.fields, entityGroup.entities)
		if err != nil {
			return nvsdmProbe{}, probeStage("field availability discovery", err)
		}
		result.add(entityGroup.name, metrics, unavailable)
	}
	return result, nil
}

// selectFields keeps fields whose IDs are in onlyFields, or every field when onlyFields is nil.
func selectFields(fields []candidateField, onlyFields map[dcgm.Short]struct{}) []candidateField {
	if onlyFields == nil {
		return fields
	}

	selected := make([]candidateField, 0, len(fields))
	for _, field := range fields {
		if _, found := onlyFields[field.ID]; found {
			selected = append(selected, field)
		}
	}
	return selected
}

// add records one entity group's sampled fields and unavailable fields.
func (p *nvsdmProbe) add(entityGroup string, metrics []probedMetric, unavailable []probedUnavailable) {
	for _, metric := range metrics {
		expected := p.MetricsByID[metric.Field.ID]
		if expected == nil {
			expected = &nvsdmmock.Metric{
				ID:   int(metric.Field.ID),
				Name: metric.Field.Name,
				Type: "gauge",
			}
			p.MetricsByID[metric.Field.ID] = expected
		}
		for _, sample := range metric.Samples {
			expected.Samples = append(expected.Samples, nvsdmmock.Sample{
				EntityGroup: entityGroup,
				EntityID:    sample.Entity.Pair.EntityId,
				ParentID:    sample.Entity.ParentID,
				Labels:      sample.Entity.Labels,
				Value:       sample.Value,
			})
		}
	}

	for _, field := range unavailable {
		p.Unavailable = append(p.Unavailable, nvsdmmock.Unavailable{
			Name:        field.Field.Name,
			EntityGroup: entityGroup,
			Reason:      field.Reason,
		})
	}
}

// sampledFieldIDs returns the IDs of fields that returned at least one sample.
func (p nvsdmProbe) sampledFieldIDs() map[dcgm.Short]struct{} {
	ids := make(map[dcgm.Short]struct{}, len(p.MetricsByID))
	for id := range p.MetricsByID {
		ids[id] = struct{}{}
	}
	return ids
}

// contract builds the NVSDM contract from this probe's metrics, adding
// earlier unavailable fields that this probe did not query.
func (p nvsdmProbe) contract(earlierUnavailable []nvsdmmock.Unavailable) nvsdmmock.Contract {
	contract := nvsdmmock.Contract{
		Version:     nvsdmmock.CurrentVersion,
		SwitchCount: p.SwitchCount,
		LinkCount:   p.LinkCount,
	}

	metricIDs := make([]dcgm.Short, 0, len(p.MetricsByID))
	for id := range p.MetricsByID {
		metricIDs = append(metricIDs, id)
	}
	sort.Slice(metricIDs, func(i, j int) bool { return metricIDs[i] < metricIDs[j] })
	for _, id := range metricIDs {
		contract.Metrics = append(contract.Metrics, *p.MetricsByID[id])
	}

	contract.Unavailable = append(append(contract.Unavailable, earlierUnavailable...), p.Unavailable...)
	return contract
}

// nvsdmEntities orders switches and includes only active links owned by discovered switches.
func nvsdmEntities(switchIDs []uint, links []dcgm.NvLinkStatus, hostname string) ([]monitoredEntity, []monitoredEntity) {
	sortedSwitches := append([]uint(nil), switchIDs...)
	sort.Slice(sortedSwitches, func(i, j int) bool { return sortedSwitches[i] < sortedSwitches[j] })
	knownSwitches := make(map[uint]struct{}, len(sortedSwitches))
	switchEntities := make([]monitoredEntity, 0, len(sortedSwitches))
	for _, switchID := range sortedSwitches {
		knownSwitches[switchID] = struct{}{}
		switchEntities = append(switchEntities, monitoredEntity{
			Pair:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_SWITCH, EntityId: switchID},
			ParentID:   switchID,
			ParentType: dcgm.FE_NONE,
			Labels: map[string]string{
				"nvswitch": fmt.Sprintf("nvswitch%d", switchID),
				"hostname": hostname,
			},
		})
	}

	linkEntities := make([]monitoredEntity, 0, len(links))
	for _, link := range links {
		if link.ParentType != dcgm.FE_SWITCH || link.State != dcgm.LS_UP {
			continue
		}
		if _, found := knownSwitches[link.ParentId]; !found {
			continue
		}
		linkEntities = append(linkEntities, monitoredEntity{
			Pair:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: link.Index},
			ParentID:   link.ParentId,
			ParentType: dcgm.FE_SWITCH,
			Labels: map[string]string{
				"nvswitch": fmt.Sprintf("nvswitch%d", link.ParentId),
				"nvlink":   strconv.FormatUint(uint64(link.Index), 10),
				"hostname": hostname,
			},
		})
	}
	sort.Slice(linkEntities, func(i, j int) bool {
		if linkEntities[i].ParentID != linkEntities[j].ParentID {
			return linkEntities[i].ParentID < linkEntities[j].ParentID
		}
		return linkEntities[i].Pair.EntityId < linkEntities[j].Pair.EntityId
	})
	return switchEntities, linkEntities
}

// numericEntityFields selects integer/double fields at one entity level and reports IDs without names.
func numericEntityFields(
	names map[dcgm.Short]string,
	metadata map[dcgm.Short]dcgm.FieldMeta,
	entityGroup dcgm.Field_Entity_Group,
) ([]candidateField, []int) {
	fields := make([]candidateField, 0, len(metadata))
	var unmapped []int
	for id, field := range metadata {
		if field.EntityLevel != entityGroup {
			continue
		}
		if uint(field.FieldType) != dcgm.DCGM_FT_INT64 && uint(field.FieldType) != dcgm.DCGM_FT_DOUBLE {
			continue
		}
		name := names[id]
		if name == "" {
			unmapped = append(unmapped, int(id))
			continue
		}
		fields = append(fields, candidateField{
			ID:        id,
			Name:      name,
			Profiling: strings.HasPrefix(name, "DCGM_FI_PROF_"),
		})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].ID < fields[j].ID })
	sort.Ints(unmapped)
	return fields, unmapped
}

// writePrivateJSON writes an exclusive 0600 report and attempts to remove incomplete output after a failure.
func writePrivateJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create private probe output: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.Remove(path)
		}
	}()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		_ = file.Close()
		return fmt.Errorf("write private probe output: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close private probe output: %w", err)
	}
	removeOnError = false
	return nil
}

// fieldNames reads numeric DCGM field names from the go-dcgm dcgmFields map without executing the source.
func fieldNames(path string) (map[dcgm.Short]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse go-dcgm field names: %w", err)
	}
	names := map[dcgm.Short]string{}
	// Inspect only literal dcgmFields entries; unrelated source declarations are ignored.
	ast.Inspect(file, func(node ast.Node) bool {
		value, ok := node.(*ast.ValueSpec)
		if !ok || len(value.Names) != 1 || len(value.Values) != 1 || value.Names[0].Name != "dcgmFields" {
			return true
		}
		literal, ok := value.Values[0].(*ast.CompositeLit)
		if !ok {
			return false
		}
		for _, element := range literal.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, keyOK := pair.Key.(*ast.BasicLit)
			value, valueOK := pair.Value.(*ast.BasicLit)
			if !keyOK || !valueOK {
				continue
			}
			name, unquoteErr := strconv.Unquote(key.Value)
			id, parseErr := strconv.ParseUint(value.Value, 10, 16)
			if unquoteErr == nil && parseErr == nil && strings.HasPrefix(name, "DCGM_FI_") {
				names[dcgm.Short(id)] = name
			}
		}
		return false
	})
	if len(names) == 0 {
		return nil, errors.New("dcgmFields map was not found")
	}
	return names, nil
}

// numericGPUFields selects named numeric GPU fields and identifies profiling fields.
func numericGPUFields(names map[dcgm.Short]string, metadata map[dcgm.Short]dcgm.FieldMeta) ([]candidateField, []int) {
	fields := make([]candidateField, 0, len(metadata))
	var unmapped []int
	for id, field := range metadata {
		if field.EntityLevel != dcgm.FE_GPU {
			continue
		}
		if uint(field.FieldType) != dcgm.DCGM_FT_INT64 && uint(field.FieldType) != dcgm.DCGM_FT_DOUBLE {
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

// scalarExportableFields removes fields that the generic exporter collector
// intentionally rejects because it cannot represent their values safely.
func scalarExportableFields(fields []candidateField) ([]candidateField, []nvmlinjection.Unavailable) {
	exportable := make([]candidateField, 0, len(fields))
	excluded := make([]nvmlinjection.Unavailable, 0)
	for _, field := range fields {
		if !fieldexport.RuleFor(field.ID).AllowsGenericScalar() {
			excluded = append(excluded, nvmlinjection.Unavailable{
				Name:   field.Name,
				Reason: "not-scalar-exportable",
			})
			continue
		}
		exportable = append(exportable, field)
	}
	return exportable, excluded
}

// nvmlInjectionFields excludes GPU fields that also require DCGM's NVLink
// module. NVML injection fixtures model GPU values but do not load that
// module, while hardware and NVSDM suites cover the link-specific path.
func nvmlInjectionFields(fields []candidateField) ([]candidateField, []nvmlinjection.Unavailable) {
	exportable := make([]candidateField, 0, len(fields))
	excluded := make([]nvmlinjection.Unavailable, 0)
	for _, field := range fields {
		if dcgm.FieldSupportsEntityGroup(field.ID, dcgm.FE_GPU, dcgm.FE_LINK) {
			excluded = append(excluded, nvmlinjection.Unavailable{
				Name:   field.Name,
				Reason: "requires-nvlink-module",
			})
			continue
		}
		exportable = append(exportable, field)
	}
	return exportable, excluded
}

// splitMultiScopeFields separates XID fields that must be observed across GPU and MIG scopes.
func splitMultiScopeFields(fields []candidateField) ([]candidateField, []candidateField) {
	var singleScope, multiScope []candidateField
	for _, field := range fields {
		if field.ID == dcgm.DCGM_FI_DEV_XID_ERRORS {
			multiScope = append(multiScope, field)
			continue
		}
		singleScope = append(singleScope, field)
	}
	return singleScope, multiScope
}

// monitoredEntities discovers whole-GPU, GPU-instance and compute-instance identities for the probe.
func monitoredEntities() ([]monitoredEntity, []monitoredEntity, []monitoredEntity, int, error) {
	gpuIDs, err := dcgm.GetEntityGroupEntities(dcgm.FE_GPU)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("get all GPU entities: %w", err)
	}
	if len(gpuIDs) == 0 {
		return nil, nil, nil, 0, nil
	}
	sort.Slice(gpuIDs, func(i, j int) bool { return gpuIDs[i] < gpuIDs[j] })
	devices := make(map[uint]dcgm.Device, len(gpuIDs))
	for _, gpu := range gpuIDs {
		device, err := dcgm.GetDeviceInfo(gpu)
		if err != nil {
			return nil, nil, nil, 0, fmt.Errorf("get GPU %d identity: %w", gpu, err)
		}
		devices[gpu] = device
	}

	hierarchy, err := dcgm.GetGPUInstanceHierarchy()
	instances := map[uint][]dcgm.MigHierarchyInfo_v2{}
	computeInstancesByGPUInstance := map[uint][]dcgm.MigHierarchyInfo_v2{}
	// Non-MIG injection fixtures do not necessarily inject the hierarchy API.
	// In that case DCGM's physical GPU list remains the authoritative entity set.
	if err == nil {
		for index := uint(0); index < hierarchy.Count; index++ {
			entry := hierarchy.EntityList[index]
			if entry.Parent.EntityGroupId == dcgm.FE_GPU && entry.Entity.EntityGroupId == dcgm.FE_GPU_I {
				instances[entry.Parent.EntityId] = append(instances[entry.Parent.EntityId], entry)
			}
			if entry.Parent.EntityGroupId == dcgm.FE_GPU_I && entry.Entity.EntityGroupId == dcgm.FE_GPU_CI {
				computeInstancesByGPUInstance[entry.Parent.EntityId] = append(
					computeInstancesByGPUInstance[entry.Parent.EntityId],
					entry,
				)
			}
		}
	}
	for gpu := range instances {
		sort.Slice(instances[gpu], func(i, j int) bool {
			return instances[gpu][i].Entity.EntityId < instances[gpu][j].Entity.EntityId
		})
	}
	for gpuInstance := range computeInstancesByGPUInstance {
		sort.Slice(computeInstancesByGPUInstance[gpuInstance], func(i, j int) bool {
			return computeInstancesByGPUInstance[gpuInstance][i].Entity.EntityId <
				computeInstancesByGPUInstance[gpuInstance][j].Entity.EntityId
		})
	}

	hostname, err := os.Hostname()
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("get hostname: %w", err)
	}
	var result []monitoredEntity
	var computeInstanceEntities []monitoredEntity
	var parentGPUEntities, gpuInstanceEntities []monitoredEntity
	for _, gpu := range gpuIDs {
		device := devices[gpu]
		base := map[string]string{
			"gpu":        strconv.FormatUint(uint64(device.GPU), 10),
			"UUID":       device.UUID,
			"pci_bus_id": device.PCI.BusID,
			"device":     fmt.Sprintf("nvidia%d", device.GPU),
			"modelName":  device.Identifiers.Model,
			"hostname":   hostname,
		}
		parentGPU := monitoredEntity{
			Pair:   dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: device.GPU},
			Labels: base,
		}
		parentGPUEntities = append(parentGPUEntities, parentGPU)
		if len(instances[gpu]) == 0 {
			result = append(result, parentGPU)
			continue
		}
		for _, instance := range instances[gpu] {
			profile, err := gpuInstanceProfile(instance.Entity.EntityId)
			if err != nil {
				return nil, nil, nil, 0, err
			}
			labels := cloneLabels(base)
			labels["GPU_I_ID"] = strconv.FormatUint(uint64(instance.Info.NvmlInstanceId), 10)
			labels["GPU_I_PROFILE"] = profile
			gpuInstance := monitoredEntity{Pair: instance.Entity, Labels: labels}
			result = append(result, gpuInstance)
			gpuInstanceEntities = append(gpuInstanceEntities, gpuInstance)
			for _, computeInstance := range computeInstancesByGPUInstance[instance.Entity.EntityId] {
				computeInstanceLabels := cloneLabels(labels)
				computeInstanceLabels["GPU_CI_ID"] = strconv.FormatUint(
					uint64(computeInstance.Info.NvmlComputeInstanceId),
					10,
				)
				computeInstanceEntities = append(computeInstanceEntities, monitoredEntity{
					Pair:       computeInstance.Entity,
					ParentID:   instance.Entity.EntityId,
					ParentType: dcgm.FE_GPU_I,
					Labels:     computeInstanceLabels,
				})
			}
		}
	}
	return result, computeInstanceEntities,
		multiScopeEntities(parentGPUEntities, gpuInstanceEntities, computeInstanceEntities),
		len(gpuIDs), nil
}

// multiScopeEntities combines GPU, GPU-instance and compute-instance observations for multi-scope fields.
func multiScopeEntities(
	parentGPUEntities, gpuInstanceEntities, computeInstanceEntities []monitoredEntity,
) []monitoredEntity {
	entities := make([]monitoredEntity, 0, len(parentGPUEntities)+len(gpuInstanceEntities)+len(computeInstanceEntities))
	entities = append(entities, parentGPUEntities...)
	entities = append(entities, gpuInstanceEntities...)
	return append(entities, computeInstanceEntities...)
}

// computeInstanceFieldEntities mirrors exporter collection for fields whose
// metadata reports FE_GPU_CI: selected whole GPUs and compute instances, but
// never GPU instances.
func computeInstanceFieldEntities(
	selectedEntities, computeInstanceEntities []monitoredEntity,
) []monitoredEntity {
	entities := make([]monitoredEntity, 0, len(selectedEntities)+len(computeInstanceEntities))
	for _, entity := range selectedEntities {
		if entity.Pair.EntityGroupId == dcgm.FE_GPU {
			entities = append(entities, entity)
		}
	}
	return append(entities, computeInstanceEntities...)
}

// gpuLevelFieldEntities mirrors exporter collection for fields whose metadata
// reports FE_GPU: every physical GPU, never a GPU instance or compute instance.
func gpuLevelFieldEntities(entities []monitoredEntity) []monitoredEntity {
	var gpus []monitoredEntity
	for _, entity := range entities {
		if entity.Pair.EntityGroupId == dcgm.FE_GPU {
			gpus = append(gpus, entity)
		}
	}
	return gpus
}

// gpuInstanceProfile requires one valid nonempty live profile name for a GPU instance.
func gpuInstanceProfile(entityID uint) (string, error) {
	values, err := dcgm.EntitiesGetLatestValues(
		[]dcgm.GroupEntityPair{{EntityGroupId: dcgm.FE_GPU_I, EntityId: entityID}},
		[]dcgm.Short{dcgm.DCGM_FI_DEV_NAME},
		dcgm.DCGM_FV_FLAG_LIVE_DATA,
	)
	if err != nil {
		return "", fmt.Errorf("get GPU-instance profile: %w", err)
	}
	if len(values) != 1 || values[0].Status != dcgm.DCGM_ST_OK {
		return "", errors.New("GPU-instance profile is unavailable")
	}
	profile := strings.TrimSpace(dcgm.Fv2_String(values[0]))
	if profile == "" {
		return "", errors.New("GPU-instance profile is empty")
	}
	return profile, nil
}

// cloneLabels detaches a label map before adding MIG identity fields.
func cloneLabels(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+2)
	for name, value := range source {
		result[name] = value
	}
	return result
}

// probeFields converts native scalar observations and availability reasons to the NVML fixture contract.
func probeFields(fields []candidateField, entities []monitoredEntity) ([]nvmlinjection.Metric, []nvmlinjection.Unavailable, error) {
	probed, unavailable, err := probeNumericFields(fields, entities)
	if err != nil {
		return nil, nil, err
	}
	metrics := make([]nvmlinjection.Metric, 0, len(probed))
	for _, metric := range probed {
		expected := nvmlinjection.Metric{
			ID:        int(metric.Field.ID),
			Name:      metric.Field.Name,
			Profiling: metric.Field.Profiling,
		}
		for _, sample := range metric.Samples {
			expected.Samples = append(expected.Samples, nvmlinjection.Sample{
				EntityGroup: sample.Entity.Pair.EntityGroupId.String(),
				EntityID:    sample.Entity.Pair.EntityId,
				Labels:      sample.Entity.Labels,
				Value:       sample.Value,
			})
		}
		metrics = append(metrics, expected)
	}
	unavailableFields := make([]nvmlinjection.Unavailable, 0, len(unavailable))
	for _, field := range unavailable {
		unavailableFields = append(unavailableFields, nvmlinjection.Unavailable{Name: field.Field.Name, Reason: field.Reason})
	}
	return metrics, unavailableFields, nil
}

// probeNumericFields queries regular/profiling batches over entity groups of
// at most probeGroupMaxEntities entities, and reports entity-group cleanup errors.
func probeNumericFields(fields []candidateField, entities []monitoredEntity) (available []probedMetric, unavailable []probedUnavailable, resultErr error) {
	availability, metrics := newProbeResults(fields)
	if len(fields) == 0 {
		return nil, nil, nil
	}
	if len(entities) == 0 {
		return nil, probeFieldsUnavailableWithoutEntities(fields), nil
	}

	// A cleanup failure is reported with the results; any other failure stops
	// the probe and also reports earlier cleanup failures.
	var cleanupErr error
	for first := 0; first < len(entities); first += probeGroupMaxEntities {
		last := min(first+probeGroupMaxEntities, len(entities))
		err, groupCleanupErr := probeEntityGroupFields(fields, entities[first:last], metrics, availability)
		cleanupErr = errors.Join(cleanupErr, groupCleanupErr)
		if err != nil {
			return nil, nil, errors.Join(err, cleanupErr)
		}
	}

	available, unavailable = collectProbeResults(fields, metrics, availability)
	return available, unavailable, cleanupErr
}

// probeEntityGroupFields puts entities in one DCGM entity group, queries every
// field batch on it into metrics and availability, and destroys the group. It
// returns setup or query failures in err and the group's destruction failure
// in cleanupErr, so callers can keep results when only cleanup failed.
func probeEntityGroupFields(
	fields []candidateField,
	entities []monitoredEntity,
	metrics map[dcgm.Short]*probedMetric,
	availability map[dcgm.Short]map[string]int,
) (err, cleanupErr error) {
	startStage("entity group creation")
	group, createErr := probeCreateEntityGroup("dcgm-exporter-e2e-oracle")
	if createErr != nil {
		return probeStage("entity group creation", createErr), nil
	}
	defer func() {
		startStage("entity group destruction")
		cleanupErr = probeStage("entity group destruction", probeDestroyEntityGroup(group))
	}()

	for _, entity := range entities {
		startStage("entity group population")
		if addErr := probeAddEntity(group, entity); addErr != nil {
			return probeStage("entity group population", addErr), nil
		}
	}

	regular, profiling := splitProbeFields(fields)
	if err := probeFieldSet(group, regular, nvmlinjection.RegularBatchSize, entities, metrics, availability); err != nil {
		return err, nil
	}
	return probeFieldSet(group, profiling, nvmlinjection.ProfilingBatchSize, entities, metrics, availability), nil
}

// newProbeResults creates the sample and availability maps for one entity-group probe.
func newProbeResults(fields []candidateField) (map[dcgm.Short]map[string]int, map[dcgm.Short]*probedMetric) {
	availability := make(map[dcgm.Short]map[string]int, len(fields))
	metrics := make(map[dcgm.Short]*probedMetric, len(fields))
	for _, field := range fields {
		availability[field.ID] = map[string]int{}
		metrics[field.ID] = &probedMetric{Field: field}
	}
	return availability, metrics
}

// probeFieldsUnavailableWithoutEntities records why an unprobeable field has no samples.
func probeFieldsUnavailableWithoutEntities(fields []candidateField) []probedUnavailable {
	unavailable := make([]probedUnavailable, 0, len(fields))
	for _, field := range fields {
		unavailable = append(unavailable, probedUnavailable{Field: field, Reason: "no-entities"})
	}
	return unavailable
}

// splitProbeFields preserves input order while separating regular and profiling fields.
func splitProbeFields(fields []candidateField) (regular, profiling []candidateField) {
	for _, field := range fields {
		if field.Profiling {
			profiling = append(profiling, field)
		} else {
			regular = append(regular, field)
		}
	}
	return regular, profiling
}

// collectProbeResults returns sampled fields and deterministic reasons for fields without samples.
func collectProbeResults(
	fields []candidateField,
	metrics map[dcgm.Short]*probedMetric,
	availability map[dcgm.Short]map[string]int,
) ([]probedMetric, []probedUnavailable) {
	var available []probedMetric
	var unavailable []probedUnavailable
	for _, field := range fields {
		metric := metrics[field.ID]
		if len(metric.Samples) != 0 {
			available = append(available, *metric)
			continue
		}
		unavailable = append(unavailable, probedUnavailable{Field: field, Reason: joinedReasons(availability[field.ID])})
	}
	return available, unavailable
}

// addEntityToGroup registers links with their parent identity and other entities with their DCGM group.
func addEntityToGroup(group dcgm.GroupHandle, entity monitoredEntity) error {
	if entity.Pair.EntityGroupId == dcgm.FE_LINK {
		return dcgm.AddLinkEntityToGroup(group, entity.Pair.EntityId, entity.ParentType, entity.ParentID)
	}
	return dcgm.AddEntityToGroup(group, entity.Pair.EntityGroupId, entity.Pair.EntityId)
}

// probeFieldSet queries bounded batches in order and returns the first batch failure.
func probeFieldSet(
	group dcgm.GroupHandle,
	fields []candidateField,
	batchSize int,
	entities []monitoredEntity,
	metrics map[dcgm.Short]*probedMetric,
	availability map[dcgm.Short]map[string]int,
) error {
	for first := 0; first < len(fields); first += batchSize {
		last := min(first+batchSize, len(fields))
		if err := probeFieldBatch(group, fields[first:last], entities, metrics, availability); err != nil {
			return err
		}
	}
	return nil
}

// probeFieldBatch isolates unsupported fields by splitting failed watches and reports all owned cleanup errors.
func probeFieldBatch(
	group dcgm.GroupHandle,
	fields []candidateField,
	entities []monitoredEntity,
	metrics map[dcgm.Short]*probedMetric,
	availability map[dcgm.Short]map[string]int,
) (resultErr error) {
	ids := make([]dcgm.Short, 0, len(fields))
	for _, field := range fields {
		ids = append(ids, field.ID)
	}
	startStage("field group creation")
	fieldGroup, err := probeCreateFieldGroup(fmt.Sprintf("dcgm-exporter-e2e-oracle-%d-%d", fields[0].ID, fields[len(fields)-1].ID), ids)
	if err != nil {
		return probeStage("field group creation", err)
	}
	fieldGroupDestroyed := false
	// Preserve cleanup failures, including early probe errors, and destroy exactly once.
	defer func() {
		if !fieldGroupDestroyed {
			startStage("field group destruction")
			resultErr = errors.Join(resultErr, probeStage("field group destruction", probeDestroyFieldGroup(fieldGroup)))
		}
	}()
	startStage("field watch")
	if err := probeWatchFields(fieldGroup, group, watchFrequencyUsec, 60, 0); err != nil {
		// go-dcgm prefixes registration failures with "error watching fields: " and
		// the later UpdateAllFields failure with "error refreshing watched fields: ",
		// after which the watch is still registered and needs release.
		if !strings.HasPrefix(err.Error(), "error watching fields: ") {
			startStage("field unwatch")
			return errors.Join(probeStage("field update", err), probeStage("field unwatch", probeUnwatchFields(fieldGroup, group)))
		}
		// Release the failed batch before recursive splitting creates replacement groups.
		fieldGroupDestroyed = true
		return handleProbeFieldWatchFailure(group, fieldGroup, fields, entities, metrics, availability, err)
	}
	return collectProbeFieldBatch(group, fieldGroup, ids, entities, metrics, availability)
}

// handleProbeFieldWatchFailure releases a failed batch before classifying or splitting it.
func handleProbeFieldWatchFailure(
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
	fields []candidateField,
	entities []monitoredEntity,
	metrics map[dcgm.Short]*probedMetric,
	availability map[dcgm.Short]map[string]int,
	watchErr error,
) error {
	startStage("field group destruction")
	if cleanupErr := probeDestroyFieldGroup(fieldGroup); cleanupErr != nil {
		return errors.Join(probeStage("field watch", watchErr), probeStage("field group destruction", cleanupErr))
	}
	reason := watchFailureReason(watchErr)
	if reason == "profiling-not-supported" {
		for _, field := range fields {
			availability[field.ID][reason] += len(entities)
		}
		return nil
	}
	if len(fields) > 1 {
		middle := len(fields) / 2
		if err := probeFieldBatch(group, fields[:middle], entities, metrics, availability); err != nil {
			return err
		}
		return probeFieldBatch(group, fields[middle:], entities, metrics, availability)
	}
	if reason == "" {
		reason = "watch-failed"
	}
	availability[fields[0].ID][reason] += len(entities)
	return nil
}

// collectProbeFieldBatch reads values for an accepted watch and unhooks it before its group is destroyed.
func collectProbeFieldBatch(
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
	ids []dcgm.Short,
	entities []monitoredEntity,
	metrics map[dcgm.Short]*probedMetric,
	availability map[dcgm.Short]map[string]int,
) (resultErr error) {
	// Retire the accepted watch before destroying the field group.
	defer func() {
		startStage("field unwatch")
		resultErr = errors.Join(resultErr, probeStage("field unwatch", probeUnwatchFields(fieldGroup, group)))
	}()

	// The watch runs at 100 ms. Waiting for two intervals gives GPM fields
	// their required baseline and current sample before the one oracle read.
	time.Sleep(2 * watchFrequencyUsec * time.Microsecond)
	startStage("field update")
	if err := probeUpdateFields(); err != nil {
		return probeStage("field update", err)
	}
	for _, entity := range entities {
		if err := recordProbeEntityValues(entity, ids, metrics, availability); err != nil {
			return err
		}
	}
	return nil
}

// recordProbeEntityValues stores valid samples and availability reasons for one entity.
func recordProbeEntityValues(
	entity monitoredEntity,
	ids []dcgm.Short,
	metrics map[dcgm.Short]*probedMetric,
	availability map[dcgm.Short]map[string]int,
) error {
	startStage("field read")
	values, err := latestValues(entity, ids)
	if err != nil {
		return probeStage("field read", err)
	}
	seen := make(map[dcgm.Short]struct{}, len(values))
	for _, value := range values {
		metric, found := metrics[value.FieldID]
		if !found {
			continue
		}
		seen[value.FieldID] = struct{}{}
		reason := availabilityReason(value)
		if reason != "available" {
			availability[value.FieldID][reason]++
			continue
		}
		numeric, err := numericValue(value)
		if err != nil {
			return probeStage("field read", err)
		}
		metric.Samples = append(metric.Samples, probedSample{Entity: entity, Value: numeric})
	}
	for _, id := range ids {
		if _, found := seen[id]; !found {
			availability[id]["no-data"]++
		}
	}
	return nil
}

// latestValues uses the parent-aware DCGM query for links and the ordinary entity query otherwise.
func latestValues(entity monitoredEntity, fields []dcgm.Short) ([]dcgm.FieldValue_v1, error) {
	if entity.Pair.EntityGroupId == dcgm.FE_LINK {
		return dcgm.LinkGetLatestValues(entity.Pair.EntityId, entity.ParentType, entity.ParentID, fields)
	}
	return dcgm.EntityGetLatestValues(entity.Pair.EntityGroupId, entity.Pair.EntityId, fields)
}

// numericValue converts numeric DCGM values to the exporter precision and rejects other types.
func numericValue(value dcgm.FieldValue_v1) (float64, error) {
	switch value.FieldType {
	case dcgm.DCGM_FT_INT64:
		return float64(value.Int64()), nil
	case dcgm.DCGM_FT_DOUBLE:
		return exporterFloatValue(value.Float64())
	default:
		return 0, fmt.Errorf("field %d has non-numeric type %d", value.FieldID, value.FieldType)
	}
}

// exporterFloatValue mirrors the collector's fixed-point conversion so the
// generated oracle describes the public Prometheus value, not hidden precision.
func exporterFloatValue(value float64) (float64, error) {
	rendered := strconv.FormatFloat(value, 'f', 6, 64)
	result, err := strconv.ParseFloat(rendered, 64)
	if err != nil {
		return 0, fmt.Errorf("parse exporter-formatted DCGM value %q: %w", rendered, err)
	}
	return result, nil
}

// watchFailureReason classifies known profiling/module watch failures for fixture availability.
func watchFailureReason(err error) string {
	switch message := err.Error(); {
	case strings.Contains(message, "Profiling is not supported"):
		return "profiling-not-supported"
	case strings.Contains(message, "module of DCGM that is not currently loaded"):
		return "module-not-loaded"
	default:
		return ""
	}
}

// availabilityReason distinguishes DCGM error status and blank/unsupported scalar sentinel values.
func availabilityReason(value dcgm.FieldValue_v1) string {
	if value.Status != dcgm.DCGM_ST_OK {
		switch value.Status {
		case dcgm.DCGM_ST_NO_DATA:
			return "no-data"
		case dcgm.DCGM_ST_NOT_SUPPORTED:
			return "not-supported"
		case dcgm.DCGM_ST_NO_PERMISSION:
			return "not-permissioned"
		default:
			return fmt.Sprintf("dcgm-status-%d", value.Status)
		}
	}
	if value.FieldType == dcgm.DCGM_FT_INT64 {
		switch value.Int64() {
		case dcgm.DCGM_FT_INT32_BLANK, dcgm.DCGM_FT_INT64_BLANK:
			return "blank"
		case dcgm.DCGM_FT_INT32_NOT_FOUND, dcgm.DCGM_FT_INT64_NOT_FOUND:
			return "not-found"
		case dcgm.DCGM_FT_INT32_NOT_SUPPORTED, dcgm.DCGM_FT_INT64_NOT_SUPPORTED:
			return "not-supported"
		case dcgm.DCGM_FT_INT32_NOT_PERMISSIONED, dcgm.DCGM_FT_INT64_NOT_PERMISSIONED:
			return "not-permissioned"
		}
	}
	if value.FieldType == dcgm.DCGM_FT_DOUBLE {
		switch value.Float64() {
		case dcgm.DCGM_FT_FP64_BLANK:
			return "blank"
		case dcgm.DCGM_FT_FP64_NOT_FOUND:
			return "not-found"
		case dcgm.DCGM_FT_FP64_NOT_SUPPORTED:
			return "not-supported"
		case dcgm.DCGM_FT_FP64_NOT_PERMISSIONED:
			return "not-permissioned"
		}
	}
	return "available"
}

// joinedReasons orders availability reason names for deterministic fixture output.
func joinedReasons(reasons map[string]int) string {
	if len(reasons) == 0 {
		return "no-data"
	}
	names := make([]string, 0, len(reasons))
	for reason := range reasons {
		names = append(names, reason)
	}
	sort.Strings(names)
	return strings.Join(names, "+")
}
