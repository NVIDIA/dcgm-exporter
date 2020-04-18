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
	"sync"
	"net/http"
	"text/template"
	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
)

type Config struct {
	FieldsFile      string
	Port            int
	CollectInterval int
}

type Collector interface {
	GetMetrics() (string, error)
}

type DCGMCollector struct {
	Fields []DCGMField
	Template *template.Template
	Cleanups []func()
	DeviceFields []dcgm.Short
}

var promMetricType = map[string]bool{
	"gauge": true,
	"counter": true,
	"histogram": true,
	"summary": true,
}

type DCGMField struct {
	FieldID   dcgm.Short
	FieldName string
	PromType  string
	Help      string
}

type CollectorMgr struct {
	Collectors []Collector
	CollectInterval int
	Out chan string
}

type MetricsServer struct {
	sync.Mutex

	server http.Server
	metrics string
	metricsChan chan string
}
