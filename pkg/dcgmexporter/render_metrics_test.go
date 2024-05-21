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

package dcgmexporter

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func getMetricsByCounterWithTestMetric() MetricsByCounter {
	metrics := MetricsByCounter{}
	counter := getTestMetric()

	metrics[counter] = append(metrics[counter], Metric{
		GPU:          "0",
		GPUDevice:    "nvidia0",
		GPUModelName: "NVIDIA T400 4GB",
		Hostname:     "testhost",
		UUID:         "UUID",
		GPUUUID:      "GPU-00000000-0000-0000-0000-000000000000",
		Counter:      counter,
		Value:        "42",
		Attributes:   map[string]string{},
	})
	return metrics
}

func getTestMetric() appconfig.Counter {
	counter := appconfig.Counter{
		FieldID:   2000,
		FieldName: "TEST_METRIC",
		PromType:  "gauge",
	}
	return counter
}

func Test_render(t *testing.T) {
	metrics := getMetricsByCounterWithTestMetric()

	tests := []struct {
		name    string
		group   dcgm.Field_Entity_Group
		metrics MetricsByCounter
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name:    fmt.Sprintf("Render %s", dcgm.FE_GPU.String()),
			group:   dcgm.FE_GPU,
			metrics: metrics,
			want: `# HELP TEST_METRIC 
# TYPE TEST_METRIC gauge
TEST_METRIC{gpu="0",UUID="GPU-00000000-0000-0000-0000-000000000000",device="nvidia0",modelName="NVIDIA T400 4GB",Hostname="testhost"} 42
`,
		},
		{
			name:    fmt.Sprintf("Render %s", dcgm.FE_SWITCH.String()),
			group:   dcgm.FE_SWITCH,
			metrics: metrics,
			want: `# HELP TEST_METRIC 
# TYPE TEST_METRIC gauge
TEST_METRIC{nvswitch="0",Hostname="testhost"} 42
`,
		},
		{
			name:    fmt.Sprintf("Render %s", dcgm.FE_LINK.String()),
			group:   dcgm.FE_LINK,
			metrics: metrics,
			want: `# HELP TEST_METRIC 
# TYPE TEST_METRIC gauge
TEST_METRIC{nvlink="0",nvswitch="nvidia0",Hostname="testhost"} 42
`,
		},
		{
			name:    fmt.Sprintf("Render %s", dcgm.FE_CPU.String()),
			group:   dcgm.FE_CPU,
			metrics: metrics,
			want: `# HELP TEST_METRIC 
# TYPE TEST_METRIC gauge
TEST_METRIC{cpu="0",Hostname="testhost"} 42
`,
		},
		{
			name:    fmt.Sprintf("Render %s", dcgm.FE_CPU_CORE.String()),
			group:   dcgm.FE_CPU_CORE,
			metrics: metrics,
			want: `# HELP TEST_METRIC 
# TYPE TEST_METRIC gauge
TEST_METRIC{cpucore="0",cpu="nvidia0",Hostname="testhost"} 42
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
			w := &bytes.Buffer{}
			err := renderGroup(w, tt.group, tt.metrics)
			if tt.wantErr != nil &&
				!tt.wantErr(t, err, fmt.Sprintf("renderGroup(w, %v, %v)", tt.group, tt.metrics)) {
				return
			}
			assert.Equalf(t, tt.want, w.String(), "renderGroup(w, %v, %v)", tt.group, tt.metrics)
		})
	}
}
