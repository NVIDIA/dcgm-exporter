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
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

const expectedResponse = `# HELP TEST_METRIC 
# TYPE TEST_METRIC gauge
TEST_METRIC{gpu="0",UUID="GPU-00000000-0000-0000-0000-000000000000",device="nvidia0",modelName="NVIDIA T400 4GB",Hostname="testhost"} 42
`

func TestMetrics(t *testing.T) {
	ctrl := gomock.NewController(t)

	metrics := getMetricsByCounterWithTestMetric()

	tests := []struct {
		name        string
		group       dcgm.Field_Entity_Group
		collector   func() Collector
		transformer func() Transform
		assert      func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:  "Returns 200",
			group: dcgm.FE_GPU,
			collector: func() Collector {
				mockCollector := NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(metrics, nil).AnyTimes()
				return mockCollector
			},
			transformer: func() Transform {
				mockTransformation := NewMockTransform(ctrl)
				mockTransformation.EXPECT().Process(gomock.Any(), gomock.Any())
				return mockTransformation
			},
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusOK, recorder.Code)
				assert.Equal(t, expectedResponse, recorder.Body.String())
			},
		},
		{
			name:  "Returns 500 when Collector return error",
			group: dcgm.FE_GPU,
			collector: func() Collector {
				mockCollector := NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(nil, errors.New("boom")).AnyTimes()
				return mockCollector
			},
			transformer: func() Transform {
				return NewMockTransform(ctrl)
			},
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusInternalServerError, recorder.Code)
				assert.Equal(t, internalServerError, strings.TrimSpace(recorder.Body.String()))
			},
		},
		{
			name:  "Returns 500 when Transformer returns error",
			group: dcgm.FE_GPU,
			collector: func() Collector {
				mockCollector := NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(metrics, nil).AnyTimes()
				return mockCollector
			},
			transformer: func() Transform {
				mockTransformation := NewMockTransform(ctrl)
				mockTransformation.EXPECT().Process(gomock.Any(), gomock.Any()).Return(errors.New("boom")).AnyTimes()
				return mockTransformation
			},
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusInternalServerError, recorder.Code)
				assert.Equal(t, internalServerError, strings.TrimSpace(recorder.Body.String()))
			},
		},
		{
			name:  "Returns 500 when group is unknown",
			group: dcgm.FE_NONE,
			collector: func() Collector {
				mockCollector := NewMockCollector(ctrl)
				mockCollector.EXPECT().GetMetrics().Return(metrics, nil).AnyTimes()
				return mockCollector
			},
			transformer: func() Transform {
				mockTransformation := NewMockTransform(ctrl)
				mockTransformation.EXPECT().Process(gomock.Any(), gomock.Any())
				return mockTransformation
			},
			assert: func(t *testing.T, recorder *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusInternalServerError, recorder.Code)
				assert.Equal(t, internalServerError, strings.TrimSpace(recorder.Body.String()))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := NewRegistry()
			reg.Register(tt.group, tt.collector())

			mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
			mockDeviceInfo.EXPECT().InfoType().Return(tt.group).AnyTimes()
			mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{}).AnyTimes()

			defaultFieldEntityGroupTypeSystemInfoItem := FieldEntityGroupTypeSystemInfoItem{
				DeviceFields: []dcgm.Short{42},
				DeviceInfo:   mockDeviceInfo,
			}

			metricServer := &MetricsServer{
				registry: reg,
				fieldEntityGroupTypeSystemInfo: &FieldEntityGroupTypeSystemInfo{
					items: map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem{
						tt.group: defaultFieldEntityGroupTypeSystemInfoItem,
					},
				},
				transformations: []Transform{
					tt.transformer(),
				},
			}

			recorder := httptest.NewRecorder()
			metricServer.Metrics(recorder, nil)
			if tt.assert != nil {
				tt.assert(t, recorder)
			}
		})
	}
}

// mockResponseWriter is a custom writer that simulates a network operation error.
type mockResponseWriter struct {
	httptest.ResponseRecorder
}

func (m *mockResponseWriter) Write([]byte) (int, error) {
	// Simulate a network operation error.
	return 0, &net.OpError{
		Op:     "write",
		Net:    "tcp",
		Source: nil,
		Addr:   nil,
		Err:    syscall.EPIPE,
	}
}

func TestMetricsReturnsErrorWhenClientClosedConnection(t *testing.T) {
	ctrl := gomock.NewController(t)

	metrics := getMetricsByCounterWithTestMetric()

	mockCollector := NewMockCollector(ctrl)
	mockCollector.EXPECT().GetMetrics().Return(metrics, nil).AnyTimes()

	reg := NewRegistry()
	reg.Register(dcgm.FE_GPU, mockCollector)

	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_CPU).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{}).AnyTimes()

	defaultFieldEntityGroupTypeSystemInfoItem := FieldEntityGroupTypeSystemInfoItem{
		DeviceFields: []dcgm.Short{42},
		DeviceInfo:   mockDeviceInfo,
	}

	metricServer := &MetricsServer{
		registry: reg,
		fieldEntityGroupTypeSystemInfo: &FieldEntityGroupTypeSystemInfo{
			items: map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem{
				dcgm.FE_CPU: defaultFieldEntityGroupTypeSystemInfoItem,
			},
		},
		transformations: []Transform{},
	}
	recorder := &mockResponseWriter{}
	metricServer.Metrics(recorder, nil)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Nil(t, recorder.Body)
}

func TestHealthReturnsOK(t *testing.T) {
	metricServer := &MetricsServer{}
	recorder := httptest.NewRecorder()
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusOK, recorder.Code)
}

func TestHealthReturnsOKWhenWriteReturnsError(t *testing.T) {
	metricServer := &MetricsServer{}
	recorder := &mockResponseWriter{}
	metricServer.Health(recorder, nil)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
}
