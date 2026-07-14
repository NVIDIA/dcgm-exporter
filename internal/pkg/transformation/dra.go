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
	"fmt"
	"log/slog"
	"time"

	resourcev1 "k8s.io/api/resource/v1"
	resourcev1beta1 "k8s.io/api/resource/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/kubeclient"
)

const (
	informerResyncPeriod   = 10 * time.Minute
	resourceSliceIndex     = "draResourceSliceDevice"
	resourceSlicePoolIndex = "draResourceSlicePool"
)

var (
	getKubeClientFunc                 = kubeclient.GetKubeClient
	waitForResourceSliceCacheSyncFunc = cache.WaitForCacheSync
	shutdownResourceSliceFactoryFunc  = func(factory informers.SharedInformerFactory) { factory.Shutdown() }
	resourceSliceCacheSyncTimeout     = 30 * time.Second
)

// resourceSliceAPIVersion identifies which ResourceSlice API version the cluster serves.
type resourceSliceAPIVersion int

const (
	resourceSliceAPIUnknown resourceSliceAPIVersion = iota
	resourceSliceAPIV1
	resourceSliceAPIV1beta1
)

func (v resourceSliceAPIVersion) String() string {
	switch v {
	case resourceSliceAPIV1:
		return resourcev1.SchemeGroupVersion.String()
	case resourceSliceAPIV1beta1:
		return resourcev1beta1.SchemeGroupVersion.String()
	default:
		return "unknown"
	}
}

// detectResourceSliceAPIVersion returns the ResourceSlice API version served by
// the cluster. It prefers resource.k8s.io/v1 and falls back to v1beta1.
func detectResourceSliceAPIVersion(client kubernetes.Interface) resourceSliceAPIVersion {
	apiVersions := []struct {
		groupVersion string
		version      resourceSliceAPIVersion
	}{
		{resourcev1.SchemeGroupVersion.String(), resourceSliceAPIV1},
		{resourcev1beta1.SchemeGroupVersion.String(), resourceSliceAPIV1beta1},
	}

	for _, apiVersion := range apiVersions {
		resources, err := client.Discovery().ServerResourcesForGroupVersion(apiVersion.groupVersion)
		if err != nil {
			slog.Debug("ResourceSlice discovery failed", "groupVersion", apiVersion.groupVersion, "error", err)
			continue
		}
		for _, resource := range resources.APIResources {
			if resource.Name == "resourceslices" {
				return apiVersion.version
			}
		}
	}

	return resourceSliceAPIUnknown
}

func NewDRAResourceSliceManager() (*DRAResourceSliceManager, error) {
	client, err := getKubeClientFunc()
	if err != nil {
		return nil, fmt.Errorf("error getting kube client: %w", err)
	}

	apiVersion := detectResourceSliceAPIVersion(client)
	if apiVersion == resourceSliceAPIUnknown {
		return nil, fmt.Errorf("ResourceSlice API not served by cluster (looked for %s and %s)",
			resourcev1.SchemeGroupVersion, resourcev1beta1.SchemeGroupVersion)
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		client,
		informerResyncPeriod,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fields.OneTermEqualSelector("spec.driver", DRAGPUDriverName).String()
		}),
	)

	m := &DRAResourceSliceManager{factory: factory}
	var hasSynced cache.InformerSynced

	switch apiVersion {
	case resourceSliceAPIV1:
		informer := factory.Resource().V1().ResourceSlices().Informer()
		if err := informer.AddIndexers(cache.Indexers{
			resourceSliceIndex:     indexV1ResourceSliceByDevice,
			resourceSlicePoolIndex: indexV1ResourceSliceByPool,
		}); err != nil {
			return nil, fmt.Errorf("error adding ResourceSlice indexer: %w", err)
		}

		m.lookup = makeV1Lookup(informer.GetIndexer())
		hasSynced = informer.HasSynced
	case resourceSliceAPIV1beta1:
		informer := factory.Resource().V1beta1().ResourceSlices().Informer()
		if err := informer.AddIndexers(cache.Indexers{
			resourceSliceIndex:     indexV1beta1ResourceSliceByDevice,
			resourceSlicePoolIndex: indexV1beta1ResourceSliceByPool,
		}); err != nil {
			return nil, fmt.Errorf("error adding ResourceSlice indexer: %w", err)
		}

		m.lookup = makeV1beta1Lookup(informer.GetIndexer())
		hasSynced = informer.HasSynced
	default:
		return nil, fmt.Errorf("unsupported ResourceSlice API version: %s", apiVersion)
	}

	slog.Info("Using ResourceSlice API", "groupVersion", apiVersion.String())

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelContext = cancel
	factory.Start(ctx.Done())

	syncCtx, syncCancel := context.WithTimeout(ctx, resourceSliceCacheSyncTimeout)
	defer syncCancel()
	if !waitForResourceSliceCacheSyncFunc(syncCtx.Done(), hasSynced) {
		cancel()
		shutdownResourceSliceFactoryFunc(factory)
		if syncCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("ResourceSlice informer cache sync timed out after %s (check RBAC: list/watch on resource.k8s.io/resourceslices)", resourceSliceCacheSyncTimeout)
		}
		return nil, fmt.Errorf("ResourceSlice informer cache sync failed")
	}

	return m, nil
}

func (m *DRAResourceSliceManager) Stop() {
	if m.cancelContext != nil {
		m.cancelContext()
	}
	if m.factory != nil {
		shutdownResourceSliceFactoryFunc(m.factory)
	}
}

// GetDeviceInfo returns the mapping UUID and MIG device info if applicable.
// For MIG devices: returns (parentUUID, *DRAMigDeviceInfo).
// For full GPUs: returns (deviceUUID, nil).
func (m *DRAResourceSliceManager) GetDeviceInfo(pool, device string) (string, *DRAMigDeviceInfo) {
	if m.lookup == nil {
		return "", nil
	}
	return m.lookup(pool, device)
}

// deviceLookupFunc resolves a DRA (pool, device) tuple from the informer cache.
type deviceLookupFunc func(pool, device string) (string, *DRAMigDeviceInfo)

func draDeviceKey(pool, device string) string {
	return pool + "/" + device
}

func draPoolIndexKey(driver, pool string) string {
	return driver + "/" + pool
}

func draDeviceIndexKey(driver, pool, device string) string {
	return draPoolIndexKey(driver, pool) + "/" + device
}

func indexV1ResourceSliceByDevice(obj any) ([]string, error) {
	slice, ok := obj.(*resourcev1.ResourceSlice)
	if !ok {
		return nil, fmt.Errorf("expected *resourcev1.ResourceSlice, got %T", obj)
	}
	if slice.Spec.Driver != DRAGPUDriverName {
		return nil, nil
	}

	keys := make([]string, 0, len(slice.Spec.Devices))
	for _, device := range slice.Spec.Devices {
		keys = append(keys, draDeviceIndexKey(slice.Spec.Driver, slice.Spec.Pool.Name, device.Name))
	}
	return keys, nil
}

func indexV1beta1ResourceSliceByDevice(obj any) ([]string, error) {
	slice, ok := obj.(*resourcev1beta1.ResourceSlice)
	if !ok {
		return nil, fmt.Errorf("expected *resourcev1beta1.ResourceSlice, got %T", obj)
	}
	if slice.Spec.Driver != DRAGPUDriverName {
		return nil, nil
	}

	keys := make([]string, 0, len(slice.Spec.Devices))
	for _, device := range slice.Spec.Devices {
		keys = append(keys, draDeviceIndexKey(slice.Spec.Driver, slice.Spec.Pool.Name, device.Name))
	}
	return keys, nil
}

func indexV1ResourceSliceByPool(obj any) ([]string, error) {
	slice, ok := obj.(*resourcev1.ResourceSlice)
	if !ok {
		return nil, fmt.Errorf("expected *resourcev1.ResourceSlice, got %T", obj)
	}
	if slice.Spec.Driver != DRAGPUDriverName {
		return nil, nil
	}
	return []string{draPoolIndexKey(slice.Spec.Driver, slice.Spec.Pool.Name)}, nil
}

func indexV1beta1ResourceSliceByPool(obj any) ([]string, error) {
	slice, ok := obj.(*resourcev1beta1.ResourceSlice)
	if !ok {
		return nil, fmt.Errorf("expected *resourcev1beta1.ResourceSlice, got %T", obj)
	}
	if slice.Spec.Driver != DRAGPUDriverName {
		return nil, nil
	}
	return []string{draPoolIndexKey(slice.Spec.Driver, slice.Spec.Pool.Name)}, nil
}

func latestV1PoolGeneration(indexer cache.Indexer, pool string) (int64, bool) {
	objects, err := indexer.ByIndex(resourceSlicePoolIndex, draPoolIndexKey(DRAGPUDriverName, pool))
	if err != nil {
		slog.Debug("looking up indexed v1 ResourceSlice pool failed", "pool", pool, "error", err)
		return 0, false
	}

	var latestGeneration, observed, expected int64
	found := false
	for _, obj := range objects {
		slice, ok := obj.(*resourcev1.ResourceSlice)
		if !ok || slice.Spec.Driver != DRAGPUDriverName || slice.Spec.Pool.Name != pool {
			continue
		}
		gen := slice.Spec.Pool.Generation
		switch {
		case !found || gen > latestGeneration:
			latestGeneration = gen
			observed = 1
			expected = slice.Spec.Pool.ResourceSliceCount
			found = true
		case gen == latestGeneration:
			observed++
		}
	}
	if !found || expected <= 0 || observed < expected {
		return 0, false
	}
	return latestGeneration, true
}

func latestV1beta1PoolGeneration(indexer cache.Indexer, pool string) (int64, bool) {
	objects, err := indexer.ByIndex(resourceSlicePoolIndex, draPoolIndexKey(DRAGPUDriverName, pool))
	if err != nil {
		slog.Debug("looking up indexed v1beta1 ResourceSlice pool failed", "pool", pool, "error", err)
		return 0, false
	}

	var latestGeneration, observed, expected int64
	found := false
	for _, obj := range objects {
		slice, ok := obj.(*resourcev1beta1.ResourceSlice)
		if !ok || slice.Spec.Driver != DRAGPUDriverName || slice.Spec.Pool.Name != pool {
			continue
		}
		gen := slice.Spec.Pool.Generation
		switch {
		case !found || gen > latestGeneration:
			latestGeneration = gen
			observed = 1
			expected = slice.Spec.Pool.ResourceSliceCount
			found = true
		case gen == latestGeneration:
			observed++
		}
	}
	if !found || expected <= 0 || observed < expected {
		return 0, false
	}
	return latestGeneration, true
}

func makeV1Lookup(indexer cache.Indexer) deviceLookupFunc {
	return func(pool, device string) (string, *DRAMigDeviceInfo) {
		latestGeneration, ok := latestV1PoolGeneration(indexer, pool)
		if !ok {
			return "", nil
		}

		objects, err := indexer.ByIndex(resourceSliceIndex, draDeviceIndexKey(DRAGPUDriverName, pool, device))
		if err != nil {
			slog.Debug("looking up indexed v1 ResourceSlice failed", "pool", pool, "device", device, "error", err)
			return "", nil
		}

		var selected *resourcev1.Device
		var selectedKey string
		for _, obj := range objects {
			slice, ok := obj.(*resourcev1.ResourceSlice)
			if !ok || slice.Spec.Driver != DRAGPUDriverName || slice.Spec.Pool.Name != pool || slice.Spec.Pool.Generation != latestGeneration {
				continue
			}
			for i := range slice.Spec.Devices {
				candidate := &slice.Spec.Devices[i]
				if candidate.Name != device {
					continue
				}
				sliceKey, err := cache.MetaNamespaceKeyFunc(slice)
				if err != nil {
					slog.Debug("ignoring v1 ResourceSlice with invalid metadata", "error", err)
					continue
				}
				if selected == nil || sliceKey > selectedKey {
					selected = candidate
					selectedKey = sliceKey
				}
			}
		}
		if selected == nil || selected.Attributes == nil {
			return "", nil
		}

		return buildDeviceMapping(
			getV1AttrString(selected.Attributes, "type"),
			getV1AttrString(selected.Attributes, "uuid"),
			getV1AttrString(selected.Attributes, "parentUUID"),
			getV1AttrString(selected.Attributes, "profile"),
		)
	}
}

func makeV1beta1Lookup(indexer cache.Indexer) deviceLookupFunc {
	return func(pool, device string) (string, *DRAMigDeviceInfo) {
		latestGeneration, ok := latestV1beta1PoolGeneration(indexer, pool)
		if !ok {
			return "", nil
		}

		objects, err := indexer.ByIndex(resourceSliceIndex, draDeviceIndexKey(DRAGPUDriverName, pool, device))
		if err != nil {
			slog.Debug("looking up indexed v1beta1 ResourceSlice failed", "pool", pool, "device", device, "error", err)
			return "", nil
		}

		var selected *resourcev1beta1.Device
		var selectedKey string
		for _, obj := range objects {
			slice, ok := obj.(*resourcev1beta1.ResourceSlice)
			if !ok || slice.Spec.Driver != DRAGPUDriverName || slice.Spec.Pool.Name != pool || slice.Spec.Pool.Generation != latestGeneration {
				continue
			}
			for i := range slice.Spec.Devices {
				candidate := &slice.Spec.Devices[i]
				if candidate.Name != device {
					continue
				}
				sliceKey, err := cache.MetaNamespaceKeyFunc(slice)
				if err != nil {
					slog.Debug("ignoring v1beta1 ResourceSlice with invalid metadata", "error", err)
					continue
				}
				if selected == nil || sliceKey > selectedKey {
					selected = candidate
					selectedKey = sliceKey
				}
			}
		}
		if selected == nil || selected.Basic == nil || selected.Basic.Attributes == nil {
			return "", nil
		}

		return buildDeviceMapping(
			getV1beta1AttrString(selected.Basic.Attributes, "type"),
			getV1beta1AttrString(selected.Basic.Attributes, "uuid"),
			getV1beta1AttrString(selected.Basic.Attributes, "parentUUID"),
			getV1beta1AttrString(selected.Basic.Attributes, "profile"),
		)
	}
}

func buildDeviceMapping(deviceType, uuid, parentUUID, profile string) (string, *DRAMigDeviceInfo) {
	switch deviceType {
	case "gpu":
		return uuid, nil
	case "mig":
		if parentUUID == "" {
			slog.Debug("MIG device missing parent UUID", "uuid", uuid)
			return "", nil
		}
		return parentUUID, &DRAMigDeviceInfo{
			MIGDeviceUUID: uuid,
			Profile:       profile,
			ParentUUID:    parentUUID,
		}
	default:
		slog.Debug("Unknown DRA device type", "type", deviceType)
		return "", nil
	}
}

func getV1AttrString(attrs map[resourcev1.QualifiedName]resourcev1.DeviceAttribute, key resourcev1.QualifiedName) string {
	if attr, ok := attrs[key]; ok && attr.StringValue != nil {
		return *attr.StringValue
	}
	return ""
}

func getV1beta1AttrString(attrs map[resourcev1beta1.QualifiedName]resourcev1beta1.DeviceAttribute, key resourcev1beta1.QualifiedName) string {
	if attr, ok := attrs[key]; ok && attr.StringValue != nil {
		return *attr.StringValue
	}
	return ""
}
