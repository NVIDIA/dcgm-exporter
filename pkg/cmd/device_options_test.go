/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package cmd

import (
	"reflect"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestParseDeviceOptions_Table checks parsed selectors against valid and invalid table cases.
func TestParseDeviceOptions_Table(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    appconfig.DeviceOptions
		wantErr string
	}{
		{name: "flex", input: "f", want: appconfig.DeviceOptions{Flex: true}},
		{name: "all major", input: "g", want: appconfig.DeviceOptions{MajorRange: []int{-1}}},
		{name: "all minor", input: "i", want: appconfig.DeviceOptions{MinorRange: []int{-1}}},
		{name: "major list and range", input: "g:0,2-4", want: appconfig.DeviceOptions{MajorRange: []int{0, 2, 3, 4}}},
		{name: "minor singleton range", input: "i:7-7", want: appconfig.DeviceOptions{MinorRange: []int{7}}},
		{
			name:  "all major and all minor",
			input: "g+i",
			want:  appconfig.DeviceOptions{MajorRange: []int{-1}, MinorRange: []int{-1}},
		},
		{
			name:  "all minor and all major",
			input: "i+g",
			want:  appconfig.DeviceOptions{MajorRange: []int{-1}, MinorRange: []int{-1}},
		},
		{
			name:  "major range and all minor",
			input: "g:0,2-4+i",
			want:  appconfig.DeviceOptions{MajorRange: []int{0, 2, 3, 4}, MinorRange: []int{-1}},
		},
		{
			name:  "all major and minor range",
			input: "g+i:7-7",
			want:  appconfig.DeviceOptions{MajorRange: []int{-1}, MinorRange: []int{7}},
		},
		{
			name:  "ranged minor and ranged major",
			input: "i:1-2+g:3",
			want:  appconfig.DeviceOptions{MajorRange: []int{3}, MinorRange: []int{1, 2}},
		},
		{name: "repeated major index", input: "g:0,0", want: appconfig.DeviceOptions{MajorRange: []int{0}}},
		{
			name:  "repeats across lists and ranges keep first-seen order",
			input: "g:2,0-2,2+i:5,5,4",
			want:  appconfig.DeviceOptions{MajorRange: []int{2, 0, 1}, MinorRange: []int{5, 4}},
		},
		{name: "too many separators", input: "g:0:1", wantErr: "there can only be one specified range"},
		{name: "too many separators in combined selector", input: "g:0:1+i", wantErr: "there can only be one specified range"},
		{name: "flex cannot have range", input: "f:0", wantErr: "no range can be specified"},
		{name: "flex cannot combine before major", input: "f+g", wantErr: "cannot be combined"},
		{name: "flex cannot combine after minor", input: "i+f", wantErr: "cannot be combined"},
		{name: "duplicate major selector", input: "g+g:0", wantErr: "duplicate device option 'g'"},
		{name: "duplicate minor selector", input: "i:0+i:1", wantErr: "duplicate device option 'i'"},
		{name: "empty trailing selector", input: "g+", wantErr: "empty selector"},
		{name: "empty leading selector", input: "+i", wantErr: "empty selector"},
		{name: "unknown selector", input: "x:0", wantErr: "only valid options"},
		{name: "bad number", input: "g:nope", wantErr: "invalid syntax"},
		{name: "empty major range", input: "g:", wantErr: "invalid syntax"},
		{name: "empty range element", input: "g:0,,1", wantErr: "invalid syntax"},
		{name: "bad range start", input: "g:a-2", wantErr: "invalid syntax"},
		{name: "bad range end", input: "g:1-b", wantErr: "invalid syntax"},
		{name: "descending range", input: "g:4-2", wantErr: "must not exceed"},
		{name: "bad range shape", input: "g:1-2-3", wantErr: "range can only be"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDeviceOptions(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParseDeviceOptionsExpansionLimitPerSelector checks each selector respects its range expansion limit.
func TestParseDeviceOptionsExpansionLimitPerSelector(t *testing.T) {
	got, err := parseDeviceOptions("g:0+i:0-1023")
	require.NoError(t, err)
	assert.Equal(t, []int{0}, got.MajorRange)
	assert.Len(t, got.MinorRange, 1024)
	assert.Equal(t, 0, got.MinorRange[0])
	assert.Equal(t, 1023, got.MinorRange[1023])

	_, err = parseDeviceOptions("i:0-1024")
	require.ErrorContains(t, err, "more than 1024 indices")
}

// TestDeviceOptionsFlagValue proves the CLI defaults can recreate each
// resolved device-selector shape that the command can display.
func TestDeviceOptionsFlagValue(t *testing.T) {
	tests := []struct {
		name    string
		options appconfig.DeviceOptions
		want    string
	}{
		{name: "flex", options: appconfig.DeviceOptions{Flex: true}, want: "f"},
		{name: "all major", options: appconfig.DeviceOptions{MajorRange: []int{-1}}, want: "g"},
		{name: "all minor", options: appconfig.DeviceOptions{MinorRange: []int{-1}}, want: "i"},
		{
			name:    "combined explicit ranges",
			options: appconfig.DeviceOptions{MajorRange: []int{1, 3}, MinorRange: []int{2, 4}},
			want:    "g:1,3+i:2,4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, deviceOptionsFlagValue(tt.options))
		})
	}
}

// FuzzParseDeviceOptions checks that arbitrary input preserves the configuration behavior contract.
func FuzzParseDeviceOptions(f *testing.F) {
	for _, seed := range []string{
		"f",
		"g",
		"i",
		"g:0,2-4+i:7",
		"g:0-1023",
		"i:0-1024",
		"g:9223372036854775807-9223372036854775807",
		"f+g",
		"g:1-2-3",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		got, err := parseDeviceOptions(input)
		if err != nil {
			return
		}

		assertValidDeviceOptions(t, input, got)
		assertDeviceOptionsDeterministic(t, input, got)
	})
}

// assertValidDeviceOptions checks parsed device options shared by selector tests.
func assertValidDeviceOptions(t *testing.T, input string, options appconfig.DeviceOptions) {
	t.Helper()
	if options.Flex {
		if options.MajorRange != nil || options.MinorRange != nil {
			t.Fatalf("flex selector returned ranges: %#v", options)
		}
	} else if options.MajorRange == nil && options.MinorRange == nil {
		t.Fatalf("successful non-flex selector returned no ranges: %q", input)
	}

	assertValidDeviceOptionRange(t, input, options.MajorRange)
	assertValidDeviceOptionRange(t, input, options.MinorRange)
}

// assertValidDeviceOptionRange checks the bounds of a parsed selector in tests.
func assertValidDeviceOptionRange(t *testing.T, input string, indices []int) {
	t.Helper()
	if indices == nil {
		return
	}

	if len(indices) == 0 {
		t.Fatalf("successful selector returned an empty range: %q", input)
	}

	if len(indices) > int(dcgm.MAX_NUM_CPU_CORES) {
		t.Fatalf("selector returned %d indices, limit is %d", len(indices), dcgm.MAX_NUM_CPU_CORES)
	}

	if len(indices) == 1 && indices[0] == -1 {
		return
	}

	for _, index := range indices {
		if index < 0 {
			t.Fatalf("selector returned negative index %d outside the all-devices sentinel", index)
		}
	}
}

// assertDeviceOptionsDeterministic checks selector parsing returns a stable order.
func assertDeviceOptionsDeterministic(t *testing.T, input string, options appconfig.DeviceOptions) {
	t.Helper()
	again, err := parseDeviceOptions(input)
	if err != nil {
		t.Fatalf("selector result changed between identical parses: %v", err)
	}

	if !reflect.DeepEqual(options, again) {
		t.Fatalf("selector parsing is not deterministic:\nfirst:  %#v\nsecond: %#v", options, again)
	}
}
