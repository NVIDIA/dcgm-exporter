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

package nvmlprovider

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

type MIGDeviceInfo struct {
	ParentUUID        string
	GPUInstanceID     int
	ComputeInstanceID int
}

var nvmlInterface NVML

// Initialize sets up the Singleton NVML interface.
func Initialize() {
	nvmlInterface = newNVMLProvider()
}

// reset clears the current NVML interface instance.
func reset() {
	nvmlInterface = nil
}

// Client retrieves the current NVML interface instance.
func Client() NVML {
	return nvmlInterface
}

// SetClient sets the current NVML interface instance to the provided one.
func SetClient(n NVML) {
	nvmlInterface = n
}

// nvmlProvider implements NVML Interface
type nvmlProvider struct {
	initialized bool
}

func newNVMLProvider() NVML {
	// Check if a NVML client already exists and return it if so.
	if Client() != nil && Client().(nvmlProvider).initialized {
		slog.Info("NVML already initialized.")
		return Client()
	}

	slog.Info("Attempting to initialize NVML library.")
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		err := errors.New(nvml.ErrorString(ret))
		slog.Error(fmt.Sprintf("Cannot init NVML library; err: %v", err))
		return nvmlProvider{initialized: false}
	}

	return nvmlProvider{initialized: true}
}

func (n nvmlProvider) preCheck() error {
	if !n.initialized {
		return fmt.Errorf("NVML library not initialized")
	}

	return nil
}

// GetMIGDeviceInfoByID returns information about MIG DEVICE by ID
func (n nvmlProvider) GetMIGDeviceInfoByID(uuid string) (*MIGDeviceInfo, error) {
	if err := n.preCheck(); err != nil {
		slog.Error(fmt.Sprintf("failed to get MIG Device Info; err: %v", err))
		return nil, err
	}

	device, ret := nvml.DeviceGetHandleByUUID(uuid)
	if ret == nvml.SUCCESS {
		return getMIGDeviceInfoForNewDriver(device)
	}

	return getMIGDeviceInfoForOldDriver(uuid)
}

// getMIGDeviceInfoForNewDriver identifies MIG Device Information for drivers >= R470 (470.42.01+),
// each MIG device is assigned a GPU UUID starting with MIG-<UUID>.
func getMIGDeviceInfoForNewDriver(device nvml.Device) (*MIGDeviceInfo, error) {
	parentDevice, ret := device.GetDeviceHandleFromMigDeviceHandle()
	if ret != nvml.SUCCESS {
		return nil, errors.New(nvml.ErrorString(ret))
	}

	parentUUID, ret := parentDevice.GetUUID()
	if ret != nvml.SUCCESS {
		return nil, errors.New(nvml.ErrorString(ret))
	}

	gi, ret := device.GetGpuInstanceId()
	if ret != nvml.SUCCESS {
		return nil, errors.New(nvml.ErrorString(ret))
	}

	ci, ret := device.GetComputeInstanceId()
	if ret != nvml.SUCCESS {
		return nil, errors.New(nvml.ErrorString(ret))
	}

	return &MIGDeviceInfo{
		ParentUUID:        parentUUID,
		GPUInstanceID:     gi,
		ComputeInstanceID: ci,
	}, nil
}

// getMIGDeviceInfoForOldDriver identifies MIG Device Information for drivers < R470 (e.g. R450 and R460),
// each MIG device is enumerated by specifying the CI and the corresponding parent GI. The format follows this
// convention: MIG-<GPU-UUID>/<GPU instance ID>/<Compute instance ID>.
func getMIGDeviceInfoForOldDriver(uuid string) (*MIGDeviceInfo, error) {
	tokens := strings.SplitN(uuid, "-", 2)
	if len(tokens) != 2 || tokens[0] != "MIG" {
		return nil, fmt.Errorf("unable to parse '%s' as MIG device UUID", uuid)
	}

	gpuTokens := strings.SplitN(tokens[1], "/", 3)
	if len(gpuTokens) != 3 || !strings.HasPrefix(gpuTokens[0], "GPU-") {
		return nil, fmt.Errorf("invalid MIG device UUID '%s'", uuid)
	}

	gi, err := strconv.Atoi(gpuTokens[1])
	if err != nil {
		return nil, fmt.Errorf("invalid GPU instance ID '%s' for MIG device '%s'", gpuTokens[1], uuid)
	}

	ci, err := strconv.Atoi(gpuTokens[2])
	if err != nil {
		return nil, fmt.Errorf("invalid Compute instance ID '%s' for MIG device '%s'", gpuTokens[2], uuid)
	}

	return &MIGDeviceInfo{
		ParentUUID:        gpuTokens[0],
		GPUInstanceID:     gi,
		ComputeInstanceID: ci,
	}, nil
}

// Cleanup performs cleanup operations for the NVML provider
func (n nvmlProvider) Cleanup() {
	if err := n.preCheck(); err == nil {
		reset()
	}
}
