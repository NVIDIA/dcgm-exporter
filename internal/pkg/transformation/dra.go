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
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/kubeclient"
)

const (
	informerResyncPeriod = 10 * time.Minute
)

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

	// Wait for at least one informer to sync (either v1 or v1beta1)
	// Both will sync if both APIs are available
	v1Synced := cache.WaitForCacheSync(ctx.Done(), v1Informer.HasSynced)
	v1beta1Synced := cache.WaitForCacheSync(ctx.Done(), v1beta1Informer.HasSynced)

	if !v1Synced && !v1beta1Synced {
		cancel()
		return nil, fmt.Errorf("ResourceSlice informer cache sync failed for both v1 and v1beta1")
	}

	if v1Synced {
		slog.Info("v1 ResourceSlice API informer synced successfully")
	}
	if v1beta1Synced {
		slog.Info("v1beta1 ResourceSlice API informer synced successfully")
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

func getAttrStringV1(attrs map[resourcev1.QualifiedName]resourcev1.DeviceAttribute, key resourcev1.QualifiedName) string {
	if attr, ok := attrs[key]; ok && attr.StringValue != nil {
		return *attr.StringValue
	}
	return ""
}

func getAttrStringV1beta1(attrs map[resourcev1beta1.QualifiedName]resourcev1beta1.DeviceAttribute, key resourcev1beta1.QualifiedName) string {
	if attr, ok := attrs[key]; ok && attr.StringValue != nil {
		return *attr.StringValue
	}
	return ""
}

// onAddOrUpdateV1 handles v1 API ResourceSlice events
func (m *DRAResourceSliceManager) onAddOrUpdateV1(obj interface{}) {
	slice := obj.(*resourcev1.ResourceSlice)
	pool := slice.Spec.Pool.Name

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dev := range slice.Spec.Devices {
		if dev.Attributes == nil {
			continue
		}
		key := pool + "/" + dev.Name
		attr := dev.Attributes

		deviceType := getAttrStringV1(attr, "type")
		switch deviceType {
		case "gpu":
			if uuid := getAttrStringV1(attr, "uuid"); uuid != "" {
				m.deviceToUUID[key] = uuid
				slog.Debug(fmt.Sprintf("Added gpu device [key:%s] with UUID: %s (v1)", key, uuid))
			}

		case "mig":
			parentUUID := getAttrStringV1(attr, "parentUUID")
			profile := getAttrStringV1(attr, "profile")
			migUUID := getAttrStringV1(attr, "uuid")

			// Only create MIG device if we have required parent UUID
			if parentUUID != "" {
				m.migDevices[key] = &DRAMigDeviceInfo{
					MIGDeviceUUID: migUUID,
					Profile:       profile,
					ParentUUID:    parentUUID,
				}
				slog.Debug(fmt.Sprintf("Added MIG device %s (profile: %s) with parent: %s (v1)", migUUID, profile, parentUUID))
			} else {
				slog.Debug(fmt.Sprintf("MIG device %s missing parent UUID", migUUID))
			}

		default:
			slog.Warn(fmt.Sprintf("Device [key:%s] has unknown type: %s", key, deviceType))
		}
	}
}

// onAddOrUpdateV1beta1 handles v1beta1 API ResourceSlice events
func (m *DRAResourceSliceManager) onAddOrUpdateV1beta1(obj interface{}) {
	slice := obj.(*resourcev1beta1.ResourceSlice)
	pool := slice.Spec.Pool.Name

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dev := range slice.Spec.Devices {
		if dev.Basic == nil || dev.Basic.Attributes == nil {
			continue
		}
		key := pool + "/" + dev.Name
		attr := dev.Basic.Attributes

		deviceType := getAttrStringV1beta1(attr, "type")
		switch deviceType {
		case "gpu":
			if uuid := getAttrStringV1beta1(attr, "uuid"); uuid != "" {
				// Only update if not already set by v1 (v1 takes precedence)
				if _, exists := m.deviceToUUID[key]; !exists {
					m.deviceToUUID[key] = uuid
					slog.Debug(fmt.Sprintf("Added gpu device [key:%s] with UUID: %s (v1beta1)", key, uuid))
				}
			}

		case "mig":
			parentUUID := getAttrStringV1beta1(attr, "parentUUID")
			profile := getAttrStringV1beta1(attr, "profile")
			migUUID := getAttrStringV1beta1(attr, "uuid")

			// Only create MIG device if we have required parent UUID
			// Only update if not already set by v1 (v1 takes precedence)
			if parentUUID != "" {
				if _, exists := m.migDevices[key]; !exists {
					m.migDevices[key] = &DRAMigDeviceInfo{
						MIGDeviceUUID: migUUID,
						Profile:       profile,
						ParentUUID:    parentUUID,
					}
					slog.Debug(fmt.Sprintf("Added MIG device %s (profile: %s) with parent: %s (v1beta1)", migUUID, profile, parentUUID))
				}
			} else {
				slog.Debug(fmt.Sprintf("MIG device %s missing parent UUID", migUUID))
			}

		default:
			slog.Warn(fmt.Sprintf("Device [key:%s] has unknown type: %s", key, deviceType))
		}
	}
}

// onDeleteV1 handles v1 API ResourceSlice delete events
func (m *DRAResourceSliceManager) onDeleteV1(obj interface{}) {
	slice := obj.(*resourcev1.ResourceSlice)
	pool := slice.Spec.Pool.Name

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dev := range slice.Spec.Devices {
		key := pool + "/" + dev.Name
		// Check if v1beta1 still has this device before deleting
		// If both APIs are available, v1beta1 might still have the same ResourceSlice
		if m.v1beta1Informer != nil {
			// Try to find the same ResourceSlice in v1beta1 cache
			items := m.v1beta1Informer.GetStore().List()
			foundInV1beta1 := false
			for _, item := range items {
				if v1beta1Slice, ok := item.(*resourcev1beta1.ResourceSlice); ok {
					if v1beta1Slice.Name == slice.Name && v1beta1Slice.Namespace == slice.Namespace {
						// Same ResourceSlice exists in v1beta1, check if it has this device
						for _, v1beta1Dev := range v1beta1Slice.Spec.Devices {
							if v1beta1Dev.Name == dev.Name {
								foundInV1beta1 = true
								break
							}
						}
						if foundInV1beta1 {
							break
						}
					}
				}
			}
			if foundInV1beta1 {
				slog.Debug(fmt.Sprintf("Not removing device %s (v1 delete) - still exists in v1beta1", key))
				continue
			}
		}
		slog.Debug(fmt.Sprintf("Removing device for %s (v1)", key))
		delete(m.deviceToUUID, key)
		delete(m.migDevices, key)
	}
}

// onDeleteV1beta1 handles v1beta1 API ResourceSlice delete events
func (m *DRAResourceSliceManager) onDeleteV1beta1(obj interface{}) {
	slice := obj.(*resourcev1beta1.ResourceSlice)
	pool := slice.Spec.Pool.Name

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dev := range slice.Spec.Devices {
		key := pool + "/" + dev.Name
		// Check if v1 still has this device before deleting (v1 takes precedence)
		// If both APIs are available, v1 might still have the same ResourceSlice
		if m.v1Informer != nil {
			// Try to find the same ResourceSlice in v1 cache
			items := m.v1Informer.GetStore().List()
			foundInV1 := false
			for _, item := range items {
				if v1Slice, ok := item.(*resourcev1.ResourceSlice); ok {
					if v1Slice.Name == slice.Name && v1Slice.Namespace == slice.Namespace {
						// Same ResourceSlice exists in v1, check if it has this device
						for _, v1Dev := range v1Slice.Spec.Devices {
							if v1Dev.Name == dev.Name {
								foundInV1 = true
								break
							}
						}
						if foundInV1 {
							break
						}
					}
				}
			}
			if foundInV1 {
				slog.Debug(fmt.Sprintf("Not removing device %s (v1beta1 delete) - still exists in v1", key))
				continue
			}
		}
		slog.Debug(fmt.Sprintf("Removing device for %s (v1beta1)", key))
		delete(m.deviceToUUID, key)
		delete(m.migDevices, key)
	}
}
