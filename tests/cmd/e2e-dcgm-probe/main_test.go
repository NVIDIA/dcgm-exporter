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
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

func TestFieldNamesReadsCurrentMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "const_fields.go")
	content := "package dcgm\nvar dcgmFields = map[string]Short{\"DCGM_FI_DEV_GPU_TEMP\": 150}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	names, err := fieldNames(path)
	if err != nil {
		t.Fatal(err)
	}
	if names[dcgm.Short(150)] != "DCGM_FI_DEV_GPU_TEMP" {
		t.Fatalf("field name = %q", names[dcgm.Short(150)])
	}
}

func TestJoinedReasonsIsStable(t *testing.T) {
	got := joinedReasons(map[string]int{"not-supported": 1, "blank": 2})
	if got != "blank+not-supported" {
		t.Fatalf("joinedReasons() = %q", got)
	}
}

func TestWatchFailureReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "profiling", err: errors.New("Profiling is not supported for this group of GPUs"), want: "profiling-not-supported"},
		{name: "module", err: errors.New("This request is serviced by a module of DCGM that is not currently loaded"), want: "module-not-loaded"},
		{name: "unexpected", err: errors.New("another watch failure"), want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := watchFailureReason(test.err); got != test.want {
				t.Fatalf("watchFailureReason() = %q, want %q", got, test.want)
			}
		})
	}
}
