/*
 * Copyright (c) 2020, NVIDIA CORPORATION.  All rights reserved.
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

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1alpha1"
)

var (
	socketDir  = "/var/lib/kubelet/pod-resources"
	socketPath = socketDir + "/kubelet.sock"

	connectionTimeout = 10 * time.Second
)

func NewPodMapper(c *Config) *PodMapper {
	logrus.Infof("Kubernetes metrics collection enabled!")

	return &PodMapper{
		Config: c,
	}
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

	pods, err := ListPods(c)
	if err != nil {
		return err
	}

	deviceToPod := ToDeviceToPod(pods, sysInfo)

	// Note: for loop are copies the value, if we want to change the value
	// and not the copy, we need to use the indexes
	for i, device := range metrics {
		for j, val := range device {
			deviceId, err := val.getIDOfType(p.Config.KubernetesGPUIdType)
			if err != nil {
				return err
			}
			if !p.Config.UseOldNamespace {
				metrics[i][j].Attributes[podAttribute] = deviceToPod[deviceId].Name
				metrics[i][j].Attributes[namespaceAttribute] = deviceToPod[deviceId].Namespace
				metrics[i][j].Attributes[containerAttribute] = deviceToPod[deviceId].Container
			} else {
				metrics[i][j].Attributes[oldPodAttribute] = deviceToPod[deviceId].Name
				metrics[i][j].Attributes[oldNamespaceAttribute] = deviceToPod[deviceId].Namespace
				metrics[i][j].Attributes[oldContainerAttribute] = deviceToPod[deviceId].Container
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

func ListPods(conn *grpc.ClientConn) (*podresourcesapi.ListPodResourcesResponse, error) {
	client := podresourcesapi.NewPodResourcesListerClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	resp, err := client.List(ctx, &podresourcesapi.ListPodResourcesRequest{})
	if err != nil {
		return nil, fmt.Errorf("failure getting pod resources %v", err)
	}

	return resp, nil
}

func ToDeviceToPod(devicePods *podresourcesapi.ListPodResourcesResponse, sysInfo SystemInfo) map[string]PodInfo {
	deviceToPodMap := make(map[string]PodInfo)

	for _, pod := range devicePods.GetPodResources() {
		for _, container := range pod.GetContainers() {
			for _, device := range container.GetDevices() {

				resourceName := device.GetResourceName()
				if resourceName != nvidiaResourceName {
					// Mig resources appear differently than GPU resources
					if strings.HasPrefix(resourceName, nvidiaMigResourcePrefix) == false {
						continue
					}
				}

				podInfo := PodInfo{
					Name:      pod.GetName(),
					Namespace: pod.GetNamespace(),
					Container: container.GetName(),
				}

				for _, uuid := range device.GetDeviceIds() {
					if strings.HasPrefix(uuid, MIG_UUID_PREFIX) {
						// MIG uuid for now at least is in the format MIG-GPU-<gpu uuid>/<gpu instance index>/<compute instance index>
						// Remove the prefix and suffix to get the GPU's uuid
						gpuUuid := uuid[len(MIG_UUID_PREFIX):]
						index := strings.Index(gpuUuid, "/")
						if index > -1 {
							gIIdStr := gpuUuid[index+1:]
							index2 := strings.Index(gIIdStr, "/")
							gIIdStr = gIIdStr[0:index2]
							gpuUuid = gpuUuid[0:index]

							giIdentifier := GetGpuInstanceIdentifier(sysInfo, gpuUuid, gIIdStr)
							deviceToPodMap[giIdentifier] = podInfo
						}
						deviceToPodMap[gpuUuid] = podInfo
					} else {
						deviceToPodMap[uuid] = podInfo
					}
				}
			}
		}
	}

	return deviceToPodMap
}
