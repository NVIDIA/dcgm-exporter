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

package cmd

import "github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"

// DCGMDbgLvl is a DCGM library debug level.
const (
	DCGMDbgLvlNone  = appconfig.DCGMDbgLvlNone
	DCGMDbgLvlFatal = appconfig.DCGMDbgLvlFatal
	DCGMDbgLvlError = appconfig.DCGMDbgLvlError
	DCGMDbgLvlWarn  = appconfig.DCGMDbgLvlWarn
	DCGMDbgLvlInfo  = appconfig.DCGMDbgLvlInfo
	DCGMDbgLvlDebug = appconfig.DCGMDbgLvlDebug
	DCGMDbgLvlVerb  = appconfig.DCGMDbgLvlVerb
)

// DCGMDbgLvlValues lists every DCGM library debug level.
var DCGMDbgLvlValues = appconfig.DCGMDbgLvlValues
