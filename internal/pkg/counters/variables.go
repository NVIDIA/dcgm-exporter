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

package counters

import osinterface "github.com/NVIDIA/dcgm-exporter/internal/pkg/os"

var os osinterface.OS = osinterface.RealOS{}

var promMetricType = map[string]bool{
	"gauge":     true,
	"counter":   true,
	"histogram": true,
	"summary":   true,
	"label":     true,
}
