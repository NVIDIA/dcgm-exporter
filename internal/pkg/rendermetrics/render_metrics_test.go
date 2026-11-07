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

package rendermetrics

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// getMetricsByCounterWithTestMetric creates one reusable scalar test metric.
func getMetricsByCounterWithTestMetric() collector.MetricsByCounter {
	return getMetricsByCounterWithTestMetricCPUSerial("CPU-SERIAL-123")
}

// getMetricsByCounterWithTestMetricCPUSerial supplies one scalar fixture with the requested optional CPU serial.
func getMetricsByCounterWithTestMetricCPUSerial(cpuSerial string) collector.MetricsByCounter {
	metrics := collector.MetricsByCounter{}
	counter := getTestMetric()

	metrics[counter] = append(metrics[counter], collector.Metric{
		GPU:          "0",
		GPUDevice:    "testdevice",
		CPUSerial:    cpuSerial,
		GPUModelName: "Test GPU Model",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-test-uuid-0000-0000-0000-000000000000",
		NvSwitch:     "0",
		NvLink:       "0",
		Counter:      counter,
		Value:        "42",
		Timestamp:    1_700_000_000_123_456,
		Attributes:   map[string]string{},
	})

	return metrics
}

// getTestMetric returns the base gauge counter used by renderer tests.
func getTestMetric() counters.Counter {
	counter := counters.Counter{
		FieldID:   2000,
		FieldName: "TEST_METRIC",
		PromType:  "gauge",
	}

	return counter
}

// Test_render verifies renderer output for each supported entity group.
func Test_render(t *testing.T) {
	metrics := getMetricsByCounterWithTestMetric()
	metricsWithoutCPUSerial := getMetricsByCounterWithTestMetricCPUSerial("")

	// Each entity-group case checks its exact public text and optional identity labels.
	tests := []struct {
		name    string
		group   dcgm.Field_Entity_Group
		metrics collector.MetricsByCounter
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name:    fmt.Sprintf("Render %s", dcgm.FE_GPU.String()),
			group:   dcgm.FE_GPU,
			metrics: metrics,
			want: "# HELP TEST_METRIC \n" +
				"# TYPE TEST_METRIC gauge\n" +
				`TEST_METRIC{gpu="0",` +
				`UUID="GPU-test-uuid-0000-0000-0000-000000000000",` +
				`pci_bus_id="",` +
				`device="testdevice",` +
				`modelName="Test GPU Model",` +
				`hostname="testhost"} 42` + "\n",
		},
		{
			name:    fmt.Sprintf("Render %s", dcgm.FE_SWITCH.String()),
			group:   dcgm.FE_SWITCH,
			metrics: metrics,
			want: `# HELP TEST_METRIC 
# TYPE TEST_METRIC gauge
TEST_METRIC{nvswitch="0",hostname="testhost"} 42
`,
		},
		{
			name:    fmt.Sprintf("Render %s", dcgm.FE_LINK.String()),
			group:   dcgm.FE_LINK,
			metrics: metrics,
			want: "# HELP TEST_METRIC \n" +
				"# TYPE TEST_METRIC gauge\n" +
				`TEST_METRIC{nvlink="0",` +
				`nvswitch="0",` +
				`gpu="0",` +
				`gpu_uuid="GPU-test-uuid-0000-0000-0000-000000000000",` +
				`device="testdevice",` +
				`model_name="Test GPU Model",` +
				`hostname="testhost"} 42` + "\n",
		},
		{
			name:    fmt.Sprintf("Render %s", dcgm.FE_CPU.String()),
			group:   dcgm.FE_CPU,
			metrics: metrics,
			want: `# HELP TEST_METRIC 
# TYPE TEST_METRIC gauge
TEST_METRIC{cpu="0",cpu_serial="CPU-SERIAL-123",hostname="testhost"} 42
`,
		},
		{
			name:    fmt.Sprintf("Render %s without CPU serial", dcgm.FE_CPU.String()),
			group:   dcgm.FE_CPU,
			metrics: metricsWithoutCPUSerial,
			want: "# HELP TEST_METRIC \n" +
				"# TYPE TEST_METRIC gauge\n" +
				`TEST_METRIC{cpu="0",hostname="testhost"} 42` + "\n",
		},
		{
			name:    fmt.Sprintf("Render %s", dcgm.FE_CPU_CORE.String()),
			group:   dcgm.FE_CPU_CORE,
			metrics: metrics,
			want: `# HELP TEST_METRIC 
# TYPE TEST_METRIC gauge
TEST_METRIC{cpucore="0",cpu="testdevice",hostname="testhost"} 42
`,
		},
		{
			name:    "Render unknown group",
			group:   42,
			metrics: metrics,
			want:    ``,
			wantErr: assert.Error,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, withSource := range []bool{false, true} {
				t.Run(fmt.Sprintf("source=%v", withSource), func(t *testing.T) {
					sourceMetrics := metricsWithSource(tt.metrics, withSource)
					w := &bytes.Buffer{}
					err := RenderGroup(w, tt.group, sourceMetrics)

					if tt.wantErr != nil {
						tt.wantErr(t, err, fmt.Sprintf("RenderGroup(w, %v, %v)", tt.group, tt.metrics))
					} else {
						require.NoError(t, err)
					}

					assert.Equalf(t, tt.want, w.String(), "RenderGroup(w, %v, %v)", tt.group, tt.metrics)
				})
			}
		})
	}
}

// metricsWithSource copies fixtures with optional raw identity to verify unchanged Prometheus output.
func metricsWithSource(metrics collector.MetricsByCounter, withSource bool) collector.MetricsByCounter {
	sourceMetrics := make(collector.MetricsByCounter, len(metrics))
	for counter, samples := range metrics {
		copies := append([]collector.Metric(nil), samples...)
		for i := range copies {
			if withSource {
				copies[i].Source = &collector.SourceIdentity{GPUModelName: "original", PodLabels: map[string]string{"raw/key": "ignored"}, NVLinkIndex: new(int64), Containers: []collector.ContainerIdentity{{ID: "full", Name: "raw"}}}
			}
		}
		sourceMetrics[counter] = copies
	}
	return sourceMetrics
}

// TestRenderContainerLabel checks escaped container attribution in Prometheus text.
func TestRenderContainerLabel(t *testing.T) {
	counter := counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: []collector.Metric{{
		GPU:          "0",
		GPUDevice:    "nvidia0",
		GPUModelName: "Test GPU Model",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-test-uuid",
		Counter:      counter,
		Value:        "42",
		Attributes: map[string]string{
			"container": "trainer\"quoted",
		},
	}}}

	for _, variants := range []int{1, 2} {
		t.Run(fmt.Sprintf("raw identities=%d", variants), func(t *testing.T) {
			identities := []collector.ContainerIdentity{{ID: "one", Name: "trainer\"quoted"}}
			if variants == 2 {
				identities = append(identities, collector.ContainerIdentity{ID: "two", Name: "trainer\"quoted"})
			}
			metrics[counter][0].Source = &collector.SourceIdentity{Containers: identities}
			w := &bytes.Buffer{}
			require.NoError(t, RenderGroup(w, dcgm.FE_GPU, metrics))

			assert.Equal(t, "# HELP TEST_METRIC \n# TYPE TEST_METRIC gauge\n"+`TEST_METRIC{gpu="0",UUID="GPU-test-uuid",pci_bus_id="",device="nvidia0",modelName="Test GPU Model",hostname="testhost",container="trainer\"quoted"} 42`+"\n", w.String())
		})
	}
}

// TestRenderExporterOwnedMetric checks exporter-only samples without GPU labels.
func TestRenderExporterOwnedMetric(t *testing.T) {
	counter := counters.Counter{FieldName: "DCGM_EXP_FIELD_POLICY_ENABLED", PromType: "gauge", Help: "Whether a DCGM field policy is enabled."}
	metrics := collector.MetricsByCounter{counter: {{
		Counter: counter, Value: "1", Hostname: "testhost", Labels: map[string]string{"policy_id": "7", "field_id": "1613"},
	}}}
	w := &bytes.Buffer{}
	require.NoError(t, RenderGroup(w, dcgm.FE_NONE, metrics))
	assert.Equal(t, "# HELP DCGM_EXP_FIELD_POLICY_ENABLED Whether a DCGM field policy is enabled.\n"+
		"# TYPE DCGM_EXP_FIELD_POLICY_ENABLED gauge\n"+
		"DCGM_EXP_FIELD_POLICY_ENABLED{hostname=\"testhost\",field_id=\"1613\",policy_id=\"7\"} 1\n", w.String())
}

// TestRenderDynamicDRAMIGOmitsUnknownUUIDLabel checks static DRA UUID metadata and omits unresolved dynamic DRA UUID metadata.
func TestRenderDynamicDRAMIGOmitsUnknownUUIDLabel(t *testing.T) {
	counter := counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"}
	baseMetric := collector.Metric{
		GPU:           "0",
		GPUDevice:     "nvidia0",
		GPUModelName:  "Test GPU Model",
		Hostname:      "testhost",
		UUID:          "UUID",
		GPUUUID:       "MIG-test-uuid",
		GPUInstanceID: "3",
		MigProfile:    "1g.12gb",
		Counter:       counter,
		Value:         "42",
	}
	staticMetric := baseMetric
	staticMetric.Attributes = map[string]string{
		"dra_device_name":     "static-mig",
		"dra_mig_device_uuid": "MIG-test-uuid",
		"dra_mig_profile":     "1g.12gb",
	}
	dynamicMetric := baseMetric
	dynamicMetric.Attributes = map[string]string{
		"dra_device_name": "gpu-0-mig-1g12gb-19-0",
		"dra_mig_profile": "1g.12gb",
	}
	metrics := collector.MetricsByCounter{
		counter: {staticMetric, dynamicMetric},
	}

	var rendered bytes.Buffer
	require.NoError(t, RenderGroup(&rendered, dcgm.FE_GPU, metrics))

	var staticLine, dynamicLine string
	for line := range strings.SplitSeq(rendered.String(), "\n") {
		switch {
		case strings.Contains(line, `dra_device_name="static-mig"`):
			staticLine = line
		case strings.Contains(line, `dra_device_name="gpu-0-mig-1g12gb-19-0"`):
			dynamicLine = line
		}
	}
	require.NotEmpty(t, staticLine)
	require.NotEmpty(t, dynamicLine)
	assert.Contains(t, staticLine, `dra_mig_device_uuid="MIG-test-uuid"`)
	assert.NotContains(t, dynamicLine, "dra_mig_device_uuid")
}

// TestRenderRejectsDuplicateContainerLabelSeries rejects repeated samples with identical container attribution.
func TestRenderRejectsDuplicateContainerLabelSeries(t *testing.T) {
	counter := counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"}
	metric := collector.Metric{
		GPU:          "0",
		GPUDevice:    "nvidia0",
		GPUModelName: "Test GPU Model",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-test-uuid",
		Counter:      counter,
		Value:        "42",
		Attributes:   map[string]string{"container": "trainer"},
	}
	metrics := map[dcgm.Field_Entity_Group]collector.MetricsByCounter{
		dcgm.FE_GPU: {counter: []collector.Metric{metric, metric}},
	}

	err := Render(&bytes.Buffer{}, metrics)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate sample labels")
}

// TestRenderCumulativeEventCounters checks total counters retain reason labels without window labels.
func TestRenderCumulativeEventCounters(t *testing.T) {
	metrics := collector.MetricsByCounter{}

	xidCounter := counters.Counter{
		FieldID:   dcgm.Short(counters.DCGMXIDErrorsTotal),
		FieldName: counters.DCGMExpXIDErrorsTotal,
		PromType:  "counter",
		Help:      "cumulative XID errors observed since exporter start",
	}
	clockCounter := counters.Counter{
		FieldID:   dcgm.Short(counters.DCGMClockEventsTotal),
		FieldName: counters.DCGMExpClockEventsTotal,
		PromType:  "counter",
		Help:      "cumulative clock events observed since exporter start (edge-counted)",
	}

	baseMetric := collector.Metric{
		GPU:          "0",
		GPUDevice:    "nvidia0",
		GPUModelName: "Test GPU Model",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-test-uuid-0000-0000-0000-000000000000",
		Attributes:   map[string]string{},
	}

	xidMetric := baseMetric
	xidMetric.Counter = xidCounter
	xidMetric.Value = "1"
	xidMetric.Labels = map[string]string{"xid": "42"}
	metrics[xidCounter] = []collector.Metric{xidMetric}

	clockMetric := baseMetric
	clockMetric.Counter = clockCounter
	clockMetric.Value = "1"
	clockMetric.Labels = map[string]string{"clock_event": "power_cap"}
	metrics[clockCounter] = []collector.Metric{clockMetric}

	w := &bytes.Buffer{}
	err := RenderGroup(w, dcgm.FE_GPU, metrics)
	assert.NoError(t, err)

	got := w.String()
	assert.Contains(t, got, "# TYPE DCGM_EXP_XID_ERRORS_TOTAL counter")
	assert.Contains(t, got, `DCGM_EXP_XID_ERRORS_TOTAL{`)
	assert.Contains(t, got, `xid="42"`)
	assert.Contains(t, got, "} 1")
	assert.Contains(t, got, "# TYPE DCGM_EXP_CLOCK_EVENTS_TOTAL counter")
	assert.Contains(t, got, `DCGM_EXP_CLOCK_EVENTS_TOTAL{`)
	assert.Contains(t, got, `clock_event="power_cap"`)
	assert.False(t, strings.Contains(got, "window_size_in_ms"))
}

// TestRenderEscapesPrometheusText verifies DTO rendering escapes text fields correctly.
func TestRenderEscapesPrometheusText(t *testing.T) {
	counter := counters.Counter{
		FieldID:   2000,
		FieldName: "TEST_METRIC",
		PromType:  "gauge",
		Help:      "help with slash \\ and newline\nescaped",
	}
	metrics := collector.MetricsByCounter{
		counter: []collector.Metric{{
			GPU:          "0",
			GPUDevice:    "nvidia0",
			GPUModelName: "Model \"Quoted\"",
			Hostname:     "host\\name",
			UUID:         "UUID",
			GPUUUID:      "GPU-test-uuid",
			Counter:      counter,
			Value:        "42",
			Labels: map[string]string{
				"pod": "pod \"quoted\"\nnext",
			},
			Attributes: map[string]string{},
		}},
	}

	w := &bytes.Buffer{}
	require.NoError(t, RenderGroup(w, dcgm.FE_GPU, metrics))

	got := w.String()
	assert.Contains(t, got, `# HELP TEST_METRIC help with slash \\ and newline\nescaped`)
	assert.Contains(t, got, `modelName="Model \"Quoted\""`)
	assert.Contains(t, got, `hostname="host\\name"`)
	assert.Contains(t, got, `pod="pod \"quoted\"\nnext"`)

	parser := expfmt.NewTextParser(model.LegacyValidation)
	_, err := parser.TextToMetricFamilies(bytes.NewReader(w.Bytes()))
	require.NoError(t, err)
}

// TestRenderAggregatesMetricFamiliesAcrossGroups verifies shared family metadata is emitted once.
func TestRenderAggregatesMetricFamiliesAcrossGroups(t *testing.T) {
	counter := counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge", Help: "test help"}
	gpuMetric := collector.Metric{
		GPU:          "0",
		GPUDevice:    "nvidia0",
		GPUModelName: "Test GPU Model",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-test-uuid",
		Counter:      counter,
		Value:        "42",
		Attributes:   map[string]string{},
	}
	switchMetric := collector.Metric{
		NvSwitch:   "0",
		Hostname:   "testhost",
		Counter:    counter,
		Value:      "84",
		Attributes: map[string]string{},
	}
	metrics := map[dcgm.Field_Entity_Group]collector.MetricsByCounter{
		dcgm.FE_GPU:    {counter: []collector.Metric{gpuMetric}},
		dcgm.FE_SWITCH: {counter: []collector.Metric{switchMetric}},
	}

	var w bytes.Buffer
	require.NoError(t, Render(&w, metrics))

	got := w.String()
	assert.Equal(t, 1, strings.Count(got, "# HELP TEST_METRIC"))
	assert.Equal(t, 1, strings.Count(got, "# TYPE TEST_METRIC gauge"))

	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(w.Bytes()))
	require.NoError(t, err)
	require.Contains(t, families, "TEST_METRIC")
	assert.Len(t, families["TEST_METRIC"].Metric, 2)
}

// TestRenderRejectsInvalidMetricShapesAndValues verifies invalid samples fail before emission.
func TestRenderRejectsInvalidMetricShapesAndValues(t *testing.T) {
	baseMetric := collector.Metric{
		GPU:          "0",
		GPUDevice:    "nvidia0",
		GPUModelName: "Test GPU Model",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-test-uuid",
		Value:        "42",
		Attributes:   map[string]string{},
	}

	// Isolated fixture builders vary one invalid shape, value, or label contract.
	tests := []struct {
		name    string
		counter counters.Counter
		metric  collector.Metric
		wantErr string
	}{
		{
			name:    "histogram shape is unsupported",
			counter: counters.Counter{FieldName: "TEST_METRIC", PromType: "histogram"},
			metric:  baseMetric,
			wantErr: "unsupported Prometheus metric type",
		},
		{
			name:    "label-only counter cannot be emitted as metric family",
			counter: counters.Counter{FieldName: "TEST_METRIC", PromType: "label"},
			metric:  baseMetric,
			wantErr: "label-only counter",
		},
		{
			name:    "invalid metric name",
			counter: counters.Counter{FieldName: "bad.metric", PromType: "gauge"},
			metric:  baseMetric,
			wantErr: "invalid Prometheus metric name",
		},
		{
			name:    "non numeric value",
			counter: counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"},
			metric: func() collector.Metric {
				m := baseMetric
				m.Value = collector.FailedToConvert

				return m
			}(),
			wantErr: "non-numeric sample value",
		},
		{
			name:    "non finite value",
			counter: counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"},
			metric: func() collector.Metric {
				m := baseMetric
				m.Value = "NaN"

				return m
			}(),
			wantErr: "non-finite sample value",
		},
		{
			name:    "negative counter",
			counter: counters.Counter{FieldName: "TEST_METRIC", PromType: "counter"},
			metric: func() collector.Metric {
				m := baseMetric
				m.Value = "-1"

				return m
			}(),
			wantErr: "negative counter sample",
		},
		{
			name:    "invalid label name",
			counter: counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"},
			metric: func() collector.Metric {
				m := baseMetric
				m.Labels = map[string]string{"1bad": "value"}

				return m
			}(),
			wantErr: "invalid Prometheus label name",
		},
		{
			name:    "reserved label name",
			counter: counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"},
			metric: func() collector.Metric {
				m := baseMetric
				m.Labels = map[string]string{"__name__": "value"}

				return m
			}(),
			wantErr: "reserved Prometheus label name",
		},
		{
			name:    "invalid UTF-8 label value",
			counter: counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"},
			metric: func() collector.Metric {
				m := baseMetric
				m.Labels = map[string]string{"pod": string([]byte{0x90})}

				return m
			}(),
			wantErr: "invalid UTF-8 value",
		},
		{
			name:    "invalid UTF-8 help text",
			counter: counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge", Help: string([]byte{0x90})},
			metric:  baseMetric,
			wantErr: "invalid UTF-8 HELP text",
		},
		{
			name:    "duplicate label name",
			counter: counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"},
			metric: func() collector.Metric {
				m := baseMetric
				m.Labels = map[string]string{"gpu": "shadow"}

				return m
			}(),
			wantErr: "duplicate label name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.metric.Counter = tt.counter
			metrics := collector.MetricsByCounter{tt.counter: []collector.Metric{tt.metric}}

			err := RenderGroup(&bytes.Buffer{}, dcgm.FE_GPU, metrics)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestRenderRejectsConflictingMetricFamilyMetadata verifies repeated families stay consistent.
func TestRenderRejectsConflictingMetricFamilyMetadata(t *testing.T) {
	baseMetric := collector.Metric{
		GPU:          "0",
		GPUDevice:    "nvidia0",
		GPUModelName: "Test GPU Model",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-test-uuid",
		Value:        "42",
		Attributes:   map[string]string{},
	}

	// Counter pairs reuse a family name while disagreeing on one metadata field.
	tests := []struct {
		name    string
		first   counters.Counter
		second  counters.Counter
		wantErr string
	}{
		{
			name:    "conflicting types",
			first:   counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge", Help: "test help"},
			second:  counters.Counter{FieldName: "TEST_METRIC", PromType: "counter", Help: "test help"},
			wantErr: "conflicting types",
		},
		{
			name:    "conflicting help",
			first:   counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge", Help: "first help"},
			second:  counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge", Help: "second help"},
			wantErr: "conflicting HELP text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			firstMetric := baseMetric
			firstMetric.Counter = tt.first
			secondMetric := baseMetric
			secondMetric.Counter = tt.second
			secondMetric.GPU = "1"
			metrics := collector.MetricsByCounter{
				tt.first:  []collector.Metric{firstMetric},
				tt.second: []collector.Metric{secondMetric},
			}

			err := RenderGroup(&bytes.Buffer{}, dcgm.FE_GPU, metrics)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestRenderRejectsDuplicateSeries verifies duplicate label sets are rejected.
func TestRenderRejectsDuplicateSeries(t *testing.T) {
	counter := counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"}
	metric := collector.Metric{
		GPU:          "0",
		GPUDevice:    "nvidia0",
		GPUModelName: "Test GPU Model",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-test-uuid",
		Counter:      counter,
		Value:        "42",
		Attributes:   map[string]string{},
	}
	metrics := map[dcgm.Field_Entity_Group]collector.MetricsByCounter{
		dcgm.FE_GPU: {counter: []collector.Metric{metric, metric}},
	}

	err := Render(&bytes.Buffer{}, metrics)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate sample labels")
}

// TestRenderAllowsParentGPUAndMIGInstanceSamples verifies mixed parent and MIG GPU samples do not collide.
func TestRenderAllowsParentGPUAndMIGInstanceSamples(t *testing.T) {
	counter := counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"}
	parentMetric := collector.Metric{
		GPU:          "0",
		GPUDevice:    "nvidia0",
		GPUModelName: "Test GPU Model",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-test-uuid",
		Counter:      counter,
		Value:        "42",
		Attributes:   map[string]string{},
	}
	migMetric := parentMetric
	migMetric.GPUInstanceID = "7"
	migMetric.MigProfile = "1g.10gb"
	migMetric.Value = "24"
	metrics := map[dcgm.Field_Entity_Group]collector.MetricsByCounter{
		dcgm.FE_GPU: {counter: []collector.Metric{parentMetric, migMetric}},
	}

	var w bytes.Buffer
	err := Render(&w, metrics)

	require.NoError(t, err)
	got := w.String()
	parentSample := `TEST_METRIC{gpu="0",` +
		`UUID="GPU-test-uuid",` +
		`pci_bus_id="",` +
		`device="nvidia0",` +
		`modelName="Test GPU Model",` +
		`hostname="testhost"} 42`
	migSample := `TEST_METRIC{gpu="0",` +
		`UUID="GPU-test-uuid",` +
		`pci_bus_id="",` +
		`device="nvidia0",` +
		`modelName="Test GPU Model",` +
		`GPU_I_PROFILE="1g.10gb",` +
		`GPU_I_ID="7",` +
		`hostname="testhost"} 24`

	assert.Equal(t, 2, strings.Count(got, "TEST_METRIC{"))
	assert.Contains(t, got, parentSample)
	assert.Contains(t, got, migSample)
}

// TestRenderDistinguishesComputeInstances keeps two compute-instance samples separate within one GPU instance.
func TestRenderDistinguishesComputeInstances(t *testing.T) {
	counter := counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"}
	baseMetric := collector.Metric{
		GPU:           "0",
		GPUDevice:     "nvidia0",
		GPUModelName:  "Test GPU Model",
		Hostname:      "testhost",
		UUID:          "UUID",
		GPUUUID:       "GPU-test-uuid",
		GPUInstanceID: "3",
		MigProfile:    "1g.10gb",
		Counter:       counter,
		Attributes:    map[string]string{},
	}
	first := baseMetric
	first.GPUComputeInstanceID = "0"
	first.Value = "100"
	second := baseMetric
	second.GPUComputeInstanceID = "1"
	second.Value = "200"
	metrics := map[dcgm.Field_Entity_Group]collector.MetricsByCounter{
		dcgm.FE_GPU: {counter: {first, second}},
	}

	var rendered bytes.Buffer
	require.NoError(t, Render(&rendered, metrics))

	got := rendered.String()
	assert.Equal(t, 2, strings.Count(got, "TEST_METRIC{"))
	assert.Contains(t, got, `GPU_I_ID="3",GPU_CI_ID="0",hostname="testhost"} 100`)
	assert.Contains(t, got, `GPU_I_ID="3",GPU_CI_ID="1",hostname="testhost"} 200`)
}

// TestRenderOmitsEmptyMetricFamilies verifies empty counter slices do not emit metadata.
func TestRenderOmitsEmptyMetricFamilies(t *testing.T) {
	counter := counters.Counter{FieldName: "TEST_METRIC", PromType: "gauge"}
	metrics := collector.MetricsByCounter{counter: nil}

	var w bytes.Buffer
	err := RenderGroup(&w, dcgm.FE_GPU, metrics)

	require.NoError(t, err)
	assert.Empty(t, w.String())
}

// TestMetricLabelsUsesCPUIdentities verifies CPU labels use the dedicated
// identities while preserving metrics produced by older collectors.
func TestMetricLabelsUsesCPUIdentities(t *testing.T) {
	// Identity cases cover dedicated fields and each legacy fallback.
	tests := []struct {
		name   string
		group  dcgm.Field_Entity_Group
		metric collector.Metric
		want   map[string]string
	}{
		{
			name:  "CPU uses its dedicated identity",
			group: dcgm.FE_CPU,
			metric: collector.Metric{
				CPU: "10",
				GPU: "legacy-cpu",
			},
			want: map[string]string{"cpu": "10"},
		},
		{
			name:  "CPU falls back to the legacy identity",
			group: dcgm.FE_CPU,
			metric: collector.Metric{
				GPU: "legacy-cpu",
			},
			want: map[string]string{"cpu": "legacy-cpu"},
		},
		{
			name:  "CPU core uses dedicated CPU and core identities",
			group: dcgm.FE_CPU_CORE,
			metric: collector.Metric{
				CPU:       "10",
				CPUCore:   "11",
				GPU:       "legacy-core",
				GPUDevice: "legacy-cpu",
			},
			want: map[string]string{"cpu": "10", "cpucore": "11"},
		},
		{
			name:  "CPU core falls back to legacy identities",
			group: dcgm.FE_CPU_CORE,
			metric: collector.Metric{
				GPU:       "legacy-core",
				GPUDevice: "legacy-cpu",
			},
			want: map[string]string{"cpu": "legacy-cpu", "cpucore": "legacy-core"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels, err := metricLabels(tt.group, tt.metric)
			require.NoError(t, err)

			got := make(map[string]string, len(labels))
			for _, label := range labels {
				got[label.GetName()] = label.GetValue()
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestLabelSignatureDoesNotCollideOnEmbeddedSeparators verifies length-prefix signatures.
func TestLabelSignatureDoesNotCollideOnEmbeddedSeparators(t *testing.T) {
	first := []*dto.LabelPair{
		{Name: stringPtr("a"), Value: stringPtr("x")},
		{Name: stringPtr("b"), Value: stringPtr("y")},
	}
	second := []*dto.LabelPair{
		{Name: stringPtr("a"), Value: stringPtr("x\xfeb\xffy")},
	}

	assert.NotEqual(t, labelSignature(first), labelSignature(second))
}

// FuzzRenderGroup checks determinism and parseable scalar exposition whenever rendering succeeds.
func FuzzRenderGroup(f *testing.F) {
	// Seeds include accepted scalar forms and an invalid shape to exercise rejection.
	seeds := []struct {
		fieldName string
		promType  string
		value     string
		help      string
		labelName string
		labelVal  string
		group     uint8
	}{
		{fieldName: "TEST_METRIC", promType: "gauge", value: "42", help: "Test gauge.", group: 0},
		{fieldName: "TEST_COUNTER", promType: "counter", value: "0", help: "Test counter.", labelName: "pod", labelVal: "trainer", group: 1},
		{fieldName: "TEST_UNTYPED", promType: "untyped", value: "1.5", help: "slash \\ and newline\nescaped", labelName: "container", labelVal: "quoted\"\nvalue", group: 2},
		{fieldName: "bad.metric", promType: "histogram", value: "NaN", help: "invalid", labelName: "1bad", labelVal: "value", group: 5},
	}
	for _, seed := range seeds {
		f.Add(seed.fieldName, seed.promType, seed.value, seed.help, seed.labelName, seed.labelVal, seed.group)
	}

	groups := [...]dcgm.Field_Entity_Group{
		dcgm.FE_GPU,
		dcgm.FE_SWITCH,
		dcgm.FE_LINK,
		dcgm.FE_CPU,
		dcgm.FE_CPU_CORE,
		dcgm.Field_Entity_Group(255),
	}

	// The callback compares repeated renders, then independently parses successful output.
	f.Fuzz(func(
		t *testing.T,
		fieldName string,
		promType string,
		value string,
		help string,
		labelName string,
		labelValue string,
		groupIndex uint8,
	) {
		counter := counters.Counter{
			FieldID:   2000,
			FieldName: fieldName,
			PromType:  promType,
			Help:      help,
		}
		metric := collector.Metric{
			GPU:          "0",
			GPUUUID:      "GPU-test-uuid",
			GPUDevice:    "nvidia0",
			CPUSerial:    "CPU-SERIAL-123",
			GPUModelName: "Test GPU Model",
			GPUPCIBusID:  "00000000:00:00.0",
			UUID:         "UUID",
			NvSwitch:     "0",
			NvLink:       "0",
			Hostname:     "testhost",
			Counter:      counter,
			Value:        value,
			Attributes:   map[string]string{},
		}
		if labelName != "" {
			metric.Labels = map[string]string{labelName: labelValue}
		}

		group := groups[int(groupIndex)%len(groups)]
		metrics := collector.MetricsByCounter{counter: []collector.Metric{metric}}
		var first bytes.Buffer
		if err := RenderGroup(&first, group, metrics); err != nil {
			return
		}

		var second bytes.Buffer
		if err := RenderGroup(&second, group, metrics); err != nil {
			t.Fatalf("render result changed between identical calls: %v", err)
		}
		if first.String() != second.String() {
			t.Fatalf("rendering is not deterministic:\nfirst:  %q\nsecond: %q", first.String(), second.String())
		}

		parser := expfmt.NewTextParser(model.LegacyValidation)
		families, err := parser.TextToMetricFamilies(bytes.NewReader(first.Bytes()))
		if err != nil {
			t.Fatalf("successful render produced invalid Prometheus text: %v\n%s", err, first.String())
		}
		if len(families) != 1 {
			t.Fatalf("successful single-counter render produced %d metric families", len(families))
		}
		family, ok := families[fieldName]
		if !ok {
			t.Fatalf("successful render omitted metric family %q", fieldName)
		}
		if len(family.GetMetric()) != 1 {
			t.Fatalf("metric family %q has %d samples, expected 1", fieldName, len(family.GetMetric()))
		}
	})
}

// TestRenderSystemHealthFENONE checks that an FE_NONE system health sample
// renders with only its hostname and health labels, even when the metric
// carries GPU, MIG, switch, link or CPU fields.
func TestRenderSystemHealthFENONE(t *testing.T) {
	counter := counters.Counter{
		FieldName: counters.DCGMExpSystemHealthStatus,
		PromType:  "gauge",
		Help:      "Current DCGM-reported host-level health status; currently the IMEX health watch only.",
	}
	metrics := collector.MetricsByCounter{
		counter: []collector.Metric{
			{
				Counter:      counter,
				Value:        "20",
				Hostname:     "test-host",
				GPU:          "7",
				GPUUUID:      "GPU-should-not-render",
				GPUDevice:    "nvidia7",
				GPUModelName: "should-not-render",
				GPUPCIBusID:  "0000:01:00.0",
				UUID:         "UUID",
				MigProfile:   "1g.10gb",
				NvSwitch:     "3",
				NvLink:       "4",
				CPUSerial:    "CPU-should-not-render",
				Labels: map[string]string{
					"health_watch":          "IMEX",
					"health_error_code":     "DCGM_FR_IMEX_UNHEALTHY",
					"health_error_severity": "ISOLATE",
					"health_error_category": "SOFTWARE_OTHER",
				},
				Attributes: map[string]string{},
			},
		},
	}

	var output bytes.Buffer
	err := RenderGroup(&output, dcgm.FE_NONE, metrics)
	require.NoError(t, err)

	rendered := output.String()
	assert.Equal(t, 1, strings.Count(rendered, "# HELP DCGM_EXP_SYSTEM_HEALTH_STATUS"))
	assert.Equal(t, 1, strings.Count(rendered, "# TYPE DCGM_EXP_SYSTEM_HEALTH_STATUS gauge"))
	assert.Equal(t, 1, strings.Count(rendered, "DCGM_EXP_SYSTEM_HEALTH_STATUS{"))
	for _, label := range []string{
		"hostname=\"test-host\"",
		"health_watch=\"IMEX\"",
		"health_error_code=\"DCGM_FR_IMEX_UNHEALTHY\"",
		"health_error_severity=\"ISOLATE\"",
		"health_error_category=\"SOFTWARE_OTHER\"",
	} {
		assert.Contains(t, rendered, label)
	}
	for _, forbidden := range []string{
		"gpu=\"",
		"UUID=\"",
		"uuid=\"",
		"pci_bus_id=\"",
		"device=\"",
		"modelName=\"",
		"model_name=\"",
		"GPU_I_",
		"cpu=\"",
		"cpucore=\"",
		"nvlink=\"",
		"nvswitch=\"",
	} {
		assert.NotContains(t, rendered, forbidden)
	}
	assert.Contains(t, rendered, "} 20\n")
}

func TestRenderGPUHealthDriverRecovery(t *testing.T) {
	testCases := []struct {
		name     string
		code     string
		severity string
		category string
	}{
		{name: "IMEX recovery", code: "DCGM_FR_GPU_RECOVERY_IMEX_DOMAIN", severity: "RESET", category: "HARDWARE_OTHER"},
		{name: "unknown recovery", code: "DCGM_FR_UNKNOWN(424242)", severity: "UNKNOWN", category: "UNKNOWN"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			counter := counters.Counter{FieldName: counters.DCGMExpGPUHealthStatus, PromType: "gauge", Help: "GPU health status."}
			metrics := collector.MetricsByCounter{counter: {{
				Counter: counter, Value: "20", GPU: "3", GPUUUID: "GPU-recovery", UUID: "UUID",
				GPUDevice: "nvidia3", GPUModelName: "Test GPU", GPUPCIBusID: "0000:03:00.0", Hostname: "test-host",
				Labels: map[string]string{
					"health_watch": "DRIVER", "health_error_code": tc.code,
					"health_error_severity": tc.severity, "health_error_category": tc.category,
				},
			}}}
			var output bytes.Buffer
			require.NoError(t, RenderGroup(&output, dcgm.FE_GPU, metrics))
			want := "# HELP DCGM_EXP_GPU_HEALTH_STATUS GPU health status.\n" +
				"# TYPE DCGM_EXP_GPU_HEALTH_STATUS gauge\n" +
				fmt.Sprintf("DCGM_EXP_GPU_HEALTH_STATUS{gpu=\"3\",UUID=\"GPU-recovery\",pci_bus_id=\"0000:03:00.0\","+
					"device=\"nvidia3\",modelName=\"Test GPU\",hostname=\"test-host\","+
					"health_error_category=\"%s\",health_error_code=\"%s\",health_error_severity=\"%s\",health_watch=\"DRIVER\"} 20\n",
					tc.category, tc.code, tc.severity)
			assert.Equal(t, want, output.String())
		})
	}
}
