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

package transformation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"

	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

// countingMIGLookup records how often the cache asks NVML about each UUID.
type countingMIGLookup struct {
	calls   map[string]int
	answers map[string]*nvmlprovider.MIGDeviceInfo
}

func (l *countingMIGLookup) lookup(uuid string) (*nvmlprovider.MIGDeviceInfo, error) {
	l.calls[uuid]++
	if info, found := l.answers[uuid]; found {
		return info, nil
	}
	return nil, errors.New("not a MIG device")
}

// newTestMIGLookupCache returns a cache with a counting NVML stand-in and a
// clock the test moves by hand.
func newTestMIGLookupCache(retryAfter time.Duration) (*migLookupCache, *countingMIGLookup, *time.Time) {
	nvml := &countingMIGLookup{
		calls: map[string]int{},
		answers: map[string]*nvmlprovider.MIGDeviceInfo{
			"MIG-a": {ParentUUID: "GPU-0", GPUInstanceID: 1},
		},
	}
	now := time.Unix(1000, 0)
	cache := newMIGLookupCache(retryAfter)
	cache.lookupFunc = nvml.lookup
	cache.now = func() time.Time { return now }
	return cache, nvml, &now
}

func TestMIGLookupCache(t *testing.T) {
	const retryAfter = 30 * time.Second

	tests := []struct {
		name      string
		run       func(cache *migLookupCache, now *time.Time)
		wantCalls map[string]int
	}{
		{
			name: "a successful answer is reused across snapshots",
			run: func(cache *migLookupCache, _ *time.Time) {
				for range 3 {
					_, _ = cache.lookup("MIG-a")
					cache.retainUsed()
				}
			},
			wantCalls: map[string]int{"MIG-a": 1},
		},
		{
			name: "a successful answer is checked again once it is migLookupMaxAge old",
			run: func(cache *migLookupCache, now *time.Time) {
				_, _ = cache.lookup("MIG-a")
				*now = now.Add(migLookupMaxAge - time.Second)
				_, _ = cache.lookup("MIG-a")
				*now = now.Add(time.Second)
				_, _ = cache.lookup("MIG-a")
				_, _ = cache.lookup("MIG-a")
			},
			wantCalls: map[string]int{"MIG-a": 2},
		},
		{
			name: "a failed answer is reused until retryAfter passes",
			run: func(cache *migLookupCache, now *time.Time) {
				_, _ = cache.lookup("MIG-bad")
				*now = now.Add(retryAfter - time.Second)
				_, _ = cache.lookup("MIG-bad")
				*now = now.Add(time.Second)
				_, _ = cache.lookup("MIG-bad")
			},
			wantCalls: map[string]int{"MIG-bad": 2},
		},
		{
			name: "a UUID missing from one snapshot is looked up again when it returns",
			run: func(cache *migLookupCache, _ *time.Time) {
				_, _ = cache.lookup("MIG-a")
				cache.retainUsed()
				cache.retainUsed()
				_, _ = cache.lookup("MIG-a")
			},
			wantCalls: map[string]int{"MIG-a": 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache, nvml, now := newTestMIGLookupCache(retryAfter)
			tc.run(cache, now)
			require.Equal(t, tc.wantCalls, nvml.calls)
		})
	}
}

func TestMIGLookupCacheReturnsCachedAnswer(t *testing.T) {
	cache, _, _ := newTestMIGLookupCache(time.Minute)

	first, err := cache.lookup("MIG-a")
	require.NoError(t, err)
	second, err := cache.lookup("MIG-a")
	require.NoError(t, err)
	require.Equal(t, first, second)

	_, err = cache.lookup("MIG-bad")
	require.Error(t, err)
	_, err = cache.lookup("MIG-bad")
	require.Error(t, err, "a cached failure must still be reported as a failure")
}

func TestMIGLookupCacheKeepsOnlyUsedEntries(t *testing.T) {
	cache, _, _ := newTestMIGLookupCache(time.Minute)
	_, _ = cache.lookup("MIG-a")
	_, _ = cache.lookup("MIG-bad")
	cache.retainUsed()

	_, _ = cache.lookup("MIG-a")
	cache.retainUsed()

	require.Len(t, cache.entries, 1)
	require.Contains(t, cache.entries, "MIG-a")
}

// TestDRAMappingKeyUsesMIGLookupCache checks that a DRA claim for an existing
// MIG device reuses the cached NVML answer.
func TestDRAMappingKeyUsesMIGLookupCache(t *testing.T) {
	cache, nvml, _ := newTestMIGLookupCache(time.Minute)
	migInfo := &DRAMigDeviceInfo{MIGDeviceUUID: "MIG-a", ParentUUID: "GPU-0"}

	for range 3 {
		draMappingKey("pool-a", "gpu-0-mig-0", "GPU-0", migInfo, &deviceinfo.Info{}, cache)
	}

	require.Equal(t, map[string]int{"MIG-a": 1}, nvml.calls)
}

// migSnapshotServer serves a pod-resources response that the test can replace
// between scrapes.
type migSnapshotServer struct {
	podresourcesapi.UnimplementedPodResourcesListerServer
	mu        sync.Mutex
	deviceIDs []string
}

func (s *migSnapshotServer) setDeviceIDs(ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviceIDs = ids
}

func (s *migSnapshotServer) List(
	context.Context, *podresourcesapi.ListPodResourcesRequest,
) (*podresourcesapi.ListPodResourcesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &podresourcesapi.ListPodResourcesResponse{PodResources: []*podresourcesapi.PodResources{{
		Name:      "mig-pod",
		Namespace: "default",
		Containers: []*podresourcesapi.ContainerResources{{
			Name: "worker",
			Devices: []*podresourcesapi.ContainerDevices{{
				ResourceName: appconfig.NvidiaMigResourcePrefix + "1g.10gb",
				DeviceIds:    append([]string(nil), s.deviceIDs...),
			}},
		}},
	}}}, nil
}

// TestPodMapperProcessLooksUpMIGDevicesOnce counts NVML calls through the real
// scrape path: repeated scrapes of the same allocation ask NVML once per MIG
// UUID, a failed lookup is not repeated within the collection interval, and a
// UUID that leaves and returns is looked up again.
func TestPodMapperProcessLooksUpMIGDevicesOnce(t *testing.T) {
	testutils.RequireLinux(t)

	tmpDir, cleanup := testutils.CreateTmpDir(t)
	defer cleanup()
	socketPath := tmpDir + "/kubelet.sock"

	kubelet := &migSnapshotServer{}
	kubelet.setDeviceIDs("MIG-good", "MIG-bad")
	server := grpc.NewServer()
	podresourcesapi.RegisterPodResourcesListerServer(server, kubelet)
	cleanupServer := testutils.StartMockServer(t, server, socketPath)
	defer cleanupServer()

	ctrl := gomock.NewController(t)
	mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
	previousNVML := nvmlprovider.Client()
	t.Cleanup(func() { nvmlprovider.SetClient(previousNVML) })
	nvmlprovider.SetClient(mockNVML)

	mockNVML.EXPECT().GetMIGDeviceInfoByID("MIG-good").
		Return(&nvmlprovider.MIGDeviceInfo{ParentUUID: "GPU-0", GPUInstanceID: 1}, nil).
		Times(2)
	mockNVML.EXPECT().GetMIGDeviceInfoByID("MIG-bad").
		Return(nil, errors.New("NVML unavailable")).
		Times(1)

	podMapper := NewPodMapper(&appconfig.Config{
		KubernetesGPUIdType:       appconfig.GPUUID,
		PodResourcesKubeletSocket: socketPath,
		CollectInterval:           int(time.Hour.Milliseconds()),
	})
	defer podMapper.Stop()

	scrape := func() {
		require.NoError(t, podMapper.Process(collector.MetricsByCounter{}, &deviceinfo.Info{}))
	}

	for range 3 {
		scrape()
	}

	kubelet.setDeviceIDs("MIG-bad")
	scrape()

	kubelet.setDeviceIDs("MIG-good", "MIG-bad")
	scrape()
}
