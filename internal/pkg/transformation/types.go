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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"k8s.io/client-go/kubernetes"
)

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/transformations/mock_transformer.go -package=transformation -copyright_file=../../../hack/header.txt . Transform

type Transform interface {
	Process(metrics collector.MetricsByCounter, deviceInfo deviceinfo.Provider) error
	Name() string
}

type PodMapper struct {
	Config *appconfig.Config
	Client kubernetes.Interface
}

type PodInfo struct {
	Name      string
	Namespace string
	Container string
	VGPU      string
	Labels    map[string]string
}
