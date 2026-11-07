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
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	stdos "os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc/connectivity"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/metriclabels"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

// kubeletPodResourcesMaxRecvMsgSize raises the kubelet pod-resources gRPC receive ceiling above
// the stock 4 MiB default for dense GPU/MIG nodes while preserving a bounded client allocation.
const kubeletPodResourcesMaxRecvMsgSize = 16 * 1024 * 1024

var (
	connectionTimeout   = 10 * time.Second
	errPodMapperStopped = errors.New("pod mapper is stopped")

	// Allow for MIG devices with or without GPU sharing to match in GKE.
	gkeMigDeviceIDRegex            = regexp.MustCompile(`^nvidia([0-9]+)/gi([0-9]+)(/vgpu[0-9]+)?$`)
	gkeVirtualGPUDeviceIDSeparator = "/vgpu"

	inClusterConfigFunc        = rest.InClusterConfig
	newKubernetesForConfigFunc = func(config *rest.Config) (kubernetes.Interface, error) {
		return kubernetes.NewForConfig(config)
	}
	newDRAResourceSliceManagerFunc = newDRAResourceSliceManager
)

// DeviceProcessingFunc is a callback function type for processing devices
type DeviceProcessingFunc func(pod *podresourcesapi.PodResources, container *podresourcesapi.ContainerResources, device *podresourcesapi.ContainerDevices)

// iterateGPUDevices encapsulates the common pattern of iterating through pods, containers, and devices
// while filtering for NVIDIA GPU resources. It calls the provided callback for each valid device.
func (p *PodMapper) iterateGPUDevices(devicePods *podresourcesapi.ListPodResourcesResponse, processDevice DeviceProcessingFunc) {
	for _, pod := range devicePods.GetPodResources() {
		for _, container := range pod.GetContainers() {
			for _, device := range container.GetDevices() {
				resourceName := device.GetResourceName()

				// Apply NVIDIA resource filtering
				if resourceName != appconfig.NvidiaResourceName && !slices.Contains(p.Config.NvidiaResourceNames, resourceName) {
					// MIG resources appear differently than GPU resources
					if !strings.HasPrefix(resourceName, appconfig.NvidiaMigResourcePrefix) {
						slog.Debug("Skipping non-NVIDIA resource",
							"resourceName", resourceName,
							"podName", pod.GetName(),
							"namespace", pod.GetNamespace(),
							"containerName", container.GetName(),
							"deviceIds", device.GetDeviceIds())
						continue
					}
				}

				// Call the processing function for valid devices
				processDevice(pod, container, device)
			}
		}
	}
}

// NewPodMapper creates Kubernetes metric attribution without lifecycle
// notifications. Call NewPodMapperWithDRAChangeCallback when a caller needs to
// react to DRA ResourceSlice changes.
func NewPodMapper(c *appconfig.Config) *PodMapper {
	return NewPodMapperWithDRAChangeCallback(c, nil)
}

// NewPodMapperWithDRAChangeCallback creates Kubernetes metric attribution and
// optionally notifies the caller after a complete DRA ResourceSlice generation
// changes. The command lifecycle passes its reload trigger here at server
// construction time, keeping it separate from parsed configuration.
func NewPodMapperWithDRAChangeCallback(c *appconfig.Config, onDRAChange func()) *PodMapper {
	slog.Info("Kubernetes metrics collection enabled!")

	// Default cache size if not configured
	cacheSize := c.KubernetesPodLabelCacheSize
	if cacheSize <= 0 {
		cacheSize = 150000 // Default: ~18MB for 150k entries (suitable for large cloud clusters)
	}

	ctx, cancel := context.WithCancel(context.Background())

	podMapper := &PodMapper{
		Config:           c,
		labelFilterCache: newLabelFilterCache(c.KubernetesPodLabelAllowlistRegex, cacheSize),
		// A failed MIG lookup is retried once per collection interval.
		migLookups: newMIGLookupCache(time.Duration(c.CollectInterval) * time.Millisecond),
		stopChan:   make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
	}

	if !c.KubernetesEnablePodLabels && !c.KubernetesEnablePodUID && !c.KubernetesEnableDRA {
		return podMapper
	}

	clusterConfig, err := inClusterConfigFunc()
	if err != nil {
		slog.Warn("Failed to get in-cluster config, pod labels will not be available", "error", err)
		podMapper.retryDRAIfEnabled(onDRAChange)

		return podMapper
	}

	clientset, err := newKubernetesForConfigFunc(clusterConfig)
	if err != nil {
		slog.Warn("Failed to get clientset, pod labels will not be available", "error", err)
		podMapper.retryDRAIfEnabled(onDRAChange)

		return podMapper
	}

	podMapper.Client = clientset

	// Initialize Pod Informer
	nodeName := stdos.Getenv("NODE_NAME")
	var factory informers.SharedInformerFactory
	if nodeName != "" {
		slog.Info("Initializing Pod Informer", "nodeName", nodeName)
		tweakListOptions := func(options *metav1.ListOptions) {
			options.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", nodeName).String()
		}
		factory = informers.NewSharedInformerFactoryWithOptions(clientset, 0, informers.WithTweakListOptions(tweakListOptions))
	} else {
		slog.Warn("NODE_NAME environment variable not set, watching all pods in cluster for metadata")
		factory = informers.NewSharedInformerFactory(clientset, 0)
	}

	podMapper.podInformerFactory = factory
	podInformer := factory.Core().V1().Pods()
	podMapper.podLister = podInformer.Lister()
	podMapper.podInformerSynced = podInformer.Informer().HasSynced

	if c.KubernetesEnableDRA {
		resourceSliceManager, err := newDRAResourceSliceManagerFunc(podMapper.ctx, onDRAChange)
		if err != nil {
			slog.Warn("Failed to get DRAResourceSliceManager, DRA pod labels will not be available until it can be created",
				"error", err)
			podMapper.retryDRAIfEnabled(onDRAChange)

			return podMapper
		}

		podMapper.ResourceSliceManager = resourceSliceManager

		slog.Info("Started DRAResourceSliceManager")
	}

	return podMapper
}

// newLabelFilterCache creates a new LRU cache with pre-compiled regex patterns
func newLabelFilterCache(patterns []string, maxSize int) *LabelFilterCache {
	cache := &LabelFilterCache{
		enabled: len(patterns) > 0,
		maxSize: maxSize,
	}

	if !cache.enabled {
		return cache
	}

	// Initialize LRU cache structures
	cache.cache = make(map[string]*list.Element)
	cache.lruList = list.New()

	// Pre-compile all regex patterns at initialization time
	cache.compiledPatterns = make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			slog.Warn("Invalid pod label allowlist; no labels will be included",
				"pattern", pattern,
				"error", err)
			cache.compiledPatterns = nil
			return cache
		}
		cache.compiledPatterns = append(cache.compiledPatterns, compiled)
		slog.Info("Compiled pod label allowlist pattern", "pattern", pattern)
	}

	slog.Info("Pod label filtering enabled",
		"patterns", len(cache.compiledPatterns),
		"maxCacheSize", cache.maxSize)

	return cache
}

// Name identifies the pod-attribution transform in logs and tests.
func (p *PodMapper) Name() string {
	return "podMapper"
}

// createPerProcessMetrics emits per-pod process values and zeroes for idle allocations.
func (p *PodMapper) createPerProcessMetrics(
	val collector.Metric,
	counter counters.Counter,
	originalMetric collector.Metric,
	dataMap *perProcessDataMap,
	getMetricGroup func() dcgm.Field_Entity_Group,
) ([]collector.Metric, error) {
	metricsKey := getMIGMetricsKey(val.GPUUUID, val.GPUInstanceID)
	if dataMap.metrics[metricsKey] == nil {
		metricsKey = val.GPUUUID
	}

	devicePods := dataMap.deviceToPods[metricsKey]
	if len(devicePods) == 0 {
		return nil, nil
	}

	data := dataMap.metrics[metricsKey]
	podValues := buildPodValueMap(dataMap.pidToPod, data, counter.FieldName)
	maps.Copy(podValues, buildIdlePodValues(podValues, devicePods))

	var result []collector.Metric
	for _, podInfo := range devicePods {
		value, ok := podValues[podInfo.UID]
		if !ok {
			continue
		}

		metric := cloneMetric(originalMetric)
		if metric.Attributes == nil {
			metric.Attributes = make(map[string]string)
		}
		metric.Value = value

		if !p.Config.UseOldNamespace {
			metric.Attributes[podAttribute] = podInfo.Name
			metric.Attributes[namespaceAttribute] = podInfo.Namespace
			metric.Attributes[containerAttribute] = podInfo.Container
		} else {
			metric.Attributes[oldPodAttribute] = podInfo.Name
			metric.Attributes[oldNamespaceAttribute] = podInfo.Namespace
			metric.Attributes[oldContainerAttribute] = podInfo.Container
		}
		metric.Attributes[uidAttribute] = podInfo.UID
		preservePodSource(&metric, podInfo)
		if podInfo.VGPU != "" {
			metric.Attributes[vgpuAttribute] = podInfo.VGPU
		}
		if len(podInfo.Labels) > 0 {
			copyPodLabels(&metric, podInfo.Labels, getMetricGroup())
		}
		for k := range metric.Attributes {
			delete(metric.Labels, k)
		}

		result = append(result, metric)
	}

	return result, nil
}

// buildPodValueMap sums observed process values for each resolved pod UID.
func buildPodValueMap(pidToPod map[uint32]*PodInfo, data *perProcessMetrics, fieldName string) map[string]string {
	podValues := make(map[string]string)
	if data == nil {
		return podValues
	}
	podAccum := make(map[string]uint64)
	for pid, podInfo := range pidToPod {
		if value, ok := data.getValueForMetric(fieldName, pid); ok {
			podAccum[podInfo.UID] += value
		}
	}
	for uid, total := range podAccum {
		podValues[uid] = formatProcessTotal(fieldName, total)
	}
	return podValues
}

// buildIdlePodValues supplies zero for allocated pods without an observed process value.
func buildIdlePodValues(existingValues map[string]string, devicePods []PodInfo) map[string]string {
	idleValues := make(map[string]string)
	for _, podInfo := range devicePods {
		if _, ok := existingValues[podInfo.UID]; !ok {
			idleValues[podInfo.UID] = "0"
		}
	}
	return idleValues
}

// Run starts the pod informer and waits for cache synchronization or shutdown.
func (p *PodMapper) Run() {
	if p.podInformerFactory != nil {
		go p.podInformerFactory.Start(p.stopChan)
		if !cache.WaitForCacheSync(p.stopChan, p.podInformerSynced) {
			slog.Error("Failed to sync pod informer cache")
			return
		}
		slog.Info("Pod informer cache synced")
	}
}

// Stop retires the mapper, cancels informers, and closes the cached kubelet connection.
func (p *PodMapper) Stop() {
	p.grpcConnMu.Lock()
	defer p.grpcConnMu.Unlock()

	if p.stopped {
		return
	}

	p.stopped = true
	close(p.stopChan)

	if p.cancel != nil {
		p.cancel()
	}

	p.draMu.Lock()
	if p.ResourceSliceManager != nil {
		p.ResourceSliceManager.Stop()
	}
	p.draMu.Unlock()

	if p.grpcConn != nil {
		p.grpcConn.Close()
		p.grpcConn = nil
	}
}

// getGRPCConn returns the cached gRPC connection to the kubelet pod-resources
// socket, creating a new one when the cached connection is absent or shut down.
func (p *PodMapper) getGRPCConn(socketPath string) (*grpc.ClientConn, error) {
	p.grpcConnMu.Lock()
	defer p.grpcConnMu.Unlock()

	if p.stopped {
		return nil, errPodMapperStopped
	}

	if p.grpcConn != nil {
		if p.grpcConn.GetState() != connectivity.Shutdown {
			return p.grpcConn, nil
		}
		p.grpcConn.Close()
		p.grpcConn = nil
	}

	conn, _, err := connectToServer(socketPath)
	if err != nil {
		return nil, err
	}
	p.grpcConn = conn
	return conn, nil
}

// getMappings reads one kubelet snapshot for sharing, dedicated, and DRA attribution.
func (p *PodMapper) getMappings(deviceInfo deviceinfo.Provider) (map[string][]PodInfo, map[string]PodInfo, map[string][]PodInfo, error) {
	socketPath := p.Config.PodResourcesKubeletSocket
	if err := validatePodResourcesSocketPath(socketPath); err != nil {
		if stdos.IsNotExist(err) {
			// The kubelet may not be up yet. Scrapes go on without pod labels, and
			// they appear once the socket does.
			if p.socketWarnings.allow() {
				slog.Warn("Kubelet pod-resources socket not found; pod labels are missing until it appears. "+
					"The exporter keeps checking.", "socket", socketPath)
			}

			return nil, nil, nil, nil
		}

		return nil, nil, nil, err
	}

	c, err := p.getGRPCConn(socketPath)
	if err != nil {
		return nil, nil, nil, err
	}

	pods, err := p.listPods(c)
	if err != nil {
		return nil, nil, nil, err
	}

	var deviceToPods map[string][]PodInfo
	var deviceToPod map[string]PodInfo
	var deviceToPodsDRA map[string][]PodInfo

	if p.Config.KubernetesVirtualGPUs {
		deviceToPods = p.toDeviceToSharingPods(pods, deviceInfo)
	} else {
		deviceToPod = p.toDeviceToPod(pods, deviceInfo)
	}

	if p.Config.KubernetesEnableDRA {
		deviceToPodsDRA = p.toDeviceToPodsDRA(pods, deviceInfo)
	}

	// Forget MIG devices that this snapshot no longer reports.
	p.migLookups.retainUsed()

	return deviceToPods, deviceToPod, deviceToPodsDRA, nil
}

// addPodIdentityAttributes applies common pod identity attributes, including the optional UID,
// for virtual-GPU, device-plugin, and DRA metric enrichment.
func (p *PodMapper) addPodIdentityAttributes(metric *collector.Metric, podInfo PodInfo) {
	preservePodSource(metric, podInfo)
	if metric.Attributes == nil {
		metric.Attributes = make(map[string]string)
	}

	if !p.Config.UseOldNamespace {
		metric.Attributes[podAttribute] = podInfo.Name
		metric.Attributes[namespaceAttribute] = podInfo.Namespace
		metric.Attributes[containerAttribute] = podInfo.Container
	} else {
		metric.Attributes[oldPodAttribute] = podInfo.Name
		metric.Attributes[oldNamespaceAttribute] = podInfo.Namespace
		metric.Attributes[oldContainerAttribute] = podInfo.Container
	}

	if p.Config.KubernetesEnablePodUID {
		metric.Attributes[uidAttribute] = podInfo.UID
	}
}

// copyPodLabels detaches labels and renames collisions without overwriting metric identity.
func copyPodLabels(
	metric *collector.Metric,
	podLabels map[string]string,
	metricGroup dcgm.Field_Entity_Group,
) {
	// Collector metrics can share label maps, so detach before adding pod labels.
	labels := make(map[string]string, len(metric.Labels)+len(podLabels))
	for k, v := range metric.Labels {
		labels[k] = v
	}
	metric.Labels = labels

	podLabelKeys := make([]string, 0, len(podLabels))
	for k := range podLabels {
		podLabelKeys = append(podLabelKeys, k)
	}
	slices.Sort(podLabelKeys)

	var collidingPodLabelKeys []string
	for _, k := range podLabelKeys {
		if isLabelNameInUse(*metric, metricGroup, k) {
			collidingPodLabelKeys = append(collidingPodLabelKeys, k)
			continue
		}
		metric.Labels[k] = podLabels[k]
	}

	for _, k := range collidingPodLabelKeys {
		metric.Labels[availablePodLabelName(*metric, metricGroup, k)] = podLabels[k]
	}
}

// availablePodLabelName finds an unused pod-label fallback within the metric group.
func availablePodLabelName(metric collector.Metric, metricGroup dcgm.Field_Entity_Group, label string) string {
	base := podLabelPrefix + label
	name := base
	for i := 1; isLabelNameInUse(metric, metricGroup, name); i++ {
		name = fmt.Sprintf("%s_conflict%d", base, i)
	}
	return name
}

// isLabelNameInUse checks renderer identity, attributes, and existing collector labels.
func isLabelNameInUse(metric collector.Metric, metricGroup dcgm.Field_Entity_Group, label string) bool {
	if isRendererReservedLabel(metric, metricGroup, label) {
		return true
	}
	if _, ok := metric.Attributes[label]; ok {
		return true
	}
	if _, ok := metric.Labels[label]; ok {
		return true
	}
	return false
}

// isRendererReservedLabel reports whether label would collide with a label the
// renderer adds for the metric's entity group.
func isRendererReservedLabel(metric collector.Metric, metricGroup dcgm.Field_Entity_Group, label string) bool {
	if label == "" {
		return false
	}
	return metriclabels.IsBuiltIn(metricGroup, label, metric.SelectedUUIDLabelName())
}

// connectToServer creates a Unix-socket kubelet client and returns its close callback.
func connectToServer(socket string) (*grpc.ClientConn, func(), error) {
	if err := validatePodResourcesSocketPath(socket); err != nil {
		return nil, doNothing, err
	}

	conn, err := grpc.NewClient(
		"passthrough:///"+socket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "unix", addr)
		}),
	)
	if err != nil {
		return nil, doNothing, fmt.Errorf("failure connecting to '%s'; err: %w", socket, err)
	}

	return conn, func() { conn.Close() }, nil
}

// validatePodResourcesSocketPath requires an absolute path identifying a Unix socket.
func validatePodResourcesSocketPath(socketPath string) error {
	if !filepath.IsAbs(socketPath) {
		return fmt.Errorf("pod-resources kubelet socket path %q must be absolute", socketPath)
	}

	return checkUnixSocket(socketPath, "pod-resources kubelet socket path")
}

// checkUnixSocket requires a path that exists and is a Unix socket. what names
// the path in the error.
func checkUnixSocket(path, what string) error {
	info, err := stdos.Stat(path) //nolint:gosec // G703: the path is an operator-configured socket path, only stat is done
	if err != nil {
		return err
	}

	if info.Mode()&stdos.ModeSocket == 0 {
		return fmt.Errorf("%s %q must be a Unix socket", what, path)
	}

	return nil
}

// listPods bounds the kubelet request time and received pod-resource message size.
func (p *PodMapper) listPods(conn *grpc.ClientConn) (*podresourcesapi.ListPodResourcesResponse, error) {
	client := podresourcesapi.NewPodResourcesListerClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	resp, err := client.List(
		ctx,
		&podresourcesapi.ListPodResourcesRequest{},
		grpc.MaxCallRecvMsgSize(kubeletPodResourcesMaxRecvMsgSize),
	)
	if err != nil {
		return nil, fmt.Errorf("failure getting pod resources; err: %w", err)
	}

	return resp, nil
}

// getSharedGPU parses the provided device ID and extracts the shared
// GPU identifier along with a boolean indicating if an identifier was
// found.
func getSharedGPU(deviceID string) (string, bool) {
	// Check if we're using the GKE device plugin or NVIDIA device plugin.
	if strings.Contains(deviceID, gkeVirtualGPUDeviceIDSeparator) {
		return strings.Split(deviceID, gkeVirtualGPUDeviceIDSeparator)[1], true
	} else if strings.Contains(deviceID, "::") {
		return strings.Split(deviceID, "::")[1], true
	}
	return "", false
}

// stripVGPUSuffix removes the ::N suffix from device IDs.
// AWS EKS with NVIDIA device plugin reports MIG devices as:
//
//	MIG-2ce7a541-c516-5dbc-a76e-26cc100d9b55::7
//
// This function strips the ::N suffix to get the MIG UUID for NVML lookups.
func stripVGPUSuffix(deviceID string) string {
	if base, _, found := strings.Cut(deviceID, "::"); found {
		return base
	}
	return deviceID
}

// draMappingKey returns the metric key for a DRA device. A whole GPU keeps its
// key; a MIG device is resolved to a GPU instance through NVML and accepted
// only when that instance exists in the current DCGM snapshot.
func draMappingKey(
	pool, device, mappingKey string,
	migInfo *DRAMigDeviceInfo,
	deviceInfo deviceinfo.Provider,
	migLookups *migLookupCache,
) string {
	if migInfo == nil {
		return mappingKey
	}
	if migInfo.MIGDeviceUUID == "" && migInfo.spec == nil {
		_, parseErr := parseDRAMigSpec(device)
		if parseErr == nil {
			parseErr = fmt.Errorf("device name %q did not produce a dynamic MIG specification", device)
		}
		slog.Debug("Skipping dynamic DRA MIG mapping without a resolvable device specification",
			"pool", pool,
			"device", device,
			"parentUUID", migInfo.ParentUUID,
			"profile", migInfo.Profile,
			"error", parseErr)
		return ""
	}
	if deviceInfo == nil {
		if migInfo.MIGDeviceUUID == "" && migInfo.spec != nil {
			slog.Debug("Skipping dynamic DRA MIG mapping without device info",
				"pool", pool,
				"device", device,
				"parentUUID", migInfo.ParentUUID,
				"parentMinor", migInfo.spec.ParentMinor,
				"profileID", migInfo.spec.ProfileID,
				"placementStart", migInfo.spec.PlacementStart)
			return ""
		}
		slog.Debug("Skipping DRA MIG mapping without device info",
			"pool", pool,
			"device", device,
			"migUUID", migInfo.MIGDeviceUUID,
			"parentUUID", migInfo.ParentUUID)
		return ""
	}

	if migInfo.MIGDeviceUUID == "" {
		return dynamicDRAMIGKey(pool, device, migInfo, deviceInfo)
	}

	return existingDRAMIGKey(pool, device, migInfo, deviceInfo, migLookups)
}

// dynamicDRAMIGKey resolves a UUID-less dynamic MIG placement to the GPU
// instance that NVML reports at that placement.
func dynamicDRAMIGKey(pool, device string, migInfo *DRAMigDeviceInfo, deviceInfo deviceinfo.Provider) string {
	gpuInstanceID, err := nvmlprovider.Client().GetGPUInstanceIDByProfileAndPlacement(
		migInfo.ParentUUID,
		migInfo.spec.ParentMinor,
		migInfo.spec.ProfileID,
		migInfo.spec.PlacementStart,
	)
	if err != nil {
		slog.Debug("Skipping dynamic DRA MIG mapping without a matching GPU instance",
			"pool", pool,
			"device", device,
			"parentUUID", migInfo.ParentUUID,
			"parentMinor", migInfo.spec.ParentMinor,
			"profileID", migInfo.spec.ProfileID,
			"placementStart", migInfo.spec.PlacementStart,
			"error", err)
		return ""
	}

	giIdentifier, parentFound := draGPUInstanceIdentifier(deviceInfo, migInfo.ParentUUID, gpuInstanceID)
	if giIdentifier == "" {
		if parentFound {
			slog.Debug("Skipping dynamic DRA MIG mapping without a matching GPU instance in the DCGM snapshot",
				"pool", pool,
				"device", device,
				"parentUUID", migInfo.ParentUUID,
				"parentMinor", migInfo.spec.ParentMinor,
				"profileID", migInfo.spec.ProfileID,
				"placementStart", migInfo.spec.PlacementStart,
				"gpuInstanceID", gpuInstanceID)
		} else {
			slog.Debug("Skipping dynamic DRA MIG mapping because the parent GPU is absent from the DCGM snapshot",
				"pool", pool,
				"device", device,
				"parentUUID", migInfo.ParentUUID,
				"parentMinor", migInfo.spec.ParentMinor,
				"profileID", migInfo.spec.ProfileID,
				"placementStart", migInfo.spec.PlacementStart,
				"gpuInstanceID", gpuInstanceID)
		}
	}
	return giIdentifier
}

// existingDRAMIGKey resolves a DRA claim for an already created MIG device by
// its UUID.
func existingDRAMIGKey(
	pool, device string,
	migInfo *DRAMigDeviceInfo,
	deviceInfo deviceinfo.Provider,
	migLookups *migLookupCache,
) string {
	migDevice, err := migLookups.lookup(migInfo.MIGDeviceUUID)
	if err != nil {
		slog.Debug("Skipping DRA MIG mapping without resolvable MIG UUID",
			"pool", pool,
			"device", device,
			"migUUID", migInfo.MIGDeviceUUID,
			"error", err)
		return ""
	}

	if migDevice == nil {
		slog.Debug("Skipping DRA MIG mapping without MIG device info",
			"pool", pool,
			"device", device,
			"migUUID", migInfo.MIGDeviceUUID)
		return ""
	}
	if migDevice.GPUInstanceID < 0 {
		slog.Debug("Skipping DRA MIG mapping with negative GPU instance ID",
			"pool", pool,
			"device", device,
			"migUUID", migInfo.MIGDeviceUUID,
			"gpuInstanceID", migDevice.GPUInstanceID)
		return ""
	}

	giIdentifier := deviceinfo.GetGPUInstanceIdentifier(
		deviceInfo,
		migDevice.ParentUUID,
		uint(migDevice.GPUInstanceID), //nolint:gosec // G115: bounds checked above
	)
	if giIdentifier == "" {
		slog.Debug("Skipping DRA MIG mapping without matching GPU instance",
			"pool", pool,
			"device", device,
			"migUUID", migInfo.MIGDeviceUUID,
			"parentUUID", migDevice.ParentUUID,
			"gpuInstanceID", migDevice.GPUInstanceID)
	}
	return giIdentifier
}

// draGPUInstanceIdentifier returns an identifier only when the resolved instance exists in the DCGM snapshot.
// The boolean reports whether the parent GPU was present in that snapshot.
func draGPUInstanceIdentifier(
	deviceInfo deviceinfo.Provider,
	parentUUID string,
	gpuInstanceID uint,
) (string, bool) {
	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		gpu := deviceInfo.GPU(i)
		if gpu.DeviceInfo.UUID != parentUUID {
			continue
		}
		for _, instance := range gpu.GPUInstances {
			if instance.Info.NvmlInstanceId == gpuInstanceID {
				return deviceinfo.GetGPUInstanceIdentifier(deviceInfo, parentUUID, gpuInstanceID), true
			}
		}
		return "", true
	}
	return "", false
}

// draMIGResolutionKey identifies a dynamic MIG placement for per-scrape resolution caching.
type draMIGResolutionKey struct {
	parentUUID     string
	parentMinor    uint32
	profileID      uint32
	placementStart uint32
}

// toDeviceToPodsDRA resolves NVIDIA claims and keeps one pod/container match per device.
func (p *PodMapper) toDeviceToPodsDRA(devicePods *podresourcesapi.ListPodResourcesResponse, deviceInfo deviceinfo.Provider) map[string][]PodInfo {
	deviceToPodsMap := make(map[string][]PodInfo)

	resolver := &draClaimResolver{
		slices:     p.draManager(),
		deviceInfo: deviceInfo,
		migCache:   make(map[draMIGResolutionKey]string),
		migLookups: p.migLookups,
	}

	slog.Debug("Processing pod dynamic resources", "totalPods", len(devicePods.GetPodResources()))

	// Track pod+namespace+container combinations per device
	// device mapping key -> "podName/namespace/containerName" -> bool
	processedPods := make(map[string]map[string]bool)

	for _, pod := range devicePods.GetPodResources() {
		for _, container := range pod.GetContainers() {
			slog.Debug("Processing container",
				"podName", pod.GetName(),
				"namespace", pod.GetNamespace(),
				"containerName", container.GetName())

			// Without DRA discovery running there is nothing to resolve a claim with.
			if len(container.GetDynamicResources()) == 0 || resolver.slices == nil {
				continue
			}

			p.addDRAContainerMappings(pod, container, resolver, processedPods, deviceToPodsMap)
		}
	}

	slog.Debug("Completed toDeviceToPodsDRA transformation",
		"totalMappings", len(deviceToPodsMap),
		"deviceToPodsMap", deviceToPodsMap)

	return deviceToPodsMap
}

// draClaimResolver turns a DRA claim's pool and device into the key that metrics
// use, caching dynamic MIG resolutions for one pass over the pod resources.
type draClaimResolver struct {
	slices     *DRAResourceSliceManager
	deviceInfo deviceinfo.Provider
	migCache   map[draMIGResolutionKey]string
	migLookups *migLookupCache
}

// resolve returns the metric mapping key and MIG details for a claimed device,
// or an empty key when the device cannot be mapped.
func (r *draClaimResolver) resolve(poolName, deviceName string) (string, *DRAMigDeviceInfo) {
	mappingKey, migInfo := r.slices.GetDeviceInfo(poolName, deviceName)
	if mappingKey == "" {
		slog.Debug("No UUID for DRA device", "pool", poolName, "device", deviceName)
		return "", nil
	}

	// A whole GPU or an already created MIG device needs no dynamic resolution.
	if migInfo == nil || migInfo.MIGDeviceUUID != "" || migInfo.spec == nil {
		return draMappingKey(poolName, deviceName, mappingKey, migInfo, r.deviceInfo, r.migLookups), migInfo
	}

	// A dynamic MIG device is resolved once per pass and then reused.
	cacheKey := draMIGResolutionKey{
		parentUUID:     migInfo.ParentUUID,
		parentMinor:    migInfo.spec.ParentMinor,
		profileID:      migInfo.spec.ProfileID,
		placementStart: migInfo.spec.PlacementStart,
	}

	if cached, ok := r.migCache[cacheKey]; ok {
		return cached, migInfo
	}

	resolved := draMappingKey(poolName, deviceName, mappingKey, migInfo, r.deviceInfo, r.migLookups)
	r.migCache[cacheKey] = resolved

	return resolved, migInfo
}

// newDynamicResourceInfo describes one claimed device for a pod match.
func newDynamicResourceInfo(
	dr *podresourcesapi.DynamicResource,
	claimResource *podresourcesapi.ClaimResource,
	mappingKey string,
	migInfo *DRAMigDeviceInfo,
	podContainerKey string,
) *DynamicResourceInfo {
	drInfo := &DynamicResourceInfo{
		ClaimName:      dr.GetClaimName(),
		ClaimNamespace: dr.GetClaimNamespace(),
		DriverName:     claimResource.GetDriverName(),
		PoolName:       claimResource.GetPoolName(),
		DeviceName:     claimResource.GetDeviceName(),
	}

	if migInfo == nil {
		slog.Debug("Added GPU pod mapping", "deviceUUID", mappingKey, "pod", podContainerKey)
		return drInfo
	}

	drInfo.MIGInfo = migInfo

	slog.Debug("Added MIG pod mapping",
		"gpuInstanceKey", mappingKey,
		"parentUUID", migInfo.ParentUUID,
		"migDevice", migInfo.MIGDeviceUUID,
		"migProfile", migInfo.Profile,
		"pod", podContainerKey)

	return drInfo
}

// addDRAContainerMappings adds one pod match for each NVIDIA device claimed by
// the container, skipping a pod and container already seen for that device.
func (p *PodMapper) addDRAContainerMappings(
	pod *podresourcesapi.PodResources,
	container *podresourcesapi.ContainerResources,
	resolver *draClaimResolver,
	processedPods map[string]map[string]bool,
	deviceToPodsMap map[string][]PodInfo,
) {
	podContainerKey := pod.GetName() + "/" + pod.GetNamespace() + "/" + container.GetName()

	for _, dr := range container.GetDynamicResources() {
		for _, claimResource := range dr.GetClaimResources() {
			if claimResource.GetDriverName() != DRAGPUDriverName {
				continue
			}

			mappingKey, migInfo := resolver.resolve(claimResource.GetPoolName(), claimResource.GetDeviceName())
			if mappingKey == "" {
				continue
			}

			if processedPods[mappingKey] == nil {
				processedPods[mappingKey] = make(map[string]bool)
			}

			if processedPods[mappingKey][podContainerKey] {
				continue
			}

			podInfo := p.createPodInfo(pod, container)
			podInfo.DynamicResources = newDynamicResourceInfo(dr, claimResource, mappingKey, migInfo, podContainerKey)
			deviceToPodsMap[mappingKey] = append(deviceToPodsMap[mappingKey], podInfo)
			processedPods[mappingKey][podContainerKey] = true
		}
	}
}

// toDeviceToSharingPods maps each supported kubelet device alias to every pod
// that shares it. Virtual-GPU allocation keeps all matches instead of choosing
// one pod as the dedicated-device path does.
func (p *PodMapper) toDeviceToSharingPods(devicePods *podresourcesapi.ListPodResourcesResponse, deviceInfo deviceinfo.Provider) map[string][]PodInfo {
	deviceToPodsMap := make(map[string][]PodInfo)

	p.iterateGPUDevices(devicePods, func(pod *podresourcesapi.PodResources, container *podresourcesapi.ContainerResources, device *podresourcesapi.ContainerDevices) {
		podInfo := p.createPodInfo(pod, container)
		if p.Config.EnableOTLP {
			podInfo.DevicePluginResourceName = device.GetResourceName()
		}

		for _, deviceID := range device.GetDeviceIds() {
			if vgpu, ok := getSharedGPU(deviceID); ok {
				podInfo.VGPU = vgpu
			}
			for _, key := range p.deviceMappingKeys(deviceID, deviceInfo) {
				deviceToPodsMap[key] = append(deviceToPodsMap[key], podInfo)
			}
		}
	})

	return deviceToPodsMap
}

// toDeviceToPod maps each supported kubelet device alias to its dedicated pod.
// When kubelet reports a GPU, MIG, GKE, or virtual-GPU identifier, the shared
// resolver supplies every equivalent metric identifier used by collectors.
func (p *PodMapper) toDeviceToPod(
	devicePods *podresourcesapi.ListPodResourcesResponse, deviceInfo deviceinfo.Provider,
) map[string]PodInfo {
	deviceToPodMap := make(map[string]PodInfo)
	// Kubelet may report several GPU resource groups for the same container.
	podInfoByContainer := make(map[*podresourcesapi.ContainerResources]PodInfo)
	p.iterateGPUDevices(devicePods, func(pod *podresourcesapi.PodResources, container *podresourcesapi.ContainerResources, device *podresourcesapi.ContainerDevices) {
		podInfo, found := podInfoByContainer[container]
		if !found {
			podInfo = p.createPodInfo(pod, container)
			podInfoByContainer[container] = podInfo
		}
		if p.Config.EnableOTLP {
			podInfo.DevicePluginResourceName = device.GetResourceName()
		}
		for _, deviceID := range device.GetDeviceIds() {
			for _, key := range p.deviceMappingKeys(deviceID, deviceInfo) {
				deviceToPodMap[key] = podInfo
			}
		}
	})
	return deviceToPodMap
}

// createPodInfo creates a PodInfo struct with metadata if enabled
func (p *PodMapper) createPodInfo(pod *podresourcesapi.PodResources, container *podresourcesapi.ContainerResources) PodInfo {
	info := PodInfo{
		Name:      pod.GetName(),
		Namespace: pod.GetNamespace(),
		Container: container.GetName(),
		Labels:    map[string]string{},
	}
	if p.podLister == nil {
		return info
	}
	podObj, err := p.podLister.Pods(pod.GetNamespace()).Get(pod.GetName())
	if err != nil {
		slog.Debug("Could not find pod in informer cache",
			"pod", pod.GetName(),
			"namespace", pod.GetNamespace(),
			"error", err)
		return info
	}
	info.UID = string(podObj.UID)
	if !p.Config.KubernetesEnablePodLabels {
		return info
	}
	originalLabels := info.Labels
	for k, v := range podObj.Labels {
		if !p.shouldIncludeLabel(k) {
			continue
		}
		originalLabels[k] = v
	}
	info.Labels = sanitizePodLabels(originalLabels)
	if p.Config.EnableOTLP && len(originalLabels) > 0 {
		info.OriginalLabels = originalLabels
	}
	return info
}

// sanitizePodLabels preserves all values and gives collisions stable names for unchanged input.
func sanitizePodLabels(originalLabels map[string]string) map[string]string {
	labels := make(map[string]string, len(originalLabels))
	var collidingKeys []string
	for _, key := range slices.Sorted(maps.Keys(originalLabels)) {
		name := utils.SanitizeLabelName(key)
		if _, exists := labels[name]; exists {
			collidingKeys = append(collidingKeys, key)
			continue
		}
		labels[name] = originalLabels[key]
	}

	// Reserve every base name before generating suffixes so real suffix-shaped keys survive.
	nextConflict := make(map[string]int)
	for _, key := range collidingKeys {
		base := utils.SanitizeLabelName(key)
		for {
			nextConflict[base]++
			name := fmt.Sprintf("%s_conflict%d", base, nextConflict[base])
			if _, exists := labels[name]; exists {
				continue
			}
			labels[name] = originalLabels[key]
			break
		}
	}
	return labels
}

// shouldIncludeLabel checks if a label should be included based on the allowlist regex patterns.
// Uses an LRU cache to avoid expensive regex matching while bounding memory:
// 1. Check cache for previously evaluated label keys
// 2. If not cached, evaluate against pre-compiled regex patterns and cache the result
func (p *PodMapper) shouldIncludeLabel(labelKey string) bool {
	cache := p.labelFilterCache

	if !cache.enabled {
		return true
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	// Check if labelKey is in cache
	if elem, exists := cache.cache[labelKey]; exists {
		// Cache hit: move to most recently used and return cached value
		cache.lruList.MoveToFront(elem)
		entry := elem.Value.(*labelCacheEntry)
		return entry.value
	}

	allowed := false
	for _, compiledPattern := range cache.compiledPatterns {
		if compiledPattern.MatchString(labelKey) {
			allowed = true
			break
		}
	}

	entry := &labelCacheEntry{
		key:   labelKey,
		value: allowed,
	}

	// If cache is at capacity, evict least recently used entry
	if cache.lruList.Len() >= cache.maxSize {
		oldest := cache.lruList.Back()
		if oldest != nil {
			cache.lruList.Remove(oldest)
			oldEntry := oldest.Value.(*labelCacheEntry)
			delete(cache.cache, oldEntry.key)
		}
	}

	// Add new entry to front (most recently used)
	elem := cache.lruList.PushFront(entry)
	cache.cache[labelKey] = elem

	return allowed
}

// preservePodSource applies raw pod identity to an owned metric source.
// Dedicated, DRA, sharing and per-process enrichment cloneMetric before mutation.
func preservePodSource(metric *collector.Metric, pod PodInfo) {
	if metric.Source == nil {
		return
	}
	metric.Source.PodLabels = maps.Clone(pod.OriginalLabels)
	metric.Source.DevicePluginResourceName = pod.DevicePluginResourceName
}
