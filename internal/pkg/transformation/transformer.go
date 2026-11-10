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

package transformation

import (
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// ManagedTransforms holds selected transforms and the runtime work they own.
// Server construction calls Start after it has accepted the transforms, then
// calls Cleanup once when that server instance retires.
type ManagedTransforms struct {
	Transformations []Transform
	start           func()
	cleanup         func()
}

// Start starts the background work owned by the selected transforms without
// delaying server startup. Pure transforms have no background work, so Start
// is safe for every set.
func (m ManagedTransforms) Start() {
	if m.start != nil {
		go m.start()
	}
}

// Cleanup stops the background work owned by the selected transforms.
// It is safe to call because each managed transform makes its own shutdown
// idempotent.
func (m ManagedTransforms) Cleanup() {
	if m.cleanup != nil {
		m.cleanup()
	}
}

// GetTransformations returns transformations selected by the resolved config.
func GetTransformations(c *appconfig.Config) []Transform {
	return NewManagedTransforms(c, nil).Transformations
}

// NewManagedTransforms selects transforms and keeps lifecycle effects beside
// the transform that needs them. It lets runtime wiring start and stop the
// pod mapper without knowing its concrete type or nested DRA resources.
func NewManagedTransforms(c *appconfig.Config, onDRAChange func()) ManagedTransforms {
	managed := ManagedTransforms{}
	if c.Kubernetes {
		podMapper := NewPodMapperWithDRAChangeCallback(c, onDRAChange)
		managed.Transformations = append(managed.Transformations, podMapper)
		managed.start = podMapper.Run
		managed.cleanup = podMapper.Stop
	}

	if c.HPCJobMappingDir != "" {
		hpcMapper := newHPCMapper(c)
		managed.Transformations = append(managed.Transformations, hpcMapper)
	}

	if c.ContainerLabels && !c.Kubernetes {
		managed.Transformations = append(managed.Transformations, newContainerMapper(c))
	}

	return managed
}
