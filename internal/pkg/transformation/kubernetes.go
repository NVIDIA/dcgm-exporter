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
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc/resolver"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

var (
	connectionTimeout = 10 * time.Second

	// Allow for MIG devices with or without GPU sharing to match in GKE.
	gkeMigDeviceIDRegex            = regexp.MustCompile(`^nvidia([0-9]+)/gi([0-9]+)(/vgpu[0-9]+)?$`)
	gkeVirtualGPUDeviceIDSeparator = "/vgpu"
)

const (
	saTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	saCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
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

func NewPodMapper(c *appconfig.Config) *PodMapper {
	slog.Info("Kubernetes metrics collection enabled!")

	// Default cache size if not configured
	cacheSize := c.KubernetesPodLabelCacheSize
	if cacheSize <= 0 {
		cacheSize = 150000 // Default: ~18MB for 150k entries (suitable for large cloud clusters)
	}

	podMapper := &PodMapper{
		Config:           c,
		labelFilterCache: newLabelFilterCache(c.KubernetesPodLabelAllowlistRegex, cacheSize),
	}

	// If using kubelet API, we don't need apiserver client for labels/UID
	if c.KubernetesUseKubeletAPI {
		slog.Info("Using kubelet API for pod metadata instead of apiserver")
	}

	if !c.KubernetesEnablePodLabels && !c.KubernetesEnablePodUID && !c.KubernetesEnableDRA {
		return podMapper
	}

	clusterConfig, err := rest.InClusterConfig()
	if err != nil {
		slog.Warn("Failed to get in-cluster config, pod labels will not be available", "error", err)
		return podMapper
	}

	clientset, err := kubernetes.NewForConfig(clusterConfig)
	if err != nil {
		slog.Warn("Failed to get clientset, pod labels will not be available", "error", err)
		return podMapper
	}

	podMapper.Client = clientset

	if c.KubernetesEnableDRA {
		resourceSliceManager, err := NewDRAResourceSliceManager()
		if err != nil {
			slog.Warn("Failed to get DRAResourceSliceManager, DRA pod labels will not be available", "error", err)
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
			slog.Warn("Failed to compile pod label allowlist regex pattern, skipping",
				"pattern", pattern,
				"error", err)
			continue
		}
		cache.compiledPatterns = append(cache.compiledPatterns, compiled)
		slog.Info("Compiled pod label allowlist pattern", "pattern", pattern)
	}

	// If all patterns failed to compile, disable filtering
	if len(cache.compiledPatterns) == 0 {
		cache.enabled = false
		slog.Warn("No valid regex patterns for pod label filtering, all labels will be included")
	} else {
		slog.Info("Pod label filtering enabled",
			"patterns", len(cache.compiledPatterns),
			"originalPatterns", len(patterns),
			"cacheSize", maxSize)
	}

	return cache
}

func (p *PodMapper) Name() string {
	return "podMapper"
}

func (p *PodMapper) Process(metrics collector.MetricsByCounter, deviceInfo deviceinfo.Provider) error {
	socketPath := p.Config.PodResourcesKubeletSocket
	_, err := os.Stat(socketPath)
	if os.IsNotExist(err) {
		slog.Info("No Kubelet socket, ignoring")
		return nil
	}

	// TODO: This needs to be moved out of the critical path.
	c, cleanup, err := connectToServer(socketPath)
	if err != nil {
		return err
	}
	defer cleanup()

	pods, err := p.listPods(c)
	if err != nil {
		return err
	}

	// Log detailed GPU allocation information for debugging purposes
	slog.Debug("Pod resources API response details",
		"podsWithResources", len(pods.GetPodResources()),
		"fullResponse", fmt.Sprintf("%+v", pods))

	// Log device plugin status and GPU allocation details
	totalGPUsAllocated := 0
	totalContainersWithGPUs := 0
	podGPUCounts := make(map[string]int) // Track GPU count per pod

	p.iterateGPUDevices(pods, func(pod *podresourcesapi.PodResources, container *podresourcesapi.ContainerResources, device *podresourcesapi.ContainerDevices) {
		podKey := pod.GetNamespace() + "/" + pod.GetName()
		podGPUCounts[podKey] += len(device.GetDeviceIds())
		totalContainersWithGPUs++
		slog.Debug("Found GPU device allocation",
			"pod", pod.GetName(),
			"namespace", pod.GetNamespace(),
			"container", container.GetName(),
			"resourceName", device.GetResourceName(),
			"deviceIds", device.GetDeviceIds())
	})

	// Log per-pod GPU allocation status
	for _, pod := range pods.GetPodResources() {
		podKey := pod.GetNamespace() + "/" + pod.GetName()
		podGPUs := podGPUCounts[podKey]
		if podGPUs > 0 {
			totalGPUsAllocated += podGPUs
			slog.Debug("Pod has GPU allocations",
				"pod", pod.GetName(),
				"namespace", pod.GetNamespace(),
				"totalGPUs", podGPUs)
		} else {
			slog.Debug("Pod has NO GPU allocations",
				"pod", pod.GetName(),
				"namespace", pod.GetNamespace(),
				"totalContainers", len(pod.GetContainers()))
		}
	}

	slog.Debug("GPU allocation summary",
		"totalPods", len(pods.GetPodResources()),
		"totalGPUsAllocated", totalGPUsAllocated,
		"totalContainersWithGPUs", totalContainersWithGPUs,
		"devicePluginWorking", totalGPUsAllocated > 0)

	if p.Config.KubernetesVirtualGPUs {
		deviceToPods := p.toDeviceToSharingPods(pods, deviceInfo)

		slog.Debug(fmt.Sprintf("Device to sharing pods mapping: %+v", deviceToPods))

		// For each counter metric, init a slice to collect metrics to associate with shared virtual GPUs.
		for counter := range metrics {
			var newmetrics []collector.Metric
			// For each instrumented device, build list of metrics and create
			// new metrics for any shared GPUs.
			for j, val := range metrics[counter] {
				deviceID, err := val.GetIDOfType(p.Config.KubernetesGPUIdType)
				if err != nil {
					return err
				}

				podInfos := deviceToPods[deviceID]
				// For all containers using the GPU, extract and annotate a metric
				// with the container info and the shared GPU label, if it exists.
				// Notably, this will increase the number of unique metrics (i.e. labelsets)
				// to by the number of containers sharing the GPU.
				for _, pi := range podInfos {
					metric, err := utils.DeepCopy(metrics[counter][j])
					if err != nil {
						return err
					}
					if !p.Config.UseOldNamespace {
						metric.Attributes[podAttribute] = pi.Name
						metric.Attributes[namespaceAttribute] = pi.Namespace
						metric.Attributes[containerAttribute] = pi.Container
					} else {
						metric.Attributes[oldPodAttribute] = pi.Name
						metric.Attributes[oldNamespaceAttribute] = pi.Namespace
						metric.Attributes[oldContainerAttribute] = pi.Container
					}
					metric.Attributes[uidAttribute] = pi.UID
					if pi.VGPU != "" {
						metric.Attributes[vgpuAttribute] = pi.VGPU
					}
					newmetrics = append(newmetrics, metric)
				}
			}
			// Upsert the annotated series into the final map only if we found any
			// pods using the devices for the metric. Otherwise, leave the original
			// metric unmodified so we still have monitoring when pods aren't using
			// GPUs.
			if len(newmetrics) > 0 {
				metrics[counter] = newmetrics
			}
		}
		return nil
	}

	slog.Debug("KubernetesVirtualGPUs is disabled, using device to pod mapping")

	deviceToPod := p.toDeviceToPod(pods, deviceInfo)

	slog.Debug(fmt.Sprintf("Device to pod mapping: %+v", deviceToPod))

	// Note: for loop are copies the value, if we want to change the value
	// and not the copy, we need to use the indexes
	for counter := range metrics {
		for j, val := range metrics[counter] {
			deviceID, err := val.GetIDOfType(p.Config.KubernetesGPUIdType)
			if err != nil {
				return err
			}
			podInfo, exists := deviceToPod[deviceID]
			if exists {
				if !p.Config.UseOldNamespace {
					metrics[counter][j].Attributes[podAttribute] = podInfo.Name
					metrics[counter][j].Attributes[namespaceAttribute] = podInfo.Namespace
					metrics[counter][j].Attributes[containerAttribute] = podInfo.Container
				} else {
					metrics[counter][j].Attributes[oldPodAttribute] = podInfo.Name
					metrics[counter][j].Attributes[oldNamespaceAttribute] = podInfo.Namespace
					metrics[counter][j].Attributes[oldContainerAttribute] = podInfo.Container
				}

				metrics[counter][j].Attributes[uidAttribute] = podInfo.UID
				maps.Copy(metrics[counter][j].Labels, podInfo.Labels)
			}
		}
	}

	if p.Config.KubernetesEnableDRA {
		deviceToPodsDRA := p.toDeviceToPodsDRA(pods)
		slog.Debug(fmt.Sprintf("Device to pod mapping for DRA: %+v", deviceToPodsDRA))

		for counter := range metrics {
			var newmetrics []collector.Metric
			// For each instrumented device, build list of metrics and create
			// new metrics for any shared GPUs.
			for j, val := range metrics[counter] {
				deviceID, err := val.GetIDOfType(p.Config.KubernetesGPUIdType)
				if err != nil {
					return err
				}

				podInfos := deviceToPodsDRA[deviceID]
				// For all containers using the GPU, extract and annotate a metric
				// with the container info and the shared GPU label, if it exists.
				// Notably, this will increase the number of unique metrics (i.e. labelsets)
				// to by the number of containers sharing the GPU.
				if podInfos != nil {
					for _, pi := range podInfos {
						metric, err := utils.DeepCopy(metrics[counter][j])
						if err != nil {
							return err
						}
						if !p.Config.UseOldNamespace {
							metric.Attributes[podAttribute] = pi.Name
							metric.Attributes[namespaceAttribute] = pi.Namespace
							metric.Attributes[containerAttribute] = pi.Container
						} else {
							metric.Attributes[oldPodAttribute] = pi.Name
							metric.Attributes[oldNamespaceAttribute] = pi.Namespace
							metric.Attributes[oldContainerAttribute] = pi.Container
						}
						if dr := pi.DynamicResources; dr != nil {
							metric.Attributes[draClaimName] = dr.ClaimName
							metric.Attributes[draClaimNamespace] = dr.ClaimNamespace
							metric.Attributes[draDriverName] = dr.DriverName
							metric.Attributes[draPoolName] = dr.PoolName
							metric.Attributes[draDeviceName] = dr.DeviceName

							// Add MIG-specific labels if this is a MIG device
							if migInfo := dr.MIGInfo; migInfo != nil {
								metric.Attributes[draMigProfile] = migInfo.Profile
								metric.Attributes[draMigDeviceUUID] = migInfo.MIGDeviceUUID
							}
						}
						newmetrics = append(newmetrics, metric)
					}
				} else {
					newmetrics = append(newmetrics, metrics[counter][j])
				}
			}
			// Upsert the annotated series into the final map only if we found any
			// pods using the devices for the metric. Otherwise, leave the original
			// metric unmodified so we still have monitoring when pods aren't using
			// GPUs.
			if len(newmetrics) > 0 {
				metrics[counter] = newmetrics
			}
		}
		return nil
	}

	return nil
}

func connectToServer(socket string) (*grpc.ClientConn, func(), error) {
	resolver.SetDefaultScheme("passthrough")
	conn, err := grpc.NewClient(
		socket,
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

func (p *PodMapper) listPods(conn *grpc.ClientConn) (*podresourcesapi.ListPodResourcesResponse, error) {
	client := podresourcesapi.NewPodResourcesListerClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	resp, err := client.List(ctx, &podresourcesapi.ListPodResourcesRequest{})
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

func (p *PodMapper) toDeviceToPodsDRA(devicePods *podresourcesapi.ListPodResourcesResponse) map[string][]PodInfo {
	deviceToPodsMap := make(map[string][]PodInfo)
	labelCache := make(map[string]PodMetadata) // Cache to avoid duplicate API calls

	slog.Debug("Processing pod dynamic resources", "totalPods", len(devicePods.GetPodResources()))
	// Track pod+namespace+container combinations per device
	// UUID -> "podName/namespace/containerName" -> bool
	processedPods := make(map[string]map[string]bool)

	for _, pod := range devicePods.GetPodResources() {
		podName := pod.GetName()
		podNamespace := pod.GetNamespace()
		for _, container := range pod.GetContainers() {
			cntName := container.GetName()
			slog.Debug("Processing container",
				"podName", podName,
				"namespace", podNamespace,
				"containerName", cntName)
			if dynamicResources := container.GetDynamicResources(); len(dynamicResources) > 0 && p.ResourceSliceManager != nil {
				for _, dr := range dynamicResources {
					for _, claimResource := range dr.GetClaimResources() {
						draDriverName := claimResource.GetDriverName()
						if draDriverName != DRAGPUDriverName {
							continue
						}
						draPoolName := claimResource.GetPoolName()
						draDeviceName := claimResource.GetDeviceName()

						mappingKey, migInfo := p.ResourceSliceManager.GetDeviceInfo(draPoolName, draDeviceName)
						if mappingKey == "" {
							slog.Debug(fmt.Sprintf("No UUID for %s/%s", draPoolName, draDeviceName))
							continue
						}

						// Create unique key for pod+namespace+container combination
						podContainerKey := podName + "/" + podNamespace + "/" + cntName

						// Initialize tracker for this device if needed
						if processedPods[mappingKey] == nil {
							processedPods[mappingKey] = make(map[string]bool)
						}

						// Skip if we already processed this pod+container for this device
						if processedPods[mappingKey][podContainerKey] {
							continue
						}

						podInfo := p.createPodInfo(pod, container, labelCache)
						drInfo := DynamicResourceInfo{
							ClaimName:      dr.GetClaimName(),
							ClaimNamespace: dr.GetClaimNamespace(),
							DriverName:     draDriverName,
							PoolName:       draPoolName,
							DeviceName:     draDeviceName,
						}
						if migInfo != nil {
							drInfo.MIGInfo = migInfo
							slog.Debug("Added MIG pod mapping",
								"parentUUID", mappingKey,
								"migDevice", migInfo.MIGDeviceUUID,
								"migProfile", migInfo.Profile,
								"pod", podContainerKey)
						} else {
							slog.Debug("Added GPU pod mapping",
								"deviceUUID", mappingKey,
								"pod", podContainerKey)
						}

						podInfo.DynamicResources = &drInfo
						deviceToPodsMap[mappingKey] = append(deviceToPodsMap[mappingKey], podInfo)
						processedPods[mappingKey][podContainerKey] = true
					}
				}
			}

		}
	}
	slog.Debug("Completed toDeviceToPodsDRA transformation",
		"totalMappings", len(deviceToPodsMap),
		"deviceToPodsMap", fmt.Sprintf("%+v", deviceToPodsMap))
	return deviceToPodsMap
}

// toDeviceToSharingPods uses the same general logic as toDeviceToPod but
// allows for multiple containers to be associated with a metric when sharing
// strategies are used in Kubernetes.
// TODO(pintohuch): the logic is manually duplicated from toDeviceToPod for
// better isolation and easier review. Ultimately, this logic should be
// merged into a single function that can handle both shared and non-shared
// GPU states.
func (p *PodMapper) toDeviceToSharingPods(devicePods *podresourcesapi.ListPodResourcesResponse, deviceInfo deviceinfo.Provider) map[string][]PodInfo {
	deviceToPodsMap := make(map[string][]PodInfo)
	metadataCache := make(map[string]PodMetadata) // Cache to avoid duplicate API calls

	p.iterateGPUDevices(devicePods, func(pod *podresourcesapi.PodResources, container *podresourcesapi.ContainerResources, device *podresourcesapi.ContainerDevices) {
		podInfo := p.createPodInfo(pod, container, metadataCache)

		for _, deviceID := range device.GetDeviceIds() {
			if vgpu, ok := getSharedGPU(deviceID); ok {
				podInfo.VGPU = vgpu
			}
			if strings.HasPrefix(deviceID, appconfig.MIG_UUID_PREFIX) {
				migDevice, err := nvmlprovider.Client().GetMIGDeviceInfoByID(deviceID)
				if err == nil {
					// Check for potential integer overflow before conversion
					if migDevice.GPUInstanceID >= 0 {
						giIdentifier := deviceinfo.GetGPUInstanceIdentifier(deviceInfo, migDevice.ParentUUID,
							uint(migDevice.GPUInstanceID))
						deviceToPodsMap[giIdentifier] = append(deviceToPodsMap[giIdentifier], podInfo)
					}
				}
				gpuUUID := deviceID[len(appconfig.MIG_UUID_PREFIX):]
				deviceToPodsMap[gpuUUID] = append(deviceToPodsMap[gpuUUID], podInfo)
			} else if gkeMigDeviceIDMatches := gkeMigDeviceIDRegex.FindStringSubmatch(deviceID); gkeMigDeviceIDMatches != nil {
				var gpuIndex string
				var gpuInstanceID string
				for groupIdx, group := range gkeMigDeviceIDMatches {
					switch groupIdx {
					case 1:
						gpuIndex = group
					case 2:
						gpuInstanceID = group
					}
				}
				giIdentifier := fmt.Sprintf("%s-%s", gpuIndex, gpuInstanceID)
				deviceToPodsMap[giIdentifier] = append(deviceToPodsMap[giIdentifier], podInfo)
			} else if strings.Contains(deviceID, gkeVirtualGPUDeviceIDSeparator) {
				deviceToPodsMap[strings.Split(deviceID, gkeVirtualGPUDeviceIDSeparator)[0]] = append(deviceToPodsMap[strings.Split(deviceID, gkeVirtualGPUDeviceIDSeparator)[0]], podInfo)
			} else if strings.Contains(deviceID, "::") {
				gpuInstanceID := strings.Split(deviceID, "::")[0]
				deviceToPodsMap[gpuInstanceID] = append(deviceToPodsMap[gpuInstanceID], podInfo)
			}
			// Default mapping between deviceID and pod information
			deviceToPodsMap[deviceID] = append(deviceToPodsMap[deviceID], podInfo)
		}
	})

	return deviceToPodsMap
}

func (p *PodMapper) toDeviceToPod(
	devicePods *podresourcesapi.ListPodResourcesResponse, deviceInfo deviceinfo.Provider,
) map[string]PodInfo {
	deviceToPodMap := make(map[string]PodInfo)
	metadataCache := make(map[string]PodMetadata) // Cache to avoid duplicate API calls

	slog.Debug("Processing pod resources", "totalPods", len(devicePods.GetPodResources()))

	// Log all resource names found across all pods for debugging
	allResourceNames := make(map[string]bool)
	for _, pod := range devicePods.GetPodResources() {
		for _, container := range pod.GetContainers() {
			for _, device := range container.GetDevices() {
				allResourceNames[device.GetResourceName()] = true
			}
		}
	}
	if len(allResourceNames) > 0 {
		slog.Debug("Found resource names in pod resources", "resourceNames", maps.Keys(allResourceNames))
	} else {
		slog.Debug("No resource names found in any pod resources")
	}

	for _, pod := range devicePods.GetPodResources() {
		slog.Debug("Processing pod",
			"podName", pod.GetName(),
			"namespace", pod.GetNamespace(),
			"totalContainers", len(pod.GetContainers()))

		for _, container := range pod.GetContainers() {
			slog.Debug("Processing container",
				"podName", pod.GetName(),
				"namespace", pod.GetNamespace(),
				"containerName", container.GetName(),
				"totalDevices", len(container.GetDevices()))

			// Add debugging for containers with no devices
			if len(container.GetDevices()) == 0 {
				slog.Debug("Container has no devices allocated",
					"podName", pod.GetName(),
					"namespace", pod.GetNamespace(),
					"containerName", container.GetName())
			}

			podInfo := p.createPodInfo(pod, container, metadataCache)
			slog.Debug("Created pod info",
				"podInfo", fmt.Sprintf("%+v", podInfo),
				"podName", pod.GetName(),
				"namespace", pod.GetNamespace(),
				"containerName", container.GetName())

			for _, device := range container.GetDevices() {
				resourceName := device.GetResourceName()
				slog.Debug("Processing device",
					"podName", pod.GetName(),
					"namespace", pod.GetNamespace(),
					"containerName", container.GetName(),
					"resourceName", resourceName,
					"deviceIds", device.GetDeviceIds())

				if resourceName != appconfig.NvidiaResourceName && !slices.Contains(p.Config.NvidiaResourceNames, resourceName) {
					// Mig resources appear differently than GPU resources
					if !strings.HasPrefix(resourceName, appconfig.NvidiaMigResourcePrefix) {
						slog.Debug("Skipping non-NVIDIA resource",
							"resourceName", resourceName,
							"podName", pod.GetName(),
							"namespace", pod.GetNamespace(),
							"containerName", container.GetName(),
							"resourceName", resourceName,
							"deviceIds", device.GetDeviceIds(),
						)
						continue
					}
				}

				for _, deviceID := range device.GetDeviceIds() {
					slog.Debug("Processing device ID", "deviceID", deviceID,
						"podName", pod.GetName(),
						"namespace", pod.GetNamespace(),
						"containerName", container.GetName(),
						"resourceName", resourceName,
						"deviceIds", device.GetDeviceIds(),
					)

					if strings.HasPrefix(deviceID, appconfig.MIG_UUID_PREFIX) {
						slog.Debug("Processing MIG device", "deviceID", deviceID,
							"podName", pod.GetName(),
							"namespace", pod.GetNamespace(),
							"containerName", container.GetName(),
							"resourceName", resourceName,
							"deviceIds", device.GetDeviceIds(),
						)
						migDevice, err := nvmlprovider.Client().GetMIGDeviceInfoByID(deviceID)
						if err == nil {
							// Check for potential integer overflow before conversion
							if migDevice.GPUInstanceID >= 0 {
								giIdentifier := deviceinfo.GetGPUInstanceIdentifier(deviceInfo, migDevice.ParentUUID,
									uint(migDevice.GPUInstanceID))
								slog.Debug("Mapped MIG device to GPU instance",
									"deviceID", deviceID,
									"giIdentifier", giIdentifier,
									"podName", pod.GetName(),
									"namespace", pod.GetNamespace(),
									"containerName", container.GetName(),
									"resourceName", resourceName,
									"deviceIds", device.GetDeviceIds(),
								)
								deviceToPodMap[giIdentifier] = podInfo
							}
						} else {
							slog.Debug("Failed to get MIG device info",
								"deviceID", deviceID,
								"error", err,
								"podName", pod.GetName(),
								"namespace", pod.GetNamespace(),
								"containerName", container.GetName(),
								"resourceName", resourceName,
								"deviceIds", device.GetDeviceIds(),
							)
						}
						gpuUUID := deviceID[len(appconfig.MIG_UUID_PREFIX):]
						slog.Debug("Mapped MIG device to GPU UUID",
							"deviceID", deviceID,
							"gpuUUID", gpuUUID,
							"podName", pod.GetName(),
							"namespace", pod.GetNamespace(),
							"containerName", container.GetName(),
							"resourceName", resourceName,
							"deviceIds", device.GetDeviceIds(),
						)
						deviceToPodMap[gpuUUID] = podInfo
					} else if gkeMigDeviceIDMatches := gkeMigDeviceIDRegex.FindStringSubmatch(deviceID); gkeMigDeviceIDMatches != nil {
						slog.Debug("Processing GKE MIG device",
							"deviceID", deviceID,
							"matches", gkeMigDeviceIDMatches,
							"podName", pod.GetName(),
							"namespace", pod.GetNamespace(),
							"containerName", container.GetName(),
							"resourceName", resourceName,
							"deviceIds", device.GetDeviceIds(),
						)
						var gpuIndex string
						var gpuInstanceID string
						for groupIdx, group := range gkeMigDeviceIDMatches {
							switch groupIdx {
							case 1:
								gpuIndex = group
							case 2:
								gpuInstanceID = group
							}
						}
						giIdentifier := fmt.Sprintf("%s-%s", gpuIndex, gpuInstanceID)
						slog.Debug("Mapped GKE MIG device",
							"deviceID", deviceID,
							"giIdentifier", giIdentifier,
							"podName", pod.GetName(),
							"namespace", pod.GetNamespace(),
							"containerName", container.GetName(),
							"resourceName", resourceName,
							"deviceIds", device.GetDeviceIds(),
						)
						deviceToPodMap[giIdentifier] = podInfo
					} else if strings.Contains(deviceID, gkeVirtualGPUDeviceIDSeparator) {
						gpuID := strings.Split(deviceID, gkeVirtualGPUDeviceIDSeparator)[0]
						slog.Debug("Mapped GKE virtual GPU device",
							"deviceID", deviceID,
							"gpuID", gpuID,
							"podName", pod.GetName(),
							"namespace", pod.GetNamespace(),
							"containerName", container.GetName(),
							"resourceName", resourceName,
							"deviceIds", device.GetDeviceIds(),
						)
						deviceToPodMap[gpuID] = podInfo
					} else if strings.Contains(deviceID, "::") {
						gpuInstanceID := strings.Split(deviceID, "::")[0]
						slog.Debug("Mapped GPU instance device",
							"deviceID", deviceID,
							"gpuInstanceID", gpuInstanceID,
							"podName", pod.GetName(),
							"namespace", pod.GetNamespace(),
							"containerName", container.GetName(),
							"resourceName", resourceName,
							"deviceIds", device.GetDeviceIds(),
						)
						deviceToPodMap[gpuInstanceID] = podInfo
					}
					// Default mapping between deviceID and pod information
					slog.Debug("Default device mapping",
						"deviceID", deviceID,
						"podName", pod.GetName(),
						"namespace", pod.GetNamespace(),
						"containerName", container.GetName(),
						"resourceName", resourceName,
						"deviceIds", device.GetDeviceIds(),
					)
					deviceToPodMap[deviceID] = podInfo
				}
			}
		}
	}

	slog.Debug("Completed toDeviceToPod transformation",
		"totalMappings", len(deviceToPodMap),
		"deviceToPodMap", fmt.Sprintf("%+v", deviceToPodMap))
	return deviceToPodMap
}

// createPodInfo creates a PodInfo struct with metadata if enabled
func (p *PodMapper) createPodInfo(pod *podresourcesapi.PodResources, container *podresourcesapi.ContainerResources, metadataCache map[string]PodMetadata) PodInfo {
	labels := map[string]string{}
	uid := ""
	cacheKey := pod.GetNamespace() + "/" + pod.GetName()

	// Check if we have cached metadata
	cachedMetadata, hasCache := metadataCache[cacheKey]

	// Determine if we need labels
	needLabels := p.Config.KubernetesEnablePodLabels && (cachedMetadata.Labels == nil)

	// Determine if we need UID
	needUID := p.Config.KubernetesEnablePodUID && cachedMetadata.UID == ""

	// Only make API call if we need something that's not cached
	if needLabels || needUID {
		var podMetadata *PodMetadata
		var err error

		// Choose data source based on config: kubelet API or apiserver
		if p.Config.KubernetesUseKubeletAPI {
			podMetadata, err = p.getPodMetadataFromKubelet(pod.GetNamespace(), pod.GetName())
		} else {
			podMetadata, err = p.getPodMetadata(pod.GetNamespace(), pod.GetName())
		}

		if err != nil {
			slog.Warn("Couldn't get pod metadata",
				"pod", pod.GetName(),
				"namespace", pod.GetNamespace(),
				"source", map[bool]string{true: "kubelet", false: "apiserver"}[p.Config.KubernetesUseKubeletAPI],
				"error", err)
			// Cache empty result to avoid repeated failures, but preserve existing cache data
			if !hasCache {
				metadataCache[cacheKey] = PodMetadata{}
			}
		} else {
			// Update cache with new data, preserving existing data if we didn't fetch it
			if needLabels {
				cachedMetadata.Labels = podMetadata.Labels
			}
			if needUID {
				cachedMetadata.UID = podMetadata.UID
			}
			metadataCache[cacheKey] = cachedMetadata
		}
	}

	// Extract the data we need based on config flags
	if p.Config.KubernetesEnablePodLabels {
		labels = cachedMetadata.Labels
	}
	if p.Config.KubernetesEnablePodUID {
		uid = cachedMetadata.UID
	}

	return PodInfo{
		Name:      pod.GetName(),
		Namespace: pod.GetNamespace(),
		Container: container.GetName(),
		UID:       uid,
		Labels:    labels,
	}
}

// getPodMetadataFromKubelet fetches metadata (labels and UID) from kubelet /pods API.
// It sanitizes label names to ensure they are valid for Prometheus metrics and applies allowlist filtering.
func (p *PodMapper) getPodMetadataFromKubelet(namespace, podName string) (*PodMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	// 1. Read ServiceAccount token and CA files
	tokenBytes, err := os.ReadFile(saTokenPath)
	if err != nil {
		// Log failure to read token to help diagnose mount or permission issues
		slog.Warn("Failed to read serviceaccount token for kubelet /pods",
			"path", saTokenPath,
			"error", err,
		)
		return nil, fmt.Errorf("failed to read serviceaccount token from %s: %w", saTokenPath, err)
	}
	token := strings.TrimSpace(string(tokenBytes))

	caPEM, err := os.ReadFile(saCAPath)
	if err != nil {
		// Log failure to read CA file
		slog.Warn("Failed to read serviceaccount CA for kubelet /pods",
			"path", saCAPath,
			"error", err,
		)
		return nil, fmt.Errorf("failed to read serviceaccount CA from %s: %w", saCAPath, err)
	}

	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caPEM) {
		// Log failure when CA certificate cannot be parsed/appended
		slog.Warn("Failed to append CA certs for kubelet /pods",
			"path", saCAPath,
		)
		return nil, fmt.Errorf("failed to append CA certs from %s", saCAPath)
	}

	// 2. Build HTTPS client
	tlsCfg := &tls.Config{
		RootCAs: rootCAs,
	}
	client := &http.Client{
		Timeout:   connectionTimeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}

	// 3. Build kubelet /pods URL
	base := p.Config.KubernetesKubeletURL
	if base == "" {
		base = "https://127.0.0.1:10250"
	}
	url := strings.TrimRight(base, "/") + "/pods"

	// Record the actual kubelet URL being accessed to verify the target node
	slog.Debug("Querying kubelet /pods",
		"url", url,
		"namespace", namespace,
		"pod", podName,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		// Log failure to build HTTP request
		slog.Warn("Failed to build kubelet /pods request",
			"url", url,
			"error", err,
		)
		return nil, fmt.Errorf("failed to build kubelet /pods request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	// 4. Call kubelet
	resp, err := client.Do(req)
	if err != nil {
		// Log failure when requesting kubelet /pods
		slog.Warn("Failed to query kubelet /pods",
			"url", url,
			"error", err,
		)
		return nil, fmt.Errorf("failed to query kubelet /pods: %w", err)
	}
	defer resp.Body.Close()

	slog.Debug("Received response from kubelet /pods",
		"url", url,
		"statusCode", resp.StatusCode,
		"status", resp.Status,
	)

	if resp.StatusCode != http.StatusOK {
		// Log non-200 status codes to help diagnose 401/403/500 and similar issues
		slog.Warn("Unexpected status from kubelet /pods",
			"url", url,
			"statusCode", resp.StatusCode,
			"status", resp.Status,
		)
		return nil, fmt.Errorf("unexpected status from kubelet /pods: %s", resp.Status)
	}

	// 5. Decode PodList
	var podList corev1.PodList
	if err := json.NewDecoder(resp.Body).Decode(&podList); err != nil {
		// Log failure to decode kubelet /pods response
		slog.Warn("Failed to decode kubelet /pods response",
			"url", url,
			"error", err,
		)
		return nil, fmt.Errorf("failed to decode kubelet /pods response: %w", err)
	}

	// Log the number of pods returned by kubelet to verify whether the target pod is in kubelet's view
	slog.Debug("Decoded kubelet /pods response",
		"totalPods", len(podList.Items),
		"namespace", namespace,
		"pod", podName,
	)

	// 6. Find the specific pod
	for _, pod := range podList.Items {
		if pod.Namespace == namespace && pod.Name == podName {
			// Sanitize and filter label names (same logic as getPodMetadata)
			sanitizedLabels := make(map[string]string)
			for k, v := range pod.Labels {
				// Apply allowlist filtering if configured
				if !p.shouldIncludeLabel(k) {
					slog.Debug("Filtering out pod label",
						"label", k,
						"pod", podName,
						"namespace", namespace)
					continue
				}

				sanitizedKey := utils.SanitizeLabelName(k)
				sanitizedLabels[sanitizedKey] = v
			}

			// Log when the target pod is found, including UID and number of labels
			slog.Debug("Found pod in kubelet /pods response",
				"pod", podName,
				"namespace", namespace,
				"uid", string(pod.UID),
				"labelCount", len(sanitizedLabels),
			)

			return &PodMetadata{
				UID:    string(pod.UID),
				Labels: sanitizedLabels,
			}, nil
		}
	}

	// Log a warning when the target pod is not found in kubelet /pods response
	slog.Warn("Pod not found in kubelet /pods response",
		"pod", podName,
		"namespace", namespace,
		"totalPods", len(podList.Items),
	)

	return nil, fmt.Errorf("pod %s/%s not found in kubelet /pods response", namespace, podName)
}

// getPodMetadata fetches metadata (labels and UID) from a Kubernetes pod via the API server.
// It sanitizes label names to ensure they are valid for Prometheus metrics and applies allowlist filtering.
func (p *PodMapper) getPodMetadata(namespace, podName string) (*PodMetadata, error) {
	if p.Client == nil {
		return nil, fmt.Errorf("kubernetes client is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	pod, err := p.Client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	// Sanitize and filter label names
	sanitizedLabels := make(map[string]string)
	for k, v := range pod.Labels {
		// Apply allowlist filtering if configured
		if !p.shouldIncludeLabel(k) {
			slog.Debug("Filtering out pod label",
				"label", k,
				"pod", podName,
				"namespace", namespace)
			continue
		}

		sanitizedKey := utils.SanitizeLabelName(k)
		sanitizedLabels[sanitizedKey] = v
	}

	return &PodMetadata{
		UID:    string(pod.UID),
		Labels: sanitizedLabels,
	}, nil
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
