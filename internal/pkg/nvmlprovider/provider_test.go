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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetMIGDeviceInfoByID_When_DriverVersion_Below_R470(t *testing.T) {
	tests := []struct {
		name          string
		uuid          string
		expectedGPU   string
		expectedGi    int
		expectedCi    int
		expectedError bool
	}{
		{
			name:        "Successfull Parsing",
			uuid:        "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedGPU: "GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			expectedGi:  1,
			expectedCi:  5,
		},
		{
			name:          "Fail, Missing MIG at the beginning of UUID",
			uuid:          "GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedError: true,
		},
		{
			name:          "Fail, Missing GPU at the beginning of GPU UUID",
			uuid:          "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedError: true,
		},
		{
			name:          "Fail, GI not parsable",
			uuid:          "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/xx/5",
			expectedError: true,
		},
		{
			name:          "Fail, CI not a parsable",
			uuid:          "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/xx",
			expectedError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deviceInfo, err := GetMIGDeviceInfoByID(tc.uuid)
			if tc.expectedError && err != nil {
				return
			}
			if tc.expectedError && err == nil {
				t.Fatalf("Expected an error, but didn't get one: uuid: %v, (gpu: %v, gi: %v, ci: %v)",
					tc.uuid,
					deviceInfo.ParentUUID,
					deviceInfo.GPUInstanceID,
					deviceInfo.ComputeInstanceID)
			}
			if !tc.expectedError && err != nil {
				t.Fatalf("Unexpected error: %v, uuid: %v, (gpu: %v, gi: %v, ci: %v)",
					err,
					tc.uuid,
					deviceInfo.ParentUUID,
					deviceInfo.GPUInstanceID,
					deviceInfo.ComputeInstanceID)
			}

			assert.Equal(t, tc.expectedGPU, deviceInfo.ParentUUID, "MIG UUID parsed incorrectly: uuid: %v, (gpu: %v, gi: %v, ci: %v)",
				tc.uuid,
				deviceInfo.ParentUUID,
				deviceInfo.GPUInstanceID,
				deviceInfo.ComputeInstanceID)
			assert.Equal(t, tc.expectedGi, deviceInfo.GPUInstanceID, "MIG UUID parsed incorrectly: uuid: %v, (gpu: %v, gi: %v, ci: %v)",
				tc.uuid,
				deviceInfo.ParentUUID,
				deviceInfo.GPUInstanceID,
				deviceInfo.ComputeInstanceID)
			assert.Equal(t, tc.expectedCi, deviceInfo.ComputeInstanceID, "MIG UUID parsed incorrectly: uuid: %v, (gpu: %v, gi: %v, ci: %v)",
				tc.uuid,
				deviceInfo.ParentUUID,
				deviceInfo.GPUInstanceID,
				deviceInfo.ComputeInstanceID)
		})
	}
}
