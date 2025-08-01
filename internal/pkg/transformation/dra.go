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
	informer := factory.Resource().V1beta1().ResourceSlices().Informer()

	m := &DRAResourceSliceManager{
		factory:      factory,
		informer:     informer,
		deviceToUUID: make(map[string]string),
		migDevices:   make(map[string]*DRAMigDeviceInfo),
	}

	_, err = informer.AddEventHandler(&cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			s := obj.(*resourcev1beta1.ResourceSlice)
			return s.Spec.Driver == DRAGPUDriverName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    m.onAddOrUpdate,
			UpdateFunc: func(_, o interface{}) { m.onAddOrUpdate(o) },
			DeleteFunc: m.onDelete,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("error adding event handler: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelContext = cancel
	factory.Start(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		cancel()
		return nil, fmt.Errorf("ResourceSlice informer cache sync failed")
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

func getAttrString(attrs map[resourcev1beta1.QualifiedName]resourcev1beta1.DeviceAttribute, key resourcev1beta1.QualifiedName) string {
	if attr, ok := attrs[key]; ok && attr.StringValue != nil {
		return *attr.StringValue
	}
	return ""
}

func (m *DRAResourceSliceManager) onAddOrUpdate(obj interface{}) {
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

		deviceType := getAttrString(attr, "type")
		switch deviceType {
		case "gpu":
			if uuid := getAttrString(attr, "uuid"); uuid != "" {
				m.deviceToUUID[key] = uuid
				slog.Debug(fmt.Sprintf("Added gpu device [key:%s] with UUID: %s", key, uuid))
			}

		case "mig":
			parentUUID := getAttrString(attr, "parentUUID")
			profile := getAttrString(attr, "profile")
			migUUID := getAttrString(attr, "uuid")

			// Only create MIG device if we have required parent UUID
			if parentUUID != "" {
				m.migDevices[key] = &DRAMigDeviceInfo{
					MIGDeviceUUID: migUUID,
					Profile:       profile,
					ParentUUID:    parentUUID,
				}
				slog.Debug(fmt.Sprintf("Added MIG device %s (profile: %s) with parent: %s", migUUID, profile, parentUUID))
			} else {
				slog.Debug(fmt.Sprintf("MIG device %s missing parent UUID", migUUID))
			}

		default:
			slog.Warn(fmt.Sprintf("Device [key:%s] has unknown type: %s", key, deviceType))
		}
	}
}

func (m *DRAResourceSliceManager) onDelete(obj interface{}) {
	slice := obj.(*resourcev1beta1.ResourceSlice)
	pool := slice.Spec.Pool.Name

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dev := range slice.Spec.Devices {
		key := pool + "/" + dev.Name
		slog.Debug(fmt.Sprintf("Removing device for %s", key))
		delete(m.deviceToUUID, key)
		delete(m.migDevices, key)
	}
}
