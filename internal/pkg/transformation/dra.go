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
	"strings"
	"time"

	resourcev1 "k8s.io/api/resource/v1"
	resourcev1beta1 "k8s.io/api/resource/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/kubeclient"
)

const (
	informerResyncPeriod = 10 * time.Minute
)

// resourceSliceAdapter provides a unified interface for accessing ResourceSlice data
// from both v1 and v1beta1 API versions
type resourceSliceAdapter interface {
	// GetDevices returns a list of device adapters
	GetDevices() []deviceAdapter
}

// deviceAdapter provides a unified interface for accessing device data
// from both v1 and v1beta1 API versions
type deviceAdapter interface {
	// GetName returns the device name
	GetName() string
	// GetAttribute returns the string value of an attribute by key, or empty string if not found
	GetAttribute(key string) string
	// HasAttributes returns true if the device has attributes
	HasAttributes() bool
}

// v1ResourceSliceAdapter adapts resourcev1.ResourceSlice to resourceSliceAdapter
type v1ResourceSliceAdapter struct {
	slice *resourcev1.ResourceSlice
}

func (a *v1ResourceSliceAdapter) GetDevices() []deviceAdapter {
	devices := make([]deviceAdapter, len(a.slice.Spec.Devices))
	for i := range a.slice.Spec.Devices {
		devices[i] = &v1DeviceAdapter{device: &a.slice.Spec.Devices[i]}
	}
	return devices
}

// v1DeviceAdapter adapts resourcev1.Device to deviceAdapter
type v1DeviceAdapter struct {
	device *resourcev1.Device
}

func (a *v1DeviceAdapter) GetName() string {
	return a.device.Name
}

func (a *v1DeviceAdapter) HasAttributes() bool {
	return a.device.Attributes != nil
}

func (a *v1DeviceAdapter) GetAttribute(key string) string {
	if a.device.Attributes == nil {
		return ""
	}
	attrKey := resourcev1.QualifiedName(key)
	if attr, ok := a.device.Attributes[attrKey]; ok && attr.StringValue != nil {
		return *attr.StringValue
	}
	return ""
}

// v1beta1ResourceSliceAdapter adapts resourcev1beta1.ResourceSlice to resourceSliceAdapter
type v1beta1ResourceSliceAdapter struct {
	slice *resourcev1beta1.ResourceSlice
}

func (a *v1beta1ResourceSliceAdapter) GetDevices() []deviceAdapter {
	devices := make([]deviceAdapter, len(a.slice.Spec.Devices))
	for i := range a.slice.Spec.Devices {
		devices[i] = &v1beta1DeviceAdapter{device: &a.slice.Spec.Devices[i]}
	}
	return devices
}

// v1beta1DeviceAdapter adapts resourcev1beta1.Device to deviceAdapter
type v1beta1DeviceAdapter struct {
	device *resourcev1beta1.Device
}

func (a *v1beta1DeviceAdapter) GetName() string {
	return a.device.Name
}

func (a *v1beta1DeviceAdapter) HasAttributes() bool {
	return a.device.Basic != nil && a.device.Basic.Attributes != nil
}

func (a *v1beta1DeviceAdapter) GetAttribute(key string) string {
	if a.device.Basic == nil || a.device.Basic.Attributes == nil {
		return ""
	}
	attrKey := resourcev1beta1.QualifiedName(key)
	if attr, ok := a.device.Basic.Attributes[attrKey]; ok && attr.StringValue != nil {
		return *attr.StringValue
	}
	return ""
}

// hasNvidiaDRASlice checks if there are any ResourceSlices with NVIDIA DRA driver
// by listing ResourceSlices for the given API version.
func hasNvidiaDRASlice(ctx context.Context, client kubernetes.Interface, useV1 bool) (bool, error) {
	if useV1 {
		resourceSlicesList, err := client.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, s := range resourceSlicesList.Items {
			if s.Spec.Driver == DRAGPUDriverName {
				return true, nil
			}
		}
		return false, nil
	} else {
		resourceSlicesList, err := client.ResourceV1beta1().ResourceSlices().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, s := range resourceSlicesList.Items {
			if s.Spec.Driver == DRAGPUDriverName {
				return true, nil
			}
		}
		return false, nil
	}
}

func supportsResourceSliceGV(client kubernetes.Interface, groupVersion string) bool {
	resources, err := client.Discovery().ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		// Discovery returns errors when the group/version isn't served.
		slog.Debug("Discovery failed for groupVersion", "groupVersion", groupVersion, "error", err)
		return false
	}

	for _, r := range resources.APIResources {
		// Be lenient: match both "resourceslices" and any subresource variants.
		if strings.HasPrefix(r.Name, "resourceslices") {
			return true
		}
	}
	return false
}

// NewDRAResourceSliceManager creates a new DRA ResourceSlice manager.
// The API version is auto-detected by checking which version has NVIDIA DRA ResourceSlices.
func NewDRAResourceSliceManager() (*DRAResourceSliceManager, error) {
	client, err := kubeclient.GetKubeClient()
	if err != nil {
		return nil, fmt.Errorf("error getting kube client: %w", err)
	}

	// Decide which API version is served. Prefer v1 when available, otherwise v1beta1.
	// We start a single informer for the selected version to avoid waiting on an API
	// that isn't served by the cluster.
	const (
		resourceGVV1      = "resource.k8s.io/v1"
		resourceGVV1beta1 = "resource.k8s.io/v1beta1"
	)

	var selected string
	switch {
	case supportsResourceSliceGV(client, resourceGVV1):
		selected = "v1"
	case supportsResourceSliceGV(client, resourceGVV1beta1):
		selected = "v1beta1"
	default:
		slog.Warn("Neither resource.k8s.io/v1 nor v1beta1 ResourceSlice API is served; DRA labels will not be available")
		return nil, nil
	}

	factory := informers.NewSharedInformerFactory(client, informerResyncPeriod)

	var v1Informer cache.SharedIndexInformer
	var v1beta1Informer cache.SharedIndexInformer
	switch selected {
	case "v1":
		v1Informer = factory.Resource().V1().ResourceSlices().Informer()
		err = v1Informer.AddIndexers(cache.Indexers{
			"poolName": func(obj interface{}) ([]string, error) {
				rs, ok := obj.(*resourcev1.ResourceSlice)
				if !ok {
					return nil, nil
				}
				return []string{rs.Spec.Pool.Name}, nil
			},
		})
		if err != nil {
			return nil, fmt.Errorf("error adding pool indexer to v1 ResourceSlice informer: %w", err)
		}
	case "v1beta1":
		v1beta1Informer = factory.Resource().V1beta1().ResourceSlices().Informer()
		err = v1beta1Informer.AddIndexers(cache.Indexers{
			"poolName": func(obj interface{}) ([]string, error) {
				rs, ok := obj.(*resourcev1beta1.ResourceSlice)
				if !ok {
					return nil, nil
				}
				return []string{rs.Spec.Pool.Name}, nil
			},
		})
		if err != nil {
			return nil, fmt.Errorf("error adding pool indexer to v1beta1 ResourceSlice informer: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported API version selection: %s", selected)
	}

	m := &DRAResourceSliceManager{
		factory:             factory,
		v1Informer:          v1Informer,
		v1beta1Informer:     v1beta1Informer,
		preferredAPIVersion: selected,
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelContext = cancel
	factory.Start(ctx.Done())

	// Wait for cache sync on the selected informer.
	var synced bool
	if m.v1Informer != nil {
		synced = cache.WaitForCacheSync(ctx.Done(), m.v1Informer.HasSynced)
	} else {
		synced = cache.WaitForCacheSync(ctx.Done(), m.v1beta1Informer.HasSynced)
	}
	if !synced {
		cancel()
		return nil, fmt.Errorf("ResourceSlice informer cache sync failed")
	}

	// Only keep the manager if the selected API has NVIDIA DRA slices.
	useV1 := selected == "v1"
	hasSlices, listErr := hasNvidiaDRASlice(ctx, client, useV1)
	if listErr != nil {
		cancel()
		return nil, fmt.Errorf("failed to list ResourceSlices for %s: %v", selected, listErr)
	}
	if !hasSlices {
		slog.Warn("No NVIDIA DRA ResourceSlices found; DRA labels will not be available", "apiVersion", selected)
		m.Stop()
		return nil, nil
	}

	slog.Info("ResourceSlice API informer synced successfully", "apiVersion", selected)
	return m, nil
}

func (m *DRAResourceSliceManager) Stop() {
	if m.cancelContext != nil {
		m.cancelContext()
	}
	// Ensure factory informers are fully stopped
	if m.factory != nil {
		m.factory.Shutdown()
	}
}

// countGPUSlices counts the number of ResourceSlice objects with GPU devices
// (matching the DRAGPUDriverName) in the given items.
func countGPUSlices(items []interface{}) int {
	count := 0
	for _, item := range items {
		switch obj := item.(type) {
		case *resourcev1.ResourceSlice:
			if obj.Spec.Driver == DRAGPUDriverName && len(obj.Spec.Devices) > 0 {
				count++
			}
		case *resourcev1beta1.ResourceSlice:
			if obj.Spec.Driver == DRAGPUDriverName && len(obj.Spec.Devices) > 0 {
				count++
			}
		}
	}
	return count
}

func (m *DRAResourceSliceManager) getV1DeviceInfo(pool, device string) (string, *DRAMigDeviceInfo) {
	if m.v1Informer == nil {
		return "", nil
	}

	items, err := m.v1Informer.GetIndexer().ByIndex("poolName", pool)
	if err != nil {
		slog.Error(fmt.Sprintf("Error listing v1 ResourceSlices by pool index for pool %s: %v", pool, err))
		return "", nil
	}

	return m.getDeviceInfoFromItems(pool, device, items)
}

func (m *DRAResourceSliceManager) getV1beta1DeviceInfo(pool, device string) (string, *DRAMigDeviceInfo) {
	if m.v1beta1Informer == nil {
		return "", nil
	}

	items, err := m.v1beta1Informer.GetIndexer().ByIndex("poolName", pool)
	if err != nil {
		slog.Error(fmt.Sprintf("Error listing v1beta1 ResourceSlices by pool index for pool %s: %v", pool, err))
		return "", nil
	}

	return m.getDeviceInfoFromItems(pool, device, items)
}

func (m *DRAResourceSliceManager) getDeviceInfoFromItems(pool, device string, items []interface{}) (string, *DRAMigDeviceInfo) {
	// Search for the device in the selected slices
	for _, item := range items {
		var adapter resourceSliceAdapter
		switch obj := item.(type) {
		case *resourcev1.ResourceSlice:
			// NOTE: dcgm-exporter's DRA handling currently assumes the schema used by
			// the NVIDIA GPU DRA driver (for example, "type", "uuid", "parentUUID", "profile"
			// attributes). Other GPU DRA drivers with different schemas may not work
			// correctly with this implementation.
			if obj.Spec.Driver != DRAGPUDriverName {
				continue
			}
			adapter = &v1ResourceSliceAdapter{slice: obj}
		case *resourcev1beta1.ResourceSlice:
			if obj.Spec.Driver != DRAGPUDriverName {
				continue
			}
			adapter = &v1beta1ResourceSliceAdapter{slice: obj}
		default:
			continue
		}

		// Search for the device in this slice
		for _, dev := range adapter.GetDevices() {
			if !dev.HasAttributes() {
				continue
			}
			if dev.GetName() != device {
				continue
			}

			deviceType := dev.GetAttribute("type")
			switch deviceType {
			case "mig":
				parentUUID := dev.GetAttribute("parentUUID")
				profile := dev.GetAttribute("profile")
				migUUID := dev.GetAttribute("uuid")
				if parentUUID != "" {
					migInfo := &DRAMigDeviceInfo{
						MIGDeviceUUID: migUUID,
						Profile:       profile,
						ParentUUID:    parentUUID,
					}
					slog.Debug(fmt.Sprintf("Found MIG device %s/%s with parent UUID: %s", pool, device, parentUUID))
					return parentUUID, migInfo
				}
			case "gpu":
				uuid := dev.GetAttribute("uuid")
				if uuid != "" {
					slog.Debug(fmt.Sprintf("Found GPU device %s/%s with UUID: %s", pool, device, uuid))
					return uuid, nil
				}
			default:
				// Log unknown device types to help users understand why a device might not be handled
				slog.Warn(fmt.Sprintf("Device [%s/%s] has unknown type: %s", pool, device, deviceType))
			}
		}
	}

	slog.Debug(fmt.Sprintf("No UUID found for pool %s, device %s", pool, device))
	return "", nil
}

// GetDeviceInfo returns the mapping UUID and MIG device info if applicable
// by querying the informer cache directly. This avoids maintaining redundant
// local caches and ensures we always have the latest state from the API server.
// For MIG devices: returns (parentUUID, *DRAMigDeviceInfo)
// For full GPUs: returns (deviceUUID, nil)
func (m *DRAResourceSliceManager) GetDeviceInfo(pool, device string) (string, *DRAMigDeviceInfo) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	switch m.preferredAPIVersion {
	case "v1":
		return m.getV1DeviceInfo(pool, device)
	case "v1beta1":
		return m.getV1beta1DeviceInfo(pool, device)
	default:
		// Be defensive if the manager is constructed manually in tests.
		uuid, mig := m.getV1DeviceInfo(pool, device)
		if uuid != "" {
			return uuid, mig
		}
		return m.getV1beta1DeviceInfo(pool, device)
	}
}

// GetDynamicResourceInfo converts a DynamicResource into a DynamicResourceInfo and
// resolves the backing GPU or MIG device UUID using the ResourceSlice informer.
// It returns the mapping key (device UUID or parent UUID for MIG devices) and
// the populated DynamicResourceInfo. If the DynamicResource is not for the
// NVIDIA GPU DRA driver or no matching device can be found, it returns "" and nil.
func (m *DRAResourceSliceManager) GetDynamicResourceInfo(resource *podresourcesapi.DynamicResource) (string, *DynamicResourceInfo) {
	if resource == nil {
		return "", nil
	}

	for _, claimResource := range resource.GetClaimResources() {
		draDriverName := claimResource.GetDriverName()
		if draDriverName != DRAGPUDriverName {
			continue
		}

		draPoolName := claimResource.GetPoolName()
		draDeviceName := claimResource.GetDeviceName()

		mappingKey, migInfo := m.GetDeviceInfo(draPoolName, draDeviceName)
		if mappingKey == "" {
			slog.Debug(fmt.Sprintf("No UUID for %s/%s", draPoolName, draDeviceName))
			continue
		}

		drInfo := &DynamicResourceInfo{
			ClaimName:      resource.GetClaimName(),
			ClaimNamespace: resource.GetClaimNamespace(),
			DriverName:     draDriverName,
			PoolName:       draPoolName,
			DeviceName:     draDeviceName,
		}
		if migInfo != nil {
			drInfo.MIGInfo = migInfo
		}

		return mappingKey, drInfo
	}

	return "", nil
}
