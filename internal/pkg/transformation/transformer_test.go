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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestGetTransformations checks configured transform selection and Kubernetes ownership of container labels.
func TestGetTransformations(t *testing.T) {
	// Each assert callback checks the selected transforms for one configuration.
	tests := []struct {
		name   string
		config *appconfig.Config
		assert func(*testing.T, []Transform)
	}{
		{
			name: "The environment is not kubernetes",
			config: &appconfig.Config{
				Kubernetes: false,
			},
			assert: func(t *testing.T, transforms []Transform) {
				assert.Len(t, transforms, 0)
			},
		},
		{
			name: "The environment is kubernetes",
			config: &appconfig.Config{
				Kubernetes: true,
			},
			assert: func(t *testing.T, transforms []Transform) {
				assert.Len(t, transforms, 1)
			},
		},
		{
			name: "The environment is HPC cluster",
			config: &appconfig.Config{
				HPCJobMappingDir:            "/var/run/nvidia/slurm",
				HPCJobMappingMaxFileBytes:   appconfig.DefaultHPCJobMappingMaxFileBytes,
				HPCJobMappingMaxJobIDBytes:  appconfig.DefaultHPCJobMappingMaxJobIDBytes,
				HPCJobMappingMaxJobsPerFile: appconfig.DefaultHPCJobMappingMaxJobsPerFile,
			},
			assert: func(t *testing.T, transforms []Transform) {
				assert.Len(t, transforms, 1)
			},
		},
		{
			name: "The environment has non-Kubernetes container labels enabled",
			config: &appconfig.Config{
				ContainerLabels:        true,
				ContainerRuntimeSocket: "/run/podman/podman.sock",
			},
			assert: func(t *testing.T, transforms []Transform) {
				require.Len(t, transforms, 1)
				assert.Equal(t, "containerMapper", transforms[0].Name())
			},
		},
		{
			name: "Kubernetes mode owns the container label",
			config: &appconfig.Config{
				Kubernetes:             true,
				ContainerLabels:        true,
				ContainerRuntimeSocket: "/run/podman/podman.sock",
			},
			assert: func(t *testing.T, transforms []Transform) {
				require.Len(t, transforms, 1)
				assert.NotEqual(t, "containerMapper", transforms[0].Name())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transformations := GetTransformations(tt.config)
			tt.assert(t, transformations)
		})
	}
}

// TestManagedTransformsOwnsPodMapperLifecycle checks managed cleanup stops its mapper and tolerates repeated calls.
func TestManagedTransformsOwnsPodMapperLifecycle(t *testing.T) {
	managed := NewManagedTransforms(&appconfig.Config{Kubernetes: true}, nil)
	require.Len(t, managed.Transformations, 1)

	podMapper, ok := managed.Transformations[0].(*PodMapper)
	require.True(t, ok)

	managed.Start()
	managed.Cleanup()
	managed.Cleanup()

	select {
	case <-podMapper.stopChan:
	default:
		t.Fatal("managed cleanup did not stop the pod mapper")
	}
}

// TestManagedTransformsStartDoesNotWaitForBackgroundWork checks Start returns while its background callback is blocked.
func TestManagedTransformsStartDoesNotWaitForBackgroundWork(t *testing.T) {
	started := make(chan struct{})
	allowFinish := make(chan struct{})
	// Release background work even if an assertion ends this test early.
	defer close(allowFinish)
	// Hold owned background work until the test releases it.
	managed := ManagedTransforms{start: func() {
		close(started)
		<-allowFinish
	}}

	finished := make(chan struct{})
	// Signal when Start itself returns, independently of background completion.
	go func() {
		managed.Start()
		close(finished)
	}()

	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Start waited for background work")
	}
}
