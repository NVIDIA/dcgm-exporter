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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/kubeclient"
)

const (
	informerResyncPeriod = 10 * time.Minute
)

// resourceSliceAdapter provides a unified interface for accessing ResourceSlice data
// from both v1 and v1beta1 API versions
type resourceSliceAdapter interface {
	// GetPoolName returns the pool name from the ResourceSlice
	GetPoolName() string
	// GetName returns the ResourceSlice name
	GetName() string
	// GetNamespace returns the ResourceSlice namespace
	GetNamespace() string
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

func (a *v1ResourceSliceAdapter) GetPoolName() string {
	return a.slice.Spec.Pool.Name
}

func (a *v1ResourceSliceAdapter) GetName() string {
	return a.slice.Name
}

func (a *v1ResourceSliceAdapter) GetNamespace() string {
	return a.slice.Namespace
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

func (a *v1beta1ResourceSliceAdapter) GetPoolName() string {
	return a.slice.Spec.Pool.Name
}

func (a *v1beta1ResourceSliceAdapter) GetName() string {
	return a.slice.Name
}

func (a *v1beta1ResourceSliceAdapter) GetNamespace() string {
	return a.slice.Namespace
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

func NewDRAResourceSliceManager() (*DRAResourceSliceManager, error) {
	client, err := kubeclient.GetKubeClient()
	if err != nil {
		return nil, fmt.Errorf("error getting kube client: %w", err)
	}

	factory := informers.NewSharedInformerFactory(client, informerResyncPeriod)

	// Register informers for both v1 and v1beta1 to support both API versions
	v1Informer := factory.Resource().V1().ResourceSlices().Informer()
	v1beta1Informer := factory.Resource().V1beta1().ResourceSlices().Informer()

	m := &DRAResourceSliceManager{
		factory:         factory,
		v1Informer:      v1Informer,
		v1beta1Informer: v1beta1Informer,
		deviceToUUID:    make(map[string]string),
		migDevices:      make(map[string]*DRAMigDeviceInfo),
	}

	// Add event handlers for v1 API
	_, err = v1Informer.AddEventHandler(&cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			s := obj.(*resourcev1.ResourceSlice)
			return s.Spec.Driver == DRAGPUDriverName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    m.onAddOrUpdateV1,
			UpdateFunc: func(_, o interface{}) { m.onAddOrUpdateV1(o) },
			DeleteFunc: m.onDeleteV1,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("error adding v1 event handler: %w", err)
	}

	// Add event handlers for v1beta1 API
	_, err = v1beta1Informer.AddEventHandler(&cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			s := obj.(*resourcev1beta1.ResourceSlice)
			return s.Spec.Driver == DRAGPUDriverName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    m.onAddOrUpdateV1beta1,
			UpdateFunc: func(_, o interface{}) { m.onAddOrUpdateV1beta1(o) },
			DeleteFunc: m.onDeleteV1beta1,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("error adding v1beta1 event handler: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelContext = cancel
	factory.Start(ctx.Done())

	// Discover which API versions are available before waiting for cache sync
	discoveryClient := client.Discovery()

	v1Available, err := discovery.IsResourceEnabled(discoveryClient, schema.GroupVersionResource{
		Group:    "resource.k8s.io",
		Version:  "v1",
		Resource: "resourceslices",
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("error checking v1 ResourceSlice API availability: %w", err)
	}

	v1beta1Available, err := discovery.IsResourceEnabled(discoveryClient, schema.GroupVersionResource{
		Group:    "resource.k8s.io",
		Version:  "v1beta1",
		Resource: "resourceslices",
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("error checking v1beta1 ResourceSlice API availability: %w", err)
	}

	if !v1Available && !v1beta1Available {
		cancel()
		return nil, fmt.Errorf("neither v1 nor v1beta1 ResourceSlice API is available")
	}

	// Wait for cache sync only for available API versions
	var v1Synced, v1beta1Synced bool
	if v1Available {
		v1Synced = cache.WaitForCacheSync(ctx.Done(), v1Informer.HasSynced)
		if v1Synced {
			slog.Info("v1 ResourceSlice API informer synced successfully")
		} else {
			slog.Warn("v1 ResourceSlice API informer cache sync failed")
		}
	} else {
		slog.Info("v1 ResourceSlice API not available, skipping cache sync")
	}

	if v1beta1Available {
		v1beta1Synced = cache.WaitForCacheSync(ctx.Done(), v1beta1Informer.HasSynced)
		if v1beta1Synced {
			slog.Info("v1beta1 ResourceSlice API informer synced successfully")
		} else {
			slog.Warn("v1beta1 ResourceSlice API informer cache sync failed")
		}
	} else {
		slog.Info("v1beta1 ResourceSlice API not available, skipping cache sync")
	}

	if !v1Synced && !v1beta1Synced {
		cancel()
		return nil, fmt.Errorf("ResourceSlice informer cache sync failed for both v1 and v1beta1")
	}

	return m, nil
}

func (m *DRAResourceSliceManager) Stop() {
	if m.cancelContext != nil {
		m.cancelContext()
	}
}

// GetDeviceInfo returns the mapping UUID and MIG device info if applicable
// For MIG devices: returns (parentUUID, *DRAMigDeviceInfo)
// For full GPUs: returns (deviceUUID, nil)
func (m *DRAResourceSliceManager) GetDeviceInfo(pool, device string) (string, *DRAMigDeviceInfo) {
	key := pool + "/" + device
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check if this is a MIG device
	if migInfo, exists := m.migDevices[key]; exists {
		// MIG device - return parent UUID and MIG info
		slog.Debug(fmt.Sprintf("Found MIG device for %s with parent UUID: %s", key, migInfo.ParentUUID))
		return migInfo.ParentUUID, migInfo
	}

	// Full GPU device - return device UUID with no MIG info
	if uuid, exists := m.deviceToUUID[key]; exists {
		slog.Debug(fmt.Sprintf("Found GPU device for %s with UUID: %s", uuid, key))
		return uuid, nil
	}

	slog.Info(fmt.Sprintf("No UUID found for %s", key))
	return "", nil
}

// onAddOrUpdate handles ResourceSlice add/update events for both v1 and v1beta1 APIs
func (m *DRAResourceSliceManager) onAddOrUpdate(adapter resourceSliceAdapter, apiVersion string, v1TakesPrecedence bool) {
	pool := adapter.GetPoolName()

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dev := range adapter.GetDevices() {
		if !dev.HasAttributes() {
			continue
		}
		key := pool + "/" + dev.GetName()

		deviceType := dev.GetAttribute("type")
		switch deviceType {
		case "gpu":
			if uuid := dev.GetAttribute("uuid"); uuid != "" {
				// Only update if not already set (when v1 takes precedence)
				if v1TakesPrecedence {
					if _, exists := m.deviceToUUID[key]; !exists {
						m.deviceToUUID[key] = uuid
						slog.Debug(fmt.Sprintf("Added gpu device [key:%s] with UUID: %s (%s)", key, uuid, apiVersion))
					}
				} else {
					m.deviceToUUID[key] = uuid
					slog.Debug(fmt.Sprintf("Added gpu device [key:%s] with UUID: %s (%s)", key, uuid, apiVersion))
				}
			}

		case "mig":
			parentUUID := dev.GetAttribute("parentUUID")
			profile := dev.GetAttribute("profile")
			migUUID := dev.GetAttribute("uuid")

			// Only create MIG device if we have required parent UUID
			if parentUUID != "" {
				// Only update if not already set (when v1 takes precedence)
				if v1TakesPrecedence {
					if _, exists := m.migDevices[key]; !exists {
						m.migDevices[key] = &DRAMigDeviceInfo{
							MIGDeviceUUID: migUUID,
							Profile:       profile,
							ParentUUID:    parentUUID,
						}
						slog.Debug(fmt.Sprintf("Added MIG device %s (profile: %s) with parent: %s (%s)", migUUID, profile, parentUUID, apiVersion))
					}
				} else {
					m.migDevices[key] = &DRAMigDeviceInfo{
						MIGDeviceUUID: migUUID,
						Profile:       profile,
						ParentUUID:    parentUUID,
					}
					slog.Debug(fmt.Sprintf("Added MIG device %s (profile: %s) with parent: %s (%s)", migUUID, profile, parentUUID, apiVersion))
				}
			} else {
				slog.Debug(fmt.Sprintf("MIG device %s missing parent UUID", migUUID))
			}

		default:
			slog.Warn(fmt.Sprintf("Device [key:%s] has unknown type: %s", key, deviceType))
		}
	}
}

// onAddOrUpdateV1 handles v1 API ResourceSlice events
func (m *DRAResourceSliceManager) onAddOrUpdateV1(obj interface{}) {
	slice := obj.(*resourcev1.ResourceSlice)
	adapter := &v1ResourceSliceAdapter{slice: slice}
	m.onAddOrUpdate(adapter, "v1", false)
}

// onAddOrUpdateV1beta1 handles v1beta1 API ResourceSlice events
func (m *DRAResourceSliceManager) onAddOrUpdateV1beta1(obj interface{}) {
	slice := obj.(*resourcev1beta1.ResourceSlice)
	adapter := &v1beta1ResourceSliceAdapter{slice: slice}
	m.onAddOrUpdate(adapter, "v1beta1", true) // v1 takes precedence
}

// onDelete handles ResourceSlice delete events for both v1 and v1beta1 APIs
func (m *DRAResourceSliceManager) onDelete(adapter resourceSliceAdapter, otherInformer cache.SharedIndexInformer, apiVersion, otherApiVersion string) {
	pool := adapter.GetPoolName()
	sliceName := adapter.GetName()
	sliceNamespace := adapter.GetNamespace()

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dev := range adapter.GetDevices() {
		key := pool + "/" + dev.GetName()
		// Check if the other API version still has this device before deleting
		// If both APIs are available, the other API might still have the same ResourceSlice
		if otherInformer != nil {
			items := otherInformer.GetStore().List()
			foundInOther := false
			for _, item := range items {
				var otherAdapter resourceSliceAdapter
				switch obj := item.(type) {
				case *resourcev1.ResourceSlice:
					otherAdapter = &v1ResourceSliceAdapter{slice: obj}
				case *resourcev1beta1.ResourceSlice:
					otherAdapter = &v1beta1ResourceSliceAdapter{slice: obj}
				default:
					continue
				}
				if otherAdapter.GetName() == sliceName && otherAdapter.GetNamespace() == sliceNamespace {
					// Same ResourceSlice exists in other API, check if it has this device
					for _, otherDev := range otherAdapter.GetDevices() {
						if otherDev.GetName() == dev.GetName() {
							foundInOther = true
							break
						}
					}
					if foundInOther {
						break
					}
				}
			}
			if foundInOther {
				slog.Debug(fmt.Sprintf("Not removing device %s (%s delete) - still exists in %s", key, apiVersion, otherApiVersion))
				continue
			}
		}
		slog.Debug(fmt.Sprintf("Removing device for %s (%s)", key, apiVersion))
		delete(m.deviceToUUID, key)
		delete(m.migDevices, key)
	}
}

// onDeleteV1 handles v1 API ResourceSlice delete events
func (m *DRAResourceSliceManager) onDeleteV1(obj interface{}) {
	slice := obj.(*resourcev1.ResourceSlice)
	adapter := &v1ResourceSliceAdapter{slice: slice}
	m.onDelete(adapter, m.v1beta1Informer, "v1", "v1beta1")
}

// onDeleteV1beta1 handles v1beta1 API ResourceSlice delete events
func (m *DRAResourceSliceManager) onDeleteV1beta1(obj interface{}) {
	slice := obj.(*resourcev1beta1.ResourceSlice)
	adapter := &v1beta1ResourceSliceAdapter{slice: slice}
	m.onDelete(adapter, m.v1Informer, "v1beta1", "v1")
}
