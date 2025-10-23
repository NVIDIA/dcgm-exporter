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
	"container/list"
	"context"
	"regexp"
	"sync"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/transformations/mock_transformer.go -package=transformation -copyright_file=../../../hack/header.txt . Transform

type Transform interface {
	Process(metrics collector.MetricsByCounter, deviceInfo deviceinfo.Provider) error
	Name() string
}

type PodMapper struct {
	Config               *appconfig.Config
	Client               kubernetes.Interface
	ResourceSliceManager *DRAResourceSliceManager
	labelFilterCache     *LabelFilterCache
}

// LabelFilterCache provides efficient caching for label filtering decisions
type LabelFilterCache struct {
	compiledPatterns []*regexp.Regexp         // Pre-compiled regex patterns
	cache            map[string]*list.Element // map[labelKey -> list element] - list element of key we've already checked
	lruList          *list.List               // Doubly-linked list for LRU ordering
	mu               sync.Mutex               // Protects cache and lruList
	maxSize          int                      // Maximum number of entries to cache
	enabled          bool                     // Whether filtering is enabled (has patterns)
}

// labelCacheEntry represents a cached label filtering result
type labelCacheEntry struct {
	key   string // Label key
	value bool   // Whether the label is allowed
}

type PodInfo struct {
	Name             string
	Namespace        string
	Container        string
	UID              string
	VGPU             string
	Labels           map[string]string
	DynamicResources *DynamicResourceInfo
}

type DRAResourceSliceManager struct {
	factory       informers.SharedInformerFactory
	informer      cache.SharedIndexInformer
	cancelContext context.CancelFunc
	mu            sync.RWMutex
	deviceToUUID  map[string]string            // pool/device -> UUID (for full GPUs)
	migDevices    map[string]*DRAMigDeviceInfo // pool/device -> MIG info (for MIG devices)
}

// PodMetadata holds pod metadata from API server
type PodMetadata struct {
	UID    string
	Labels map[string]string
}

type DynamicResourceInfo struct {
	ClaimName      string
	ClaimNamespace string
	DriverName     string
	PoolName       string
	DeviceName     string
	// MIG-specific information
	MIGInfo *DRAMigDeviceInfo
}

type DRAMigDeviceInfo struct {
	MIGDeviceUUID string
	Profile       string
	ParentUUID    string
}
