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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourcev1 "k8s.io/api/resource/v1"
	resourcev1beta1 "k8s.io/api/resource/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

var resourceSlicesAPIResource = metav1.APIResource{
	Name:       "resourceslices",
	Namespaced: false,
	Kind:       "ResourceSlice",
}

type testDRADeviceMapping struct {
	uuid string
	mig  *DRAMigDeviceInfo
}

func newTestDRAManager() *DRAResourceSliceManager {
	return newTestDRAManagerWithDevices(nil)
}

func newTestDRAManagerWithDevices(devices map[string]testDRADeviceMapping) *DRAResourceSliceManager {
	return &DRAResourceSliceManager{
		lookup: func(pool, device string) (string, *DRAMigDeviceInfo) {
			mapping, ok := devices[draDeviceKey(pool, device)]
			if !ok {
				return "", nil
			}
			return mapping.uuid, mapping.mig
		},
	}
}

func strAttr(value string) resourcev1beta1.DeviceAttribute {
	return resourcev1beta1.DeviceAttribute{StringValue: &value}
}

func strAttrV1(value string) resourcev1.DeviceAttribute {
	return resourcev1.DeviceAttribute{StringValue: &value}
}

func testResourceSlice(driver, pool string, devices ...resourcev1beta1.Device) *resourcev1beta1.ResourceSlice {
	return &resourcev1beta1.ResourceSlice{
		Spec: resourcev1beta1.ResourceSliceSpec{
			Driver:  driver,
			Pool:    resourcev1beta1.ResourcePool{Name: pool},
			Devices: devices,
		},
	}
}

func testResourceSliceWithName(name, driver, pool string, devices ...resourcev1beta1.Device) *resourcev1beta1.ResourceSlice {
	slice := testResourceSlice(driver, pool, devices...)
	slice.ObjectMeta = metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name)}
	return slice
}

func testResourceSliceV1WithName(name, driver, pool string, generation int64, devices ...resourcev1.Device) *resourcev1.ResourceSlice {
	return &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name)},
		Spec: resourcev1.ResourceSliceSpec{
			Driver:  driver,
			Pool:    resourcev1.ResourcePool{Name: pool, Generation: generation, ResourceSliceCount: 1},
			Devices: devices,
		},
	}
}

func testResourceSliceV1beta1WithName(name, driver, pool string, generation int64, devices ...resourcev1beta1.Device) *resourcev1beta1.ResourceSlice {
	slice := testResourceSliceWithName(name, driver, pool, devices...)
	slice.Spec.Pool.Generation = generation
	slice.Spec.Pool.ResourceSliceCount = 1
	return slice
}

func testGPUDevice(name, uuid string) resourcev1beta1.Device {
	return resourcev1beta1.Device{
		Name: name,
		Basic: &resourcev1beta1.BasicDevice{Attributes: map[resourcev1beta1.QualifiedName]resourcev1beta1.DeviceAttribute{
			"type": strAttr("gpu"),
			"uuid": strAttr(uuid),
		}},
	}
}

func testGPUDeviceV1(name, uuid string) resourcev1.Device {
	return resourcev1.Device{
		Name: name,
		Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			"type": strAttrV1("gpu"),
			"uuid": strAttrV1(uuid),
		},
	}
}

func testMigDevice(name, uuid, parentUUID, profile string) resourcev1beta1.Device {
	return resourcev1beta1.Device{
		Name: name,
		Basic: &resourcev1beta1.BasicDevice{Attributes: map[resourcev1beta1.QualifiedName]resourcev1beta1.DeviceAttribute{
			"type":       strAttr("mig"),
			"uuid":       strAttr(uuid),
			"parentUUID": strAttr(parentUUID),
			"profile":    strAttr(profile),
		}},
	}
}

func testMigDeviceV1(name, uuid, parentUUID, profile string) resourcev1.Device {
	return resourcev1.Device{
		Name: name,
		Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			"type":       strAttrV1("mig"),
			"uuid":       strAttrV1(uuid),
			"parentUUID": strAttrV1(parentUUID),
			"profile":    strAttrV1(profile),
		},
	}
}

func newV1Indexer(t *testing.T, slices ...*resourcev1.ResourceSlice) cache.Indexer {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		resourceSliceIndex:     indexV1ResourceSliceByDevice,
		resourceSlicePoolIndex: indexV1ResourceSliceByPool,
	})
	for _, slice := range slices {
		require.NoError(t, indexer.Add(slice))
	}
	return indexer
}

func newV1beta1Indexer(t *testing.T, slices ...*resourcev1beta1.ResourceSlice) cache.Indexer {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		resourceSliceIndex:     indexV1beta1ResourceSliceByDevice,
		resourceSlicePoolIndex: indexV1beta1ResourceSliceByPool,
	})
	for _, slice := range slices {
		require.NoError(t, indexer.Add(slice))
	}
	return indexer
}

func fakeClientWithResources(resources ...*metav1.APIResourceList) *fake.Clientset {
	return fakeClientWithResourcesAndObjects(resources)
}

func fakeClientWithResourcesAndObjects(resources []*metav1.APIResourceList, objects ...runtime.Object) *fake.Clientset {
	client := fake.NewSimpleClientset(objects...)
	client.Resources = resources
	return client
}

func assertResourceSliceListUsesDriverFieldSelector(t *testing.T, client *fake.Clientset) {
	t.Helper()
	expectedSelector := fields.OneTermEqualSelector("spec.driver", DRAGPUDriverName).String()
	foundList := false

	for _, action := range client.Actions() {
		if action.GetVerb() != "list" || action.GetResource().Resource != "resourceslices" {
			continue
		}
		listAction, ok := action.(k8stesting.ListAction)
		require.True(t, ok, "resourceslices list action should expose list restrictions")
		assert.Equal(t, expectedSelector, listAction.GetListRestrictions().Fields.String())
		foundList = true
	}

	require.True(t, foundList, "expected ResourceSlice list action")
}

func TestDetectResourceSliceAPIVersion(t *testing.T) {
	v1GV := resourcev1.SchemeGroupVersion.String()
	v1beta1GV := resourcev1beta1.SchemeGroupVersion.String()

	tests := []struct {
		name      string
		resources []*metav1.APIResourceList
		want      resourceSliceAPIVersion
	}{
		{
			name: "v1-only",
			resources: []*metav1.APIResourceList{{
				GroupVersion: v1GV,
				APIResources: []metav1.APIResource{resourceSlicesAPIResource},
			}},
			want: resourceSliceAPIV1,
		},
		{
			name: "v1beta1-only",
			resources: []*metav1.APIResourceList{{
				GroupVersion: v1beta1GV,
				APIResources: []metav1.APIResource{resourceSlicesAPIResource},
			}},
			want: resourceSliceAPIV1beta1,
		},
		{
			name: "both-served-prefers-v1",
			resources: []*metav1.APIResourceList{
				{GroupVersion: v1GV, APIResources: []metav1.APIResource{resourceSlicesAPIResource}},
				{GroupVersion: v1beta1GV, APIResources: []metav1.APIResource{resourceSlicesAPIResource}},
			},
			want: resourceSliceAPIV1,
		},
		{
			name:      "neither-served",
			resources: nil,
			want:      resourceSliceAPIUnknown,
		},
		{
			name: "v1-group-without-resourceslices-falls-back-to-v1beta1",
			resources: []*metav1.APIResourceList{
				{
					GroupVersion: v1GV,
					APIResources: []metav1.APIResource{{Name: "deviceclasses", Namespaced: false, Kind: "DeviceClass"}},
				},
				{GroupVersion: v1beta1GV, APIResources: []metav1.APIResource{resourceSlicesAPIResource}},
			},
			want: resourceSliceAPIV1beta1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectResourceSliceAPIVersion(fakeClientWithResources(tc.resources...))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDRAResourceSliceManager_GetDeviceInfo_NilLookup(t *testing.T) {
	manager := &DRAResourceSliceManager{}
	uuid, mig := manager.GetDeviceInfo("pool-a", "gpu-0")
	assert.Empty(t, uuid)
	assert.Nil(t, mig)
}

func TestNewDRAResourceSliceManager_UsesInformerCacheLookup(t *testing.T) {
	prevGetKubeClient := getKubeClientFunc
	t.Cleanup(func() { getKubeClientFunc = prevGetKubeClient })

	v1Resources := &metav1.APIResourceList{
		GroupVersion: resourcev1.SchemeGroupVersion.String(),
		APIResources: []metav1.APIResource{resourceSlicesAPIResource},
	}
	v1beta1Resources := &metav1.APIResourceList{
		GroupVersion: resourcev1beta1.SchemeGroupVersion.String(),
		APIResources: []metav1.APIResource{resourceSlicesAPIResource},
	}

	tests := []struct {
		name       string
		resources  []*metav1.APIResourceList
		objects    []runtime.Object
		gpuUUID    string
		migUUID    string
		migProfile string
	}{
		{
			name:      "v1 informer cache lookup",
			resources: []*metav1.APIResourceList{v1Resources},
			objects: []runtime.Object{
				testResourceSliceV1WithName(
					"slice-v1",
					DRAGPUDriverName,
					"pool-a",
					1,
					testGPUDeviceV1("gpu-0", "GPU-v1"),
					testMigDeviceV1("mig-0", "MIG-v1", "GPU-v1", "1g.10gb"),
				),
			},
			gpuUUID:    "GPU-v1",
			migUUID:    "MIG-v1",
			migProfile: "1g.10gb",
		},
		{
			name:      "v1beta1 informer cache lookup",
			resources: []*metav1.APIResourceList{v1beta1Resources},
			objects: []runtime.Object{
				testResourceSliceV1beta1WithName(
					"slice-v1beta1",
					DRAGPUDriverName,
					"pool-a",
					1,
					testGPUDevice("gpu-0", "GPU-v1beta1"),
					testMigDevice("mig-0", "MIG-v1beta1", "GPU-v1beta1", "2g.20gb"),
				),
			},
			gpuUUID:    "GPU-v1beta1",
			migUUID:    "MIG-v1beta1",
			migProfile: "2g.20gb",
		},
		{
			name:      "both APIs served prefers v1 informer cache",
			resources: []*metav1.APIResourceList{v1Resources, v1beta1Resources},
			objects: []runtime.Object{
				testResourceSliceV1WithName(
					"slice-v1",
					DRAGPUDriverName,
					"pool-a",
					1,
					testGPUDeviceV1("gpu-0", "GPU-v1"),
					testMigDeviceV1("mig-0", "MIG-v1", "GPU-v1", "1g.10gb"),
				),
				testResourceSliceV1beta1WithName(
					"slice-v1beta1",
					DRAGPUDriverName,
					"pool-a",
					1,
					testGPUDevice("gpu-0", "GPU-v1beta1"),
					testMigDevice("mig-0", "MIG-v1beta1", "GPU-v1beta1", "2g.20gb"),
				),
			},
			gpuUUID:    "GPU-v1",
			migUUID:    "MIG-v1",
			migProfile: "1g.10gb",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := fakeClientWithResourcesAndObjects(tc.resources, tc.objects...)
			getKubeClientFunc = func() (kubernetes.Interface, error) { return client, nil }
			t.Cleanup(func() { getKubeClientFunc = prevGetKubeClient })

			manager, err := NewDRAResourceSliceManager()
			require.NoError(t, err)
			t.Cleanup(manager.Stop)

			uuid, mig := manager.GetDeviceInfo("pool-a", "gpu-0")
			assert.Equal(t, tc.gpuUUID, uuid)
			assert.Nil(t, mig)

			uuid, mig = manager.GetDeviceInfo("pool-a", "mig-0")
			assert.Equal(t, tc.gpuUUID, uuid)
			if assert.NotNil(t, mig) {
				assert.Equal(t, tc.migUUID, mig.MIGDeviceUUID)
				assert.Equal(t, tc.migProfile, mig.Profile)
				assert.Equal(t, tc.gpuUUID, mig.ParentUUID)
			}

			assertResourceSliceListUsesDriverFieldSelector(t, client)
		})
	}
}

func TestBuildDeviceMapping(t *testing.T) {
	tests := []struct {
		name        string
		deviceType  string
		uuid        string
		parentUUID  string
		profile     string
		wantUUID    string
		wantMIGInfo *DRAMigDeviceInfo
	}{
		{
			name:       "full-gpu",
			deviceType: "gpu",
			uuid:       "GPU-abcd",
			wantUUID:   "GPU-abcd",
		},
		{
			name:       "mig-with-parent",
			deviceType: "mig",
			uuid:       "MIG-1234",
			parentUUID: "GPU-parent",
			profile:    "1g.6gb",
			wantUUID:   "GPU-parent",
			wantMIGInfo: &DRAMigDeviceInfo{
				MIGDeviceUUID: "MIG-1234",
				Profile:       "1g.6gb",
				ParentUUID:    "GPU-parent",
			},
		},
		{
			name:       "mig-missing-parent",
			deviceType: "mig",
			uuid:       "MIG-orphan",
		},
		{
			name:       "unknown-type",
			deviceType: "tpu",
			uuid:       "TPU-xyz",
		},
		{
			name: "empty-type",
			uuid: "GPU-abcd",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotUUID, gotMIG := buildDeviceMapping(tc.deviceType, tc.uuid, tc.parentUUID, tc.profile)
			assert.Equal(t, tc.wantUUID, gotUUID)
			assert.Equal(t, tc.wantMIGInfo, gotMIG)
		})
	}
}

func TestMakeV1Lookup(t *testing.T) {
	indexer := newV1Indexer(
		t,
		testResourceSliceV1WithName(
			"slice-gpu",
			DRAGPUDriverName,
			"node-a",
			1,
			testGPUDeviceV1("gpu-0", "GPU-aaaa"),
			testMigDeviceV1("gpu-1-mig-0", "MIG-bbbb", "GPU-aaaa", "1g.6gb"),
			resourcev1.Device{Name: "gpu-no-attrs"},
		),
		testResourceSliceV1WithName(
			"slice-other-driver",
			"other.example.com",
			"node-a",
			1,
			testGPUDeviceV1("non-nvidia-device", "GPU-other"),
		),
	)
	lookup := makeV1Lookup(indexer)

	tests := []struct {
		name        string
		pool        string
		device      string
		wantUUID    string
		wantMIGInfo *DRAMigDeviceInfo
	}{
		{
			name:     "gpu-device-resolves-to-uuid",
			pool:     "node-a",
			device:   "gpu-0",
			wantUUID: "GPU-aaaa",
		},
		{
			name:     "mig-device-resolves-to-parent-uuid-with-mig-info",
			pool:     "node-a",
			device:   "gpu-1-mig-0",
			wantUUID: "GPU-aaaa",
			wantMIGInfo: &DRAMigDeviceInfo{
				MIGDeviceUUID: "MIG-bbbb",
				Profile:       "1g.6gb",
				ParentUUID:    "GPU-aaaa",
			},
		},
		{
			name:   "different-driver-not-matched",
			pool:   "node-a",
			device: "non-nvidia-device",
		},
		{
			name:   "unknown-pool",
			pool:   "node-z",
			device: "gpu-0",
		},
		{
			name:   "unknown-device",
			pool:   "node-a",
			device: "gpu-99",
		},
		{
			name:   "device-without-attributes",
			pool:   "node-a",
			device: "gpu-no-attrs",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotUUID, gotMIG := lookup(tc.pool, tc.device)
			assert.Equal(t, tc.wantUUID, gotUUID)
			assert.Equal(t, tc.wantMIGInfo, gotMIG)
		})
	}
}

func TestMakeV1beta1Lookup(t *testing.T) {
	indexer := newV1beta1Indexer(
		t,
		testResourceSliceV1beta1WithName(
			"slice-gpu",
			DRAGPUDriverName,
			"node-a",
			1,
			testGPUDevice("gpu-0", "GPU-aaaa"),
			testMigDevice("gpu-1-mig-0", "MIG-bbbb", "GPU-aaaa", "1g.6gb"),
			resourcev1beta1.Device{Name: "gpu-no-basic"},
		),
	)
	lookup := makeV1beta1Lookup(indexer)

	tests := []struct {
		name        string
		pool        string
		device      string
		wantUUID    string
		wantMIGInfo *DRAMigDeviceInfo
	}{
		{
			name:     "gpu-device-resolves-to-uuid",
			pool:     "node-a",
			device:   "gpu-0",
			wantUUID: "GPU-aaaa",
		},
		{
			name:     "mig-device-resolves-to-parent-uuid-with-mig-info",
			pool:     "node-a",
			device:   "gpu-1-mig-0",
			wantUUID: "GPU-aaaa",
			wantMIGInfo: &DRAMigDeviceInfo{
				MIGDeviceUUID: "MIG-bbbb",
				Profile:       "1g.6gb",
				ParentUUID:    "GPU-aaaa",
			},
		},
		{
			name:   "unknown-device",
			pool:   "node-a",
			device: "gpu-99",
		},
		{
			name:   "device-without-basic",
			pool:   "node-a",
			device: "gpu-no-basic",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotUUID, gotMIG := lookup(tc.pool, tc.device)
			assert.Equal(t, tc.wantUUID, gotUUID)
			assert.Equal(t, tc.wantMIGInfo, gotMIG)
		})
	}
}

func TestDRAResourceSliceIndexer_UpdateRemovesStaleMappings(t *testing.T) {
	indexer := newV1beta1Indexer(
		t,
		testResourceSliceV1beta1WithName(
			"slice-a",
			DRAGPUDriverName,
			"pool-a",
			1,
			testGPUDevice("gpu-0", "GPU-0"),
			testGPUDevice("gpu-1", "GPU-1"),
		),
	)
	lookup := makeV1beta1Lookup(indexer)

	uuid, mig := lookup("pool-a", "gpu-0")
	require.Equal(t, "GPU-0", uuid)
	require.Nil(t, mig)

	require.NoError(t, indexer.Update(testResourceSliceV1beta1WithName(
		"slice-a",
		DRAGPUDriverName,
		"pool-a",
		2,
		testGPUDevice("gpu-1", "GPU-1"),
	)))

	uuid, mig = lookup("pool-a", "gpu-0")
	assert.Empty(t, uuid)
	assert.Nil(t, mig)
	uuid, mig = lookup("pool-a", "gpu-1")
	assert.Equal(t, "GPU-1", uuid)
	assert.Nil(t, mig)
}

func TestDRAResourceSliceIndexer_V1UpdateRemovesStaleMappings(t *testing.T) {
	indexer := newV1Indexer(
		t,
		testResourceSliceV1WithName(
			"slice-a",
			DRAGPUDriverName,
			"pool-a",
			1,
			testGPUDeviceV1("gpu-0", "GPU-0"),
			testGPUDeviceV1("gpu-1", "GPU-1"),
		),
	)
	lookup := makeV1Lookup(indexer)

	uuid, mig := lookup("pool-a", "gpu-0")
	require.Equal(t, "GPU-0", uuid)
	require.Nil(t, mig)

	require.NoError(t, indexer.Update(testResourceSliceV1WithName(
		"slice-a",
		DRAGPUDriverName,
		"pool-a",
		2,
		testGPUDeviceV1("gpu-1", "GPU-1"),
	)))

	uuid, mig = lookup("pool-a", "gpu-0")
	assert.Empty(t, uuid)
	assert.Nil(t, mig)
	uuid, mig = lookup("pool-a", "gpu-1")
	assert.Equal(t, "GPU-1", uuid)
	assert.Nil(t, mig)
}

func TestDRAResourceSliceIndexer_UpdateHandlesDeviceTypeChanges(t *testing.T) {
	indexer := newV1beta1Indexer(
		t,
		testResourceSliceV1beta1WithName(
			"slice-a",
			DRAGPUDriverName,
			"pool-a",
			1,
			testGPUDevice("device-0", "GPU-0"),
		),
	)
	lookup := makeV1beta1Lookup(indexer)

	uuid, mig := lookup("pool-a", "device-0")
	require.Equal(t, "GPU-0", uuid)
	require.Nil(t, mig)

	require.NoError(t, indexer.Update(testResourceSliceV1beta1WithName(
		"slice-a",
		DRAGPUDriverName,
		"pool-a",
		2,
		testMigDevice("device-0", "MIG-0", "GPU-0", "1g.10gb"),
	)))

	uuid, mig = lookup("pool-a", "device-0")
	assert.Equal(t, "GPU-0", uuid)
	if assert.NotNil(t, mig) {
		assert.Equal(t, "MIG-0", mig.MIGDeviceUUID)
	}
}

func TestDRAResourceSliceIndexer_DeletePreservesOtherSliceMapping(t *testing.T) {
	sliceA := testResourceSliceV1beta1WithName(
		"slice-a",
		DRAGPUDriverName,
		"pool-a",
		1,
		testGPUDevice("gpu-0", "GPU-0"),
	)
	sliceB := testResourceSliceV1beta1WithName(
		"slice-b",
		DRAGPUDriverName,
		"pool-a",
		1,
		testGPUDevice("gpu-0", "GPU-0"),
	)
	indexer := newV1beta1Indexer(t, sliceA, sliceB)
	lookup := makeV1beta1Lookup(indexer)

	require.NoError(t, indexer.Delete(sliceA))

	uuid, mig := lookup("pool-a", "gpu-0")
	assert.Equal(t, "GPU-0", uuid)
	assert.Nil(t, mig)
}

func TestDRAResourceSliceIndexer_PrefersNewestPoolGeneration(t *testing.T) {
	indexer := newV1beta1Indexer(
		t,
		testResourceSliceV1beta1WithName(
			"slice-old",
			DRAGPUDriverName,
			"pool-a",
			1,
			testGPUDevice("gpu-0", "GPU-old"),
		),
		testResourceSliceV1beta1WithName(
			"slice-new",
			DRAGPUDriverName,
			"pool-a",
			2,
			testGPUDevice("gpu-0", "GPU-new"),
		),
	)
	lookup := makeV1beta1Lookup(indexer)

	uuid, mig := lookup("pool-a", "gpu-0")
	assert.Equal(t, "GPU-new", uuid)
	assert.Nil(t, mig)
}

func TestDRAResourceSliceIndexer_V1PrefersNewestPoolGeneration(t *testing.T) {
	indexer := newV1Indexer(
		t,
		testResourceSliceV1WithName(
			"slice-old",
			DRAGPUDriverName,
			"pool-a",
			1,
			testGPUDeviceV1("gpu-0", "GPU-old"),
		),
		testResourceSliceV1WithName(
			"slice-new",
			DRAGPUDriverName,
			"pool-a",
			2,
			testGPUDeviceV1("gpu-0", "GPU-new"),
		),
	)
	lookup := makeV1Lookup(indexer)

	uuid, mig := lookup("pool-a", "gpu-0")
	assert.Equal(t, "GPU-new", uuid)
	assert.Nil(t, mig)
}

func TestDRAResourceSliceIndexer_PartialLatestGenerationReturnsEmpty(t *testing.T) {
	old1 := testResourceSliceV1beta1WithName(
		"slice-old-1",
		DRAGPUDriverName,
		"pool-a",
		1,
		testGPUDevice("gpu-0", "GPU-old-0"),
	)
	old1.Spec.Pool.ResourceSliceCount = 2
	old2 := testResourceSliceV1beta1WithName(
		"slice-old-2",
		DRAGPUDriverName,
		"pool-a",
		1,
		testGPUDevice("gpu-1", "GPU-old-1"),
	)
	old2.Spec.Pool.ResourceSliceCount = 2
	new1 := testResourceSliceV1beta1WithName(
		"slice-new-1",
		DRAGPUDriverName,
		"pool-a",
		2,
		testGPUDevice("gpu-0", "GPU-new-0"),
	)
	new1.Spec.Pool.ResourceSliceCount = 2

	indexer := newV1beta1Indexer(t, old1, old2, new1)
	lookup := makeV1beta1Lookup(indexer)

	uuid, mig := lookup("pool-a", "gpu-0")
	assert.Empty(t, uuid)
	assert.Nil(t, mig)
	uuid, mig = lookup("pool-a", "gpu-1")
	assert.Empty(t, uuid)
	assert.Nil(t, mig)
}

func TestDRAResourceSliceIndexer_V1PartialLatestGenerationReturnsEmpty(t *testing.T) {
	old1 := testResourceSliceV1WithName(
		"slice-old-1",
		DRAGPUDriverName,
		"pool-a",
		1,
		testGPUDeviceV1("gpu-0", "GPU-old-0"),
	)
	old1.Spec.Pool.ResourceSliceCount = 2
	old2 := testResourceSliceV1WithName(
		"slice-old-2",
		DRAGPUDriverName,
		"pool-a",
		1,
		testGPUDeviceV1("gpu-1", "GPU-old-1"),
	)
	old2.Spec.Pool.ResourceSliceCount = 2
	new1 := testResourceSliceV1WithName(
		"slice-new-1",
		DRAGPUDriverName,
		"pool-a",
		2,
		testGPUDeviceV1("gpu-0", "GPU-new-0"),
	)
	new1.Spec.Pool.ResourceSliceCount = 2

	indexer := newV1Indexer(t, old1, old2, new1)
	lookup := makeV1Lookup(indexer)

	uuid, mig := lookup("pool-a", "gpu-0")
	assert.Empty(t, uuid)
	assert.Nil(t, mig)
	uuid, mig = lookup("pool-a", "gpu-1")
	assert.Empty(t, uuid)
	assert.Nil(t, mig)
}

func TestDRAResourceSliceIndexer_IgnoresDeviceFromOlderPoolGeneration(t *testing.T) {
	indexer := newV1beta1Indexer(
		t,
		testResourceSliceV1beta1WithName(
			"slice-old",
			DRAGPUDriverName,
			"pool-a",
			1,
			testGPUDevice("gpu-0", "GPU-old"),
		),
		testResourceSliceV1beta1WithName(
			"slice-new",
			DRAGPUDriverName,
			"pool-a",
			2,
			testGPUDevice("gpu-1", "GPU-new"),
		),
	)
	lookup := makeV1beta1Lookup(indexer)

	uuid, mig := lookup("pool-a", "gpu-0")
	assert.Empty(t, uuid)
	assert.Nil(t, mig)
	uuid, mig = lookup("pool-a", "gpu-1")
	assert.Equal(t, "GPU-new", uuid)
	assert.Nil(t, mig)
}

func TestDRAResourceSliceIndexer_V1IgnoresDeviceFromOlderPoolGeneration(t *testing.T) {
	indexer := newV1Indexer(
		t,
		testResourceSliceV1WithName(
			"slice-old",
			DRAGPUDriverName,
			"pool-a",
			1,
			testGPUDeviceV1("gpu-0", "GPU-old"),
		),
		testResourceSliceV1WithName(
			"slice-new",
			DRAGPUDriverName,
			"pool-a",
			2,
			testGPUDeviceV1("gpu-1", "GPU-new"),
		),
	)
	lookup := makeV1Lookup(indexer)

	uuid, mig := lookup("pool-a", "gpu-0")
	assert.Empty(t, uuid)
	assert.Nil(t, mig)
	uuid, mig = lookup("pool-a", "gpu-1")
	assert.Equal(t, "GPU-new", uuid)
	assert.Nil(t, mig)
}

func TestDRAResourceSliceIndexer_SameGenerationDuplicateUsesDeterministicSliceKey(t *testing.T) {
	indexer := newV1beta1Indexer(
		t,
		testResourceSliceV1beta1WithName(
			"slice-a",
			DRAGPUDriverName,
			"pool-a",
			1,
			testGPUDevice("gpu-0", "GPU-a"),
		),
		testResourceSliceV1beta1WithName(
			"slice-b",
			DRAGPUDriverName,
			"pool-a",
			1,
			testGPUDevice("gpu-0", "GPU-b"),
		),
	)
	lookup := makeV1beta1Lookup(indexer)

	uuid, mig := lookup("pool-a", "gpu-0")
	assert.Equal(t, "GPU-b", uuid)
	assert.Nil(t, mig)
}

func TestDRAGetAttrString(t *testing.T) {
	assert.Equal(t, "gpu", getV1AttrString(map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
		"type": strAttrV1("gpu"),
	}, "type"))
	assert.Empty(t, getV1AttrString(map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
		"type": {},
	}, "type"))
	assert.Empty(t, getV1AttrString(nil, "missing"))

	assert.Equal(t, "gpu", getV1beta1AttrString(map[resourcev1beta1.QualifiedName]resourcev1beta1.DeviceAttribute{
		"type": strAttr("gpu"),
	}, "type"))
	assert.Empty(t, getV1beta1AttrString(map[resourcev1beta1.QualifiedName]resourcev1beta1.DeviceAttribute{
		"type": {},
	}, "type"))
	assert.Empty(t, getV1beta1AttrString(nil, "missing"))
}

func TestDRAResourceSliceManager_StopIsIdempotent(t *testing.T) {
	manager := newTestDRAManager()
	manager.Stop()
	manager.Stop()
}

func TestNewDRAResourceSliceManager(t *testing.T) {
	prevGetKubeClient := getKubeClientFunc
	prevWaitForCacheSync := waitForResourceSliceCacheSyncFunc
	prevShutdownFactory := shutdownResourceSliceFactoryFunc
	prevTimeout := resourceSliceCacheSyncTimeout
	t.Cleanup(func() {
		getKubeClientFunc = prevGetKubeClient
		waitForResourceSliceCacheSyncFunc = prevWaitForCacheSync
		shutdownResourceSliceFactoryFunc = prevShutdownFactory
		resourceSliceCacheSyncTimeout = prevTimeout
	})

	v1Resources := &metav1.APIResourceList{
		GroupVersion: resourcev1.SchemeGroupVersion.String(),
		APIResources: []metav1.APIResource{resourceSlicesAPIResource},
	}
	v1beta1Resources := &metav1.APIResourceList{
		GroupVersion: resourcev1beta1.SchemeGroupVersion.String(),
		APIResources: []metav1.APIResource{resourceSlicesAPIResource},
	}

	t.Run("kube client error", func(t *testing.T) {
		getKubeClientFunc = func() (kubernetes.Interface, error) {
			return nil, errors.New("no kube client")
		}
		t.Cleanup(func() { getKubeClientFunc = prevGetKubeClient })

		manager, err := NewDRAResourceSliceManager()

		assert.Nil(t, manager)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "error getting kube client")
	})

	t.Run("resourceslice API not served", func(t *testing.T) {
		getKubeClientFunc = func() (kubernetes.Interface, error) {
			return fakeClientWithResources(), nil
		}
		t.Cleanup(func() { getKubeClientFunc = prevGetKubeClient })

		manager, err := NewDRAResourceSliceManager()

		assert.Nil(t, manager)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ResourceSlice API not served")
	})

	t.Run("success starts v1 informer", func(t *testing.T) {
		getKubeClientFunc = func() (kubernetes.Interface, error) {
			return fakeClientWithResources(v1Resources), nil
		}
		t.Cleanup(func() { getKubeClientFunc = prevGetKubeClient })

		manager, err := NewDRAResourceSliceManager()

		require.NoError(t, err)
		require.NotNil(t, manager)
		assert.NotNil(t, manager.factory)
		assert.NotNil(t, manager.lookup)
		manager.Stop()
	})

	t.Run("success starts v1beta1 informer", func(t *testing.T) {
		getKubeClientFunc = func() (kubernetes.Interface, error) {
			return fakeClientWithResources(v1beta1Resources), nil
		}
		t.Cleanup(func() { getKubeClientFunc = prevGetKubeClient })

		manager, err := NewDRAResourceSliceManager()

		require.NoError(t, err)
		require.NotNil(t, manager)
		assert.NotNil(t, manager.factory)
		assert.NotNil(t, manager.lookup)
		manager.Stop()
	})

	t.Run("cache sync timeout returns error", func(t *testing.T) {
		getKubeClientFunc = func() (kubernetes.Interface, error) {
			return fakeClientWithResources(v1beta1Resources), nil
		}
		waitForResourceSliceCacheSyncFunc = func(stopCh <-chan struct{}, _ ...cache.InformerSynced) bool {
			<-stopCh
			return false
		}
		shutdownCalled := false
		shutdownResourceSliceFactoryFunc = func(factory informers.SharedInformerFactory) {
			shutdownCalled = true
			factory.Shutdown()
		}
		resourceSliceCacheSyncTimeout = time.Millisecond
		t.Cleanup(func() {
			getKubeClientFunc = prevGetKubeClient
			waitForResourceSliceCacheSyncFunc = prevWaitForCacheSync
			shutdownResourceSliceFactoryFunc = prevShutdownFactory
			resourceSliceCacheSyncTimeout = prevTimeout
		})

		manager, err := NewDRAResourceSliceManager()

		assert.Nil(t, manager)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ResourceSlice informer cache sync timed out")
		assert.True(t, shutdownCalled, "factory should be shut down on cache sync failure")
	})
}
