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

package k8s

import (
	"errors"
	"strings"
	"testing"
)

// TestRemovedValuesError checks which errors count as the chart's rejection of removed values.
func TestRemovedValuesError(t *testing.T) {
	full := errors.New("kubernetes.enablePodLabels, service.webReadTimeout: removed chart values. In your values file:\n  " +
		strings.Join(removedValueMoves(), "\n  "))

	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "no error", err: nil, wantErr: true},
		{name: "unrelated error", err: errors.New("timed out waiting for the condition"), wantErr: true},
		{name: "rejection without the replacements", err: errors.New("removed chart values"), wantErr: true},
		{name: "full rejection", err: full},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := removedValuesError(tt.err); (got != nil) != tt.wantErr {
				t.Errorf("removedValuesError() = %v, wantErr %t", got, tt.wantErr)
			}
		})
	}
}
