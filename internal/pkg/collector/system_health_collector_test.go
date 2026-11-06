/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package collector

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// TestNewSystemHealthStatusCollector checks that the constructor primes the
// IMEX health group once on success, returns errIMEXHealthUnsupported when the
// hostengine lacks the IMEX watch, and destroys any group it created on failure.
func TestNewSystemHealthStatusCollector(t *testing.T) {
	groupID := systemHealthTestGroupHandle()

	tests := []struct {
		name            string
		counterList     counters.CounterList
		setUp           func(*mockdcgm.MockDCGM)
		wantErr         string
		wantUnsupported bool
		cleanup         bool
	}{
		{
			name:        "counter absent",
			counterList: nil,
			wantErr:     "collector is disabled",
		},
		{
			name:        "supported runtime primes once",
			counterList: systemHealthTestCounterList(),
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(
					dcgm.DCGM_HEALTH_WATCH_IMEX, nil,
				)
				expectSystemHealthGroupInitialization(client, groupID)
				client.EXPECT().HealthCheck(groupID).Return(dcgm.HealthResponse{}, nil).Times(1)
				client.EXPECT().DestroyGroup(groupID).Return(nil).Times(1)
			},
			cleanup: true,
		},
		{
			name:        "old runtime",
			counterList: systemHealthTestCounterList(),
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(
					dcgm.DCGM_HEALTH_WATCH_PCIE, nil,
				)
			},
			wantErr:         "the DCGM hostengine does not support the IMEX health watch",
			wantUnsupported: true,
		},
		{
			name:        "capability query error",
			counterList: systemHealthTestCounterList(),
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(
					dcgm.HealthSystem(0), errors.New("query failed"),
				)
			},
			wantErr: "query supported health systems",
		},
		{
			name:        "capability API unavailable",
			counterList: systemHealthTestCounterList(),
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(dcgm.HealthSystem(0), &dcgm.FeatureUnavailableError{Feature: "health capability query", Symbol: "dcgmHealthGetSupportedSystems"})
			},
			wantErr:         "the DCGM hostengine does not support the IMEX health watch: health capability query is unavailable",
			wantUnsupported: true,
		},
		{
			name:        "group initialization error is fatal",
			counterList: systemHealthTestCounterList(),
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(
					dcgm.DCGM_HEALTH_WATCH_IMEX, nil,
				)
				client.EXPECT().GetSupportedDevices().Return(nil, errors.New("group setup failed"))
			},
			wantErr: "initialize system health group",
		},
		{
			name:        "priming error destroys group and is fatal",
			counterList: systemHealthTestCounterList(),
			setUp: func(client *mockdcgm.MockDCGM) {
				client.EXPECT().HealthGetSupportedSystems().Return(
					dcgm.DCGM_HEALTH_WATCH_IMEX, nil,
				)
				expectSystemHealthGroupInitialization(client, groupID)
				client.EXPECT().HealthCheck(groupID).Return(
					dcgm.HealthResponse{}, errors.New("prime failed"),
				)
				client.EXPECT().DestroyGroup(groupID).Return(nil).Times(1)
			},
			wantErr: "prime system health check",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mockdcgm.NewMockDCGM(ctrl)
			if tt.setUp != nil {
				tt.setUp(client)
			}

			got, err := newSystemHealthStatusCollectorWithClient(client, tt.counterList, "test-host")

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Equal(t, tt.wantUnsupported, errors.Is(err, errIMEXHealthUnsupported))
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)

			// Cleanup is idempotent: the mock allows only one DestroyGroup call.
			if tt.cleanup {
				got.Cleanup()
				got.Cleanup()
			}
		})
	}
}

// TestSystemHealthStatusCollectorGetMetrics checks that a scrape reports the
// most severe host-level IMEX incident as one sample, ignores incidents for
// other watches or entities, and reports PASS when there is none.
func TestSystemHealthStatusCollectorGetMetrics(t *testing.T) {
	healthIncident := func(
		entityGroup dcgm.Field_Entity_Group,
		entityID uint,
		system dcgm.HealthSystem,
		health dcgm.HealthResult,
		code dcgm.HealthCheckErrorCode,
	) dcgm.Incident {
		return dcgm.Incident{
			System: system,
			Health: health,
			Error: dcgm.DiagErrorDetail{
				Message: "test incident",
				Code:    code,
			},
			EntityInfo: dcgm.GroupEntityPair{
				EntityGroupId: entityGroup,
				EntityId:      entityID,
			},
		}
	}

	tests := []struct {
		name       string
		response   dcgm.HealthResponse
		healthErr  error
		wantHealth dcgm.HealthResult
		wantCode   dcgm.HealthCheckErrorCode
		wantErr    bool
	}{
		{
			name:       "no incidents emits pass",
			response:   dcgm.HealthResponse{},
			wantHealth: dcgm.DCGM_HEALTH_RESULT_PASS,
			wantCode:   dcgm.DCGM_FR_OK,
		},
		{
			name: "one FE_NONE zero IMEX incident",
			response: dcgm.HealthResponse{
				OverallHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
				Incidents: []dcgm.Incident{
					healthIncident(
						dcgm.FE_NONE,
						0,
						dcgm.DCGM_HEALTH_WATCH_IMEX,
						dcgm.DCGM_HEALTH_RESULT_FAIL,
						dcgm.DCGM_FR_IMEX_UNHEALTHY,
					),
				},
			},
			wantHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
			wantCode:   dcgm.DCGM_FR_IMEX_UNHEALTHY,
		},
		{
			name: "domain and daemon incidents reduce to the more severe one",
			response: dcgm.HealthResponse{
				OverallHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
				Incidents: []dcgm.Incident{
					healthIncident(
						dcgm.FE_NONE,
						0,
						dcgm.DCGM_HEALTH_WATCH_IMEX,
						dcgm.DCGM_HEALTH_RESULT_FAIL,
						dcgm.DCGM_FR_IMEX_UNHEALTHY,
					),
					healthIncident(
						dcgm.FE_NONE,
						0,
						dcgm.DCGM_HEALTH_WATCH_IMEX,
						dcgm.DCGM_HEALTH_RESULT_WARN,
						dcgm.DCGM_FR_INTERNAL,
					),
				},
			},
			wantHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
			wantCode:   dcgm.DCGM_FR_IMEX_UNHEALTHY,
		},
		{
			name: "fail wins over warn",
			response: dcgm.HealthResponse{
				OverallHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
				Incidents: []dcgm.Incident{
					healthIncident(
						dcgm.FE_NONE,
						0,
						dcgm.DCGM_HEALTH_WATCH_IMEX,
						dcgm.DCGM_HEALTH_RESULT_WARN,
						dcgm.DCGM_FR_INTERNAL,
					),
					healthIncident(
						dcgm.FE_NONE,
						0,
						dcgm.DCGM_HEALTH_WATCH_IMEX,
						dcgm.DCGM_HEALTH_RESULT_FAIL,
						dcgm.DCGM_FR_IMEX_UNHEALTHY,
					),
				},
			},
			wantHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
			wantCode:   dcgm.DCGM_FR_IMEX_UNHEALTHY,
		},
		{
			name: "equal health keeps first incident",
			response: dcgm.HealthResponse{
				OverallHealth: dcgm.DCGM_HEALTH_RESULT_WARN,
				Incidents: []dcgm.Incident{
					healthIncident(
						dcgm.FE_NONE,
						0,
						dcgm.DCGM_HEALTH_WATCH_IMEX,
						dcgm.DCGM_HEALTH_RESULT_WARN,
						dcgm.DCGM_FR_IMEX_UNHEALTHY,
					),
					healthIncident(
						dcgm.FE_NONE,
						0,
						dcgm.DCGM_HEALTH_WATCH_IMEX,
						dcgm.DCGM_HEALTH_RESULT_WARN,
						dcgm.DCGM_FR_INTERNAL,
					),
				},
			},
			wantHealth: dcgm.DCGM_HEALTH_RESULT_WARN,
			wantCode:   dcgm.DCGM_FR_IMEX_UNHEALTHY,
		},
		{
			name: "GPU incident is ignored",
			response: dcgm.HealthResponse{
				OverallHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
				Incidents: []dcgm.Incident{
					healthIncident(
						dcgm.FE_GPU,
						0,
						dcgm.DCGM_HEALTH_WATCH_IMEX,
						dcgm.DCGM_HEALTH_RESULT_FAIL,
						dcgm.DCGM_FR_IMEX_UNHEALTHY,
					),
				},
			},
			wantHealth: dcgm.DCGM_HEALTH_RESULT_PASS,
			wantCode:   dcgm.DCGM_FR_OK,
		},
		{
			name: "nonzero FE_NONE entity is ignored",
			response: dcgm.HealthResponse{
				OverallHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
				Incidents: []dcgm.Incident{
					healthIncident(
						dcgm.FE_NONE,
						1,
						dcgm.DCGM_HEALTH_WATCH_IMEX,
						dcgm.DCGM_HEALTH_RESULT_FAIL,
						dcgm.DCGM_FR_IMEX_UNHEALTHY,
					),
				},
			},
			wantHealth: dcgm.DCGM_HEALTH_RESULT_PASS,
			wantCode:   dcgm.DCGM_FR_OK,
		},
		{
			name: "non IMEX system is ignored",
			response: dcgm.HealthResponse{
				OverallHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
				Incidents: []dcgm.Incident{
					healthIncident(
						dcgm.FE_NONE,
						0,
						dcgm.DCGM_HEALTH_WATCH_NVLINK,
						dcgm.DCGM_HEALTH_RESULT_FAIL,
						dcgm.DCGM_FR_IMEX_UNHEALTHY,
					),
				},
			},
			wantHealth: dcgm.DCGM_HEALTH_RESULT_PASS,
			wantCode:   dcgm.DCGM_FR_OK,
		},
		{
			name: "GPU recovery action incident is left to the GPU health metric",
			response: dcgm.HealthResponse{
				OverallHealth: dcgm.DCGM_HEALTH_RESULT_FAIL,
				Incidents: []dcgm.Incident{
					healthIncident(
						dcgm.FE_GPU,
						0,
						dcgm.DCGM_HEALTH_WATCH_DRIVER,
						dcgm.DCGM_HEALTH_RESULT_FAIL,
						dcgm.DCGM_FR_GPU_RECOVERY_RESET,
					),
				},
			},
			wantHealth: dcgm.DCGM_HEALTH_RESULT_PASS,
			wantCode:   dcgm.DCGM_FR_OK,
		},
		{
			name:      "health check error returns no sample",
			healthErr: errors.New("health check failed"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mockdcgm.NewMockDCGM(ctrl)
			groupID := systemHealthTestGroupHandle()
			collector := newPrimedSystemHealthCollectorForTest(t, client, groupID)
			client.EXPECT().HealthCheck(groupID).Return(tt.response, tt.healthErr)

			if tt.wantCode != dcgm.DCGM_FR_OK && !tt.wantErr {
				client.EXPECT().GetErrorMeta(tt.wantCode).Return(&dcgm.ErrorMeta{
					ErrorID:  tt.wantCode,
					Severity: dcgm.DCGM_ERROR_ISOLATE,
					Category: dcgm.DCGM_FR_EC_SOFTWARE_OTHER,
				})
			}

			metrics, err := collector.GetMetrics()

			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, metrics)
				return
			}

			require.NoError(t, err)
			counter := systemHealthTestCounterList()[0]
			require.Len(t, metrics, 1)
			require.Len(t, metrics[counter], 1)
			metric := metrics[counter][0]
			assert.Equal(t, fmt.Sprint(tt.wantHealth), metric.Value)
			assert.Equal(t, "test-host", metric.Hostname)

			wantSeverity := "NONE"
			wantCategory := "NONE"
			if tt.wantCode != dcgm.DCGM_FR_OK {
				wantSeverity = "ISOLATE"
				wantCategory = "SOFTWARE_OTHER"
			}
			assert.Equal(t, map[string]string{
				"health_watch":          "IMEX",
				"health_error_code":     healthCheckErrorToString(tt.wantCode),
				"health_error_severity": wantSeverity,
				"health_error_category": wantCategory,
			}, metric.Labels)
			assert.Empty(t, metric.GPU)
			assert.Empty(t, metric.GPUUUID)
			assert.Empty(t, metric.GPUPCIBusID)
		})
	}
}

// systemHealthTestCounterList returns an exporter counter list that enables
// only DCGM_EXP_SYSTEM_HEALTH_STATUS.
func systemHealthTestCounterList() counters.CounterList {
	return counters.CounterList{
		{
			FieldName: counters.DCGMExpSystemHealthStatus,
			PromType:  "gauge",
			Help:      "Current DCGM-reported host-level health status; currently the IMEX health watch only.",
		},
	}
}

// systemHealthTestGroupHandle returns a fixed non-zero group handle so mock
// expectations can tell the system health group apart from a zero handle.
func systemHealthTestGroupHandle() dcgm.GroupHandle {
	groupID := dcgm.GroupHandle{}
	groupID.SetHandle(uintptr(42))
	return groupID
}

// TestSystemHealthStatusCollectorUsesItsOwnClient checks that a collector built
// on one DCGM client keeps using it for scrapes, error metadata and cleanup
// after the process client is replaced, as happens during DCGM recovery.
func TestSystemHealthStatusCollectorUsesItsOwnClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	ownClient := mockdcgm.NewMockDCGM(ctrl)
	replacementClient := mockdcgm.NewMockDCGM(ctrl)
	previous := dcgmprovider.Client()
	dcgmprovider.SetClient(replacementClient)
	t.Cleanup(func() { dcgmprovider.SetClient(previous) })
	groupID := systemHealthTestGroupHandle()

	ownClient.EXPECT().HealthGetSupportedSystems().Return(dcgm.DCGM_HEALTH_WATCH_IMEX, nil)
	expectSystemHealthGroupInitialization(ownClient, groupID)
	ownClient.EXPECT().HealthCheck(groupID).Return(dcgm.HealthResponse{}, nil)
	collector, err := newSystemHealthStatusCollectorWithClient(ownClient, systemHealthTestCounterList(), "test-host")
	require.NoError(t, err)

	ownClient.EXPECT().HealthCheck(groupID).Return(dcgm.HealthResponse{
		Incidents: []dcgm.Incident{{
			System:     dcgm.DCGM_HEALTH_WATCH_IMEX,
			Health:     dcgm.DCGM_HEALTH_RESULT_WARN,
			Error:      dcgm.DiagErrorDetail{Code: dcgm.DCGM_FR_IMEX_UNHEALTHY},
			EntityInfo: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_NONE},
		}},
	}, nil)
	ownClient.EXPECT().GetErrorMeta(dcgm.DCGM_FR_IMEX_UNHEALTHY).Return(nil)
	metrics, err := collector.GetMetrics()
	require.NoError(t, err)
	require.Len(t, metrics, 1)

	ownClient.EXPECT().DestroyGroup(groupID).Return(nil)
	collector.Cleanup()
}

// expectSystemHealthGroupInitialization expects one-GPU health group setup with
// the IMEX and DRIVER watches on client, returning groupID from CreateGroup.
func expectSystemHealthGroupInitialization(client *mockdcgm.MockDCGM, groupID dcgm.GroupHandle) {
	client.EXPECT().GetSupportedDevices().Return([]uint{0}, nil)
	client.EXPECT().CreateGroup(gomock.Cond(func(value any) bool {
		name, ok := value.(string)
		return ok && strings.HasPrefix(name, "system_health_monitor_")
	})).Return(groupID, nil)
	client.EXPECT().AddEntityToGroup(groupID, dcgm.FE_GPU, uint(0)).Return(nil)
	// DRIVER keeps DCGM's shared GPU recovery-action watch; see systemHealthWatches.
	client.EXPECT().HealthSet(groupID, dcgm.DCGM_HEALTH_WATCH_IMEX|dcgm.DCGM_HEALTH_WATCH_DRIVER).Return(nil)
}

// newPrimedSystemHealthCollectorForTest builds a system health collector on
// client and consumes the priming HealthCheck, so the next HealthCheck the
// caller expects is the scrape. It does not expect cleanup.
func newPrimedSystemHealthCollectorForTest(
	t *testing.T,
	client *mockdcgm.MockDCGM,
	groupID dcgm.GroupHandle,
) Collector {
	t.Helper()

	client.EXPECT().HealthGetSupportedSystems().Return(
		dcgm.DCGM_HEALTH_WATCH_IMEX, nil,
	)
	expectSystemHealthGroupInitialization(client, groupID)
	client.EXPECT().HealthCheck(groupID).Return(dcgm.HealthResponse{}, nil)

	collector, err := newSystemHealthStatusCollectorWithClient(client, systemHealthTestCounterList(), "test-host")
	require.NoError(t, err)
	return collector
}
