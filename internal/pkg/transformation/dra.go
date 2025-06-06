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
	"context"
	"fmt"
	"log/slog"
	"time"

	resourcev1beta1 "k8s.io/api/resource/v1beta1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

const (
	informerResyncPeriod = 10 * time.Minute
)

func NewDRAResourceSliceManager(cfg *appconfig.Config) (*DRAResourceSliceManager, error) {
	client, err := appconfig.GetKubeClient()
	if err != nil {
		return nil, fmt.Errorf("error getting kube client: %w", err)
	}

	factory := informers.NewSharedInformerFactory(client, informerResyncPeriod)
	informer := factory.Resource().V1beta1().ResourceSlices().Informer()

	m := &DRAResourceSliceManager{
		factory:      factory,
		informer:     informer,
		deviceToUUID: make(map[string]string),
	}

	informer.AddEventHandler(&cache.FilteringResourceEventHandler{
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

// GetUUID returns the UUID for the given pool/device, if known.
func (m *DRAResourceSliceManager) GetUUID(pool, device string) string {
	key := pool + "/" + device
	m.mu.RLock()
	defer m.mu.RUnlock()
	uuid, _ := m.deviceToUUID[key]

	slog.Info(fmt.Sprintf("Found UUID: %s for %s", uuid, key))
	return uuid
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
		if attr, ok := dev.Basic.Attributes["uuid"]; ok && attr.StringValue != nil {
			m.deviceToUUID[pool+"/"+dev.Name] = *attr.StringValue
		}
	}
}

func (m *DRAResourceSliceManager) onDelete(obj interface{}) {
	slice := obj.(*resourcev1beta1.ResourceSlice)
	pool := slice.Spec.Pool.Name

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dev := range slice.Spec.Devices {
		delete(m.deviceToUUID, pool+"/"+dev.Name)
	}
}
