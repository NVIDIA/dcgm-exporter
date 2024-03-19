/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package common

const (
	CLIFieldsFile                 = "collectors"
	CLIAddress                    = "address"
	CLICollectInterval            = "collect-interval"
	CLIKubernetes                 = "kubernetes"
	CLIKubernetesGPUIDType        = "kubernetes-gpu-id-type"
	CLIUseOldNamespace            = "use-old-namespace"
	CLIRemoteHEInfo               = "remote-hostengine-info"
	CLIGPUDevices                 = "devices"
	CLISwitchDevices              = "switch-devices"
	CLICPUDevices                 = "cpu-devices"
	CLINoHostname                 = "no-hostname"
	CLIUseFakeGPUs                = "fake-gpus"
	CLIConfigMapData              = "configmap-data"
	CLIWebSystemdSocket           = "web-systemd-socket"
	CLIWebConfigFile              = "web-common-file"
	CLIXIDCountWindowSize         = "xid-count-window-size"
	CLIReplaceBlanksInModelName   = "replace-blanks-in-model-name"
	CLIDebugMode                  = "debug"
	CLIClockEventsCountWindowSize = "clock-events-count-window-size"
	CLIEnableDCGMLog              = "enable-dcgmprovider-log"
	CLIDCGMLogLevel               = "dcgmprovider-log-level"
)

const (
	GPUUID     KubernetesGPUIDType = "uid"
	DeviceName KubernetesGPUIDType = "device-name"
)

// Constants for logging fields
const (
	LoggerGroupIDKey = "groupID"
	LoggerDumpKey    = "dump"
	LoggerStackTrace = "stacktrace"
)

// DCGMDbgLvl is a DCGM library debug level.
const (
	DCGMDbgLvlNone  = "NONE"
	DCGMDbgLvlFatal = "FATAL"
	DCGMDbgLvlError = "ERROR"
	DCGMDbgLvlWarn  = "WARN"
	DCGMDbgLvlInfo  = "INFO"
	DCGMDbgLvlDebug = "DEBUG"
	DCGMDbgLvlVerb  = "VERB"
)

const (
	FlexKey  = "f" // Monitor all gpus if MIG is disabled or all GPU instances if MIG is enabled
	MajorKey = "g" // Monitor top-level entities: gpus or NvSwitches or cpus
	MinorKey = "i" // Monitor sub-level entities: GPU instances/NvLinks/CPUCores - GPUI cannot be specified if MIG is disabled
)
