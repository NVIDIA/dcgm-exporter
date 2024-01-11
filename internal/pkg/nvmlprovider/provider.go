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
	"strconv"
	"strings"
	"sync"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/sirupsen/logrus"
)

var nvmlOnce *sync.Once = new(sync.Once)

type MIGDeviceInfo struct {
	ParentUUID        string
	GPUInstanceID     int
	ComputeInstanceID int
}

// GetMIGDeviceInfoByID returns information about MIG DEVICE by ID
func GetMIGDeviceInfoByID(uuid string) (*MIGDeviceInfo, error) {
	var err error

	nvmlOnce.Do(func() {
		ret := nvml.Init()
		if ret != nvml.SUCCESS {
			err = errors.New(nvml.ErrorString(ret))
			logrus.Error("Can not init NVML library")
		}
	})
	if err != nil {
		return nil, err
	}

	// 	1. With drivers >= R470 (470.42.01+), each MIG device is assigned a GPU UUID starting
	//  with MIG-<UUID>.

	device, ret := nvml.DeviceGetHandleByUUID(uuid)
	if ret == nvml.SUCCESS {
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

	//  2. With drivers < R470 (e.g. R450 and R460), each MIG device is enumerated by
	// specifying the CI and the corresponding parent GI. The format follows this
	// convention: MIG-<GPU-UUID>/<GPU instance ID>/<compute instance ID>.

	tokens := strings.SplitN(uuid, "-", 2)
	if len(tokens) != 2 || tokens[0] != "MIG" {
		return nil, fmt.Errorf("Unable to parse UUID as MIG device")
	}

	tokens = strings.SplitN(tokens[1], "/", 3)
	if len(tokens) != 3 || !strings.HasPrefix(tokens[0], "GPU-") {
		return nil, fmt.Errorf("Unable to parse UUID as MIG device")
	}

	gi, err := strconv.Atoi(tokens[1])
	if err != nil {
		return nil, fmt.Errorf("Unable to parse UUID as MIG device")
	}

	ci, err := strconv.Atoi(tokens[2])
	if err != nil {
		return nil, fmt.Errorf("Unable to parse UUID as MIG device")
	}

	return &MIGDeviceInfo{
		ParentUUID:        tokens[0],
		GPUInstanceID:     gi,
		ComputeInstanceID: ci,
	}, nil
}
