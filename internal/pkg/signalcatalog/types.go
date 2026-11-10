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
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

type (
	SignalID       string
	SetID          string
	ProfileID      string
	SourceKind     = appconfig.SourceKind
	EntityScope    = appconfig.EntityScope
	ValueType      string
	OutputKind     string
	InstrumentKind string
	Temporality    string
	TimestampRule  string
	ResetRule      string
	Conversion     string
)

const (
	SourceDCGM     = appconfig.SourceDCGM
	SourceExporter = appconfig.SourceExporter
)

const (
	EntityGPU        = appconfig.EntityGPU
	EntityGPUOrMIGGI = appconfig.EntityGPUOrMIGGI
	EntityVGPU       = appconfig.EntityVGPU
	EntityExporter   = appconfig.EntityExporter
)

const (
	ValueInt64   ValueType = "int64"
	ValueFloat64 ValueType = "float64"
	ValueString  ValueType = "string"
)

const (
	OutputMetric    OutputKind = "metric"
	OutputAttribute OutputKind = "attribute"
)

const (
	InstrumentGauge         InstrumentKind = "gauge"
	InstrumentSum           InstrumentKind = "sum"
	InstrumentUpDownCounter InstrumentKind = "up-down-counter"
)

const (
	TemporalityInstant    Temporality = "instant"
	TemporalityWindow     Temporality = "window"
	TemporalityCumulative Temporality = "cumulative"
)

const (
	TimestampDCGMOrReadTime    TimestampRule = "dcgm-or-read-time"
	TimestampExporterEventTime TimestampRule = "exporter-event-time"
)

const (
	ResetNone            ResetRule = "none"
	ResetSource          ResetRule = "source-reset"
	ResetExporterRestart ResetRule = "exporter-restart"
)

const (
	ConversionIdentity            Conversion = "identity"
	ConversionMillijoulesToJoules Conversion = "millijoules-to-joules"
	ConversionMebibytesToBytes    Conversion = "mebibytes-to-bytes"
	ConversionPercentToRatio      Conversion = "percent-to-ratio"
	ConversionMegahertzToHertz    Conversion = "megahertz-to-hertz"
	ConversionHealthState         Conversion = "health-state"
)

// Source identifies the DCGM field or exporter metric that supplies a signal.
type Source = appconfig.Source

// Attribute is a fixed name/value pair attached to an output.
type Attribute struct {
	Name  string
	Value string
}

// Binding maps a signal to an output metric or attribute, or records why it is omitted.
type Binding struct {
	SourceOverride Source
	Name           string
	OmitReason     string
	OutputKind     OutputKind
	Instrument     InstrumentKind
	Unit           string
	Temporality    Temporality
	Monotonic      bool
	Reset          ResetRule
	Conversion     Conversion
	Attributes     []Attribute
}

// Signal defines a value's source, type, timestamp rule, and output mapping.
type Signal struct {
	ID               SignalID
	Source           Source
	Entity           EntityScope
	ValueType        ValueType
	Timestamp        TimestampRule
	RequiredIdentity []string
	Output           Binding
}

type namedSet struct {
	ID      SetID
	Signals []SignalID
}

type definition struct {
	Version uint
	Signals []Signal
	Sets    []namedSet
}

type outputContract struct {
	kind        OutputKind
	instrument  InstrumentKind
	unit        string
	temporality Temporality
	monotonic   bool
}

// validate checks the catalog version, signals, output mappings, and named sets.
func validate(def definition) error {
	if def.Version != 1 {
		return fmt.Errorf("unsupported catalog version %d", def.Version)
	}
	signals, err := validateSignals(def.Signals)
	if err != nil {
		return err
	}
	if err := validateOutputConflicts(def); err != nil {
		return err
	}
	return validateNamedSets(def.Sets, signals)
}

// validateSignals checks signal definitions and returns a map keyed by signal ID.
func validateSignals(definitions []Signal) (map[SignalID]Signal, error) {
	signals := make(map[SignalID]Signal, len(definitions))
	for i, signal := range definitions {
		if signal.ID == "" {
			return nil, fmt.Errorf("signal %d has empty ID", i)
		}
		if _, ok := signals[signal.ID]; ok {
			return nil, fmt.Errorf("duplicate signal ID %q", signal.ID)
		}
		if err := validateSource(signal.Source); err != nil {
			return nil, fmt.Errorf("signal %q source: %w", signal.ID, err)
		}
		if err := validateSignalMetadata(signal); err != nil {
			return nil, fmt.Errorf("signal %q: %w", signal.ID, err)
		}
		if err := validateNames(signal.RequiredIdentity, "required identity"); err != nil {
			return nil, fmt.Errorf("signal %q: %w", signal.ID, err)
		}
		if err := validateBinding(signal.ID, signal.Output); err != nil {
			return nil, err
		}
		signals[signal.ID] = signal
	}
	return signals, nil
}

// validateNamedSets rejects empty or repeated set IDs and checks each set's members.
func validateNamedSets(definitions []namedSet, signals map[SignalID]Signal) error {
	sets := make(map[SetID]struct{}, len(definitions))
	for i, set := range definitions {
		if set.ID == "" {
			return fmt.Errorf("named set %d has empty ID", i)
		}
		if _, ok := sets[set.ID]; ok {
			return fmt.Errorf("duplicate named set %q", set.ID)
		}
		sets[set.ID] = struct{}{}

		if err := validateSetMembers(set, signals); err != nil {
			return err
		}
	}
	return nil
}

// validateSetMembers rejects repeated or unknown signal IDs and signals omitted
// from the NVIDIA output profile.
func validateSetMembers(set namedSet, signals map[SignalID]Signal) error {
	members := make(map[SignalID]struct{}, len(set.Signals))
	for _, id := range set.Signals {
		if _, ok := members[id]; ok {
			return fmt.Errorf("named set %q has duplicate member %q", set.ID, id)
		}
		members[id] = struct{}{}
		signal, ok := signals[id]
		if !ok {
			return fmt.Errorf("named set %q references unknown signal %q", set.ID, id)
		}
		if signal.Output.Name == "" {
			return fmt.Errorf("named set %q member %q is omitted from nvidia/v1", set.ID, id)
		}
	}
	return nil
}

// validateSource requires a source name. DCGM sources also require a field ID;
// exporter sources must leave it zero.
func validateSource(source Source) error {
	switch source.Kind {
	case SourceDCGM:
		if source.Name == "" || source.FieldID == 0 {
			return errors.New("dcgm source has incomplete identity")
		}
	case SourceExporter:
		if source.Name == "" || source.FieldID != 0 {
			return fmt.Errorf("%s source has invalid identity", source.Kind)
		}
	default:
		return fmt.Errorf("source %q has unknown kind %q", source.Name, source.Kind)
	}
	return nil
}

// validateSignalMetadata checks supported entity scopes, value types, and timestamp rules.
func validateSignalMetadata(signal Signal) error {
	switch signal.Entity {
	case EntityGPU, EntityGPUOrMIGGI, EntityVGPU, EntityExporter:
	default:
		return fmt.Errorf("unknown entity scope %q", signal.Entity)
	}

	switch signal.ValueType {
	case ValueInt64, ValueFloat64, ValueString:
	default:
		return fmt.Errorf("unknown value type %q", signal.ValueType)
	}

	switch signal.Timestamp {
	case TimestampDCGMOrReadTime, TimestampExporterEventTime:
	default:
		return fmt.Errorf("unknown timestamp rule %q", signal.Timestamp)
	}

	return nil
}

// validateNames requires at least one name and rejects empty or repeated names.
func validateNames(names []string, kind string) error {
	if len(names) == 0 {
		return fmt.Errorf("%s is empty", kind)
	}

	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("%s has empty name", kind)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate %s %q", kind, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// validateBinding requires an output mapping or an omission reason and checks
// the mapping's settings.
func validateBinding(id SignalID, binding Binding) error {
	if (binding.Name == "") == (binding.OmitReason == "") {
		return fmt.Errorf("signal %q %s binding must define exactly one of output name or omission", id, ProfileNVIDIA)
	}
	if binding.Name == "" {
		if binding.SourceOverride != (Source{}) || binding.OutputKind != "" || binding.Instrument != "" ||
			binding.Unit != "" || binding.Temporality != "" || binding.Monotonic || binding.Reset != "" ||
			binding.Conversion != "" || len(binding.Attributes) != 0 {
			return fmt.Errorf("signal %q %s omitted binding defines output metadata", id, ProfileNVIDIA)
		}
		return nil
	}

	if binding.SourceOverride != (Source{}) {
		if err := validateSource(binding.SourceOverride); err != nil {
			return fmt.Errorf("signal %q %s source override: %w", id, ProfileNVIDIA, err)
		}
	}
	if err := validateAttributes(binding.Attributes); err != nil {
		return fmt.Errorf("signal %q %s binding: %w", id, ProfileNVIDIA, err)
	}
	if binding.Conversion == "" {
		return fmt.Errorf("signal %q %s binding has empty conversion", id, ProfileNVIDIA)
	}
	switch binding.Conversion {
	case ConversionIdentity, ConversionMillijoulesToJoules, ConversionMebibytesToBytes,
		ConversionPercentToRatio, ConversionMegahertzToHertz, ConversionHealthState:
	default:
		return fmt.Errorf("signal %q %s binding has unknown conversion %q", id, ProfileNVIDIA, binding.Conversion)
	}
	return validateOutputSettings(id, binding)
}

// validateOutputSettings requires metric settings for metrics and forbids them
// for attributes.
func validateOutputSettings(id SignalID, binding Binding) error {
	switch binding.OutputKind {
	case OutputAttribute:
		if binding.Instrument != "" || binding.Unit != "" || binding.Temporality != "" || binding.Monotonic || binding.Reset != "" {
			return fmt.Errorf("signal %q %s attribute binding defines metric settings", id, ProfileNVIDIA)
		}
	case OutputMetric:
		if binding.Instrument == "" || binding.Unit == "" || binding.Temporality == "" || binding.Reset == "" {
			return fmt.Errorf("signal %q %s metric binding has incomplete settings", id, ProfileNVIDIA)
		}
		if err := validateMetricSettings(binding); err != nil {
			return fmt.Errorf("signal %q %s binding: %w", id, ProfileNVIDIA, err)
		}
	default:
		return fmt.Errorf("signal %q %s binding has unknown output kind %q", id, ProfileNVIDIA, binding.OutputKind)
	}
	return nil
}

// validateAttributes requires nonempty attribute names and values, with no repeated names.
func validateAttributes(attributes []Attribute) error {
	seen := make(map[string]struct{}, len(attributes))
	for _, attribute := range attributes {
		if attribute.Name == "" || attribute.Value == "" {
			return errors.New("static attribute has incomplete identity")
		}
		if _, ok := seen[attribute.Name]; ok {
			return fmt.Errorf("duplicate static attribute %q", attribute.Name)
		}
		seen[attribute.Name] = struct{}{}
	}
	return nil
}

// validateMetricSettings checks when values accumulate or reset and whether
// they may decrease, according to the selected metric instrument.
func validateMetricSettings(binding Binding) error {
	switch binding.Reset {
	case ResetNone, ResetSource, ResetExporterRestart:
	default:
		return fmt.Errorf("unknown reset rule %q", binding.Reset)
	}

	switch binding.Instrument {
	case InstrumentGauge:
		if binding.Monotonic || (binding.Temporality != TemporalityInstant && binding.Temporality != TemporalityWindow) || binding.Reset != ResetNone {
			return errors.New("gauge has inconsistent temporality, monotonicity, or reset")
		}
	case InstrumentSum:
		if !binding.Monotonic || binding.Temporality != TemporalityCumulative {
			return errors.New("sum must be monotonic and cumulative")
		}
	case InstrumentUpDownCounter:
		if binding.Monotonic || binding.Temporality != TemporalityCumulative {
			return errors.New("up-down-counter must be non-monotonic and cumulative")
		}
	default:
		return fmt.Errorf("unknown instrument %q", binding.Instrument)
	}
	return nil
}

// validateOutputConflicts rejects incompatible settings or identical attribute
// sets for signals with the same output name.
func validateOutputConflicts(def definition) error {
	contracts := make(map[string]outputContract)
	identities := make(map[string]map[string]struct{})
	for _, signal := range def.Signals {
		binding := signal.Output
		if binding.Name == "" {
			continue
		}
		contract := outputContract{
			kind:        binding.OutputKind,
			instrument:  binding.Instrument,
			unit:        binding.Unit,
			temporality: binding.Temporality,
			monotonic:   binding.Monotonic,
		}
		if previous, ok := contracts[binding.Name]; ok {
			switch {
			case previous.kind != contract.kind:
				return fmt.Errorf("profile %s output %q has conflicting output kind", ProfileNVIDIA, binding.Name)
			case previous.instrument != contract.instrument:
				return fmt.Errorf("profile %s output %q has conflicting instrument", ProfileNVIDIA, binding.Name)
			case previous.unit != contract.unit:
				return fmt.Errorf("profile %s output %q has conflicting unit", ProfileNVIDIA, binding.Name)
			case previous.temporality != contract.temporality:
				return fmt.Errorf("profile %s output %q has conflicting temporality", ProfileNVIDIA, binding.Name)
			case previous.monotonic != contract.monotonic:
				return fmt.Errorf("profile %s output %q has conflicting monotonicity", ProfileNVIDIA, binding.Name)
			}
		} else {
			contracts[binding.Name] = contract
		}

		signature := attributeSignature(binding.Attributes)
		if identities[binding.Name] == nil {
			identities[binding.Name] = make(map[string]struct{})
		}
		if _, ok := identities[binding.Name][signature]; ok {
			return fmt.Errorf("profile %s output %q has duplicate output identity", ProfileNVIDIA, binding.Name)
		}
		identities[binding.Name][signature] = struct{}{}
	}
	return nil
}

// attributeSignature returns a comparison key from sorted attribute name/value pairs.
func attributeSignature(attributes []Attribute) string {
	parts := make([]string, len(attributes))
	for i, attribute := range attributes {
		parts[i] = attribute.Name + "=" + attribute.Value
	}
	slices.Sort(parts)
	return strings.Join(parts, "\x00")
}
