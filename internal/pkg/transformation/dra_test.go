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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourcev1 "k8s.io/api/resource/v1"
	resourcev1beta1 "k8s.io/api/resource/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

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
		informer:        &testInformerForDRA{store: store},
		sliceAPIVersion: "v1",
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
		informer:        &testInformerForDRA{store: store},
		sliceAPIVersion: "v1",
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
		informer:        &testInformerForDRA{store: store},
		sliceAPIVersion: "v1",
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
		informer:        &testInformerForDRA{store: store},
		sliceAPIVersion: "v1",
	}

	uuid, migInfo := m.GetDeviceInfo("gpu-pool", "gpu0")
	assert.Empty(t, uuid, "expected no UUID when pool doesn't match")
	assert.Nil(t, migInfo, "expected no MIG info when pool doesn't match")
}

func stringPtr(s string) *string {
	return &s
}

// TestGetDeviceInfo_EmptyInformerStore_ReturnsEmpty verifies an empty informer store yields no mapping.
func TestGetDeviceInfo_EmptyInformerStore_ReturnsEmpty(t *testing.T) {
	v1Store := newDRAIndexer()

	m := &DRAResourceSliceManager{
		informer:        &testInformerForDRA{store: v1Store},
		sliceAPIVersion: "v1",
	}

	uuid, migInfo := m.GetDeviceInfo("gpu-pool", "gpu0")
	assert.Empty(t, uuid, "expected no UUID when informer store has no matching slices")
	assert.Nil(t, migInfo, "expected no MIG info for GPU device")
}

// TestGetDeviceInfo_V1SliceInStore resolves UUID from v1 ResourceSlice objects in the informer.
func TestGetDeviceInfo_V1SliceInStore(t *testing.T) {
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

	m := &DRAResourceSliceManager{
		informer:        &testInformerForDRA{store: v1Store},
		sliceAPIVersion: "v1",
	}

	uuid, migInfo := m.GetDeviceInfo("gpu-pool", "gpu0")
	require.NotEmpty(t, uuid, "expected UUID to be found from v1")
	assert.Equal(t, "GPU-UUID-V1", uuid)
	assert.Nil(t, migInfo, "expected no MIG info for GPU device")
}

func TestGetDeviceInfo_NilInformer_ReturnsEmpty(t *testing.T) {
	m := &DRAResourceSliceManager{}

	uuid, migInfo := m.GetDeviceInfo("gpu-pool", "gpu0")
	assert.Empty(t, uuid, "expected no UUID when informer is nil")
	assert.Nil(t, migInfo, "expected no MIG info when informer is nil")
}
