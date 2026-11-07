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
	"log/slog"
	"sync"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

// createHealthGroup creates a DCGM group named groupNamePrefix_<random>
// containing every supported GPU on client and enables the requested health
// watches. On success the caller owns the group and must call the returned
// cleanup function, which destroys the group on the same client, logs a
// warning if that fails, and does nothing on later calls. On failure the
// function has already destroyed any group it created and returns nil cleanup.
func createHealthGroup(
	client dcgmprovider.DCGM,
	groupNamePrefix string,
	systems dcgm.HealthSystem,
) (dcgm.GroupHandle, func(), error) {
	supportedGPUs, err := client.GetSupportedDevices()
	if err != nil {
		return dcgm.GroupHandle{}, nil, fmt.Errorf("get supported GPU devices: %w", err)
	}
	if len(supportedGPUs) == 0 {
		return dcgm.GroupHandle{}, nil, errors.New("no supported GPU devices found")
	}

	groupNumber, err := utils.RandUint64()
	if err != nil {
		return dcgm.GroupHandle{}, nil, fmt.Errorf("generate health group name: %w", err)
	}

	groupID, err := client.CreateGroup(fmt.Sprintf("%s_%d", groupNamePrefix, groupNumber))
	if err != nil {
		return dcgm.GroupHandle{}, nil, fmt.Errorf("create health group: %w", err)
	}

	cleanup := sync.OnceFunc(func() {
		if destroyErr := client.DestroyGroup(groupID); destroyErr != nil {
			slog.Warn("cannot destroy health group",
				slog.Any("groupID", groupID),
				slog.String(logging.ErrorKey, destroyErr.Error()))
		}
	})

	initialized := false
	defer func() {
		if !initialized {
			cleanup()
		}
	}()

	for _, gpu := range supportedGPUs {
		if err := client.AddEntityToGroup(groupID, dcgm.FE_GPU, gpu); err != nil {
			return dcgm.GroupHandle{}, nil, fmt.Errorf("add GPU %d to health group: %w", gpu, err)
		}
	}

	if err := client.HealthSet(groupID, systems); err != nil {
		return dcgm.GroupHandle{}, nil, fmt.Errorf("set health watches for group: %w", err)
	}

	initialized = true
	return groupID, cleanup, nil
}
