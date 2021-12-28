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

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	coreinformers "k8s.io/client-go/informers/core/v1"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1alpha1"
)

var (
	socketDir  = "/var/lib/kubelet/pod-resources"
	socketPath = socketDir + "/kubelet.sock"

	connectionTimeout = 10 * time.Second

	charReplacerRegex = regexp.MustCompile("[./-]")
)

func NewPodMapper(c *Config, podInformer coreinformers.PodInformer) (*PodMapper, error) {
	logrus.Infof("Kubernetes metrics collection enabled!")

	ret := nvml.Init()

	if ret != nil {
		return nil, ret
	}

	return &PodMapper{
		PodInformer: podInformer,
		Config:      c,
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

	pods, err := ListPods(c)
	if err != nil {
		return err
	}

	deviceToPod := ToDeviceToPod(pods, sysInfo, p.PodInformer, p.Config.UsePodLabels, p.Config.UsePodAnnotations)

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
			// add pod label
			for l, v := range deviceToPod[deviceId].Labels {
				metrics[i][j].Attributes[l] = v
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

func ToDeviceToPod(devicePods *podresourcesapi.ListPodResourcesResponse, sysInfo SystemInfo,
	podInformer coreinformers.PodInformer, podLabels []string, podAnnotations []string) map[string]PodInfo {
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
					Labels:    make(map[string]string),
				}
				addPodLabel(&podInfo, podInformer, podLabels, podAnnotations)
				for _, uuid := range device.GetDeviceIds() {
					if strings.HasPrefix(uuid, MIG_UUID_PREFIX) {
						gpuUuid, gi, _, err := nvml.ParseMigDeviceUUID(uuid)
						if err == nil {
							giIdentifier := GetGpuInstanceIdentifier(sysInfo, gpuUuid, gi)
							deviceToPodMap[giIdentifier] = podInfo
						} else {
							gpuUuid = uuid[len(MIG_UUID_PREFIX):]
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

func addPodLabel(podInfo *PodInfo, podInformer coreinformers.PodInformer, podLabels []string, podAnnotations []string) {
	logrus.Infof("try add label %v for pod <%v:%v>, nil == podInformer? %v", podLabels, podInfo.Namespace, podInfo.Name, nil == podInformer)
	if podInformer == nil {
		return
	}
	if len(podLabels) == 0 && len(podAnnotations) == 0 {
		return
	}
	v1Pod, err := podInformer.Lister().Pods(podInfo.Namespace).Get(podInfo.Name)
	if err != nil {
		logrus.Errorf("query pod <%v/%v> err: %v", podInfo.Namespace, podInfo.Name, err)
		return
	}
	if len(podLabels) > 0 {
		for _, label := range podLabels {
			if v, ok := v1Pod.Labels[label]; ok {
				metricLabel := charReplacerRegex.ReplaceAllString(label, "_")
				podInfo.Labels[metricLabel] = v
				logrus.Infof("query pod <%v/%v> label %v==>%v value %v", podInfo.Namespace, podInfo.Name, label, metricLabel, v)
			}
		}
	}
	if len(podAnnotations) > 0 {
		for _, annotation := range podAnnotations {
			if v, ok := v1Pod.Annotations[annotation]; ok {
				metricLabel := charReplacerRegex.ReplaceAllString(annotation, "_")
				podInfo.Labels[metricLabel] = v
				logrus.Infof("query pod <%v/%v> annotation %v==>%v value %v", podInfo.Namespace, podInfo.Name, annotation, metricLabel, v)
			}
		}
	}
}
