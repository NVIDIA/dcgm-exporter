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

package devicewatcher

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// ExplainRefusedFields adds a plain explanation to a watch failure when DCGM
// refused a field group with its bad-parameter status. It creates a one-field
// group for each field of groups and names every field the hostengine refuses.
// Callers pass every watch group they want named (the failed watch's scopes, or
// every entity watch list at start-up), so one message names all fields to
// fix. The probe runs only here, on the failure path. When no field is refused
// on its own, or a probe fails for another reason, it returns cause unchanged.
func ExplainRefusedFields(client dcgmprovider.DCGM, groups []FieldWatchGroup, cause error) error {
	var createErr *fieldGroupCreateError
	if !errors.As(cause, &createErr) || !isBadParam(cause) {
		return cause
	}

	var parts []string
	for _, group := range groups {
		refused, err := fieldsRefusedByHostengine(client, group.Fields)
		if err != nil {
			return cause
		}
		if len(refused) > 0 {
			parts = append(parts, fmt.Sprintf("watch group %q: %s",
				group.Name, strings.Join(fieldDisplayNames(client, refused), ", ")))
		}
	}
	if len(parts) == 0 {
		return cause
	}

	return &RefusedFieldsError{parts: parts, cause: cause}
}

// RefusedFieldsError reports watched fields that the hostengine refuses. Its
// message names the fields. Unwrap returns the original DCGM error.
type RefusedFieldsError struct {
	parts []string
	cause error
}

// Error returns the user-facing message: one part per watch group with refused
// fields, what to do, and the original DCGM error text.
func (e *RefusedFieldsError) Error() string {
	return fmt.Sprintf("the DCGM hostengine refused these fields: %s. Run the DCGM version this exporter "+
		"supports, or remove these fields from the metric file. DCGM error: %v", strings.Join(e.parts, "; "), e.cause)
}

// Unwrap returns the original DCGM error, so callers can still match it with
// errors.Is and errors.As.
func (e *RefusedFieldsError) Unwrap() error {
	return e.cause
}

// fieldsRefusedByHostengine returns the fields the hostengine refuses. It
// creates a one-field group for each field and destroys it again. Only DCGM's
// bad-parameter refusal counts as a refused field; any other probe or cleanup
// failure is returned, so the caller does not blame the fields.
func fieldsRefusedByHostengine(client dcgmprovider.DCGM, fields []dcgm.Short) ([]dcgm.Short, error) {
	var refused []dcgm.Short
	for _, field := range fields {
		group, err := newFieldGroupWithClient(client, []dcgm.Short{field})
		if isBadParam(err) {
			refused = append(refused, field)
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("probe field %d: %w", field, err)
		}

		if err := client.FieldGroupDestroy(group); err != nil {
			return nil, fmt.Errorf("remove the probe group for field %d: %w", field, err)
		}
	}

	return refused, nil
}

// dcgmBadParamText is DCGM's message for its bad-parameter status. go-dcgm's
// FieldGroupCreate returns it as plain text rather than as a *dcgm.Error.
const dcgmBadParamText = "Bad parameter passed to function"

// isBadParam reports whether err is DCGM's bad-parameter status, either as a
// *dcgm.Error or as go-dcgm's plain-text message. A hostengine returns it, for
// example, when a field group names a field ID it does not know.
func isBadParam(err error) bool {
	if err == nil {
		return false
	}

	var dcgmErr *dcgm.Error
	if errors.As(err, &dcgmErr) {
		return int(dcgmErr.Code) == dcgm.DCGM_ST_BADPARAM
	}

	return strings.HasSuffix(err.Error(), dcgmBadParamText)
}

// fieldDisplayNames returns each field as "name (field ID)", or just
// "field ID n" when the local DCGM library has no name for it.
func fieldDisplayNames(client dcgmprovider.DCGM, fields []dcgm.Short) []string {
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		id := strconv.Itoa(int(field))
		meta, err := client.FieldGetByID(field)
		if err != nil || meta.Tag == "" {
			names = append(names, "field ID "+id)
			continue
		}
		names = append(names, meta.Tag+" (field ID "+id+")")
	}
	return names
}
