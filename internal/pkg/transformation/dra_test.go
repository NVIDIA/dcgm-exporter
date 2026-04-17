/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourcev1 "k8s.io/api/resource/v1"
	resourcev1beta1 "k8s.io/api/resource/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// testInformer is a simple test implementation of SharedIndexInformer
type testInformer struct {
	store cache.Store
}

func (t *testInformer) GetStore() cache.Store {
	return t.store
}

func (t *testInformer) GetIndexer() cache.Indexer {
	return t.store.(cache.Indexer)
}

// newDRAIndexer creates an Indexer with a poolName index matching the production
// informer configuration so tests can exercise GetDeviceInfo without relying on
// informer.AddIndexers.
func newDRAIndexer() cache.Indexer {
	return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		"poolName": func(obj interface{}) ([]string, error) {
			switch rs := obj.(type) {
			case *resourcev1.ResourceSlice:
				return []string{rs.Spec.Pool.Name}, nil
			case *resourcev1beta1.ResourceSlice:
				return []string{rs.Spec.Pool.Name}, nil
			default:
				return nil, nil
			}
		},
	})
}

func (t *testInformer) AddIndexers(indexers cache.Indexers) error {
	return nil
}

func (t *testInformer) GetController() cache.Controller {
	return nil
}

func (t *testInformer) LastSyncResourceVersion() string {
	return ""
}

func (t *testInformer) AddEventHandler(handler cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	return nil, nil
}

func (t *testInformer) AddEventHandlerWithResyncPeriod(handler cache.ResourceEventHandler, resyncPeriod time.Duration) (cache.ResourceEventHandlerRegistration, error) {
	return nil, nil
}

func (t *testInformer) AddEventHandlerWithOptions(handler cache.ResourceEventHandler, options cache.HandlerOptions) (cache.ResourceEventHandlerRegistration, error) {
	return nil, nil
}

func (t *testInformer) RemoveEventHandler(handle cache.ResourceEventHandlerRegistration) error {
	return nil
}

func (t *testInformer) IsStopped() bool {
	return false
}

func (t *testInformer) SetWatchErrorHandler(handler cache.WatchErrorHandler) error {
	return nil
}

func (t *testInformer) SetWatchErrorHandlerWithContext(handler cache.WatchErrorHandlerWithContext) error {
	return nil
}

func (t *testInformer) SetTransform(handler cache.TransformFunc) error {
	return nil
}

func (t *testInformer) HasSynced() bool {
	return true
}

func (t *testInformer) Run(stopCh <-chan struct{}) {
}

func (t *testInformer) RunWithContext(ctx context.Context) {
}

func TestGetDeviceInfo_GPUDevice(t *testing.T) {
	// Create a store with a ResourceSlice containing a GPU device
	store := newDRAIndexer()
	slice := &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-slice",
			Namespace: "default",
		},
		Spec: resourcev1.ResourceSliceSpec{
			Driver: DRAGPUDriverName,
			Pool: resourcev1.ResourcePool{
				Name: "gpu-pool",
			},
			Devices: []resourcev1.Device{
				{
					Name: "gpu0",
					Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
						"type": {StringValue: stringPtr("gpu")},
						"uuid": {StringValue: stringPtr("GPU-UUID-0")},
					},
				},
			},
		},
	}
	store.Add(slice)

	m := &DRAResourceSliceManager{
		v1Informer:          &testInformer{store: store},
		preferredAPIVersion: "v1",
	}

	uuid, migInfo := m.GetDeviceInfo("gpu-pool", "gpu0")
	require.NotEmpty(t, uuid, "expected UUID to be found")
	assert.Equal(t, "GPU-UUID-0", uuid)
	assert.Nil(t, migInfo, "expected no MIG info for GPU device")
}

func TestGetDeviceInfo_MIGDevice(t *testing.T) {
	// Create a store with a ResourceSlice containing a MIG device
	store := newDRAIndexer()
	slice := &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-slice",
			Namespace: "default",
		},
		Spec: resourcev1.ResourceSliceSpec{
			Driver: DRAGPUDriverName,
			Pool: resourcev1.ResourcePool{
				Name: "gpu-pool",
			},
			Devices: []resourcev1.Device{
				{
					Name: "mig0",
					Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
						"type":       {StringValue: stringPtr("mig")},
						"uuid":       {StringValue: stringPtr("MIG-UUID-0")},
						"profile":    {StringValue: stringPtr("1g.10gb")},
						"parentUUID": {StringValue: stringPtr("GPU-UUID-0")},
					},
				},
			},
		},
	}
	store.Add(slice)

	m := &DRAResourceSliceManager{
		v1Informer:          &testInformer{store: store},
		preferredAPIVersion: "v1",
	}

	parentUUID, migInfo := m.GetDeviceInfo("gpu-pool", "mig0")
	require.NotEmpty(t, parentUUID, "expected parent UUID to be found")
	assert.Equal(t, "GPU-UUID-0", parentUUID)
	require.NotNil(t, migInfo, "expected MIG info to be present")
	assert.Equal(t, "MIG-UUID-0", migInfo.MIGDeviceUUID)
	assert.Equal(t, "1g.10gb", migInfo.Profile)
	assert.Equal(t, "GPU-UUID-0", migInfo.ParentUUID)
}

func TestGetDeviceInfo_NotFound(t *testing.T) {
	// Create an empty store
	store := newDRAIndexer()

	m := &DRAResourceSliceManager{
		v1Informer:          &testInformer{store: store},
		preferredAPIVersion: "v1",
	}

	uuid, migInfo := m.GetDeviceInfo("gpu-pool", "gpu0")
	assert.Empty(t, uuid, "expected no UUID for non-existent device")
	assert.Nil(t, migInfo, "expected no MIG info for non-existent device")
}

func TestGetDeviceInfo_WrongPool(t *testing.T) {
	// Create a store with a ResourceSlice in a different pool
	store := newDRAIndexer()
	slice := &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-slice",
			Namespace: "default",
		},
		Spec: resourcev1.ResourceSliceSpec{
			Driver: DRAGPUDriverName,
			Pool: resourcev1.ResourcePool{
				Name: "other-pool",
			},
			Devices: []resourcev1.Device{
				{
					Name: "gpu0",
					Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
						"type": {StringValue: stringPtr("gpu")},
						"uuid": {StringValue: stringPtr("GPU-UUID-0")},
					},
				},
			},
		},
	}
	store.Add(slice)

	m := &DRAResourceSliceManager{
		v1Informer:          &testInformer{store: store},
		preferredAPIVersion: "v1",
	}

	uuid, migInfo := m.GetDeviceInfo("gpu-pool", "gpu0")
	assert.Empty(t, uuid, "expected no UUID when pool doesn't match")
	assert.Nil(t, migInfo, "expected no MIG info when pool doesn't match")
}

func stringPtr(s string) *string {
	return &s
}

// TestVersionSelection_V1Preferred_NoSlices_ReturnEmpty tests that when v1 is preferred,
// we do not fall back to v1beta1.
func TestVersionSelection_V1Preferred_NoSlices_ReturnEmpty(t *testing.T) {
	// Create empty v1 store (v1 served but no GPU slices)
	v1Store := newDRAIndexer()

	// Create v1beta1 store with GPU slices
	v1beta1Store := newDRAIndexer()
	v1beta1Slice := &resourcev1beta1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "v1beta1-slice",
			Namespace: "default",
		},
		Spec: resourcev1beta1.ResourceSliceSpec{
			Driver: DRAGPUDriverName,
			Pool: resourcev1beta1.ResourcePool{
				Name: "gpu-pool",
			},
			Devices: []resourcev1beta1.Device{
				{
					Name: "gpu0",
					Basic: &resourcev1beta1.BasicDevice{
						Attributes: map[resourcev1beta1.QualifiedName]resourcev1beta1.DeviceAttribute{
							"type": {StringValue: stringPtr("gpu")},
							"uuid": {StringValue: stringPtr("GPU-UUID-0")},
						},
					},
				},
			},
		},
	}
	v1beta1Store.Add(v1beta1Slice)

	m := &DRAResourceSliceManager{
		v1Informer:          &testInformer{store: v1Store},
		v1beta1Informer:     &testInformer{store: v1beta1Store},
		preferredAPIVersion: "v1",
	}

	// GetDeviceInfo should not fall back to v1beta1 when v1 is preferred
	uuid, migInfo := m.GetDeviceInfo("gpu-pool", "gpu0")
	assert.Empty(t, uuid, "expected no UUID when preferred version has no slices")
	assert.Nil(t, migInfo, "expected no MIG info for GPU device")
}

// TestVersionSelection_BothServedAndBothHaveObjects_PreferV1 tests that when both
// v1 and v1beta1 are served and both have objects, we prefer v1.
func TestVersionSelection_BothServedAndBothHaveObjects_PreferV1(t *testing.T) {
	// Create v1 store with GPU slices
	v1Store := newDRAIndexer()
	v1Slice := &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "v1-slice",
			Namespace: "default",
		},
		Spec: resourcev1.ResourceSliceSpec{
			Driver: DRAGPUDriverName,
			Pool: resourcev1.ResourcePool{
				Name: "gpu-pool",
			},
			Devices: []resourcev1.Device{
				{
					Name: "gpu0",
					Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
						"type": {StringValue: stringPtr("gpu")},
						"uuid": {StringValue: stringPtr("GPU-UUID-V1")},
					},
				},
			},
		},
	}
	v1Store.Add(v1Slice)

	// Create v1beta1 store with GPU slices
	v1beta1Store := newDRAIndexer()
	v1beta1Slice := &resourcev1beta1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "v1beta1-slice",
			Namespace: "default",
		},
		Spec: resourcev1beta1.ResourceSliceSpec{
			Driver: DRAGPUDriverName,
			Pool: resourcev1beta1.ResourcePool{
				Name: "gpu-pool",
			},
			Devices: []resourcev1beta1.Device{
				{
					Name: "gpu0",
					Basic: &resourcev1beta1.BasicDevice{
						Attributes: map[resourcev1beta1.QualifiedName]resourcev1beta1.DeviceAttribute{
							"type": {StringValue: stringPtr("gpu")},
							"uuid": {StringValue: stringPtr("GPU-UUID-V1BETA1")},
						},
					},
				},
			},
		},
	}
	v1beta1Store.Add(v1beta1Slice)

	m := &DRAResourceSliceManager{
		v1Informer:          &testInformer{store: v1Store},
		v1beta1Informer:     &testInformer{store: v1beta1Store},
		preferredAPIVersion: "v1",
	}

	// GetDeviceInfo should prefer v1 since both have slices
	uuid, migInfo := m.GetDeviceInfo("gpu-pool", "gpu0")
	require.NotEmpty(t, uuid, "expected UUID to be found from v1")
	assert.Equal(t, "GPU-UUID-V1", uuid, "should prefer v1 when both have slices")
	assert.Nil(t, migInfo, "expected no MIG info for GPU device")
}

func TestGetDeviceInfo_InvalidPreferredVersion_ReturnsEmpty(t *testing.T) {
	v1Store := newDRAIndexer()
	v1Slice := &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "v1-slice",
			Namespace: "default",
		},
		Spec: resourcev1.ResourceSliceSpec{
			Driver: DRAGPUDriverName,
			Pool: resourcev1.ResourcePool{
				Name: "gpu-pool",
			},
			Devices: []resourcev1.Device{
				{
					Name: "gpu0",
					Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
						"type": {StringValue: stringPtr("gpu")},
						"uuid": {StringValue: stringPtr("GPU-UUID-V1")},
					},
				},
			},
		},
	}
	v1Store.Add(v1Slice)

	v1beta1Store := newDRAIndexer()
	v1beta1Slice := &resourcev1beta1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "v1beta1-slice",
			Namespace: "default",
		},
		Spec: resourcev1beta1.ResourceSliceSpec{
			Driver: DRAGPUDriverName,
			Pool: resourcev1beta1.ResourcePool{
				Name: "gpu-pool",
			},
			Devices: []resourcev1beta1.Device{
				{
					Name: "gpu0",
					Basic: &resourcev1beta1.BasicDevice{
						Attributes: map[resourcev1beta1.QualifiedName]resourcev1beta1.DeviceAttribute{
							"type": {StringValue: stringPtr("gpu")},
							"uuid": {StringValue: stringPtr("GPU-UUID-V1BETA1")},
						},
					},
				},
			},
		},
	}
	v1beta1Store.Add(v1beta1Slice)

	m := &DRAResourceSliceManager{
		v1Informer:          &testInformer{store: v1Store},
		v1beta1Informer:     &testInformer{store: v1beta1Store},
		preferredAPIVersion: "invalid",
	}

	uuid, migInfo := m.GetDeviceInfo("gpu-pool", "gpu0")
	assert.Empty(t, uuid, "expected no UUID when preferred version is invalid")
	assert.Nil(t, migInfo, "expected no MIG info when preferred version is invalid")
}
