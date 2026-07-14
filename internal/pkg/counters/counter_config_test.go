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
	stdos "os"
	"reflect"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestEmptyConfigMap(t *testing.T) {
	// ConfigMap matches criteria but is empty
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap1",
			Namespace: "default",
		},
		Data: map[string]string{"metrics": ""},
	})

	source := appconfig.ConfigMapMetricSource{
		Namespace: "default",
		Name:      "configmap1",
	}
	records, err := readConfigMapSource(context.Background(), clientset, source)
	require.Error(t, err, "Should have returned an error")
	require.Empty(t, records, "Should have no records")
}

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

func TestGetCounterSetLoadsLegacyConfigMapData(t *testing.T) {
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
		ConfigMapData: "default:configmap1",
	}

	got, err := GetCounterSet(context.Background(), &c)

	require.NoError(t, err)
	require.Len(t, got.DCGMCounters, 1)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", got.DCGMCounters[0].FieldName)
}

func TestGetCounterSetReturnsConfigMapLoadError(t *testing.T) {
	originalGetKubeClient := getKubeClient
	t.Cleanup(func() {
		getKubeClient = originalGetKubeClient
	})
	getKubeClient = func() (kubernetes.Interface, error) {
		return fake.NewSimpleClientset(), nil
	}

	c := appconfig.Config{
		ConfigMapData: "default:configmap-does-not-exist",
	}

	got, err := GetCounterSet(context.Background(), &c)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not retrieve ConfigMap")
	assert.Nil(t, got)
}

func TestGetCounterSetReturnsNilForMetricFileLoadError(t *testing.T) {
	c := appconfig.Config{
		ConfigMapData: appconfig.UndefinedConfigMapData,
		MetricSource: appconfig.MetricSource{
			Kind: appconfig.MetricSourceFile,
			File: t.TempDir() + "/missing.csv",
		},
	}

	got, err := GetCounterSet(context.Background(), &c)

	require.Error(t, err)
	assert.Nil(t, got)
}

func TestGetCounterSetLoadsInlineMetricSource(t *testing.T) {
	c := appconfig.Config{
		ConfigMapData: appconfig.UndefinedConfigMapData,
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

	got, err := GetCounterSet(context.Background(), &c)

	require.NoError(t, err)
	require.Len(t, got.DCGMCounters, 1)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", got.DCGMCounters[0].FieldName)
}

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

func TestExtractCounters_EdgeAndFailurePaths(t *testing.T) {
	tests := []struct {
		name    string
		records [][]string
		config  appconfig.Config
		want    *CounterSet
		wantErr string
	}{
		{
			name: "trims fields and skips empty rows",
			records: [][]string{
				{},
				{" DCGM_FI_DEV_GPU_TEMP ", " gauge ", " temperature "},
			},
			config: appconfig.Config{CollectDCP: false},
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
			config: appconfig.Config{CollectDCP: false},
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
			config: appconfig.Config{CollectDCP: false},
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
			config:  appconfig.Config{CollectDCP: false},
			wantErr: "malformed CSV record",
		},
		{
			name: "bad prometheus type",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "histogram-ish", "temperature"},
			},
			config:  appconfig.Config{CollectDCP: false},
			wantErr: "unsupported Prometheus metric type",
		},
		{
			name: "histogram shape is rejected because exporter emits scalar samples",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "histogram", "temperature"},
			},
			config:  appconfig.Config{CollectDCP: false},
			wantErr: "unsupported Prometheus metric type",
		},
		{
			name: "summary shape is rejected because exporter emits scalar samples",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "summary", "temperature"},
			},
			config:  appconfig.Config{CollectDCP: false},
			wantErr: "unsupported Prometheus metric type",
		},
		{
			name: "untyped scalar metric is accepted",
			records: [][]string{
				{"DCGM_FI_DEV_GPU_TEMP", "untyped", "temperature"},
			},
			config: appconfig.Config{CollectDCP: false},
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
			config:  appconfig.Config{CollectDCP: false},
			wantErr: "unsupported Prometheus metric type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractCounters(tt.records, &tt.config)
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

	got, err := ExtractCounters(records, &appconfig.Config{
		CollectDCP:   true,
		MetricGroups: []dcgm.MetricGroup{{FieldIds: fieldIDs}},
	})

	require.NoError(t, err)
	require.Len(t, got.DCGMCounters, len(profilingTotalFields))
	require.Empty(t, got.ExporterCounters)
	for i, fieldName := range profilingTotalFields {
		assert.Equal(t, fieldName, got.DCGMCounters[i].FieldName)
		assert.Equal(t, "counter", got.DCGMCounters[i].PromType)
	}
}

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

		config := appconfig.Config{CollectDCP: false}
		got, err := ExtractCounters(records, &config)
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
		again, err := ExtractCounters(freshRecords, &config)
		if err != nil {
			t.Fatalf("counter extraction changed between identical reads: %v", err)
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("counter extraction is not deterministic:\nfirst:  %#v\nsecond: %#v", got, again)
		}
	})
}

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
		ConfigMapData:  appconfig.UndefinedConfigMapData,
		CollectorsFile: tmpFile.Name(),
	}
	cc, err := GetCounterSet(context.Background(), &c)
	if valid {
		assert.NoError(t, err, "Expected no error.")
		assert.Equal(t, 1, len(cc.DCGMCounters), "Expected 1 record counters.")
	} else {
		assert.Error(t, err, "Expected error.")
		assert.Nil(t, cc, "Expected no counters.")
	}
}

// TestFieldIsSupported_DCPRequirements pins the behaviour counters rely on
// from the DCP capabilities snapshot restored by hotReload:
//
//   - Non-DCP fields always pass, regardless of DCP state.
//   - DCP fields require CollectDCP=true AND the fieldID to appear in some
//     MetricGroup. The buggy combination (CollectDCP=true, MetricGroups=nil)
//     silently drops every profiling field; applyTo is responsible for
//     ensuring that state is never observed after a hot reload.
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
		config  appconfig.Config
		want    bool
	}{
		{
			name:    "non-DCP field passes regardless of DCP state",
			fieldID: nonDCPField,
			config:  appconfig.Config{CollectDCP: false},
			want:    true,
		},
		{
			name:    "CPU field (>= cpuFieldsStart) passes regardless of DCP state",
			fieldID: cpuField,
			config:  appconfig.Config{CollectDCP: false},
			want:    true,
		},
		{
			name:    "DCP field with CollectDCP disabled is rejected",
			fieldID: dcpField,
			config:  appconfig.Config{CollectDCP: false, MetricGroups: dcpGroups},
			want:    false,
		},
		{
			name:    "CollectDCP with nil MetricGroups drops every DCP field",
			fieldID: dcpField,
			config:  appconfig.Config{CollectDCP: true, MetricGroups: nil},
			want:    false,
		},
		{
			name:    "DCP field present in MetricGroups is accepted",
			fieldID: dcpField,
			config:  appconfig.Config{CollectDCP: true, MetricGroups: dcpGroups},
			want:    true,
		},
		{
			name:    "DCP field missing from populated MetricGroups is rejected",
			fieldID: dcpField + 50,
			config:  appconfig.Config{CollectDCP: true, MetricGroups: dcpGroups},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, fieldIsSupported(tt.fieldID, &tt.config))
		})
	}
}
