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

	"google.golang.org/grpc"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/transformation/mock_transformer.go -package=transformation -copyright_file=../../../hack/header.txt . Transform

// Transform enriches collected metrics using the current device inventory.
type Transform interface {
	Process(metrics collector.MetricsByCounter, deviceInfo deviceinfo.Provider) error
	Name() string
}

// PodMapper owns kubelet, pod metadata, and optional DRA attribution state.
type PodMapper struct {
	Config *appconfig.Config
	Client kubernetes.Interface
	// ResourceSliceManager is nil until DRA setup succeeds. A setup that fails is
	// retried in the background, so read it with draManager.
	ResourceSliceManager *DRAResourceSliceManager
	draMu                sync.RWMutex
	// Each kind of failure has its own limiter so one cannot hide another: the
	// kubelet socket is missing, a pod-resources response is over the size
	// limit, and any other failed request.
	socketWarnings      warningLimiter
	oversizeWarnings    warningLimiter
	unavailableWarnings warningLimiter
	labelFilterCache    *LabelFilterCache
	// migLookups keeps NVML answers for the MIG UUIDs kubelet reports.
	migLookups         *migLookupCache
	podInformerFactory informers.SharedInformerFactory
	podLister          corev1listers.PodLister
	podInformerSynced  cache.InformerSynced
	stopChan           chan struct{}
	// ctx is owned by the mapper and cancelled by Stop, so DRA setup and its
	// retries end with the mapper.
	ctx    context.Context
	cancel context.CancelFunc
	// grpcConn is a persistent connection to the kubelet pod-resources endpoint,
	// reused across scrapes to avoid the per-scrape allocation/goroutine overhead
	// of grpc.NewClient, which made memory use grow slowly over time.
	grpcConn   *grpc.ClientConn
	grpcConnMu sync.Mutex
	stopped    bool
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

// PodInfo carries one allocation's workload identity, sharing slot, and DRA metadata.
type PodInfo struct {
	Name                     string
	Namespace                string
	Container                string
	UID                      string
	VGPU                     string
	Labels                   map[string]string
	OriginalLabels           map[string]string
	DevicePluginResourceName string
	DynamicResources         *DynamicResourceInfo
}

// DRAResourceSliceManager owns synchronized discovery and complete-pool reload tracking.
type DRAResourceSliceManager struct {
	factory                 informers.SharedInformerFactory
	cancelContext           context.CancelFunc
	indexer                 cache.Indexer
	lookup                  deviceLookupFunc
	poolGeneration          poolGenerationFunc
	poolName                resourceSlicePoolNameFunc
	onGenerationChange      func()
	generationBaselines     map[string]int64
	generationTrackingReady bool
	generationTrackingMu    sync.Mutex
	stopped                 bool
}

// poolGenerationFunc returns a pool's newest generation and reports whether
// all ResourceSlices needed to consider that generation complete are present.
type poolGenerationFunc func(pool string) (int64, bool)

// resourceSlicePoolNameFunc extracts a GPU DRA pool name from an event object
// and rejects objects from another driver or an unsupported event shape.
type resourceSlicePoolNameFunc func(obj any) (string, bool)

// PodMetadata holds pod metadata from API server
type PodMetadata struct {
	UID    string
	Labels map[string]string
}

// DynamicResourceInfo identifies a claim allocation and any resolved MIG metadata.
type DynamicResourceInfo struct {
	ClaimName      string
	ClaimNamespace string
	DriverName     string
	PoolName       string
	DeviceName     string
	// MIG-specific information
	MIGInfo *DRAMigDeviceInfo
}

// DRAMigDeviceInfo carries static UUID metadata or a dynamic placement specification.
type DRAMigDeviceInfo struct {
	MIGDeviceUUID string
	Profile       string
	ParentUUID    string
	spec          *draMIGSpec
}

// draMIGSpec identifies the physical MIG placement encoded in a dynamic DRA
// device name.
type draMIGSpec struct {
	ParentMinor    uint32
	ProfileID      uint32
	PlacementStart uint32
}
