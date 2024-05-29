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

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/devicewatcher/mock_device_watcher.go -package=devicewatcher -copyright_file=../../../hack/header.txt . Watcher

package devicewatcher

import (
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

type Watcher interface {
	GetDeviceFields([]counters.Counter, dcgm.Field_Entity_Group) []dcgm.Short
	WatchDeviceFields([]dcgm.Short, deviceinfo.Provider, int64) ([]dcgm.GroupHandle, dcgm.FieldHandle, []func(), error)
}
