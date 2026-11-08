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
	"fmt"
	"testing"
)

// TestGPUOperatorExpectedExporterImage checks explicit and fallback image selection, including missing input.
func TestGPUOperatorExpectedExporterImage(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		exporter string
		want     string
		wantErr  bool
	}{
		{name: "explicit", explicit: "override/image:tag", exporter: "repo/image:tag", want: "override/image:tag"},
		{name: "explicit ignores exporter digest", explicit: "override/image:tag", exporter: "repo/image:tag@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", want: "override/image:tag"},
		{name: "exporter", exporter: "repo/image:tag", want: "repo/image:tag"},
		{name: "exporter with tag and digest", exporter: "repo/image:tag@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", want: "repo/image:tag@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{name: "missing image", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := gpuOperatorExpectedExporterImage(test.explicit, test.exporter)
			if test.wantErr {
				if err == nil {
					t.Fatal("gpuOperatorExpectedExporterImage() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("gpuOperatorExpectedExporterImage() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("gpuOperatorExpectedExporterImage() = %q, want %q", got, test.want)
			}
		})
	}
}

// gpuOperatorExpectedExporterImage prefers an explicit override and errors when neither image is set.
func gpuOperatorExpectedExporterImage(explicit, exporter string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if exporter == "" {
		return "", fmt.Errorf("GPU Operator image check requires -exporter-image")
	}
	return exporter, nil
}
