/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmexporter

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1alpha1"
)

var (
	socketDir  = "/var/lib/kubelet/pod-resources"
	socketPath = socketDir + "/kubelet.sock"

	connectionTimeout = 10 * time.Second

	gkeMigDeviceIDRegex            = regexp.MustCompile(`^nvidia([0-9]+)/gi([0-9]+)$`)
	gkeVirtualGPUDeviceIDSeparator = "/vgpu"
	nvmlGetMIGDeviceInfoByIDHook   = nvmlprovider.GetMIGDeviceInfoByID
)

func NewPodMapper(c *Config) (*PodMapper, error) {
	logrus.Infof("Kubernetes metrics collection enabled!")

	return &PodMapper{
		Config: c,
	}, nil
}

func (p *PodMapper) Name() string {
	return "podMapper"
}

func (p *PodMapper) Process(metrics [][]Metric, sysInfo SystemInfo) error {
	_, err := os.Stat(socketPath)
	if os.IsNotExist(err) {
		logrus.Infof("No Kubelet socket, ignoring")
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

	deviceToPod := p.toDeviceToPod(pods, sysInfo)

	// Note: for loop are copies the value, if we want to change the value
	// and not the copy, we need to use the indexes
	for i, device := range metrics {
		for j, val := range device {
			deviceID, err := val.getIDOfType(p.Config.KubernetesGPUIdType)
			if err != nil {
				return err
			}
			if !p.Config.UseOldNamespace {
				metrics[i][j].Attributes[podAttribute] = deviceToPod[deviceID].Name
				metrics[i][j].Attributes[namespaceAttribute] = deviceToPod[deviceID].Namespace
				metrics[i][j].Attributes[containerAttribute] = deviceToPod[deviceID].Container
			} else {
				metrics[i][j].Attributes[oldPodAttribute] = deviceToPod[deviceID].Name
				metrics[i][j].Attributes[oldNamespaceAttribute] = deviceToPod[deviceID].Namespace
				metrics[i][j].Attributes[oldContainerAttribute] = deviceToPod[deviceID].Container
			}
		}
	}

	return nil
}

func connectToServer(socket string) (*grpc.ClientConn, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, socket, grpc.WithInsecure(), grpc.WithBlock(),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)

	if err != nil {
		return nil, func() {}, fmt.Errorf("failure connecting to %s: %v", socket, err)
	}

	return conn, func() { conn.Close() }, nil
}

func (p *PodMapper) listPods(conn *grpc.ClientConn) (*podresourcesapi.ListPodResourcesResponse, error) {
	client := podresourcesapi.NewPodResourcesListerClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	resp, err := client.List(ctx, &podresourcesapi.ListPodResourcesRequest{})
	if err != nil {
		return nil, fmt.Errorf("failure getting pod resources %v", err)
	}

	return resp, nil
}

func (p *PodMapper) toDeviceToPod(devicePods *podresourcesapi.ListPodResourcesResponse, sysInfo SystemInfo) map[string]PodInfo {
	deviceToPodMap := make(map[string]PodInfo)

	for _, pod := range devicePods.GetPodResources() {
		for _, container := range pod.GetContainers() {
			for _, device := range container.GetDevices() {

				resourceName := device.GetResourceName()
				if resourceName != nvidiaResourceName {
					// Mig resources appear differently than GPU resources
					if !strings.HasPrefix(resourceName, nvidiaMigResourcePrefix) {
						continue
					}
				}

				podInfo := PodInfo{
					Name:      pod.GetName(),
					Namespace: pod.GetNamespace(),
					Container: container.GetName(),
				}

				for _, deviceID := range device.GetDeviceIds() {
					if strings.HasPrefix(deviceID, MIG_UUID_PREFIX) {
						migDevice, err := nvmlGetMIGDeviceInfoByIDHook(deviceID)
						if err == nil {
							giIdentifier := GetGPUInstanceIdentifier(sysInfo, migDevice.ParentUUID, uint(migDevice.ComputeInstanceID))
							deviceToPodMap[giIdentifier] = podInfo
						}
						gpuUUID := deviceID[len(MIG_UUID_PREFIX):]
						deviceToPodMap[gpuUUID] = podInfo
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
						deviceToPodMap[giIdentifier] = podInfo
					} else if strings.Contains(deviceID, gkeVirtualGPUDeviceIDSeparator) {
						deviceToPodMap[strings.Split(deviceID, gkeVirtualGPUDeviceIDSeparator)[0]] = podInfo
					} else if strings.Contains(deviceID, "::") {
						gpuInstanceID := strings.Split(deviceID, "::")[0]
						deviceToPodMap[gpuInstanceID] = podInfo
					}
					// Default mapping between deviceID and pod information
					deviceToPodMap[deviceID] = podInfo
				}
			}
		}
	}

	return deviceToPodMap
}
