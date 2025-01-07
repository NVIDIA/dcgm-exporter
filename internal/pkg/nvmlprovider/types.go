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

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/nvmlprovider/mock_client.go -package=nvmlprovider -copyright_file=../../../hack/header.txt . NVML

package nvmlprovider

type NVML interface {
	GetMIGDeviceInfoByID(string) (*MIGDeviceInfo, error)
	Cleanup()
}
