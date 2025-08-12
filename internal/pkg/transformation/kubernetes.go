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
	"maps"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc/resolver"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

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

	podMapper := &PodMapper{
		Config: c,
	}

	if !c.KubernetesEnablePodLabels && !c.KubernetesEnableDRA {
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
					if pi.VGPU != "" {
						metric.Attributes[vgpuAttribute] = pi.VGPU
					}
					newmetrics = append(newmetrics, metric)
				}
			}
			// Upsert the annotated metrics into the final map.
			metrics[counter] = newmetrics
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
						newmetrics = append(newmetrics, metric)
					}
				}
			}
			metrics[counter] = newmetrics
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
	labelCache := make(map[string]map[string]string) // Cache to avoid duplicate API calls

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
	labelCache := make(map[string]map[string]string) // Cache to avoid duplicate API calls

	p.iterateGPUDevices(devicePods, func(pod *podresourcesapi.PodResources, container *podresourcesapi.ContainerResources, device *podresourcesapi.ContainerDevices) {
		podInfo := p.createPodInfo(pod, container, labelCache)

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
	labelCache := make(map[string]map[string]string) // Cache to avoid duplicate API calls

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

			podInfo := p.createPodInfo(pod, container, labelCache)
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

// createPodInfo creates a PodInfo struct with labels if enabled
func (p *PodMapper) createPodInfo(pod *podresourcesapi.PodResources, container *podresourcesapi.ContainerResources, labelCache map[string]map[string]string) PodInfo {
	labels := map[string]string{}
	if p.Config.KubernetesEnablePodLabels {
		// Use cache key combining namespace and name
		cacheKey := pod.GetNamespace() + "/" + pod.GetName()
		if cachedLabels, exists := labelCache[cacheKey]; exists {
			labels = cachedLabels
		} else {
			// Only make API call if not in cache
			if podLabels, err := p.getPodLabels(pod.GetNamespace(), pod.GetName()); err != nil {
				slog.Warn("Couldn't get pod labels",
					"pod", pod.GetName(),
					"namespace", pod.GetNamespace(),
					"error", err)
				labelCache[cacheKey] = map[string]string{} // Cache empty result to avoid repeated failures
			} else {
				labels = podLabels
				labelCache[cacheKey] = podLabels // Cache successful result
			}
		}
	}

	return PodInfo{
		Name:      pod.GetName(),
		Namespace: pod.GetNamespace(),
		Container: container.GetName(),
		Labels:    labels,
	}
}

// getPodLabels fetches labels from a Kubernetes pod via the API server.
// It sanitizes label names to ensure they are valid for Prometheus metrics.
func (p *PodMapper) getPodLabels(namespace, podName string) (map[string]string, error) {
	if p.Client == nil {
		return nil, fmt.Errorf("kubernetes client is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	pod, err := p.Client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	// Sanitize label names
	sanitizedLabels := make((map[string]string), len(pod.Labels))
	for k, v := range pod.Labels {
		sanitizedKey := utils.SanitizeLabelName(k)
		sanitizedLabels[sanitizedKey] = v
	}

	return sanitizedLabels, nil
}
