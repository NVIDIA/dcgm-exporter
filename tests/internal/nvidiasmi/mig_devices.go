/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package nvidiasmi

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
)

// MIGDevice is one MIG device as nvidia-smi lists it: its parent GPU's UUID
// and its GPU instance ID.
type MIGDevice struct {
	GPUUUID       string
	GPUInstanceID string
}

// queryLog is the part of the `nvidia-smi -q -x` report that names MIG devices.
type queryLog struct {
	GPUs []struct {
		UUID       string `xml:"uuid"`
		MIGDevices []struct {
			GPUInstanceID string `xml:"gpu_instance_id"`
		} `xml:"mig_devices>mig_device"`
	} `xml:"gpu"`
}

// QueryMIGDevices runs `nvidia-smi -q -x` through run and returns the MIG
// devices it lists. Inside a pod that holds one MIG device, that is the one
// device. Without a deadline on ctx, the query gets 30 seconds.
func QueryMIGDevices(ctx context.Context, run Runner) ([]MIGDevice, error) {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()

	output, err := run(ctx, "-q", "-x")
	if err != nil {
		return nil, fmt.Errorf("query MIG devices with nvidia-smi: %w", err)
	}

	devices, err := ParseMIGDevices(output)
	if err != nil {
		return nil, fmt.Errorf("parse nvidia-smi MIG devices: %w", err)
	}

	return devices, nil
}

// ParseMIGDevices parses the XML report of `nvidia-smi -q -x`. It requires a
// GPU UUID and a GPU instance ID for every MIG device; a GPU without MIG
// devices adds nothing.
func ParseMIGDevices(output []byte) ([]MIGDevice, error) {
	var report queryLog
	if err := xml.Unmarshal(output, &report); err != nil {
		return nil, fmt.Errorf("read XML report: %w", err)
	}

	var devices []MIGDevice
	for _, gpu := range report.GPUs {
		for _, raw := range gpu.MIGDevices {
			device := MIGDevice{
				GPUUUID:       strings.TrimSpace(gpu.UUID),
				GPUInstanceID: strings.TrimSpace(raw.GPUInstanceID),
			}
			if device.GPUUUID == "" || device.GPUInstanceID == "" {
				return nil, fmt.Errorf("MIG device without a GPU UUID or GPU instance ID: %+v", device)
			}

			devices = append(devices, device)
		}
	}

	return devices, nil
}
