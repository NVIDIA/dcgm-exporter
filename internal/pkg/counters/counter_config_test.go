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

package counters

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	stdos "os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestFilterWatchSources checks capability filtering, unused metadata removal, and independent source lists.
func TestFilterWatchSources(t *testing.T) {
	profile := appconfig.WatchSource{Source: appconfig.Source{Kind: appconfig.SourceDCGM, FieldID: uint16(dcgm.DCGM_FI_PROF_DRAM_ACTIVE)}}
	temperature := appconfig.WatchSource{Source: appconfig.Source{Kind: appconfig.SourceDCGM, FieldID: uint16(dcgm.DCGM_FI_DEV_GPU_TEMP)}}
	metadata := appconfig.WatchSource{Source: appconfig.Source{Kind: appconfig.SourceDCGM, FieldID: uint16(dcgm.DCGM_FI_DEV_PCIE_LINK_GEN)}, Metadata: true}
	exporter := appconfig.WatchSource{Source: appconfig.Source{Kind: appconfig.SourceExporter, Name: DCGMExpXIDErrorsTotal}}
	for _, tt := range []struct {
		name          string
		sources, want []appconfig.WatchSource
		groups        []dcgm.MetricGroup
	}{
		{name: "unsupported profiling omits unused metadata", sources: []appconfig.WatchSource{profile, metadata}},
		{name: "ordinary sources retain identity", sources: []appconfig.WatchSource{profile, temperature, metadata}, want: []appconfig.WatchSource{temperature, metadata}},
		{name: "supported profiling", sources: []appconfig.WatchSource{profile, metadata}, want: []appconfig.WatchSource{profile, metadata}, groups: []dcgm.MetricGroup{{FieldIds: []uint{uint(dcgm.DCGM_FI_PROF_DRAM_ACTIVE)}}}},
		{name: "exporter backing fields", sources: []appconfig.WatchSource{exporter, metadata}, want: []appconfig.WatchSource{exporter, metadata}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := append([]appconfig.WatchSource(nil), tt.sources...)
			input := []appconfig.OTLPWatchCollection{{Name: "telemetry", Interval: 5000, Sources: tt.sources}}
			got := FilterWatchSources(input, NewDCPCapabilities(tt.groups))
			if tt.want == nil {
				require.Empty(t, got)
			} else {
				require.Equal(t, []appconfig.OTLPWatchCollection{{Name: "telemetry", Interval: 5000, Sources: tt.want}}, got)
				got[0].Sources[0].Source.Name = "changed"
			}
			require.Equal(t, before, input[0].Sources)
		})
	}
}

// TestConfigMapCSVErrors checks that a CSV parse error in the metrics key is
// returned wrapped, with the ConfigMap and key named, and that a ConfigMap with
// no metric rows reports that no metrics were found.
func TestConfigMapCSVErrors(t *testing.T) {
	const wantPrefix = "malformed ConfigMap 'default:configmap1' key 'metrics': "

	tests := []struct {
		name      string
		data      string
		wantErr   string
		wantIs    error
		wantLine  int
		wantParse bool
	}{
		{
			name:      "wrong field count",
			data:      "DCGM_FI_DEV_GPU_TEMP, gauge, temperature\nDCGM_FI_DEV_POWER_USAGE, gauge\n",
			wantErr:   wantPrefix,
			wantIs:    csv.ErrFieldCount,
			wantLine:  2,
			wantParse: true,
		},
		{
			name:      "unclosed quote",
			data:      "DCGM_FI_DEV_GPU_TEMP,gauge,\"temperature\n",
			wantErr:   wantPrefix,
			wantIs:    csv.ErrQuote,
			wantLine:  1,
			wantParse: true,
		},
		{
			name:    "empty",
			data:    "",
			wantErr: wantPrefix + "no metrics found",
		},
		{
			name:    "comments only",
			data:    "# DCGM_FI_DEV_GPU_TEMP, gauge, temperature\n# another comment\n",
			wantErr: wantPrefix + "no metrics found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset(&v1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "configmap1", Namespace: "default"},
				Data:       map[string]string{"metrics": tt.data},
			})
			source := appconfig.ConfigMapMetricSource{Namespace: "default", Name: "configmap1"}

			records, err := readConfigMapSource(context.Background(), clientset, source)

			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, records)

			// A parse error must stay reachable through the wrap so callers see the parser's line.
			var parseErr *csv.ParseError
			require.Equal(t, tt.wantParse, errors.As(err, &parseErr))
			if tt.wantParse {
				require.ErrorIs(t, err, tt.wantIs)
				require.Equal(t, tt.wantLine, parseErr.Line)
			}
		})
	}
}

// TestValidConfigMap reads a ConfigMap with one metric line under the metrics key and expects one record and
// no error.
func TestValidConfigMap(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap1",
			Namespace: "default",
		},
		Data: map[string]string{"metrics": "DCGM_FI_DEV_GPU_TEMP, gauge, temperature"},
	})

	source := appconfig.ConfigMapMetricSource{
		Namespace: "default",
		Name:      "configmap1",
	}
	records, err := readConfigMapSource(context.Background(), clientset, source)
	require.NoError(t, err, "Should have succeeded")
	require.Len(t, records, 1, "Should have 1 record")
}

// TestInvalidConfigMapData reads a ConfigMap that keeps its metric line under a key other than metrics and
// expects an error and no records.
func TestInvalidConfigMapData(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap1",
			Namespace: "default",
		},
		Data: map[string]string{"bad": "DCGM_FI_DEV_GPU_TEMP, gauge, temperature"},
	})

	source := appconfig.ConfigMapMetricSource{
		Namespace: "default",
		Name:      "configmap1",
	}
	records, err := readConfigMapSource(context.Background(), clientset, source)
	require.Error(t, err, "Should have returned an error")
	require.Empty(t, records, "Should have no records")
}

// TestInvalidConfigMapName asks for a ConfigMap name that does not exist in the namespace and expects an
// error and no records.
func TestInvalidConfigMapName(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap",
			Namespace: "default",
		},
	})

	source := appconfig.ConfigMapMetricSource{
		Namespace: "default",
		Name:      "configmap1",
	}
	records, err := readConfigMapSource(context.Background(), clientset, source)
	require.Error(t, err, "Should have returned an error")
	require.Empty(t, records, "Should have no records")
}

// TestInvalidConfigMapNamespace asks for a ConfigMap in a namespace that holds none and expects an error and
// no records.
func TestInvalidConfigMapNamespace(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap",
			Namespace: "c1",
		},
	})

	source := appconfig.ConfigMapMetricSource{
		Namespace: "default",
		Name:      "configmap1",
	}
	records, err := readConfigMapSource(context.Background(), clientset, source)
	require.Error(t, err, "Should have returned an error")
	require.Empty(t, records, "Should have no records")
}

// TestGetCounterSetLoadsResolvedConfigMapSource checks that a resolved ConfigMap source supplies counters to a registry build.
func TestGetCounterSetLoadsResolvedConfigMapSource(t *testing.T) {
	originalGetKubeClient := getKubeClient
	t.Cleanup(func() {
		getKubeClient = originalGetKubeClient
	})
	getKubeClient = func() (kubernetes.Interface, error) {
		return fake.NewSimpleClientset(&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "configmap1",
				Namespace: "default",
			},
			Data: map[string]string{"metrics": "DCGM_FI_DEV_GPU_TEMP, gauge, temperature"},
		}), nil
	}

	c := appconfig.Config{
		MetricSource: appconfig.MetricSource{
			Kind: appconfig.MetricSourceConfigMap,
			ConfigMap: appconfig.ConfigMapMetricSource{
				Namespace: "default",
				Name:      "configmap1",
			},
		},
	}

	got, err := GetCounterSet(context.Background(), &c, DCPCapabilities{})

	require.NoError(t, err)
	require.Len(t, got.DCGMCounters, 1)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", got.DCGMCounters[0].FieldName)
}

// TestGetCounterSetReturnsConfigMapLoadError points the metric source at a ConfigMap that does
// not exist and expects a nil counter set and a "could not retrieve ConfigMap" error.
func TestGetCounterSetReturnsConfigMapLoadError(t *testing.T) {
	originalGetKubeClient := getKubeClient
	t.Cleanup(func() {
		getKubeClient = originalGetKubeClient
	})
	getKubeClient = func() (kubernetes.Interface, error) {
		return fake.NewSimpleClientset(), nil
	}

	c := appconfig.Config{
		MetricSource: appconfig.MetricSource{
			Kind: appconfig.MetricSourceConfigMap,
			ConfigMap: appconfig.ConfigMapMetricSource{
				Namespace: "default",
				Name:      "configmap-does-not-exist",
			},
		},
	}

	got, err := GetCounterSet(context.Background(), &c, DCPCapabilities{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not retrieve ConfigMap")
	assert.Nil(t, got)
}

// TestGetCounterSetReturnsNilForMetricFileLoadError points the metric source at a missing CSV
// file and expects an error and a nil counter set.
func TestGetCounterSetReturnsNilForMetricFileLoadError(t *testing.T) {
	c := appconfig.Config{
		MetricSource: appconfig.MetricSource{
			Kind: appconfig.MetricSourceFile,
			File: t.TempDir() + "/missing.csv",
		},
	}

	got, err := GetCounterSet(context.Background(), &c, DCPCapabilities{})

	require.Error(t, err)
	assert.Nil(t, got)
}

// TestGetCounterSetRejectsInvalidUTF8HelpFromFile loads a metrics file whose first record has a HELP text
// with a byte that is not valid UTF-8, and expects a nil set and an error naming the bad HELP text and metric
// record 1.
func TestGetCounterSetRejectsInvalidUTF8HelpFromFile(t *testing.T) {
	path := t.TempDir() + "/invalid-help.csv"
	err := stdos.WriteFile(path, []byte("# comment before the first metric\nDCGM_FI_DEV_GPU_TEMP,gauge,bad\xff help\n"), 0o600)
	require.NoError(t, err)
	c := appconfig.Config{MetricSource: appconfig.MetricSource{
		Kind: appconfig.MetricSourceFile,
		File: path,
	}}

	got, err := GetCounterSet(context.Background(), &c, DCPCapabilities{})

	require.ErrorContains(t, err, "invalid UTF-8 HELP text")
	require.ErrorContains(t, err, "metric record 1")
	assert.Nil(t, got)
}

// TestReadCSVFileWithLinesCountsCommentAndBlankLines checks that each record
// keeps the line it starts on in the file.
func TestReadCSVFileWithLinesCountsCommentAndBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.csv")
	text := "# header comment\n\nDCGM_FI_DEV_GPU_TEMP,gauge,Temp\n# between\n\n\nDCGM_FI_DEV_FB_USED,gauge,Used\n"
	require.NoError(t, stdos.WriteFile(path, []byte(text), 0o600))

	records, lines, err := ReadCSVFileWithLines(path)

	require.NoError(t, err)
	assert.Equal(t, [][]string{
		{"DCGM_FI_DEV_GPU_TEMP", "gauge", "Temp"},
		{"DCGM_FI_DEV_FB_USED", "gauge", "Used"},
	}, records)
	assert.Equal(t, []int{3, 7}, lines)
}

// TestGetCounterSetNamesTheFileLineOfABadRecord checks that a bad type or an
// unsupported field in a metric file is reported with the line it is on, even
// when comments and blank lines come before it, and that a source without line
// numbers reports the same problem without a line.
func TestGetCounterSetNamesTheFileLineOfABadRecord(t *testing.T) {
	prefix := "# comment\n\nDCGM_FI_DEV_GPU_TEMP,gauge,Temp\n# another comment\n\n"
	for _, tc := range []struct {
		name string
		row  string
		want string
	}{
		{"bad type", "DCGM_FI_DEV_FB_USED,histogram,Used\n", "line 6: unsupported Prometheus metric type"},
		{"retired field", "DCGM_FI_DEV_PCIE_TX_THROUGHPUT,gauge,Tx\n", "line 6: DCGM field \"DCGM_FI_DEV_PCIE_TX_THROUGHPUT\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "metrics.csv")
			require.NoError(t, stdos.WriteFile(path, []byte(prefix+tc.row), 0o600))
			c := appconfig.Config{MetricSource: appconfig.MetricSource{Kind: appconfig.MetricSourceFile, File: path}}

			got, err := GetCounterSet(context.Background(), &c, DCPCapabilities{})

			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, got)
		})
	}

	inline := appconfig.Config{MetricSource: appconfig.MetricSource{
		Kind: appconfig.MetricSourceInline,
		Fields: []appconfig.MetricField{
			{Name: "DCGM_FI_DEV_FB_USED", PrometheusType: "histogram", Help: "Used"},
		},
	}}
	_, err := GetCounterSet(context.Background(), &inline, DCPCapabilities{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "line ")
}

// TestGetCounterSetRejectsUnresolvedMetricSource proves source selection is a
// command-layer responsibility. Counter loading must not recreate legacy file
// or ConfigMap fallback rules while it builds a registry.
func TestGetCounterSetRejectsUnresolvedMetricSource(t *testing.T) {
	got, err := GetCounterSet(context.Background(), &appconfig.Config{}, DCPCapabilities{})

	require.Error(t, err)
	assert.ErrorContains(t, err, "metric source is not resolved")
	assert.Nil(t, got)
}

// TestGetCounterSetRejectsUnknownMetricSource keeps invalid resolved settings from
// accidentally being treated as a CSV file path.
func TestGetCounterSetRejectsUnknownMetricSource(t *testing.T) {
	config := &appconfig.Config{
		MetricSource: appconfig.MetricSource{Kind: "unexpected"},
	}

	counterSet, err := GetCounterSet(context.Background(), config, DCPCapabilities{})

	assert.Nil(t, counterSet)
	assert.ErrorContains(t, err, `unsupported metric source "unexpected"`)
}

// TestGetCounterSetLoadsInlineMetricSource passes one inline metric field and expects it
// back as the only DCGM counter.
func TestGetCounterSetLoadsInlineMetricSource(t *testing.T) {
	c := appconfig.Config{
		MetricSource: appconfig.MetricSource{
			Kind: appconfig.MetricSourceInline,
			Fields: []appconfig.MetricField{
				{
					Name:           "DCGM_FI_DEV_GPU_TEMP",
					PrometheusType: "gauge",
					Help:           "temperature",
				},
			},
		},
	}

	got, err := GetCounterSet(context.Background(), &c, DCPCapabilities{})

	require.NoError(t, err)
	require.Len(t, got.DCGMCounters, 1)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", got.DCGMCounters[0].FieldName)
}

// TestExtractCounters accepts a recognized DCGM scalar field and rejects an unknown field name.
func TestExtractCounters(t *testing.T) {
	tests := []struct {
		name  string
		field string
		valid bool
	}{
		{
			name:  "Valid Input DCGM_FI_DEV_GPU_TEMP",
			field: "DCGM_FI_DEV_GPU_TEMP, gauge, temperature\n",
			valid: true,
		},
		{
			name:  "Invalid Input DCGM_EXP_XID_ERRORS_COUNTXXX",
			field: "DCGM_EXP_XID_ERRORS_COUNTXXX, gauge, temperature\n",
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractCountersHelper(t, tt.field, tt.valid)
		})
	}
}

// TestExtractCountersRejectsRetiredPCIeThroughputFields expects the old PCIe throughput
// fields and their legacy aliases to fail with an error naming the field and its PROF replacement.
func TestExtractCountersRejectsRetiredPCIeThroughputFields(t *testing.T) {
	tests := []struct {
		name        string
		fieldName   string
		replacement string
	}{
		{
			name:        "canonical TX field",
			fieldName:   "DCGM_FI_DEV_PCIE_TX_THROUGHPUT",
			replacement: "DCGM_FI_PROF_PCIE_TX_BYTES",
		},
		{
			name:        "canonical RX field",
			fieldName:   "DCGM_FI_DEV_PCIE_RX_THROUGHPUT",
			replacement: "DCGM_FI_PROF_PCIE_RX_BYTES",
		},
		{
			name:        "legacy TX alias",
			fieldName:   "dcgm_pcie_tx_throughput",
			replacement: "DCGM_FI_PROF_PCIE_TX_BYTES",
		},
		{
			name:        "legacy RX alias",
			fieldName:   "dcgm_pcie_rx_throughput",
			replacement: "DCGM_FI_PROF_PCIE_RX_BYTES",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractCounters([][]string{{tt.fieldName, "gauge", "PCIe throughput"}}, DCPCapabilities{})

			require.Error(t, err)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), "is no longer supported")
			assert.Contains(t, err.Error(), tt.fieldName)
			assert.Contains(t, err.Error(), tt.replacement)
		})
	}
}

// TestExtractCountersRejectsFieldsNotSafeForGenericScalarExport expects bitmaps, packed
// values, lists and fields that read zero on failure to be refused, with an error naming the field,
// the reason and, where one exists, the field to use instead.
func TestExtractCountersRejectsFieldsNotSafeForGenericScalarExport(t *testing.T) {
	tests := []struct {
		name        string
		fieldName   string
		reason      string
		replacement string
	}{
		{
			name:      "packed compute capability",
			fieldName: "DCGM_FI_CUDA_GPU_COMPUTE_CAPABILITY",
			reason:    "major and minor compute-capability",
		},
		{
			name:        "raw P2P bitmap",
			fieldName:   "DCGM_FI_DEV_NVLINK_P2P_STATUS",
			reason:      "P2P bitmap",
			replacement: DCGMExpP2PStatus,
		},
		{
			name:        "raw P2P bitmap legacy alias",
			fieldName:   "DCGM_FI_DEV_P2P_NVLINK_STATUS",
			reason:      "P2P bitmap",
			replacement: DCGMExpP2PStatus,
		},
		{
			name:        "clock event bitmap",
			fieldName:   "DCGM_FI_DEV_CLOCKS_EVENT_REASONS",
			reason:      "clock-event bitmap",
			replacement: "DCGM_EXP_CLOCK_EVENTS_COUNT",
		},
		{
			name:      "supported clock list",
			fieldName: "DCGM_FI_DEV_CLOCKS_SUPPORTED",
			reason:    "supported-clock list",
		},
		{
			name:      "requested power profile bitmap",
			fieldName: "DCGM_FI_DEV_BOARD_POWER_PROFILE_REQUESTED_MASK",
			reason:    "power-profile bitmask",
		},
		{
			name:      "enforced power profile bitmap",
			fieldName: "DCGM_FI_DEV_BOARD_POWER_PROFILE_ENFORCED_MASK",
			reason:    "power-profile bitmask",
		},
		{
			name:      "supported power profile bitmap",
			fieldName: "DCGM_FI_DEV_BOARD_POWER_PROFILE_SUPPORTED_MASK",
			reason:    "power-profile bitmask",
		},
		{
			name:        "fabric health mask",
			fieldName:   "DCGM_FI_DEV_FABRIC_HEALTH_MASK",
			reason:      "fabric-health mask",
			replacement: "DCGM_FI_DEV_FABRIC_HEALTH_SUMMARY",
		},
		{
			name:      "ConnectX correctable error mask",
			fieldName: "DCGM_FI_DEV_CONNECTX_CORRECTABLE_ERROR_MASK",
			reason:    "ConnectX error bitmask",
		},
		{
			name:      "ConnectX uncorrectable error mask",
			fieldName: "DCGM_FI_DEV_CONNECTX_UNCORRECTABLE_ERROR_MASK",
			reason:    "ConnectX error bitmask",
		},
		{
			name:      "MIG mode failure-to-zero field",
			fieldName: "DCGM_FI_DEV_MIG_MODE",
			reason:    "OK status with a zero value",
		},
		{
			name:      "MIG slice limit failure-to-zero field",
			fieldName: "DCGM_FI_DEV_MIG_MAX_SLICES",
			reason:    "OK status with a zero value",
		},
		{
			name:      "confidential-compute mode failure-to-zero field",
			fieldName: "DCGM_FI_DEV_CC_MODE",
			reason:    "OK status with a zero value",
		},
		{
			name:      "GPM support failure-to-zero field",
			fieldName: "DCGM_FI_DEV_GPM_SUPPORT",
			reason:    "OK status with a zero value",
		},
		{
			name:      "first CPU affinity bitmap",
			fieldName: "DCGM_FI_DEV_CPU_AFFINITY_0",
			reason:    "CPU affinity bitmap",
		},
		{
			name:      "second CPU affinity bitmap",
			fieldName: "DCGM_FI_DEV_CPU_AFFINITY_1",
			reason:    "CPU affinity bitmap",
		},
		{
			name:      "third CPU affinity bitmap",
			fieldName: "DCGM_FI_DEV_CPU_AFFINITY_2",
			reason:    "CPU affinity bitmap",
		},
		{
			name:      "fourth CPU affinity bitmap",
			fieldName: "DCGM_FI_DEV_CPU_AFFINITY_3",
			reason:    "CPU affinity bitmap",
		},
		{
			name:      "first memory affinity bitmap",
			fieldName: "DCGM_FI_DEV_MEMORY_AFFINITY_0",
			reason:    "memory affinity bitmap",
		},
		{
			name:      "second memory affinity bitmap",
			fieldName: "DCGM_FI_DEV_MEMORY_AFFINITY_1",
			reason:    "memory affinity bitmap",
		},
		{
			name:      "third memory affinity bitmap",
			fieldName: "DCGM_FI_DEV_MEMORY_AFFINITY_2",
			reason:    "memory affinity bitmap",
		},
		{
			name:      "fourth memory affinity bitmap",
			fieldName: "DCGM_FI_DEV_MEMORY_AFFINITY_3",
			reason:    "memory affinity bitmap",
		},
		{
			name:      "memory affinity bitmap legacy alias",
			fieldName: "DCGM_FI_DEV_MEM_AFFINITY_0",
			reason:    "memory affinity bitmap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractCounters([][]string{{tt.fieldName, "gauge", "unsafe field"}}, DCPCapabilities{})

			require.Error(t, err)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), tt.fieldName)
			assert.Contains(t, err.Error(), tt.reason)
			if tt.replacement != "" {
				assert.Contains(t, err.Error(), tt.replacement)
			}
		})
	}
}

// TestExtractCounters_EdgeAndFailurePaths trims valid records, recognizes exporter counters, and rejects malformed or unsupported input.
func TestExtractCounters_EdgeAndFailurePaths(t *testing.T) {
	tests := []struct {
		name    string
		records [][]string
		dcp     DCPCapabilities
		want    *CounterSet
		wantErr string
	}{
		{
			name: "trims fields and skips empty rows",
			records: [][]string{
				{},
				{" DCGM_FI_DEV_GPU_TEMP ", " gauge ", " temperature "},
			},
			dcp: DCPCapabilities{},
			want: &CounterSet{DCGMCounters: CounterList{{
				FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
				FieldName: "DCGM_FI_DEV_GPU_TEMP",
				PromType:  "gauge",
				Help:      "temperature",
			}}},
		},
		{
			name: "exporter counter is recognized",
			records: [][]string{
				{"DCGM_EXP_XID_ERRORS_COUNT", "gauge", "xid count"},
			},
			dcp: DCPCapabilities{},
			want: &CounterSet{ExporterCounters: CounterList{{
				FieldID:   dcgm.Short(DCGMXIDErrorsCount),
				FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
				PromType:  "gauge",
				Help:      "xid count",
			}}},
		},
		{
			name: "exporter total counters are recognized",
			records: [][]string{
				{"DCGM_EXP_XID_ERRORS_TOTAL", "counter", "xid total"},
				{"DCGM_EXP_CLOCK_EVENTS_TOTAL", "counter", "clock total"},
			},
			dcp: DCPCapabilities{},
			want: &CounterSet{ExporterCounters: CounterList{
				{
					FieldID:   dcgm.Short(DCGMXIDErrorsTotal),
					FieldName: "DCGM_EXP_XID_ERRORS_TOTAL",
					PromType:  "counter",
					Help:      "xid total",
				},
				{
					FieldID:   dcgm.Short(DCGMClockEventsTotal),
					FieldName: "DCGM_EXP_CLOCK_EVENTS_TOTAL",
					PromType:  "counter",
					Help:      "clock total",
				},
			}},
		},
		{
			name: "malformed row",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "gauge"},
			},
			dcp:     DCPCapabilities{},
			wantErr: "malformed CSV record",
		},
		{
			name: "invalid UTF-8 HELP for DCGM counter",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "gauge", "bad\xff help"},
			},
			dcp:     DCPCapabilities{},
			wantErr: "invalid UTF-8 HELP text",
		},
		{
			name: "invalid UTF-8 HELP for exporter counter",
			records: [][]string{
				{"DCGM_EXP_XID_ERRORS_COUNT", "gauge", "bad\xff help"},
			},
			dcp:     DCPCapabilities{},
			wantErr: "invalid UTF-8 HELP text",
		},
		{
			name: "valid Unicode HELP",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "gauge", "temperatura °C"},
			},
			dcp: DCPCapabilities{},
			want: &CounterSet{DCGMCounters: CounterList{{
				FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP",
				PromType: "gauge", Help: "temperatura °C",
			}}},
		},
		{
			name: "bad prometheus type",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "histogram-ish", "temperature"},
			},
			dcp:     DCPCapabilities{},
			wantErr: "unsupported Prometheus metric type",
		},
		{
			name: "histogram shape is rejected because exporter emits scalar samples",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "histogram", "temperature"},
			},
			dcp:     DCPCapabilities{},
			wantErr: "unsupported Prometheus metric type",
		},
		{
			name: "summary shape is rejected because exporter emits scalar samples",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "summary", "temperature"},
			},
			dcp:     DCPCapabilities{},
			wantErr: "unsupported Prometheus metric type",
		},
		{
			name: "untyped scalar metric is accepted",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "untyped", "temperature"},
			},
			dcp: DCPCapabilities{},
			want: &CounterSet{DCGMCounters: CounterList{{
				FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
				FieldName: "DCGM_FI_DEV_GPU_TEMP",
				PromType:  "untyped",
				Help:      "temperature",
			}}},
		},
		{
			name: "exporter counter with bad prometheus type",
			records: [][]string{
				{"DCGM_EXP_XID_ERRORS_COUNT", "histogram-ish", "xid count"},
			},
			dcp:     DCPCapabilities{},
			wantErr: "unsupported Prometheus metric type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractCounters(tt.records, tt.dcp)
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

// TestExtractCounters_ProfilingTotalFieldsFromCustomConfig accepts the profiling total fields
// from a custom CSV when the GPU supports them, keeping their order and counter type.
func TestExtractCounters_ProfilingTotalFieldsFromCustomConfig(t *testing.T) {
	fieldIDs := make([]uint, 0, len(profilingTotalFields))
	records := make([][]string, 0, len(profilingTotalFields))
	for _, fieldName := range profilingTotalFields {
		fieldID, ok := dcgm.GetFieldID(fieldName)
		if !ok {
			t.Skip("requires go-dcgm with profiling total field IDs")
		}

		fieldIDs = append(fieldIDs, uint(fieldID))
		records = append(records, []string{fieldName, "counter", "custom profiling total field"})
	}

	got, err := ExtractCounters(records, NewDCPCapabilities([]dcgm.MetricGroup{{FieldIds: fieldIDs}}))

	require.NoError(t, err)
	require.Len(t, got.DCGMCounters, len(profilingTotalFields))
	require.Empty(t, got.ExporterCounters)
	for i, fieldName := range profilingTotalFields {
		assert.Equal(t, fieldName, got.DCGMCounters[i].FieldName)
		assert.Equal(t, "counter", got.DCGMCounters[i].PromType)
	}
}

// FuzzExtractCountersFromCSV feeds arbitrary CSV to ExtractCounters and, when it succeeds,
// expects a non-nil set, no more counters than rows, only known types, and the same result on a second run.
func FuzzExtractCountersFromCSV(f *testing.F) {
	seeds := []string{
		"DCGM_FI_DEV_GPU_TEMP,gauge,GPU temperature.\n",
		"DCGM_EXP_XID_ERRORS_TOTAL,counter,Cumulative XID errors.\n",
		"# comment\nDCGM_FI_DEV_FB_USED,untyped,\"Framebuffer memory, in MiB.\"\n",
		"DCGM_FI_DRIVER_VERSION,label,\"Driver\nversion\"\n",
		"DCGM_FI_DEV_GPU_TEMP,gauge\n",
		"DCGM_FI_DEV_GPU_TEMP,histogram,Unsupported type.\n",
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	readRecords := func(data []byte) ([][]string, error) {
		reader := csv.NewReader(bytes.NewReader(data))
		reader.Comment = '#'
		return reader.ReadAll()
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		records, err := readRecords(data)
		if err != nil {
			return
		}

		dcp := DCPCapabilities{}
		got, err := ExtractCounters(records, dcp)
		if err != nil {
			return
		}
		if got == nil {
			t.Fatal("successful extraction returned a nil counter set")
		}

		counterCount := len(got.DCGMCounters) + len(got.ExporterCounters)
		if counterCount > len(records) {
			t.Fatalf("extracted %d counters from %d records", counterCount, len(records))
		}
		for _, counter := range append(got.DCGMCounters, got.ExporterCounters...) {
			if _, ok := promMetricType[counter.PromType]; !ok {
				t.Fatalf("successful extraction returned unsupported type %q", counter.PromType)
			}
		}

		freshRecords, err := readRecords(data)
		if err != nil {
			t.Fatalf("CSV parse changed between identical reads: %v", err)
		}
		again, err := ExtractCounters(freshRecords, dcp)
		if err != nil {
			t.Fatalf("counter extraction changed between identical reads: %v", err)
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("counter extraction is not deterministic:\nfirst:  %#v\nsecond: %#v", got, again)
		}
	})
}

// extractCountersHelper writes input to a counter file and loads it. It asserts
// the load succeeds when valid is true and fails when valid is false.
func extractCountersHelper(t *testing.T, input string, valid bool) {
	tmpFile, err := stdos.CreateTemp(stdos.TempDir(), "prefix-")
	if err != nil {
		t.Fatalf("Cannot create temporary file: %v", err)
	}

	defer func() { _ = stdos.Remove(tmpFile.Name()) }()

	text := []byte(input)
	if _, err = tmpFile.Write(text); err != nil {
		t.Fatalf("Failed to write to temporary file: %v", err)
	}

	t.Logf("Using file: %s", tmpFile.Name())

	if err := tmpFile.Close(); err != nil {
		t.Fatalf("Cannot close temp file: %v", err)
	}

	c := appconfig.Config{
		MetricSource: appconfig.MetricSource{Kind: appconfig.MetricSourceFile, File: tmpFile.Name()},
	}
	cc, err := GetCounterSet(context.Background(), &c, DCPCapabilities{})
	if valid {
		assert.NoError(t, err, "Expected no error.")
		assert.Equal(t, 1, len(cc.DCGMCounters), "Expected 1 record counters.")
	} else {
		assert.Error(t, err, "Expected error.")
		assert.Nil(t, cc, "Expected no counters.")
	}
}

// TestFieldIsSupported_DCPRequirements pins the behaviour counters rely on
// from the DCP capability snapshot held by the lifecycle coordinator:
//
//   - Non-DCP fields always pass, regardless of DCP state.
//   - DCP fields require the field ID to appear in the capability snapshot.
func TestFieldIsSupported_DCPRequirements(t *testing.T) {
	const (
		dcpField    uint = 1001 // DCGM_FI_PROF range (1000-1099)
		nonDCPField uint = 100  // well below dcpFieldsStart
		cpuField    uint = 1100 // at cpuFieldsStart boundary
	)

	dcpGroups := []dcgm.MetricGroup{{FieldIds: []uint{dcpField}}}

	tests := []struct {
		name    string
		fieldID uint
		dcp     DCPCapabilities
		want    bool
	}{
		{
			name:    "non-DCP field passes without capabilities",
			fieldID: nonDCPField,
			dcp:     DCPCapabilities{},
			want:    true,
		},
		{
			name:    "CPU field (>= cpuFieldsStart) passes without capabilities",
			fieldID: cpuField,
			dcp:     DCPCapabilities{},
			want:    true,
		},
		{
			name:    "DCP field without capabilities is rejected",
			fieldID: dcpField,
			dcp:     DCPCapabilities{},
			want:    false,
		},
		{
			name:    "empty capabilities drop every DCP field",
			fieldID: dcpField,
			dcp:     NewDCPCapabilities(nil),
			want:    false,
		},
		{
			name:    "DCP field present in capabilities is accepted",
			fieldID: dcpField,
			dcp:     NewDCPCapabilities(dcpGroups),
			want:    true,
		},
		{
			name:    "DCP field missing from capabilities is rejected",
			fieldID: dcpField + 50,
			dcp:     NewDCPCapabilities(dcpGroups),
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, fieldIsSupported(tt.fieldID, tt.dcp))
		})
	}
}

// TestDCPCapabilitiesCopiesMetricGroups verifies a later DCGM result mutation
// cannot change the runtime capability snapshot used by an in-flight build.
func TestDCPCapabilitiesCopiesMetricGroups(t *testing.T) {
	groups := []dcgm.MetricGroup{{FieldIds: []uint{1001}}}
	capabilities := NewDCPCapabilities(groups)

	groups[0].FieldIds[0] = 1002

	assert.True(t, capabilities.Supports(1001))
	assert.False(t, capabilities.Supports(1002))
}

// TestDCPCapabilitiesPreservesEmptyFieldLists checks that an empty profiling field list stays empty in a capability snapshot.
func TestDCPCapabilitiesPreservesEmptyFieldLists(t *testing.T) {
	capabilities := NewDCPCapabilities([]dcgm.MetricGroup{{FieldIds: []uint{}}})

	require.Len(t, capabilities.metricGroups, 1)
	assert.NotNil(t, capabilities.metricGroups[0].FieldIds)
}

// TestValidateRecordsChecksProfilingWithoutTopologyFiltering checks static profiling validation retains rows without mutating inputs.
func TestValidateRecordsChecksProfilingWithoutTopologyFiltering(t *testing.T) {
	records := [][]string{{" DCGM_FI_PROF_GR_ENGINE_ACTIVE ", " gauge ", "Activity"}}
	result, err := ValidateRecords(records)
	require.NoError(t, err)
	require.Len(t, result.DCGMCounters, 1)
	assert.Equal(t, "DCGM_FI_PROF_GR_ENGINE_ACTIVE", result.DCGMCounters[0].FieldName)
	assert.Equal(t, " DCGM_FI_PROF_GR_ENGINE_ACTIVE ", records[0][0], "validation must not mutate caller records")
}

// A metric file that is not a regular file, or is too large, is refused without
// reading it all.
func TestReadCSVFileRefusesUnboundedInput(t *testing.T) {
	t.Run("device", func(t *testing.T) {
		_, err := ReadCSVFile("/dev/zero")
		require.ErrorContains(t, err, "not a regular file")
	})
	t.Run("fifo", func(t *testing.T) {
		fifo := filepath.Join(t.TempDir(), "metrics.csv")
		require.NoError(t, syscall.Mkfifo(fifo, 0o600))

		// The call must return without a writer; the timeout keeps a regression from hanging the suite.
		done := make(chan error, 1)
		go func() {
			_, err := ReadCSVFile(fifo)
			done <- err
		}()

		select {
		case err := <-done:
			require.ErrorContains(t, err, "not a regular file")
		case <-time.After(5 * time.Second):
			t.Fatal("ReadCSVFile blocked on a FIFO")
		}
	})
	t.Run("too large", func(t *testing.T) {
		big := filepath.Join(t.TempDir(), "metrics.csv")
		require.NoError(t, stdos.WriteFile(big, bytes.Repeat([]byte("# comment\n"), maxMetricFileBytes/10+10), 0o600))
		_, err := ReadCSVFile(big)
		require.ErrorContains(t, err, "is larger than")
	})
	t.Run("regular file", func(t *testing.T) {
		small := filepath.Join(t.TempDir(), "metrics.csv")
		require.NoError(t, stdos.WriteFile(small, []byte("A,gauge,help\n"), 0o600))
		records, err := ReadCSVFile(small)
		require.NoError(t, err)
		assert.Equal(t, [][]string{{"A", "gauge", "help"}}, records)
	})
}

// TestReadLocalRecordsReadsFileAndInlineSources checks that a file source
// returns its records with their file lines, an inline source returns its
// definitions with no lines, and any other source says it is not local.
func TestReadLocalRecordsReadsFileAndInlineSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.csv")
	require.NoError(t, stdos.WriteFile(path, []byte("# comment\nDCGM_FI_DEV_GPU_TEMP, gauge, Temperature.\n"), 0o600))

	records, lines, err := ReadLocalRecords(appconfig.MetricSource{Kind: appconfig.MetricSourceFile, File: path})
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"DCGM_FI_DEV_GPU_TEMP", " gauge", " Temperature."}}, records)
	assert.Equal(t, []int{2}, lines)

	records, lines, err = ReadLocalRecords(appconfig.MetricSource{
		Kind:   appconfig.MetricSourceInline,
		Fields: []appconfig.MetricField{{Name: "DCGM_FI_DEV_SM_CLOCK", PrometheusType: "gauge", Help: "Clock."}},
	})
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"DCGM_FI_DEV_SM_CLOCK", "gauge", "Clock."}}, records)
	assert.Nil(t, lines)

	_, _, err = ReadLocalRecords(appconfig.MetricSource{Kind: appconfig.MetricSourceConfigMap})
	assert.ErrorIs(t, err, ErrSourceNotLocal)

	_, _, err = ReadLocalRecords(appconfig.MetricSource{Kind: appconfig.MetricSourceFile, File: path + ".missing"})
	assert.ErrorIs(t, err, stdos.ErrNotExist)
}

// TestDeprecatedMetricFileWarning expects a warning naming the file and its
// replacement for each deprecated shipped file, wherever it is installed, and
// none for other files.
func TestDeprecatedMetricFileWarning(t *testing.T) {
	tests := []struct {
		path        string
		wantWarning string
	}{
		{
			path:        "/etc/dcgm-exporter/1.x-compatibility-metrics.csv",
			wantWarning: "metric file 1.x-compatibility-metrics.csv is deprecated",
		},
		{
			path:        "custom/dcp-metrics-included.csv",
			wantWarning: "metric file dcp-metrics-included.csv is deprecated",
		},
		{path: "/etc/dcgm-exporter/default-counters.csv"},
		{path: "/etc/dcgm-exporter/my-1.x-compatibility-metrics.csv"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			warning, deprecated := deprecatedMetricFileWarning(tc.path)

			if tc.wantWarning == "" {
				assert.False(t, deprecated)
				assert.Empty(t, warning)
				return
			}
			assert.True(t, deprecated)
			assert.Contains(t, warning, tc.wantWarning)
			assert.Contains(t, warning, "use default-counters.csv")
		})
	}
}
