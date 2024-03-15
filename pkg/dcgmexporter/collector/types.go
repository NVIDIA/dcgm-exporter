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

//go:generate mockgen -destination=mocks/pkg/dcgmexporter/collector/mock_expcollector.go github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/collector Collector

package collector

import (
	"fmt"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	common2 "github.com/NVIDIA/dcgm-exporter/pkg/common"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/sysinfo"
)

type Metric struct {
	Counter common2.Counter
	Value   string

	GPU          string
	GPUUUID      string
	GPUDevice    string
	GPUModelName string

	UUID string

	MigProfile    string
	GPUInstanceID string
	Hostname      string

	Labels     map[string]string
	Attributes map[string]string
}

func (m Metric) GetIDOfType(idType common2.KubernetesGPUIDType) (string, error) {
	// For MIG devices, return the MIG profile instead of
	if m.MigProfile != "" {
		return fmt.Sprintf("%s-%s", m.GPU, m.GPUInstanceID), nil
	}
	switch idType {
	case common2.GPUUID:
		return m.GPUUUID, nil
	case common2.DeviceName:
		return m.GPUDevice, nil
	}
	return "", fmt.Errorf("unsupported KubernetesGPUIDType for MetricID '%s'", idType)
}

// Collector interface
type Collector interface {
	GetMetrics() (MetricsByCounter, error)
	Cleanup()
	GetSysinfo() sysinfo.SystemInfoInterface
}

// MetricsByCounter represents a map where each Counter is associated with a slice of Metric objects
type MetricsByCounter map[common2.Counter][]Metric

type DCGMCollector struct {
	Counters                 []common2.Counter
	DeviceFields             []dcgm.Short
	Cleanups                 []func()
	UseOldNamespace          bool
	SysInfo                  sysinfo.SystemInfoInterface
	Hostname                 string
	ReplaceBlanksInModelName bool
}
