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

package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// cumulativePollContextError keeps DCGM cursor context available both in the
// wrapped error string and as structured log attributes.
type cumulativePollContextError struct {
	operation        string
	groupHandle      uintptr
	fieldGroupHandle uintptr
	sinceTimestamp   time.Time
	err              error
}

func newCumulativePollContextError(
	operation string,
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
	since time.Time,
	err error,
) error {
	return &cumulativePollContextError{
		operation:        operation,
		groupHandle:      group.GetHandle(),
		fieldGroupHandle: fieldGroup.GetHandle(),
		sinceTimestamp:   since,
		err:              err,
	}
}

func (e *cumulativePollContextError) Error() string {
	return fmt.Sprintf("%s group_handle=%d field_group_handle=%d since_timestamp=%s: %v",
		e.operation,
		e.groupHandle,
		e.fieldGroupHandle,
		e.sinceTimestamp.UTC().Format(time.RFC3339Nano),
		e.err)
}

func (e *cumulativePollContextError) Unwrap() error {
	return e.err
}

func (e *cumulativePollContextError) logAttrs() []slog.Attr {
	return []slog.Attr{
		slog.Uint64("group_handle", uint64(e.groupHandle)),
		slog.Uint64("field_group_handle", uint64(e.fieldGroupHandle)),
		slog.String("since_timestamp", e.sinceTimestamp.UTC().Format(time.RFC3339Nano)),
	}
}

func logCumulativeEventPollFailure(collector string, err error) {
	attrs := []slog.Attr{
		slog.String("collector", collector),
		slog.String("error", err.Error()),
	}

	var contextErr *cumulativePollContextError
	if errors.As(err, &contextErr) {
		attrs = append(attrs, contextErr.logAttrs()...)
	}

	slog.LogAttrs(context.Background(), slog.LevelWarn, "cumulative event poll failed", attrs...)
}
