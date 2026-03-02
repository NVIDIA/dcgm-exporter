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

package collector

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	stdos "os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/resolver"
	podresourcesv1 "k8s.io/kubelet/pkg/apis/podresources/v1"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

const (
	podResourcesConnectionTimeout = 10 * time.Second

	podLabel       = "pod"
	namespaceLabel = "namespace"
	containerLabel = "container"
)

// processPodInfo holds the pod/container association for a process.
type processPodInfo struct {
	pod       string
	namespace string
	container string
}

// nvmlDeviceHandle is a minimal interface over nvml.Device to allow mocking in tests.
type nvmlDevice interface {
	GetUUID() (string, nvml.Return)
	GetName() (string, nvml.Return)
	GetProcessUtilization(lastSeenTimestamp uint64) ([]nvml.ProcessUtilizationSample, nvml.Return)
}

// nvmlLib is a minimal interface over the nvml package-level functions to allow mocking.
type nvmlLib interface {
	DeviceGetCount() (int, nvml.Return)
	DeviceGetHandleByIndex(index int) (nvmlDevice, nvml.Return)
}

// podResourcesClient is a minimal interface over the kubelet pod-resources gRPC client to allow mocking.
type podResourcesClient interface {
	List(ctx context.Context, req *podresourcesv1.ListPodResourcesRequest, opts ...grpc.CallOption) (*podresourcesv1.ListPodResourcesResponse, error)
}

// realNVMLLib wraps the real nvml package-level functions.
type realNVMLLib struct{}

func (r realNVMLLib) DeviceGetCount() (int, nvml.Return) {
	return nvml.DeviceGetCount()
}

func (r realNVMLLib) DeviceGetHandleByIndex(index int) (nvmlDevice, nvml.Return) {
	return nvml.DeviceGetHandleByIndex(index)
}

// processPodCollector emits per-pod GPU SM utilization metrics by combining
// NVML process utilization data with kubelet pod-resources information.
type processPodCollector struct {
	counter      counters.Counter
	hostname     string
	config       *appconfig.Config
	nvml         nvmlLib
	podResClient podResourcesClient
	connCleanup  func()
}

// NewProcessPodCollector creates a new processPodCollector.
// It establishes a gRPC connection to the kubelet pod-resources socket and
// initialises the NVML library handle.
func NewProcessPodCollector(
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
) (Collector, error) {
	// This is a synthetic metric driven by NVML, not a DCGM field, so it does
	// not need to appear in the metrics CSV. Use a built-in default; allow the
	// user to override via the CSV for custom help text or PromType.
	counter := counters.Counter{
		FieldID:  0,
		FieldName: counters.DCGMExpSMUtilPerPod,
		PromType: "gauge",
		Help:     "SM utilization attributed to a Kubernetes pod (CUDA time-slicing only)",
	}
	if csvCounter, csvErr := findCounterByName(counterList, counters.DCGMExpSMUtilPerPod); csvErr == nil {
		counter = csvCounter
	}

	conn, cleanup, err := connectToPodResourcesSocket(config.PodResourcesKubeletSocket)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to pod-resources socket %s: %w",
			config.PodResourcesKubeletSocket, err)
	}

	return &processPodCollector{
		counter:      counter,
		hostname:     hostname,
		config:       config,
		nvml:         realNVMLLib{},
		podResClient: podresourcesv1.NewPodResourcesListerClient(conn),
		connCleanup:  cleanup,
	}, nil
}

// newProcessPodCollectorWithDeps creates a processPodCollector with injected dependencies
// for use in tests.
func newProcessPodCollectorWithDeps(
	counter counters.Counter,
	hostname string,
	config *appconfig.Config,
	nvmlLib nvmlLib,
	podResClient podResourcesClient,
) *processPodCollector {
	return &processPodCollector{
		counter:      counter,
		hostname:     hostname,
		config:       config,
		nvml:         nvmlLib,
		podResClient: podResClient,
		connCleanup:  func() {},
	}
}

// GetMetrics implements the Collector interface.
// It queries NVML for per-process GPU SM utilization and correlates each PID
// with the owning pod/container via the kubelet pod-resources API.
func (c *processPodCollector) GetMetrics() (MetricsByCounter, error) {
	// Build UUID → pod mapping from pod-resources API.
	uuidToPods, err := c.buildUUIDToPodMap()
	if err != nil {
		return nil, fmt.Errorf("failed to build UUID-to-pod map: %w", err)
	}

	// Aggregate per-pod SM utilization across all GPU devices.
	// Key: (gpuUUID, namespace, pod, container) → summed SM util.
	type podGPUKey struct {
		uuid      string
		namespace string
		pod       string
		container string
	}
	smUtilSum := make(map[podGPUKey]uint32)
	smUtilCount := make(map[podGPUKey]uint32)

	// Per-device metadata used when building the final Metric structs.
	type gpuDeviceMeta struct {
		gpuIndex  string
		gpuDevice string
		modelName string
	}
	deviceMeta := make(map[string]gpuDeviceMeta) // uuid → metadata

	deviceCount, ret := c.nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return nil, fmt.Errorf("nvml DeviceGetCount failed: %s", nvml.ErrorString(ret))
	}

	for i := range deviceCount {
		device, ret := c.nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			slog.Warn("nvml DeviceGetHandleByIndex failed",
				slog.Int("index", i),
				slog.String("error", nvml.ErrorString(ret)))
			continue
		}

		uuid, ret := device.GetUUID()
		if ret != nvml.SUCCESS {
			slog.Warn("nvml GetUUID failed",
				slog.Int("index", i),
				slog.String("error", nvml.ErrorString(ret)))
			continue
		}

		modelName, ret := device.GetName()
		if ret != nvml.SUCCESS {
			slog.Debug("nvml GetName failed",
				slog.String("uuid", uuid),
				slog.String("error", nvml.ErrorString(ret)))
			modelName = ""
		}

		deviceMeta[uuid] = gpuDeviceMeta{
			gpuIndex:  fmt.Sprintf("%d", i),
			gpuDevice: fmt.Sprintf("nvidia%d", i),
			modelName: modelName,
		}

		// lastSeenTimestamp=0 requests all samples since the driver started.
		samples, ret := device.GetProcessUtilization(0)
		if ret != nvml.SUCCESS {
			slog.Warn("nvml GetProcessUtilization failed",
				slog.String("uuid", uuid),
				slog.String("error", nvml.ErrorString(ret)))
			continue
		}

		for _, sample := range samples {
			podInfo, ok := c.pidToPodInfo(sample.Pid, uuidToPods, uuid)
			if !ok {
				continue
			}

			key := podGPUKey{
				uuid:      uuid,
				namespace: podInfo.namespace,
				pod:       podInfo.pod,
				container: podInfo.container,
			}
			smUtilSum[key] += sample.SmUtil
			smUtilCount[key]++
		}
	}

	metrics := make(MetricsByCounter)
	metrics[c.counter] = make([]Metric, 0, len(smUtilSum))

	for key, sumUtil := range smUtilSum {
		count := smUtilCount[key]
		avgUtil := uint32(0)
		if count > 0 {
			avgUtil = sumUtil / count
		}

		meta := deviceMeta[key.uuid]

		labels := map[string]string{
			podLabel:       key.pod,
			namespaceLabel: key.namespace,
			containerLabel: key.container,
		}

		m := Metric{
			Counter:      c.counter,
			Value:        fmt.Sprintf("%d", avgUtil),
			GPU:          meta.gpuIndex,
			UUID:         "UUID",
			GPUUUID:      key.uuid,
			GPUDevice:    meta.gpuDevice,
			GPUModelName: meta.modelName,
			Hostname:     c.hostname,
			Labels:       labels,
			Attributes:   map[string]string{},
		}
		metrics[c.counter] = append(metrics[c.counter], m)
	}

	return metrics, nil
}

// Cleanup implements the Collector interface.
func (c *processPodCollector) Cleanup() {
	if c.connCleanup != nil {
		c.connCleanup()
	}
}

// buildUUIDToPodMap queries the kubelet pod-resources API and returns a
// mapping of GPU UUID → slice of pods/containers that have allocated the GPU.
func (c *processPodCollector) buildUUIDToPodMap() (map[string][]processPodInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), podResourcesConnectionTimeout)
	defer cancel()

	resp, err := c.podResClient.List(ctx, &podresourcesv1.ListPodResourcesRequest{})
	if err != nil {
		return nil, fmt.Errorf("kubelet pod-resources List call failed: %w", err)
	}

	uuidToPods := make(map[string][]processPodInfo)

	for _, pod := range resp.GetPodResources() {
		for _, container := range pod.GetContainers() {
			for _, device := range container.GetDevices() {
				for _, deviceID := range device.GetDeviceIds() {
					// Normalise: kubelet may include MIG UUIDs or plain GPU UUIDs.
					uuid := strings.TrimSpace(deviceID)
					if uuid == "" {
						continue
					}
					info := processPodInfo{
						pod:       pod.GetName(),
						namespace: pod.GetNamespace(),
						container: container.GetName(),
					}
					uuidToPods[uuid] = append(uuidToPods[uuid], info)
				}
			}
		}
	}

	return uuidToPods, nil
}

// pidToPodInfo returns the pod/container owning the given PID.
// It first tries to match via cgroup path, falling back to the UUID→pod map
// when there is exactly one pod allocated to the GPU (common for time-slicing
// when each virtual GPU is assigned to a single pod).
func (c *processPodCollector) pidToPodInfo(
	pid uint32,
	uuidToPods map[string][]processPodInfo,
	uuid string,
) (processPodInfo, bool) {
	// Attempt cgroup-based lookup first.
	if info, ok := c.podInfoFromCgroup(pid, uuidToPods); ok {
		return info, true
	}

	// Fall back: if only one pod holds this GPU, attribute all processes to it.
	pods, ok := uuidToPods[uuid]
	if !ok || len(pods) != 1 {
		return processPodInfo{}, false
	}
	return pods[0], true
}

// podInfoFromCgroup reads /proc/<pid>/cgroup and attempts to match the
// container ID in the cgroup path against known pod/container pairs.
func (c *processPodCollector) podInfoFromCgroup(
	pid uint32,
	uuidToPods map[string][]processPodInfo,
) (processPodInfo, bool) {
	cgroupPath := filepath.Join("/proc", fmt.Sprintf("%d", pid), "cgroup")

	data, err := stdos.ReadFile(cgroupPath) //nolint:gosec // path is constructed from controlled input
	if err != nil {
		// Process may have exited; this is not an error worth logging at warn level.
		slog.Debug("could not read cgroup file",
			slog.String("path", cgroupPath),
			slog.String("error", err.Error()))
		return processPodInfo{}, false
	}

	cgroupContent := string(data)

	for _, pods := range uuidToPods {
		for _, info := range pods {
			// The cgroup path for a container typically contains a segment
			// derived from the pod UID and container name/ID.
			// A reliable heuristic: look for the pod name in the cgroup path.
			if strings.Contains(cgroupContent, info.pod) {
				return info, true
			}
		}
	}

	return processPodInfo{}, false
}

// findCounterByName returns the first counter with the given field name.
func findCounterByName(list counters.CounterList, name string) (counters.Counter, error) {
	for _, c := range list {
		if c.FieldName == name {
			return c, nil
		}
	}
	return counters.Counter{}, fmt.Errorf("counter %q not found", name)
}

// connectToPodResourcesSocket dials the kubelet pod-resources Unix socket.
func connectToPodResourcesSocket(socket string) (*grpc.ClientConn, func(), error) {
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
		return nil, func() {}, fmt.Errorf("failed to dial pod-resources socket %q: %w", socket, err)
	}

	return conn, func() { conn.Close() }, nil
}
