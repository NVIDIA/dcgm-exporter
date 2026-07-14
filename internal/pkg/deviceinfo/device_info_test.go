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

package deviceinfo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

var fakeProfileName = "2fake.4gb"

func captureWarnLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	return &logs
}

func warningRecords(t *testing.T, logs *bytes.Buffer, message string) []map[string]any {
	t.Helper()

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}

		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		if record["msg"] == message {
			records = append(records, record)
		}
	}

	return records
}

// dcgmStringFieldValue builds a string DCGM field value for device identity fallback tests.
func dcgmStringFieldValue(fieldID dcgm.Short, value string) dcgm.FieldValue_v1 {
	var raw [4096]byte
	copy(raw[:], value)
	return dcgm.FieldValue_v1{
		FieldID:   fieldID,
		FieldType: dcgm.DCGM_FT_STRING,
		Value:     raw,
	}
}

func SpoofGPUDeviceInfo() Info {
	var deviceInfo Info
	deviceInfo.gpuCount = 2
	deviceInfo.gpus[0].DeviceInfo.GPU = 0
	gi := GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{GpuUuid: "fake", NvmlProfileSlices: 3},
		ProfileName: fakeProfileName,
		EntityId:    0,
	}
	deviceInfo.gpus[0].GPUInstances = append(deviceInfo.gpus[0].GPUInstances, gi)
	gi2 := GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{GpuUuid: "fake", NvmlInstanceId: 1, NvmlProfileSlices: 3},
		ProfileName: fakeProfileName,
		EntityId:    14,
	}
	deviceInfo.gpus[1].GPUInstances = append(deviceInfo.gpus[1].GPUInstances, gi2)
	deviceInfo.gpus[1].DeviceInfo.GPU = 1

	return deviceInfo
}

type nonInfoProvider struct{}

func (nonInfoProvider) GPUCount() uint                    { return 0 }
func (nonInfoProvider) GPUs() []GPUInfo                   { return nil }
func (nonInfoProvider) GPU(uint) GPUInfo                  { return GPUInfo{} }
func (nonInfoProvider) Switches() []SwitchInfo            { return nil }
func (nonInfoProvider) Switch(uint) SwitchInfo            { return SwitchInfo{} }
func (nonInfoProvider) CPUs() []CPUInfo                   { return nil }
func (nonInfoProvider) CPU(uint) CPUInfo                  { return CPUInfo{} }
func (nonInfoProvider) GOpts() appconfig.DeviceOptions    { return appconfig.DeviceOptions{} }
func (nonInfoProvider) SOpts() appconfig.DeviceOptions    { return appconfig.DeviceOptions{} }
func (nonInfoProvider) COpts() appconfig.DeviceOptions    { return appconfig.DeviceOptions{} }
func (nonInfoProvider) InfoType() dcgm.Field_Entity_Group { return dcgm.FE_NONE }
func (nonInfoProvider) IsCPUWatched(uint) bool            { return false }
func (nonInfoProvider) IsCoreWatched(uint, uint) bool     { return false }
func (nonInfoProvider) IsSwitchWatched(uint) bool         { return false }
func (nonInfoProvider) IsLinkWatched(uint, uint) bool     { return false }

func TestWarnMIGInstancesWithoutComputeInstances(t *testing.T) {
	t.Run("GI with compute instances does not warn", func(t *testing.T) {
		logs := captureWarnLogs(t)
		info := Info{gpuCount: 1}
		info.gpus[0].DeviceInfo.GPU = 0
		info.gpus[0].MigEnabled = true
		info.gpus[0].GPUInstances = append(info.gpus[0].GPUInstances, GPUInstanceInfo{
			EntityId:         1,
			ProfileName:      "1g.10gb",
			ComputeInstances: []ComputeInstanceInfo{{EntityId: 2}},
		})

		info.WarnMIGInstancesWithoutComputeInstances()

		assert.Empty(t, warningRecords(t, logs, migGPUInstanceWithoutCIWarning))
	})

	t.Run("GI without compute instances warns with attrs", func(t *testing.T) {
		logs := captureWarnLogs(t)
		info := Info{gpuCount: 1}
		info.gpus[0].DeviceInfo.GPU = 0
		info.gpus[0].MigEnabled = true
		info.gpus[0].GPUInstances = append(info.gpus[0].GPUInstances, GPUInstanceInfo{
			EntityId:    7,
			ProfileName: "1g.10gb",
			Info:        dcgm.MigEntityInfo{NvmlInstanceId: 3},
		})

		info.WarnMIGInstancesWithoutComputeInstances()

		records := warningRecords(t, logs, migGPUInstanceWithoutCIWarning)
		require.Len(t, records, 1)
		assert.EqualValues(t, 0, records[0]["gpu_id"])
		assert.EqualValues(t, 7, records[0]["gpu_instance_entity_id"])
		assert.EqualValues(t, 3, records[0]["nvml_instance_id"])
		assert.Equal(t, "1g.10gb", records[0]["mig_profile"])
		assert.Equal(t, migGPUInstanceWithoutCIWarningHint, records[0]["hint"])
	})

	t.Run("non-MIG GPU does not warn", func(t *testing.T) {
		logs := captureWarnLogs(t)
		info := Info{gpuCount: 1}
		info.gpus[0].DeviceInfo.GPU = 0
		info.gpus[0].MigEnabled = false

		info.WarnMIGInstancesWithoutComputeInstances()

		assert.Empty(t, warningRecords(t, logs, migGPUInstanceWithoutCIWarning))
	})

	t.Run("mixed GPUs warn only for empty GI", func(t *testing.T) {
		logs := captureWarnLogs(t)
		info := Info{gpuCount: 2}
		info.gpus[0].DeviceInfo.GPU = 0
		info.gpus[0].MigEnabled = true
		info.gpus[0].GPUInstances = append(info.gpus[0].GPUInstances, GPUInstanceInfo{
			EntityId:         1,
			ProfileName:      "3g.40gb",
			ComputeInstances: []ComputeInstanceInfo{{EntityId: 2}},
		})
		info.gpus[1].DeviceInfo.GPU = 1
		info.gpus[1].MigEnabled = true
		info.gpus[1].GPUInstances = append(info.gpus[1].GPUInstances, GPUInstanceInfo{
			EntityId:    14,
			ProfileName: "1g.10gb",
			Info:        dcgm.MigEntityInfo{NvmlInstanceId: 4},
		})

		info.WarnMIGInstancesWithoutComputeInstances()

		records := warningRecords(t, logs, migGPUInstanceWithoutCIWarning)
		require.Len(t, records, 1)
		assert.EqualValues(t, 1, records[0]["gpu_id"])
		assert.EqualValues(t, 14, records[0]["gpu_instance_entity_id"])
		assert.EqualValues(t, 4, records[0]["nvml_instance_id"])
		assert.Equal(t, "1g.10gb", records[0]["mig_profile"])
	})
}

func TestGetters(t *testing.T) {
	fakeDevices := SpoofGPUDevices()
	fakeDeviceInfo := [dcgm.MAX_NUM_DEVICES]GPUInfo{}
	fakeDeviceInfo[0] = GPUInfo{
		DeviceInfo: fakeDevices[0],
		MigEnabled: false,
	}
	fakeDeviceInfo[1] = GPUInfo{
		DeviceInfo: fakeDevices[1],
		MigEnabled: true,
	}

	fakeSwitches := []SwitchInfo{
		{
			EntityId: 0,
			NvLinks:  nil,
		},
		{
			EntityId: 1,
			NvLinks:  nil,
		},
	}

	fakeCPUs := []CPUInfo{
		{
			EntityId: 0,
			Cores:    nil,
		},
		{
			EntityId: 1,
			Cores:    nil,
		},
	}

	fakeGOpts := appconfig.DeviceOptions{
		Flex: true,
	}

	fakeSOpts := appconfig.DeviceOptions{
		Flex:       false,
		MajorRange: []int{-1},
		MinorRange: []int{1, 2, 3},
	}

	fakeCOpts := appconfig.DeviceOptions{
		Flex:       false,
		MajorRange: []int{0, 1},
		MinorRange: []int{1, 2, 3},
	}

	fakeInfoType := dcgm.FE_GPU

	deviceInfo := Info{
		gpuCount: uint(len(fakeDevices)),
		gpus:     fakeDeviceInfo,
		switches: fakeSwitches,
		cpus:     fakeCPUs,
		gOpt:     fakeGOpts,
		sOpt:     fakeSOpts,
		cOpt:     fakeCOpts,
		infoType: fakeInfoType,
	}

	assert.Equal(t, uint(len(fakeDevices)), deviceInfo.GPUCount(), "GPU count mismatch")
	assert.Equal(t, fakeDeviceInfo[:deviceInfo.GPUCount()], deviceInfo.GPUs(), "GPUs mismatch")
	assert.Equal(t, fakeDeviceInfo[0], deviceInfo.GPU(uint(0)), "GPU mismatch")
	assert.Equal(t, fakeSwitches, deviceInfo.Switches(), "Switches mismatch")
	assert.Equal(t, fakeSwitches[1], deviceInfo.Switch(uint(1)), "Switch mismatch")
	assert.Equal(t, fakeCPUs, deviceInfo.CPUs(), "CPUs mismatch")
	assert.Equal(t, fakeCPUs[1], deviceInfo.CPU(uint(1)), "CPU mismatch")
	assert.Equal(t, fakeGOpts, deviceInfo.GOpts(), "GPUs options mismatch")
	assert.Equal(t, fakeSOpts, deviceInfo.SOpts(), "Switches options mismatch")
	assert.Equal(t, fakeCOpts, deviceInfo.COpts(), "CPUs options mismatch")
	assert.Equal(t, fakeInfoType, deviceInfo.InfoType(), "InfoType mismatch")
}

func TestInfoJSONAndBase64RoundTrip(t *testing.T) {
	info := SpoofGPUDeviceInfo()
	info.infoType = dcgm.FE_GPU
	info.gOpt = appconfig.DeviceOptions{Flex: true}
	info.switches = []SwitchInfo{{EntityId: 7, NvLinks: []dcgm.NvLinkStatus{{Index: 2}}}}
	info.cpus = []CPUInfo{{EntityId: 3, Cores: []uint{0, 1}}}

	data, err := json.Marshal(&info)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"gpu_count":2`)

	encoded := info.ToBase64()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	assert.JSONEq(t, string(data), string(decoded))

	var roundTrip Info
	require.NoError(t, json.Unmarshal(data, &roundTrip))
	assert.Equal(t, info.GPUCount(), roundTrip.GPUCount())
	assert.Equal(t, info.GPUs(), roundTrip.GPUs())
	assert.Equal(t, info.Switches(), roundTrip.Switches())
	assert.Equal(t, info.CPUs(), roundTrip.CPUs())
	assert.Equal(t, info.GOpts(), roundTrip.GOpts())
	assert.Equal(t, info.InfoType(), roundTrip.InfoType())
}

func TestToBase64Provider_NonInfoProvider(t *testing.T) {
	got := ToBase64Provider(nonInfoProvider{})
	decoded, err := base64.StdEncoding.DecodeString(got)

	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(decoded))
}

func TestInfoUnmarshalJSONRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{name: "malformed json", data: `{`, wantErr: "unexpected end of JSON input"},
		{name: "gpu count mismatch", data: `{"gpu_count":2,"gpus":[{}]}`, wantErr: "data inconsistency"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var info Info
			err := json.Unmarshal([]byte(tt.data), &info)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestInitializeUsesMinimalGPUInfoFallback verifies GPU identity can be
// populated from DCGM field values when GetDeviceInfo is unavailable.
func TestInitializeUsesMinimalGPUInfoFallback(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
	mockDCGMProvider.EXPECT().GetDeviceInfo(uint(0)).Return(
		dcgm.Device{},
		&dcgm.Error{Code: dcgm.DCGM_ST_FUNCTION_NOT_FOUND},
	)
	mockDCGMProvider.EXPECT().EntityGetLatestValues(
		dcgm.FE_GPU,
		uint(0),
		[]dcgm.Short{dcgm.DCGM_FI_DEV_NAME, dcgm.DCGM_FI_DEV_UUID, dcgm.DCGM_FI_DEV_PCI_BUSID},
	).Return([]dcgm.FieldValue_v1{
		dcgmStringFieldValue(dcgm.DCGM_FI_DEV_NAME, "NVIDIA GB200"),
		dcgmStringFieldValue(dcgm.DCGM_FI_DEV_UUID, "GPU-remote"),
		dcgmStringFieldValue(dcgm.DCGM_FI_DEV_PCI_BUSID, "00000008:01:00.0"),
	}, nil)
	mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
	mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)

	info, err := Initialize(appconfig.DeviceOptions{Flex: true}, appconfig.DeviceOptions{}, appconfig.DeviceOptions{}, false, dcgm.FE_GPU)

	require.NoError(t, err)
	require.Equal(t, uint(1), info.GPUCount())
	gpu := info.GPU(0).DeviceInfo
	assert.Equal(t, uint(0), gpu.GPU)
	assert.Equal(t, "Yes", gpu.DCGMSupported)
	assert.Equal(t, "GPU-remote", gpu.UUID)
	assert.Equal(t, "NVIDIA GB200", gpu.Identifiers.Model)
	assert.Equal(t, "00000008:01:00.0", gpu.PCI.BusID)
}

// TestInitializeUsesSyntheticMinimalGPUInfoFallback verifies remote hostengine
// metrics still initialize when DCGM exposes GPU entity IDs but not identity fields.
func TestInitializeUsesSyntheticMinimalGPUInfoFallback(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
	mockDCGMProvider.EXPECT().GetDeviceInfo(uint(0)).Return(
		dcgm.Device{},
		&dcgm.Error{Code: dcgm.DCGM_ST_FUNCTION_NOT_FOUND},
	)
	mockDCGMProvider.EXPECT().EntityGetLatestValues(
		dcgm.FE_GPU,
		uint(0),
		[]dcgm.Short{dcgm.DCGM_FI_DEV_NAME, dcgm.DCGM_FI_DEV_UUID, dcgm.DCGM_FI_DEV_PCI_BUSID},
	).Return(nil, &dcgm.Error{Code: dcgm.DCGM_ST_FUNCTION_NOT_FOUND})
	mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
	mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)

	info, err := Initialize(appconfig.DeviceOptions{Flex: true}, appconfig.DeviceOptions{}, appconfig.DeviceOptions{}, false, dcgm.FE_GPU)

	require.NoError(t, err)
	require.Equal(t, uint(1), info.GPUCount())
	gpu := info.GPU(0).DeviceInfo
	assert.Equal(t, uint(0), gpu.GPU)
	assert.Equal(t, "Yes", gpu.DCGMSupported)
	assert.Equal(t, "GPU-0", gpu.UUID)
	assert.Empty(t, gpu.Identifiers.Model)
	assert.Empty(t, gpu.PCI.BusID)
}

func TestInitializeDoesNotFallbackForMinimalGPUInfoQueryErrors(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
	mockDCGMProvider.EXPECT().GetDeviceInfo(uint(0)).Return(
		dcgm.Device{},
		&dcgm.Error{Code: dcgm.DCGM_ST_FUNCTION_NOT_FOUND},
	)
	mockDCGMProvider.EXPECT().EntityGetLatestValues(
		dcgm.FE_GPU,
		uint(0),
		[]dcgm.Short{dcgm.DCGM_FI_DEV_NAME, dcgm.DCGM_FI_DEV_UUID, dcgm.DCGM_FI_DEV_PCI_BUSID},
	).Return(nil, fmt.Errorf("identity query failed"))

	_, err := Initialize(appconfig.DeviceOptions{Flex: true}, appconfig.DeviceOptions{}, appconfig.DeviceOptions{}, false, dcgm.FE_GPU)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity query failed")
}

// TestInitializeDoesNotFallbackForOtherGPUInfoErrors verifies unrelated
// GetDeviceInfo errors still fail initialization instead of being hidden.
func TestInitializeDoesNotFallbackForOtherGPUInfoErrors(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
	mockDCGMProvider.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{}, fmt.Errorf("device info failed"))

	_, err := Initialize(appconfig.DeviceOptions{Flex: true}, appconfig.DeviceOptions{}, appconfig.DeviceOptions{}, false, dcgm.FE_GPU)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "device info failed")
}

func TestInitialize(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	fakeDevices := SpoofGPUDevices()
	_, fakeGPUs, _, _ := SpoofMigHierarchy()

	tests := []struct {
		name           string
		gOpts          appconfig.DeviceOptions
		sOpts          appconfig.DeviceOptions
		cOpts          appconfig.DeviceOptions
		entityType     dcgm.Field_Entity_Group
		mockCalls      func()
		expectedOutput func() *Info
		assertions     func(*Info, *Info)
		wantErr        bool
	}{
		{
			name:       "Initialize GPUs",
			gOpts:      appconfig.DeviceOptions{Flex: true},
			entityType: dcgm.FE_GPU,
			mockCalls: func() {
				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 1,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil)
			},
			expectedOutput: func() *Info {
				return &Info{
					gpuCount: 0,
					gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
						{
							DeviceInfo: fakeDevices[0],
						},
					},
					switches: nil,
					cpus:     nil,
					gOpt:     appconfig.DeviceOptions{Flex: true},
					sOpt:     appconfig.DeviceOptions{},
					cOpt:     appconfig.DeviceOptions{},
					infoType: dcgm.FE_GPU,
				}
			},
			assertions: func(expected, actual *Info) {
				assert.Equal(t, expected.gpus[0].DeviceInfo, actual.gpus[0].DeviceInfo,
					"GPU device info mismatch")

				assert.Equal(t, expected.gpus[0].MigEnabled, actual.gpus[0].MigEnabled,
					"MIG info mismatch")

				assert.Equal(t, len(expected.gpus[0].GPUInstances), len(actual.gpus[0].GPUInstances),
					"GPU Instances length mismatch")

				assert.Equal(t, expected.gOpt, actual.gOpt, "GPU options mismatch")

				assert.Equal(t, expected.infoType, actual.infoType, "GPU info type mismatch")
			},
			wantErr: false,
		},
		{
			name:       "Initialize GPUs error",
			gOpts:      appconfig.DeviceOptions{Flex: true},
			entityType: dcgm.FE_GPU,
			mockCalls: func() {
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(0), fmt.Errorf("some error"))
			},
			wantErr: true,
		},
		{
			name:       "Initialize Switches",
			sOpts:      appconfig.DeviceOptions{Flex: true},
			entityType: dcgm.FE_SWITCH,
			mockCalls: func() {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return([]uint{1}, nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{
					{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				}, nil)
			},
			expectedOutput: func() *Info {
				return &Info{
					gpuCount: 0,
					gpus:     [dcgm.MAX_NUM_DEVICES]GPUInfo{},
					switches: []SwitchInfo{
						{
							EntityId: uint(1),
							NvLinks: []dcgm.NvLinkStatus{
								{
									ParentId:   uint(1),
									ParentType: dcgm.FE_SWITCH,
									Index:      uint(1),
								},
							},
						},
					},
					cpus:     nil,
					gOpt:     appconfig.DeviceOptions{},
					sOpt:     appconfig.DeviceOptions{Flex: true},
					cOpt:     appconfig.DeviceOptions{},
					infoType: dcgm.FE_SWITCH,
				}
			},
			assertions: func(expected, actual *Info) {
				assert.Equal(t, len(expected.switches), len(actual.switches),
					"Switches length mismatch")

				assert.Equal(t, expected.switches[0].EntityId, actual.switches[0].EntityId,
					"Switch Entity ID mismatch")

				assert.Equal(t, len(expected.switches[0].NvLinks), len(actual.switches[0].NvLinks),
					"Switches NV link length mismatch")

				assert.Equal(t, expected.switches[0].NvLinks[0].Index, actual.switches[0].NvLinks[0].Index,
					"Switches NV link Index mismatch")

				assert.Equal(t, expected.sOpt, actual.sOpt, "Switch options mismatch")

				assert.Equal(t, expected.infoType, actual.infoType, "Switch info type mismatch")
			},
			wantErr: false,
		},
		{
			name:       "Initialize Switches error",
			sOpts:      appconfig.DeviceOptions{Flex: true},
			entityType: dcgm.FE_SWITCH,
			mockCalls: func() {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(dcgm.FE_SWITCH).Return([]uint{uint(0)},
					fmt.Errorf("some error"))
			},
			wantErr: true,
		},
		{
			name:       "Initialize NV Links",
			sOpts:      appconfig.DeviceOptions{Flex: true},
			entityType: dcgm.FE_LINK,
			mockCalls: func() {
				// Mock calls for initializeNvSwitchInfo
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return([]uint{1}, nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{
					{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				}, nil)
				// Mock calls for initializeGPUInfo (called for FE_LINK)
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(0), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{Count: 0}, nil)
			},
			expectedOutput: func() *Info {
				return &Info{
					gpuCount: 0,
					gpus:     [dcgm.MAX_NUM_DEVICES]GPUInfo{},
					switches: []SwitchInfo{
						{
							EntityId: uint(1),
							NvLinks: []dcgm.NvLinkStatus{
								{
									ParentId:   uint(1),
									ParentType: dcgm.FE_SWITCH,
									Index:      uint(1),
								},
							},
						},
					},
					cpus:     nil,
					gOpt:     appconfig.DeviceOptions{},
					sOpt:     appconfig.DeviceOptions{Flex: true},
					cOpt:     appconfig.DeviceOptions{},
					infoType: dcgm.FE_LINK,
				}
			},
			assertions: func(expected, actual *Info) {
				assert.Equal(t, len(expected.switches), len(actual.switches),
					"Switches length mismatch")

				assert.Equal(t, expected.switches[0].EntityId, actual.switches[0].EntityId,
					"Switch Entity ID mismatch")

				assert.Equal(t, len(expected.switches[0].NvLinks), len(actual.switches[0].NvLinks),
					"Switches NV link length mismatch")

				assert.Equal(t, expected.switches[0].NvLinks[0].Index, actual.switches[0].NvLinks[0].Index,
					"Switches NV link Index mismatch")

				assert.Equal(t, expected.sOpt, actual.sOpt, "NV Link options mismatch")

				assert.Equal(t, expected.infoType, actual.infoType, "NV Link info type mismatch")
			},
			wantErr: false,
		},
		{
			name:       "Initialize NV Link error",
			sOpts:      appconfig.DeviceOptions{Flex: true},
			entityType: dcgm.FE_LINK,
			mockCalls: func() {
				// Mock calls for initializeNvSwitchInfo (will return error but is ignored for FE_LINK)
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(dcgm.FE_SWITCH).Return([]uint{uint(0)},
					fmt.Errorf("some error"))
				// Mock calls for initializeGPUInfo (called for FE_LINK even if initializeNvSwitchInfo fails)
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(0), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{Count: 0}, nil)
			},
			expectedOutput: func() *Info {
				return &Info{
					gpuCount: 0,
					gpus:     [dcgm.MAX_NUM_DEVICES]GPUInfo{},
					switches: []SwitchInfo{},
					cpus:     nil,
					gOpt:     appconfig.DeviceOptions{},
					sOpt:     appconfig.DeviceOptions{Flex: true},
					cOpt:     appconfig.DeviceOptions{},
					infoType: dcgm.FE_LINK,
				}
			},
			assertions: func(expected, actual *Info) {
				assert.Equal(t, expected.gpuCount, actual.gpuCount, "GPU count mismatch")
				assert.Equal(t, len(expected.switches), len(actual.switches), "Switches length mismatch")
				assert.Equal(t, expected.infoType, actual.infoType, "Info type mismatch")
			},
			wantErr: false,
		},
		{
			name:       "initialize CPUs",
			cOpts:      appconfig.DeviceOptions{Flex: true},
			entityType: dcgm.FE_CPU,
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 1,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{1, 2, 8},
							Serial:     "CPU-SERIAL-123",
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			expectedOutput: func() *Info {
				return &Info{
					gpuCount: 0,
					gpus:     [dcgm.MAX_NUM_DEVICES]GPUInfo{},
					switches: nil,
					cpus: []CPUInfo{
						{
							EntityId: uint(0),
							Cores:    []uint{0, 65, 131},
							Serial:   "CPU-SERIAL-123",
						},
					},
					gOpt:     appconfig.DeviceOptions{},
					sOpt:     appconfig.DeviceOptions{},
					cOpt:     appconfig.DeviceOptions{Flex: true},
					infoType: dcgm.FE_CPU,
				}
			},
			assertions: func(expected, actual *Info) {
				assert.Equal(t, len(expected.cpus), len(actual.cpus),
					"CPU length mismatch")

				assert.Equal(t, expected.cpus[0].EntityId, actual.cpus[0].EntityId,
					"CPU Entity ID mismatch")

				assert.Equal(t, expected.cpus[0].Serial, actual.cpus[0].Serial,
					"CPU serial mismatch")

				assert.Equal(t, len(expected.cpus[0].Cores), len(actual.cpus[0].Cores),
					"CPU Core length mismatch")

				assert.True(t, slices.Equal(expected.cpus[0].Cores, actual.cpus[0].Cores),
					"CPU Cores mismatch")

				assert.Equal(t, expected.cOpt, actual.cOpt, "CPU options mismatch")

				assert.Equal(t, expected.infoType, actual.infoType, "CPU info type mismatch")
			},
			wantErr: false,
		},
		{
			name:       "Initialize CPU Cores error",
			cOpts:      appconfig.DeviceOptions{Flex: true},
			entityType: dcgm.FE_CPU_CORE,
			mockCalls: func() {
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(dcgm.CPUHierarchy_v2{}, fmt.Errorf("some error"))
			},
			wantErr: true,
		},
		{
			name:       "Initialize Invalid type error",
			cOpts:      appconfig.DeviceOptions{Flex: true},
			entityType: dcgm.FE_VGPU,
			mockCalls:  func() {},
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mockCalls()

			if !tt.wantErr {
				deviceInfo, err := Initialize(tt.gOpts, tt.sOpts, tt.cOpts, false, tt.entityType)
				assert.NoError(t, err, "Error not expected")
				assert.NotNil(t, deviceInfo, "Expected output to be not nil")

				expectedDeviceInfo := tt.expectedOutput()
				tt.assertions(expectedDeviceInfo, deviceInfo)
			} else {
				_, err := Initialize(tt.gOpts, tt.sOpts, tt.cOpts, false, tt.entityType)
				assert.Error(t, err, "Error expected")
			}
		})
	}
}

func TestInitializeGPUInfo(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	fakeDevices := SpoofGPUDevices()
	fakeMigHierarchy, fakeGPUs, fakeGPUInstances, fakeGPUComputeInstances := SpoofMigHierarchy()

	tests := []struct {
		name           string
		gOpts          appconfig.DeviceOptions
		mockCalls      func()
		expectedOutput map[uint]GPUInfo
		wantErr        bool
	}{
		{
			name: "GPU with 0 Device Count",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(0), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{
					Count: 0,
				}, nil)
			},
			expectedOutput: map[uint]GPUInfo{},
			wantErr:        false,
		},
		{
			name: "GPU with 0 Device Count with GetAllDeviceCount error",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(0), fmt.Errorf("some error"))
			},
			expectedOutput: map[uint]GPUInfo{},
			wantErr:        true,
		},
		{
			name: "GPU Count 1 with No Hierarchy",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 1,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil)
			},
			expectedOutput: map[uint]GPUInfo{
				0: {
					DeviceInfo: fakeDevices[0],
				},
			},
			wantErr: false,
		},
		{
			name: "GPU count 2 GPU with No Hierarchy",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 2,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]
				mockHierarchy.EntityList[0] = fakeGPUs[1]

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(len(fakeDevices)), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil)

				for i := 0; i < len(fakeDevices); i++ {
					mockDCGMProvider.EXPECT().GetDeviceInfo(uint(i)).Return(fakeDevices[i], nil)
				}
			},
			expectedOutput: map[uint]GPUInfo{
				0: {DeviceInfo: fakeDevices[0]},
				1: {DeviceInfo: fakeDevices[1]},
			},
			wantErr: false,
		},
		{
			name: "GPU Count 1 with No Hierarchy but GetDeviceInfo error",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], fmt.Errorf("some error"))
			},
			expectedOutput: map[uint]GPUInfo{},
			wantErr:        true,
		},
		{
			name: "GPU Count 1 with No Hierarchy but GetGpuInstanceHierarchy error",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{},
					fmt.Errorf("some error"))
			},
			expectedOutput: map[uint]GPUInfo{},
			wantErr:        true,
		},
		{
			name: "GPU Count 1 with MIG hierarchy unavailable because NVML is not loaded",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{},
					fmt.Errorf("error retrieving DCGM MIG hierarchy: Cannot perform the requested operation because NVML doesn't exist on this system."))
			},
			expectedOutput: map[uint]GPUInfo{
				0: {
					DeviceInfo: fakeDevices[0],
				},
			},
			wantErr: false,
		},
		{
			name: "GPU Count 1 with Hierarchy",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 6,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]
				mockHierarchy.EntityList[1] = fakeGPUInstances[0]
				mockHierarchy.EntityList[2] = fakeGPUComputeInstances[0]
				mockHierarchy.EntityList[3] = fakeGPUComputeInstances[1]
				mockHierarchy.EntityList[4] = fakeGPUInstances[1]
				mockHierarchy.EntityList[5] = fakeGPUComputeInstances[2]

				mockEntitiesInput := []dcgm.GroupEntityPair{
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
				}

				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: mockEntitiesInput[0].EntityId},
					{EntityID: mockEntitiesInput[1].EntityId},
				}

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil)
				mockDCGMProvider.EXPECT().EntitiesGetLatestValues(mockEntitiesInput, gomock.Any(),
					gomock.Any()).Return(mockEntitiesResult, nil)
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[0]).Return("instance_profile_0")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[1]).Return("instance_profile_1")
			},
			expectedOutput: map[uint]GPUInfo{
				0: {
					DeviceInfo: fakeDevices[0],
					GPUInstances: []GPUInstanceInfo{
						{
							EntityId: fakeGPUInstances[0].Entity.EntityId,
							Info:     fakeGPUInstances[0].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[0].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[0].Info,
								},
								{
									EntityId:     fakeGPUComputeInstances[1].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[1].Info,
								},
							},
							ProfileName: "instance_profile_0",
						},
						{
							EntityId: fakeGPUInstances[1].Entity.EntityId,
							Info:     fakeGPUInstances[1].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[2].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[2].Info,
								},
							},
							ProfileName: "instance_profile_1",
						},
					},
					MigEnabled: true,
				},
			},
			wantErr: false,
		},
		{
			name: "GPU Count 2 with Hierarchy",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockEntitiesInput := []dcgm.GroupEntityPair{
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[2].Entity.EntityId},
				}

				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: mockEntitiesInput[0].EntityId},
					{EntityID: mockEntitiesInput[1].EntityId},
					{EntityID: mockEntitiesInput[2].EntityId},
				}

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(len(fakeDevices)), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(fakeMigHierarchy, nil)
				mockDCGMProvider.EXPECT().EntitiesGetLatestValues(mockEntitiesInput, gomock.Any(),
					gomock.Any()).Return(mockEntitiesResult, nil)
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[0]).Return("instance_profile_0")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[1]).Return("instance_profile_1")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[2]).Return("instance_profile_2")

				for i := 0; i < len(fakeDevices); i++ {
					mockDCGMProvider.EXPECT().GetDeviceInfo(uint(i)).Return(fakeDevices[i], nil)
				}
			},
			expectedOutput: map[uint]GPUInfo{
				0: {
					DeviceInfo: fakeDevices[0],
					GPUInstances: []GPUInstanceInfo{
						{
							EntityId: fakeGPUInstances[0].Entity.EntityId,
							Info:     fakeGPUInstances[0].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[0].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[0].Info,
								},
								{
									EntityId:     fakeGPUComputeInstances[1].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[1].Info,
								},
							},
							ProfileName: "instance_profile_0",
						},
						{
							EntityId: fakeGPUInstances[1].Entity.EntityId,
							Info:     fakeGPUInstances[1].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[2].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[2].Info,
								},
							},
							ProfileName: "instance_profile_1",
						},
					},
					MigEnabled: true,
				},
				1: {
					DeviceInfo: fakeDevices[1],
					GPUInstances: []GPUInstanceInfo{
						{
							EntityId: fakeGPUInstances[2].Entity.EntityId,
							Info:     fakeGPUInstances[2].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[3].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[3].Info,
								},
							},
							ProfileName: "instance_profile_2",
						},
					},
					MigEnabled: true,
				},
			},
			wantErr: false,
		},
		{
			name: "GPU Count 1 with MIG compute instance before parent GPU instance",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 5,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]
				mockHierarchy.EntityList[1] = fakeGPUInstances[0]
				mockHierarchy.EntityList[2] = fakeGPUComputeInstances[2]
				mockHierarchy.EntityList[3] = fakeGPUInstances[1]
				mockHierarchy.EntityList[4] = fakeGPUComputeInstances[0]

				mockEntitiesInput := []dcgm.GroupEntityPair{
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
				}

				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: mockEntitiesInput[0].EntityId},
					{EntityID: mockEntitiesInput[1].EntityId},
				}

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				mockDCGMProvider.EXPECT().GetDeviceInfo(uint(0)).Return(fakeDevices[0], nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil)
				mockDCGMProvider.EXPECT().EntitiesGetLatestValues(mockEntitiesInput, gomock.Any(),
					gomock.Any()).Return(mockEntitiesResult, nil)
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[0]).Return("instance_profile_0")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[1]).Return("instance_profile_1")
			},
			expectedOutput: map[uint]GPUInfo{
				0: {
					DeviceInfo: fakeDevices[0],
					GPUInstances: []GPUInstanceInfo{
						{
							EntityId: fakeGPUInstances[0].Entity.EntityId,
							Info:     fakeGPUInstances[0].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[0].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[0].Info,
								},
							},
							ProfileName: "instance_profile_0",
						},
						{
							EntityId: fakeGPUInstances[1].Entity.EntityId,
							Info:     fakeGPUInstances[1].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[2].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[2].Info,
								},
							},
							ProfileName: "instance_profile_1",
						},
					},
					MigEnabled: true,
				},
			},
			wantErr: false,
		},
		{
			name: "GPU Count 2 with Hierarchy but EntitiesGetLatestValues error",
			gOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(len(fakeDevices)), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(fakeMigHierarchy, nil)
				mockDCGMProvider.EXPECT().EntitiesGetLatestValues(gomock.Any(), gomock.Any(),
					gomock.Any()).Return([]dcgm.FieldValue_v2{}, fmt.Errorf("some error"))

				for i := 0; i < len(fakeDevices); i++ {
					mockDCGMProvider.EXPECT().GetDeviceInfo(uint(i)).Return(fakeDevices[i], nil)
				}
			},
			wantErr: true,
		},
		{
			name: "GPU Count 2 with Hierarchy and device options",
			gOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{0, 1},
				MinorRange: []int{1, 2, 3},
			},
			mockCalls: func() {
				mockEntitiesInput := []dcgm.GroupEntityPair{
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[2].Entity.EntityId},
				}

				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: mockEntitiesInput[0].EntityId},
					{EntityID: mockEntitiesInput[1].EntityId},
					{EntityID: mockEntitiesInput[2].EntityId},
				}

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(len(fakeDevices)), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(fakeMigHierarchy, nil)
				mockDCGMProvider.EXPECT().EntitiesGetLatestValues(mockEntitiesInput, gomock.Any(),
					gomock.Any()).Return(mockEntitiesResult, nil)
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[0]).Return("instance_profile_0")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[1]).Return("instance_profile_1")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[2]).Return("instance_profile_2")

				for i := 0; i < len(fakeDevices); i++ {
					mockDCGMProvider.EXPECT().GetDeviceInfo(uint(i)).Return(fakeDevices[i], nil)
				}
			},
			expectedOutput: map[uint]GPUInfo{
				0: {
					DeviceInfo: fakeDevices[0],
					GPUInstances: []GPUInstanceInfo{
						{
							EntityId: fakeGPUInstances[0].Entity.EntityId,
							Info:     fakeGPUInstances[0].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[0].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[0].Info,
								},
								{
									EntityId:     fakeGPUComputeInstances[1].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[1].Info,
								},
							},
							ProfileName: "instance_profile_0",
						},
						{
							EntityId: fakeGPUInstances[1].Entity.EntityId,
							Info:     fakeGPUInstances[1].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[2].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[2].Info,
								},
							},
							ProfileName: "instance_profile_1",
						},
					},
					MigEnabled: true,
				},
				1: {
					DeviceInfo: fakeDevices[1],
					GPUInstances: []GPUInstanceInfo{
						{
							EntityId: fakeGPUInstances[2].Entity.EntityId,
							Info:     fakeGPUInstances[2].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[3].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[3].Info,
								},
							},
							ProfileName: "instance_profile_2",
						},
					},
					MigEnabled: true,
				},
			},
			wantErr: false,
		},
		/*
			// TODO (roarora): Today, Specifying Major range does not remove extra GPUs
			{
				name: "GPU Count 2 with Hierarchy and device options with extra GPU discovery",
				gOpts: appconfig.DeviceOptions{
					Flex:       false,
					MajorRange: []int{0},
					MinorRange: []int{1, 2},
				},
				mockCalls: func() {
					mockEntitiesInput := []dcgm.GroupEntityPair{
						{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
						{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
						{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[2].Entity.EntityId},
					}

					mockEntitiesResult := []dcgm.FieldValue_v2{
						{EntityId: mockEntitiesInput[0].EntityId},
						{EntityId: mockEntitiesInput[1].EntityId},
						{EntityId: mockEntitiesInput[2].EntityId},
					}

					mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(len(fakeDevices)), nil)
					mockDCGMProvider.EXPECT().GetGpuInstanceHierarchy().Return(fakeMigHierarchy, nil)
					mockDCGMProvider.EXPECT().EntitiesGetLatestValues(mockEntitiesInput, gomock.Any(),
						gomock.Any()).Return(mockEntitiesResult, nil)
					mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[0]).Return("instance_profile_0")
					mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[1]).Return("instance_profile_1")
					mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[2]).Return("instance_profile_2")

					for i := 0; i < len(fakeDevices); i++ {
						mockDCGMProvider.EXPECT().GetDeviceInfo(uint(i)).Return(fakeDevices[i], nil)
					}
				},
				expectedOutput: map[uint]GPUInfo{
					0: {
						DeviceInfo: fakeDevices[0],
						GPUInstances: []GPUInstanceInfo{
							{
								EntityId: fakeGPUInstances[0].Entity.EntityId,
								Info:     fakeGPUInstances[0].Info,
								ComputeInstances: []ComputeInstanceInfo{
									{
										EntityId:     fakeGPUComputeInstances[0].Entity.EntityId,
										InstanceInfo: fakeGPUComputeInstances[0].Info,
									},
									{
										EntityId:     fakeGPUComputeInstances[1].Entity.EntityId,
										InstanceInfo: fakeGPUComputeInstances[1].Info,
									},
								},
								ProfileName: "instance_profile_0",
							},
							{
								EntityId: fakeGPUInstances[1].Entity.EntityId,
								Info:     fakeGPUInstances[1].Info,
								ComputeInstances: []ComputeInstanceInfo{
									{
										EntityId:     fakeGPUComputeInstances[2].Entity.EntityId,
										InstanceInfo: fakeGPUComputeInstances[2].Info,
									},
								},
								ProfileName: "instance_profile_1",
							},
						},
						MigEnabled: true,
					},
				},
				wantErr: false,
			},
			// TODO (roarora): Today, Specifying Minor range does not remove extra GPU Instances
			{
				name: "GPU Count 2 with Hierarchy and device options with extra GPU Instance discovery",
				gOpts: appconfig.DeviceOptions{
					Flex:       false,
					MajorRange: []int{0, 1},
					MinorRange: []int{1, 3},
				},
				mockCalls: func() {
					mockEntitiesInput := []dcgm.GroupEntityPair{
						{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
						{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
						{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[2].Entity.EntityId},
					}

					mockEntitiesResult := []dcgm.FieldValue_v2{
						{EntityId: mockEntitiesInput[0].EntityId},
						{EntityId: mockEntitiesInput[1].EntityId},
						{EntityId: mockEntitiesInput[2].EntityId},
					}

					mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(len(fakeDevices)), nil)
					mockDCGMProvider.EXPECT().GetGpuInstanceHierarchy().Return(fakeMigHierarchy, nil)
					mockDCGMProvider.EXPECT().EntitiesGetLatestValues(mockEntitiesInput, gomock.Any(),
						gomock.Any()).Return(mockEntitiesResult, nil)
					mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[0]).Return("instance_profile_0")
					mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[1]).Return("instance_profile_1")
					mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[2]).Return("instance_profile_2")

					for i := 0; i < len(fakeDevices); i++ {
						mockDCGMProvider.EXPECT().GetDeviceInfo(uint(i)).Return(fakeDevices[i], nil)
					}
				},
				expectedOutput: map[uint]GPUInfo{
					0: {
						DeviceInfo: fakeDevices[0],
						GPUInstances: []GPUInstanceInfo{
							{
								EntityId: fakeGPUInstances[0].Entity.EntityId,
								Info:     fakeGPUInstances[0].Info,
								ComputeInstances: []ComputeInstanceInfo{
									{
										EntityId:     fakeGPUComputeInstances[0].Entity.EntityId,
										InstanceInfo: fakeGPUComputeInstances[0].Info,
									},
									{
										EntityId:     fakeGPUComputeInstances[1].Entity.EntityId,
										InstanceInfo: fakeGPUComputeInstances[1].Info,
									},
								},
								ProfileName: "instance_profile_0",
							},
						},
						MigEnabled: true,
					},
					1: {
						DeviceInfo: fakeDevices[1],
						GPUInstances: []GPUInstanceInfo{
							{
								EntityId: fakeGPUInstances[2].Entity.EntityId,
								Info:     fakeGPUInstances[2].Info,
								ComputeInstances: []ComputeInstanceInfo{
									{
										EntityId:     fakeGPUComputeInstances[3].Entity.EntityId,
										InstanceInfo: fakeGPUComputeInstances[3].Info,
									},
								},
								ProfileName: "instance_profile_2",
							},
						},
						MigEnabled: true,
					},
				},
				wantErr: false,
			},
		*/
		{
			name: "GPU Count 2 with Hierarchy and device options Major -1",
			gOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{-1},
				MinorRange: []int{1, 2, 3},
			},
			mockCalls: func() {
				mockEntitiesInput := []dcgm.GroupEntityPair{
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[2].Entity.EntityId},
				}

				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: mockEntitiesInput[0].EntityId},
					{EntityID: mockEntitiesInput[1].EntityId},
					{EntityID: mockEntitiesInput[2].EntityId},
				}

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(len(fakeDevices)), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(fakeMigHierarchy, nil)
				mockDCGMProvider.EXPECT().EntitiesGetLatestValues(mockEntitiesInput, gomock.Any(),
					gomock.Any()).Return(mockEntitiesResult, nil)
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[0]).Return("instance_profile_0")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[1]).Return("instance_profile_1")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[2]).Return("instance_profile_2")

				for i := 0; i < len(fakeDevices); i++ {
					mockDCGMProvider.EXPECT().GetDeviceInfo(uint(i)).Return(fakeDevices[i], nil)
				}
			},
			expectedOutput: map[uint]GPUInfo{
				0: {
					DeviceInfo: fakeDevices[0],
					GPUInstances: []GPUInstanceInfo{
						{
							EntityId: fakeGPUInstances[0].Entity.EntityId,
							Info:     fakeGPUInstances[0].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[0].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[0].Info,
								},
								{
									EntityId:     fakeGPUComputeInstances[1].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[1].Info,
								},
							},
							ProfileName: "instance_profile_0",
						},
						{
							EntityId: fakeGPUInstances[1].Entity.EntityId,
							Info:     fakeGPUInstances[1].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[2].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[2].Info,
								},
							},
							ProfileName: "instance_profile_1",
						},
					},
					MigEnabled: true,
				},
				1: {
					DeviceInfo: fakeDevices[1],
					GPUInstances: []GPUInstanceInfo{
						{
							EntityId: fakeGPUInstances[2].Entity.EntityId,
							Info:     fakeGPUInstances[2].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[3].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[3].Info,
								},
							},
							ProfileName: "instance_profile_2",
						},
					},
					MigEnabled: true,
				},
			},
			wantErr: false,
		},
		{
			name: "GPU Count 2 with Hierarchy and device options Major -1 and Minor -1",
			gOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{-1},
				MinorRange: []int{-1},
			},
			mockCalls: func() {
				mockEntitiesInput := []dcgm.GroupEntityPair{
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[2].Entity.EntityId},
				}

				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: mockEntitiesInput[0].EntityId},
					{EntityID: mockEntitiesInput[1].EntityId},
					{EntityID: mockEntitiesInput[2].EntityId},
				}

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(len(fakeDevices)), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(fakeMigHierarchy, nil)
				mockDCGMProvider.EXPECT().EntitiesGetLatestValues(mockEntitiesInput, gomock.Any(),
					gomock.Any()).Return(mockEntitiesResult, nil)
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[0]).Return("instance_profile_0")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[1]).Return("instance_profile_1")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[2]).Return("instance_profile_2")

				for i := 0; i < len(fakeDevices); i++ {
					mockDCGMProvider.EXPECT().GetDeviceInfo(uint(i)).Return(fakeDevices[i], nil)
				}
			},
			expectedOutput: map[uint]GPUInfo{
				0: {
					DeviceInfo: fakeDevices[0],
					GPUInstances: []GPUInstanceInfo{
						{
							EntityId: fakeGPUInstances[0].Entity.EntityId,
							Info:     fakeGPUInstances[0].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[0].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[0].Info,
								},
								{
									EntityId:     fakeGPUComputeInstances[1].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[1].Info,
								},
							},
							ProfileName: "instance_profile_0",
						},
						{
							EntityId: fakeGPUInstances[1].Entity.EntityId,
							Info:     fakeGPUInstances[1].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[2].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[2].Info,
								},
							},
							ProfileName: "instance_profile_1",
						},
					},
					MigEnabled: true,
				},
				1: {
					DeviceInfo: fakeDevices[1],
					GPUInstances: []GPUInstanceInfo{
						{
							EntityId: fakeGPUInstances[2].Entity.EntityId,
							Info:     fakeGPUInstances[2].Info,
							ComputeInstances: []ComputeInstanceInfo{
								{
									EntityId:     fakeGPUComputeInstances[3].Entity.EntityId,
									InstanceInfo: fakeGPUComputeInstances[3].Info,
								},
							},
							ProfileName: "instance_profile_2",
						},
					},
					MigEnabled: true,
				},
			},
			wantErr: false,
		},
		{
			name: "GPU Count 2 with Hierarchy and missing GPU",
			gOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{0, 1, 2},
				MinorRange: []int{1, 2, 3},
			},
			mockCalls: func() {
				mockEntitiesInput := []dcgm.GroupEntityPair{
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[2].Entity.EntityId},
				}

				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: mockEntitiesInput[0].EntityId},
					{EntityID: mockEntitiesInput[1].EntityId},
					{EntityID: mockEntitiesInput[2].EntityId},
				}

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(len(fakeDevices)), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(fakeMigHierarchy, nil)
				mockDCGMProvider.EXPECT().EntitiesGetLatestValues(mockEntitiesInput, gomock.Any(),
					gomock.Any()).Return(mockEntitiesResult, nil)
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[0]).Return("instance_profile_0")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[1]).Return("instance_profile_1")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[2]).Return("instance_profile_2")

				for i := 0; i < len(fakeDevices); i++ {
					mockDCGMProvider.EXPECT().GetDeviceInfo(uint(i)).Return(fakeDevices[i], nil)
				}
			},
			wantErr: true,
		},
		{
			name: "GPU Count 2 with Hierarchy and missing GPU Instances",
			gOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{0, 1},
				MinorRange: []int{1, 2, 3, 4},
			},
			mockCalls: func() {
				mockEntitiesInput := []dcgm.GroupEntityPair{
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
					{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[2].Entity.EntityId},
				}

				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: mockEntitiesInput[0].EntityId},
					{EntityID: mockEntitiesInput[1].EntityId},
					{EntityID: mockEntitiesInput[2].EntityId},
				}

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(len(fakeDevices)), nil)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(fakeMigHierarchy, nil)
				mockDCGMProvider.EXPECT().EntitiesGetLatestValues(mockEntitiesInput, gomock.Any(),
					gomock.Any()).Return(mockEntitiesResult, nil)
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[0]).Return("instance_profile_0")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[1]).Return("instance_profile_1")
				mockDCGMProvider.EXPECT().Fv2_String(mockEntitiesResult[2]).Return("instance_profile_2")

				for i := 0; i < len(fakeDevices); i++ {
					mockDCGMProvider.EXPECT().GetDeviceInfo(uint(i)).Return(fakeDevices[i], nil)
				}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mockCalls()

			if !tt.wantErr {
				deviceInfo := Info{}
				err := deviceInfo.initializeGPUInfo(tt.gOpts, false)
				assert.NoError(t, err, "Error not expected")
				assert.Equal(t, len(tt.expectedOutput), int(deviceInfo.gpuCount), "GPU length mismatch")

				for i := 0; i < int(deviceInfo.gpuCount); i++ {
					actualGPU := deviceInfo.gpus[i]
					expectedGPU := tt.expectedOutput[actualGPU.DeviceInfo.GPU]

					assert.Equal(t, expectedGPU.DeviceInfo, actualGPU.DeviceInfo,
						"GPU device info mismatch")

					assert.Equal(t, expectedGPU.MigEnabled, actualGPU.MigEnabled,
						"MIG info mismatch")

					assert.Equal(t, len(expectedGPU.GPUInstances), len(actualGPU.GPUInstances),
						"GPU Instances length mismatch")

					// Ensure each GPU Instance and Computer matches
					for _, expectedInstance := range expectedGPU.GPUInstances {
						instanceExist := slices.ContainsFunc(actualGPU.GPUInstances,
							func(actualInstance GPUInstanceInfo) bool {
								return expectedInstance.Info == actualInstance.Info &&
									expectedInstance.EntityId == actualInstance.EntityId &&
									slices.Equal(expectedInstance.ComputeInstances, actualInstance.ComputeInstances)
							})

						assert.True(t, instanceExist, "Expected instance %+v not found", expectedInstance)
					}
				}
			} else {
				deviceInfo := Info{}
				err := deviceInfo.initializeGPUInfo(tt.gOpts, false)
				assert.Error(t, err, "Error expected")
			}
		})
	}
}

func TestInitializeCPUInfo(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	tests := []struct {
		name                  string
		cOpts                 appconfig.DeviceOptions
		mockCalls             func()
		expectedCPUCoreOutput map[uint][]int
		expectedCPUSerials    map[uint]string
		wantErr               bool
	}{
		{
			name: "CPU Hierarchy with 0 CPUs",
			cOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 0,
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			wantErr: true,
		},
		{
			name: "CPU Hierarchy with 1 CPU",
			cOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 1,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{1, 2, 8},
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			expectedCPUCoreOutput: map[uint][]int{0: {0, 65, 131}},
			wantErr:               false,
		},
		{
			name: "CPU Hierarchy with 1 CPUs but GetCpuHierarchy error",
			cOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 1,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{1, 2, 8},
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, fmt.Errorf("some error"))
			},
			wantErr: true,
		},
		{
			name: "CPU Hierarchy with 2 CPUs",
			cOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 2,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{1, 2, 8},
							Serial:     "CPU-SERIAL-000",
						},
						{
							CPUID:      1,
							OwnedCores: []uint64{8, 16, 32},
							Serial:     "CPU-SERIAL-001",
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			expectedCPUCoreOutput: map[uint][]int{0: {0, 65, 131}, 1: {3, 68, 133}},
			expectedCPUSerials:    map[uint]string{0: "CPU-SERIAL-000", 1: "CPU-SERIAL-001"},
			wantErr:               false,
		},
		{
			name: "CPU Hierarchy with multiple CPUs and device options",
			cOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{0, 1, 2, 3, 4},
				MinorRange: []int{1, 2, 4, 8, 16, 32, 64, 128, 256},
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 5,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{0b10110},
						},
						{
							CPUID:      1,
							OwnedCores: []uint64{0x100010100},
						},
						{
							CPUID:      2,
							OwnedCores: []uint64{0x0, 0x1, 0x1, 0x0},
						},
						{
							CPUID:      3,
							OwnedCores: []uint64{0x0, 0x0, 0x0, 0x0, 0x1},
						},
						{
							CPUID: 4,
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			expectedCPUCoreOutput: map[uint][]int{0: {1, 2, 4}, 1: {8, 16, 32}, 2: {64, 128}, 3: {256}, 4: {}},
			wantErr:               false,
		},
		{
			name: "CPU Hierarchy with multiple CPUs and device options with extra CPU discovery",
			cOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{0, 1, 2},
				MinorRange: []int{1, 2, 4, 8, 16, 32, 64, 128},
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 5,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{0b10110},
						},
						{
							CPUID:      1,
							OwnedCores: []uint64{0x100010100},
						},
						{
							CPUID:      2,
							OwnedCores: []uint64{0x0, 0x1, 0x1},
						},
						{
							CPUID:      3,
							OwnedCores: []uint64{0x0, 0x0, 0x0, 0x0, 0x1},
						},
						{
							CPUID: 4,
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			expectedCPUCoreOutput: map[uint][]int{0: {1, 2, 4}, 1: {8, 16, 32}, 2: {64, 128}},
			wantErr:               false,
		},
		{
			name: "CPU Hierarchy with multiple CPUs and device options with extra CPU core discovery",
			cOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{0, 1, 2},
				MinorRange: []int{1, 2, 4, 8, 16, 32, 64},
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 5,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{0b10110},
						},
						{
							CPUID:      1,
							OwnedCores: []uint64{0x100010100},
						},
						{
							CPUID:      2,
							OwnedCores: []uint64{0x0, 0x1, 0x1, 0x1},
						},
						{
							CPUID:      3,
							OwnedCores: []uint64{0x0, 0x0, 0x0, 0x0, 0x1},
						},
						{
							CPUID: 4,
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			expectedCPUCoreOutput: map[uint][]int{0: {1, 2, 4}, 1: {8, 16, 32}, 2: {64}},
			wantErr:               false,
		},
		{
			name: "CPU Hierarchy with multiple CPUs and device options Major -1",
			cOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{-1},
				MinorRange: []int{1, 2, 4, 8, 16, 32, 64, 128, 256},
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 5,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{0b10110},
						},
						{
							CPUID:      1,
							OwnedCores: []uint64{0x100010100},
						},
						{
							CPUID:      2,
							OwnedCores: []uint64{0x0, 0x1, 0x1, 0x0},
						},
						{
							CPUID:      3,
							OwnedCores: []uint64{0x0, 0x0, 0x0, 0x0, 0x1},
						},
						{
							CPUID: 4,
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			expectedCPUCoreOutput: map[uint][]int{0: {1, 2, 4}, 1: {8, 16, 32}, 2: {64, 128}, 3: {256}, 4: {}},
			wantErr:               false,
		},
		{
			name: "CPU Hierarchy with multiple CPUs and device options Major -1 and Minor -1",
			cOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{-1},
				MinorRange: []int{-1},
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 5,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{0b10110},
						},
						{
							CPUID:      1,
							OwnedCores: []uint64{0x100010100},
						},
						{
							CPUID:      2,
							OwnedCores: []uint64{0x0, 0x1, 0x1, 0x0},
						},
						{
							CPUID:      3,
							OwnedCores: []uint64{0x0, 0x0, 0x0, 0x0, 0x1},
						},
						{
							CPUID: 4,
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			expectedCPUCoreOutput: map[uint][]int{0: {1, 2, 4}, 1: {8, 16, 32}, 2: {64, 128}, 3: {256}, 4: {}},
			wantErr:               false,
		},
		{
			name: "CPU Hierarchy with multiple CPUs and missing CPU",
			cOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{0, 1, 2, 3, 4, 5},
				MinorRange: []int{1, 2, 4, 8, 16, 32, 64, 128, 256},
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 5,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{0b10110},
						},
						{
							CPUID:      1,
							OwnedCores: []uint64{0x100010100},
						},
						{
							CPUID:      2,
							OwnedCores: []uint64{0x0, 0x1, 0x1, 0x0},
						},
						{
							CPUID:      3,
							OwnedCores: []uint64{0x0, 0x0, 0x0, 0x0, 0x1},
						},
						{
							CPUID: 4,
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			wantErr: true,
		},
		{
			name: "CPU Hierarchy with multiple CPUs and missing CPU cores",
			cOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{0, 1, 2, 3, 4},
				MinorRange: []int{1, 2, 4, 8, 16, 32, 64, 128, 256, 1024},
			},
			mockCalls: func() {
				mockCPUHierarchy := dcgm.CPUHierarchy_v2{
					NumCPUs: 5,
					CPUs: [dcgm.MAX_NUM_CPUS]dcgm.CPUHierarchyCPU_v2{
						{
							CPUID:      0,
							OwnedCores: []uint64{0b10110},
						},
						{
							CPUID:      1,
							OwnedCores: []uint64{0x100010100},
						},
						{
							CPUID:      2,
							OwnedCores: []uint64{0x0, 0x1, 0x1, 0x0},
						},
						{
							CPUID:      3,
							OwnedCores: []uint64{0x0, 0x0, 0x0, 0x0, 0x1},
						},
						{
							CPUID: 4,
						},
					},
				}
				mockDCGMProvider.EXPECT().GetCPUHierarchy().Return(mockCPUHierarchy, nil)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mockCalls()

			if !tt.wantErr {

				deviceInfo := Info{}
				err := deviceInfo.initializeCPUInfo(tt.cOpts)
				assert.NoError(t, err, "Error not expected")
				assert.Equal(t, len(tt.expectedCPUCoreOutput), len(deviceInfo.cpus), "CPU length mismatch")

				for _, cpu := range deviceInfo.cpus {
					assert.Equal(t, len(tt.expectedCPUCoreOutput[cpu.EntityId]), len(cpu.Cores), "Core length mismatch")
					if tt.expectedCPUSerials != nil {
						assert.Equal(t, tt.expectedCPUSerials[cpu.EntityId], cpu.Serial, "CPU serial mismatch")
					}

					for _, core := range cpu.Cores {
						assert.True(t, slices.Contains(tt.expectedCPUCoreOutput[cpu.EntityId], int(core)),
							"Core mismatch")
					}
				}
			} else {
				deviceInfo := Info{}
				err := deviceInfo.initializeCPUInfo(tt.cOpts)
				assert.Error(t, err, "Error expected")
			}
		})
	}
}

func TestInitializeNvSwitchInfo(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	tests := []struct {
		name                    string
		sOpts                   appconfig.DeviceOptions
		switchOutput            []uint
		linkStatusOutput        []dcgm.NvLinkStatus
		mockCalls               func([]uint, []dcgm.NvLinkStatus)
		expectedSwitchToLinkMap map[uint][]uint
		wantErr                 bool
	}{
		{
			name: "Zero Switches",
			sOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			switchOutput: []uint{},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
			},
			wantErr: true,
		},
		{
			name: "Single switch Single Link",
			sOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			switchOutput: []uint{1},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			expectedSwitchToLinkMap: map[uint][]uint{1: {1}},
			wantErr:                 false,
		},
		{
			name: "Single switch Multiple Links",
			sOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			switchOutput: []uint{1},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(2)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(3)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			expectedSwitchToLinkMap: map[uint][]uint{1: {1, 2, 3}},
			wantErr:                 false,
		},
		{
			name: "Multiple switch Multiple Links",
			sOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			switchOutput: []uint{1, 2, 3},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(2)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(3)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			expectedSwitchToLinkMap: map[uint][]uint{1: {1, 2}, 2: {3}, 3: {}},
			wantErr:                 false,
		},
		{
			name: "Multiple switch Multiple Links with device options",
			sOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{1, 2, 3, 4, 5},
				MinorRange: []int{1, 2, 3, 4, 5, 6, 7, 8, 9},
			},
			switchOutput: []uint{1, 2, 3, 4, 5},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(2)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(3)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(4)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(5)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(6)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(7)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(8)},
				{ParentId: uint(4), ParentType: dcgm.FE_SWITCH, Index: uint(9)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			expectedSwitchToLinkMap: map[uint][]uint{1: {1, 2, 3}, 2: {4, 5, 6}, 3: {7, 8}, 4: {9}, 5: {}},
			wantErr:                 false,
		},
		{
			name: "Multiple switch Multiple Links with device options with extra Switch discovery",
			sOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{1, 2, 3},
				MinorRange: []int{1, 2, 3, 4, 5, 6, 7, 8},
			},
			switchOutput: []uint{1, 2, 3, 4, 5},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(2)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(3)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(4)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(5)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(6)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(7)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(8)},
				{ParentId: uint(4), ParentType: dcgm.FE_SWITCH, Index: uint(9)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			expectedSwitchToLinkMap: map[uint][]uint{1: {1, 2, 3}, 2: {4, 5, 6}, 3: {7, 8}},
			wantErr:                 false,
		},
		{
			name: "Multiple switch Multiple Links with device options with extra Link discovery",
			sOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{1, 2, 3},
				MinorRange: []int{1, 2, 3, 4, 5, 6, 7},
			},
			switchOutput: []uint{1, 2, 3, 4},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(2)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(3)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(4)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(5)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(6)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(7)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(8)},
				{ParentId: uint(4), ParentType: dcgm.FE_SWITCH, Index: uint(9)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			expectedSwitchToLinkMap: map[uint][]uint{1: {1, 2, 3}, 2: {4, 5, 6}, 3: {7}},
			wantErr:                 false,
		},
		{
			name: "Multiple switch Multiple Links and device options Major -1",
			sOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{-1},
				MinorRange: []int{1, 2, 3, 4, 5, 6, 7, 8, 9},
			},
			switchOutput: []uint{1, 2, 3, 4, 5},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(2)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(3)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(4)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(5)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(6)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(7)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(8)},
				{ParentId: uint(4), ParentType: dcgm.FE_SWITCH, Index: uint(9)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			expectedSwitchToLinkMap: map[uint][]uint{1: {1, 2, 3}, 2: {4, 5, 6}, 3: {7, 8}, 4: {9}, 5: {}},
			wantErr:                 false,
		},
		{
			name: "Multiple switch Multiple Links and device options Major empty",
			sOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{},
				MinorRange: []int{-1},
			},
			switchOutput: []uint{1, 2, 3, 4, 5},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(2)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(3)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(4)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(5)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(6)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(7)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(8)},
				{ParentId: uint(4), ParentType: dcgm.FE_SWITCH, Index: uint(9)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			expectedSwitchToLinkMap: map[uint][]uint{},
			wantErr:                 false,
		},
		{
			name: "Multiple switch Multiple Links and device options Major -1 and Minor -1",
			sOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{-1},
				MinorRange: []int{-1},
			},
			switchOutput: []uint{1, 2, 3, 4, 5},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(2)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(3)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(4)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(5)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(6)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(7)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(8)},
				{ParentId: uint(4), ParentType: dcgm.FE_SWITCH, Index: uint(9)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			expectedSwitchToLinkMap: map[uint][]uint{1: {1, 2, 3}, 2: {4, 5, 6}, 3: {7, 8}, 4: {9}, 5: {}},
			wantErr:                 false,
		},
		{
			name: "Multiple switch Multiple Links with missing switches",
			sOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{1, 2, 3, 4, 5, 6},
				MinorRange: []int{1, 2, 3, 4, 5, 6, 7, 8, 9},
			},
			switchOutput: []uint{1, 2, 3, 4},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(2)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(3)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(4)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(5)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(6)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(7)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(8)},
				{ParentId: uint(4), ParentType: dcgm.FE_SWITCH, Index: uint(9)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			wantErr: true,
		},
		{
			name: "Multiple switch Multiple Links with missing links",
			sOpts: appconfig.DeviceOptions{
				Flex:       false,
				MajorRange: []int{1, 2, 3, 4},
				MinorRange: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13},
			},
			switchOutput: []uint{1, 2, 3, 4},
			linkStatusOutput: []dcgm.NvLinkStatus{
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(1)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(2)},
				{ParentId: uint(1), ParentType: dcgm.FE_SWITCH, Index: uint(3)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(4)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(5)},
				{ParentId: uint(2), ParentType: dcgm.FE_SWITCH, Index: uint(6)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(7)},
				{ParentId: uint(3), ParentType: dcgm.FE_SWITCH, Index: uint(8)},
				{ParentId: uint(4), ParentType: dcgm.FE_SWITCH, Index: uint(9)},
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, nil)
			},
			wantErr: true,
		},
		{
			name: "Error GetEntityGroupEntities Response",
			sOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, fmt.Errorf("some error"),
				)
			},
			wantErr: true,
		},
		{
			name: "Error GetNvLinkLinkStatus Response",
			sOpts: appconfig.DeviceOptions{
				Flex: true,
			},
			switchOutput: []uint{1},
			mockCalls: func(switchOutput []uint, linkStatusOutput []dcgm.NvLinkStatus) {
				mockDCGMProvider.EXPECT().GetEntityGroupEntities(gomock.Any()).Return(
					switchOutput, nil,
				)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return(linkStatusOutput, fmt.Errorf("some error"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mockCalls(tt.switchOutput, tt.linkStatusOutput)

			if !tt.wantErr {
				deviceInfo := Info{}
				err := deviceInfo.initializeNvSwitchInfo(tt.sOpts)
				assert.NoError(t, err, "Error not expected")
				assert.Equal(t, len(tt.expectedSwitchToLinkMap), len(deviceInfo.switches), "Switch length mismatch")

				for _, swInfo := range deviceInfo.switches {
					assert.Equal(t, len(tt.expectedSwitchToLinkMap[swInfo.EntityId]), len(swInfo.NvLinks),
						"NV Link length mismatch")

					for _, nvLink := range swInfo.NvLinks {
						assert.True(t, slices.Contains(tt.expectedSwitchToLinkMap[swInfo.EntityId], nvLink.Index),
							"NV Link Index mismatch")
					}
				}
			} else {
				deviceInfo := Info{}
				err := deviceInfo.initializeNvSwitchInfo(tt.sOpts)
				assert.Error(t, err, "Error expected")
			}
		})
	}
}

func TestVerifyDevicePresence(t *testing.T) {
	deviceInfo := SpoofGPUDeviceInfo()
	deviceInfo.gOpt.Flex = true
	err := deviceInfo.verifyDevicePresence()
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)

	deviceInfo.gOpt.Flex = false
	deviceInfo.gOpt.MajorRange = append(deviceInfo.gOpt.MajorRange, -1)
	deviceInfo.gOpt.MinorRange = append(deviceInfo.gOpt.MinorRange, -1)
	err = deviceInfo.verifyDevicePresence()
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)

	deviceInfo.gOpt.MinorRange[0] = 10 // this GPU instance doesn't exist
	err = deviceInfo.verifyDevicePresence()
	require.NotEqual(t, err, nil, "Expected to have an error for a non-existent GPU instance, but none found")

	deviceInfo.gOpt.MajorRange[0] = 10 // this GPU doesn't exist
	deviceInfo.gOpt.MinorRange[0] = -1
	err = deviceInfo.verifyDevicePresence()
	require.NotEqual(t, err, nil, "Expected to have an error for a non-existent GPU, but none found")

	// Add gpus and instances that exist
	deviceInfo.gOpt.MajorRange[0] = 0
	deviceInfo.gOpt.MajorRange = append(deviceInfo.gOpt.MajorRange, 1)
	deviceInfo.gOpt.MinorRange[0] = 0
	deviceInfo.gOpt.MinorRange = append(deviceInfo.gOpt.MinorRange, 14)
	err = deviceInfo.verifyDevicePresence()
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)
}

func TestIsSwitchWatched(t *testing.T) {
	tests := []struct {
		name       string
		switchID   uint
		deviceInfo Info
		want       bool
	}{
		{
			name:     "Monitor all devices",
			switchID: 1,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					Flex: true,
				},
			},
			want: true,
		},
		{
			name:     "MajorRange empty",
			switchID: 2,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{},
				},
			},
			want: false,
		},
		{
			name:     "MajorRange contains -1 to watch all devices",
			switchID: 3,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{-1},
				},
			},
			want: true,
		},
		{
			name:     "SwitchID in MajorRange",
			switchID: 4,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{3, 4, 5},
				},
			},
			want: true,
		},
		{
			name:     "SwitchID not in MajorRange",
			switchID: 5,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{3, 4, 6},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.deviceInfo.IsSwitchWatched(tt.switchID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsLinkWatched(t *testing.T) {
	tests := []struct {
		name       string
		linkIndex  uint
		switchID   uint
		deviceInfo Info
		want       bool
	}{
		{
			name:       "Monitor all devices",
			linkIndex:  1,
			deviceInfo: Info{sOpt: appconfig.DeviceOptions{Flex: true}},
			want:       true,
		},
		{
			name:       "No watched devices",
			linkIndex:  1,
			deviceInfo: Info{},
			want:       false,
		},
		{
			name:      "Watched link with empty MinorRange",
			linkIndex: 2,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{-1},
				},
				switches: []SwitchInfo{
					{
						EntityId: 1,
						NvLinks: []dcgm.NvLinkStatus{
							{Index: 2},
						},
					},
				},
			},
			want: false,
		},
		{
			name:      "MinorRange contains -1 to watch all links",
			switchID:  1,
			linkIndex: 3,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{-1},
					MinorRange: []int{-1},
				},
				switches: []SwitchInfo{
					{
						EntityId: 1,
						NvLinks: []dcgm.NvLinkStatus{
							{Index: 3},
						},
					},
				},
			},
			want: true,
		},
		{
			name:      "The link not in the watched switch",
			switchID:  1,
			linkIndex: 4,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{-1},
					MinorRange: []int{1, 2, 3},
				},
				switches: []SwitchInfo{
					{
						EntityId: 1,
						NvLinks: []dcgm.NvLinkStatus{
							{Index: 4},
						},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.deviceInfo.IsLinkWatched(tt.linkIndex, tt.switchID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsCPUWatched(t *testing.T) {
	tests := []struct {
		name       string
		cpuID      uint
		deviceInfo Info
		want       bool
	}{
		{
			name:  "Monitor all devices",
			cpuID: 1,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{Flex: true},
				cpus: []CPUInfo{
					{
						EntityId: 1,
					},
				},
			},
			want: true,
		},
		{
			name:  "MajorRange Contains -1",
			cpuID: 2,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{MajorRange: []int{-1}},
				cpus: []CPUInfo{
					{
						EntityId: 2,
					},
				},
			},
			want: true,
		},
		{
			name:  "CPU ID in MajorRange",
			cpuID: 3,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{MajorRange: []int{1, 2, 3}},
				cpus: []CPUInfo{
					{
						EntityId: 3,
					},
				},
			},
			want: true,
		},
		{
			name:  "CPU ID Not in MajorRange",
			cpuID: 4,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{MajorRange: []int{1, 2, 3}},
				cpus: []CPUInfo{
					{
						EntityId: 4,
					},
				},
			},
			want: false,
		},
		{
			name:  "MajorRange Empty",
			cpuID: 5,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{MajorRange: []int{}},
				cpus: []CPUInfo{
					{
						EntityId: 5,
					},
				},
			},
			want: false,
		},
		{
			name:  "CPU not found",
			cpuID: 6,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{MajorRange: []int{}},
				cpus: []CPUInfo{
					{
						EntityId: 5,
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.deviceInfo.IsCPUWatched(tt.cpuID))
		})
	}
}

func TestIsCoreWatched(t *testing.T) {
	tests := []struct {
		name       string
		coreID     uint
		cpuID      uint
		deviceInfo Info
		want       bool
	}{
		{
			name:   "Monitor all devices",
			coreID: 1,
			cpuID:  1,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{Flex: true},
			},
			want: true,
		},
		{
			name:   "Core in MinorRange",
			coreID: 2,
			cpuID:  1,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{
					MinorRange: []int{1, 2, 3},
					MajorRange: []int{-1},
				},
				cpus: []CPUInfo{{EntityId: 1}},
			},
			want: true,
		},
		{
			name:   "Core Not in MinorRange",
			coreID: 4,
			cpuID:  1,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{
					MinorRange: []int{1, 2, 3},
					MajorRange: []int{-1},
				},
				cpus: []CPUInfo{{EntityId: 1}},
			},
			want: false,
		},
		{
			name:   "MinorRange Contains -1",
			coreID: 5,
			cpuID:  1,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{
					MinorRange: []int{-1},
					MajorRange: []int{-1},
				},
				cpus: []CPUInfo{{EntityId: 1}},
			},
			want: true,
		},
		{
			name:   "CPU Not Found",
			coreID: 1,
			cpuID:  2,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{
					MinorRange: []int{1, 2, 3},
					MajorRange: []int{-1},
				},
				cpus: []CPUInfo{{EntityId: 1}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.deviceInfo.IsCoreWatched(tt.coreID, tt.cpuID))
		})
	}
}

func TestSetMigProfileNames(t *testing.T) {
	config := &appconfig.Config{
		EnableDCGMLog: true,
		DCGMLogLevel:  "DEBUG",
	}
	dcgmprovider.SmartDCGMInit(t, config)
	defer dcgmprovider.Client().Cleanup()

	tests := []struct {
		name       string
		deviceInfo Info

		values []dcgm.FieldValue_v2
		valid  bool
	}{
		{
			name: "MIG profile found",
			deviceInfo: Info{
				gpuCount: 1,
				gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 1},
						},
					},
				},
			},
			values: []dcgm.FieldValue_v2{
				{
					EntityID:    1,
					FieldType:   dcgm.DCGM_FT_STRING,
					StringValue: &fakeProfileName,
				},
			},
			valid: true,
		},
		{
			name: "Multiple MIG gpus",
			deviceInfo: Info{
				gpuCount: 3,
				gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 1},
						},
					},
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 2},
						},
					},
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 3},
						},
					},
				},
			},
			values: []dcgm.FieldValue_v2{
				{
					EntityID:    2,
					FieldType:   dcgm.DCGM_FT_STRING,
					StringValue: &fakeProfileName,
				},
			},
			valid: true,
		},
		{
			name: "Multiple MIG gpus and Values",
			deviceInfo: Info{
				gpuCount: 3,
				gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 1},
						},
					},
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 2},
						},
					},
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 3},
						},
					},
				},
			},
			values: []dcgm.FieldValue_v2{
				{
					EntityID:    2,
					FieldType:   dcgm.DCGM_FT_STRING,
					StringValue: &fakeProfileName,
				},
				{
					EntityID:    3,
					FieldType:   dcgm.DCGM_FT_STRING,
					StringValue: &fakeProfileName,
				},
			},
			valid: true,
		},
		{
			name: "MIG profile not found",
			deviceInfo: Info{
				gpuCount: 1,
				gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 1},
						},
					},
				},
			},
			values: []dcgm.FieldValue_v2{
				{
					EntityID:    2,
					FieldType:   dcgm.DCGM_FT_STRING,
					StringValue: &fakeProfileName,
				},
			},
			valid: false,
		},
		{
			name: "MIG profile not string type",
			deviceInfo: Info{
				gpuCount: 1,
				gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 1},
						},
					},
				},
			},
			values: []dcgm.FieldValue_v2{
				{
					EntityID:    1,
					FieldType:   dcgm.DCGM_FT_BINARY,
					StringValue: &fakeProfileName,
					Value:       [4096]byte{'1', '2', '3'},
				},
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.valid {
				assert.NoError(t, tt.deviceInfo.setMigProfileNames(tt.values), "Expected no error.")
			} else {
				assert.Error(t, tt.deviceInfo.setMigProfileNames(tt.values), "Expected an error.")
			}
		})
	}
}

func Test_getCoreArray(t *testing.T) {
	tests := []struct {
		name    string
		bitmask []uint64
		want    []uint
	}{
		{
			name:    "Empty bitmask",
			bitmask: []uint64{},
			want:    []uint{},
		},
		{
			name:    "Single value - single core",
			bitmask: []uint64{1},
			want:    []uint{0},
		},
		{
			name:    "Multiple values - multiple cores",
			bitmask: []uint64{1, 2, 8},
			want:    []uint{0, 65, 131},
		},
		{
			name:    "Single uint64 value - multiple cores",
			bitmask: []uint64{0b1101},
			want:    []uint{0, 2, 3},
		},
		{
			name:    "Multiple uint64 values - multiple cores",
			bitmask: []uint64{0b1101, 0b0111},
			want:    []uint{0, 2, 3, 64, 65, 66},
		},
		{
			name:    "Large bitmask",
			bitmask: []uint64{0b1101, 0b1010, 0b1111000011110000},
			want:    []uint{0, 2, 3, 65, 67, 132, 133, 134, 135, 140, 141, 142, 143},
		},
		{
			name: "Overflow uint64 values",
			bitmask: []uint64{
				0b0001, 0b0001, 0b0001, 0b0001, 0b0001, 0b0001, 0b0001, 0b0001, 0b0001, 0b0001, 0b0001,
				0b0001, 0b0001, 0b0001, 0b0001, 0b0001, 0b0001, 0b0001,
			},
			want: []uint{0, 64, 128, 192, 256, 320, 384, 548, 512, 576, 640, 704, 768, 832, 896, 960, 1024, 1088},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.bitmask) > 16 {
				assert.Panics(t, func() { getCoreArray(tt.bitmask) }, "Expected getCoreArray to panic")
			} else {
				result := getCoreArray(tt.bitmask)
				assert.True(t, slices.Equal(tt.want, result), "getCoreArray results not equal", tt.want, result)
			}
		})
	}
}

func TestGetGPUInstanceIdentifier(t *testing.T) {
	fakeDevices := SpoofGPUDevices()
	gpuInstanceID := 3

	type args struct {
		deviceInfo    Provider
		gpuuuid       string
		gpuInstanceID uint
	}
	tests := []struct {
		name           string
		args           args
		expectedOutput string
	}{
		{
			name: "GPU UUID found",
			args: args{
				deviceInfo: &Info{
					gpuCount: 2,
					gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
						{
							DeviceInfo: fakeDevices[0],
						},
						{
							DeviceInfo: fakeDevices[1],
						},
					},
				},
				gpuuuid:       fakeDevices[1].UUID,
				gpuInstanceID: uint(gpuInstanceID),
			},
			expectedOutput: fmt.Sprintf("%d-%d", fakeDevices[1].GPU, gpuInstanceID),
		},
		{
			name: "GPU UUID not found",
			args: args{
				deviceInfo: &Info{
					gpuCount: 2,
					gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
						{
							DeviceInfo: fakeDevices[0],
						},
						{
							DeviceInfo: fakeDevices[1],
						},
					},
				},
				gpuuuid: "random",
			},
			expectedOutput: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.expectedOutput, GetGPUInstanceIdentifier(tt.args.deviceInfo, tt.args.gpuuuid,
				tt.args.gpuInstanceID), "GPU Instance Identifier mismatch")
		})
	}
}
