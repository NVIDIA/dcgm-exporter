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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// FieldSupportEnv names the environment variable that holds DCGM field
// support evidence for the GPUs in the run, as one JSON document:
//
//	{"supported": ["<CSV name>", ...], "unsupported": {"<CSV name>": "<reason>"}}
//
// Names are CSV output names, such as DCGM_FI_DEV_GPU_UTIL_RATIO. The e2e CLI
// sets it when it can query the DCGM the exporter uses.
const FieldSupportEnv = "E2E_DCGM_FIELD_SUPPORT"

// fieldSupportDocument is the JSON form of FieldSupportEnv.
type fieldSupportDocument struct {
	Supported   []string          `json:"supported"`
	Unsupported map[string]string `json:"unsupported"`
}

// FieldStatus says what DCGM field support evidence proves about one CSV row.
type FieldStatus int

const (
	// FieldUnknown means no evidence classifies the row.
	FieldUnknown FieldStatus = iota
	// FieldSupported means DCGM returned a value for the row on a GPU in the run.
	FieldSupported
	// FieldUnsupported means DCGM refused the row or returned no value for it
	// on every GPU in the run. It does not prove DCGM lacks the field (event
	// fields are empty until an event), so a missing row is only not required.
	FieldUnsupported
)

// FieldSupport is parsed DCGM field support evidence. The zero value means the
// run has no evidence.
type FieldSupport struct {
	present     bool
	supported   map[string]bool
	unsupported map[string]string
}

// ParseFieldSupport reads the FieldSupportEnv document. Every name must be an
// enabled row of known, so a typo cannot silently drop a required row. An
// empty value returns evidence that is not present.
func ParseFieldSupport(raw string, known []DefaultCounterRow) (FieldSupport, error) {
	if strings.TrimSpace(raw) == "" {
		return FieldSupport{}, nil
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document fieldSupportDocument
	if err := decoder.Decode(&document); err != nil {
		return FieldSupport{}, fmt.Errorf("%s is not a field support JSON document: %w", FieldSupportEnv, err)
	}

	// A second value after the document is malformed input, not more evidence.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return FieldSupport{}, fmt.Errorf("%s holds more than one JSON value", FieldSupportEnv)
	}

	return newFieldSupport(document, known)
}

// newFieldSupport checks a field support document against the known CSV rows:
// names must be enabled rows and appear once, and unsupported rows need a reason.
func newFieldSupport(document fieldSupportDocument, known []DefaultCounterRow) (FieldSupport, error) {
	enabled := make(map[string]bool, len(known))
	for _, row := range known {
		enabled[row.Name] = true
	}

	support := FieldSupport{
		present:     true,
		supported:   make(map[string]bool, len(document.Supported)),
		unsupported: make(map[string]string, len(document.Unsupported)),
	}
	var errs []error
	for _, name := range document.Supported {
		switch {
		case !enabled[name]:
			errs = append(errs, fmt.Errorf("%s supported field %q is not an enabled row of the CSV", FieldSupportEnv, name))
		case support.supported[name]:
			errs = append(errs, fmt.Errorf("%s lists supported field %q twice", FieldSupportEnv, name))
		default:
			support.supported[name] = true
		}
	}

	for name, reason := range document.Unsupported {
		switch {
		case !enabled[name]:
			errs = append(errs, fmt.Errorf("%s unsupported field %q is not an enabled row of the CSV", FieldSupportEnv, name))
		case support.supported[name]:
			errs = append(errs, fmt.Errorf("%s lists field %q as both supported and unsupported", FieldSupportEnv, name))
		case strings.TrimSpace(reason) == "":
			errs = append(errs, fmt.Errorf("%s unsupported field %q has no reason", FieldSupportEnv, name))
		default:
			support.unsupported[name] = reason
		}
	}

	if err := errors.Join(errs...); err != nil {
		return FieldSupport{}, err
	}
	return support, nil
}

// Classify returns what the evidence proves about one CSV row and, for an
// unsupported or unknown row, why. A row the evidence does not list is unknown
// even when evidence is present: the probe could not classify it, so a
// missing family is reported as unchecked coverage, never as a pass.
func (s FieldSupport) Classify(name string) (FieldStatus, string) {
	if !s.present {
		return FieldUnknown, "no DCGM field support evidence for this run (" + FieldSupportEnv + " is not set)"
	}

	if s.supported[name] {
		return FieldSupported, ""
	}

	if reason, ok := s.unsupported[name]; ok {
		return FieldUnsupported, reason
	}

	return FieldUnknown, "the DCGM field support evidence does not classify this row"
}
