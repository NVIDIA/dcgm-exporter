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

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/urfave/cli/v2"
)

type KubernetesGPUIDType string

type DeviceOptions struct {
	Flex       bool  // If true, then monitor all GPUs if MIG mode is disabled or all GPU instances if MIG is enabled.
	MajorRange []int // The indices of each GPU/NvSwitch to monitor, or -1 to monitor all
	MinorRange []int // The indices of each GPUInstance/NvLink to monitor, or -1 to monitor all
}

type Config struct {
	CollectorsFile             string
	Address                    string
	CollectInterval            int
	Kubernetes                 bool
	KubernetesGPUIdType        KubernetesGPUIDType
	CollectDCP                 bool
	UseOldNamespace            bool
	UseRemoteHE                bool
	RemoteHEInfo               string
	GPUDevices                 DeviceOptions
	SwitchDevices              DeviceOptions
	CPUDevices                 DeviceOptions
	NoHostname                 bool
	UseFakeGPUs                bool
	ConfigMapData              string
	MetricGroups               []dcgm.MetricGroup
	WebSystemdSocket           bool
	WebConfigFile              string
	XIDCountWindowSize         int
	ReplaceBlanksInModelName   bool
	Debug                      bool
	ClockEventsCountWindowSize int
	EnableDCGMLog              bool
	DCGMLogLevel               string
}

func (c *Config) Load(cliCtx *cli.Context) error {
	gOpt, err := parseDeviceOptions(cliCtx.String(CLIGPUDevices))
	if err != nil {
		return err
	}

	sOpt, err := parseDeviceOptions(cliCtx.String(CLISwitchDevices))
	if err != nil {
		return err
	}

	cOpt, err := parseDeviceOptions(cliCtx.String(CLICPUDevices))
	if err != nil {
		return err
	}

	dcgmLogLevel := cliCtx.String(CLIDCGMLogLevel)
	if !slices.Contains(DCGMDbgLvlValues, dcgmLogLevel) {
		return fmt.Errorf("invalid %s parameter value: %s", CLIDCGMLogLevel, dcgmLogLevel)
	}

	c = &Config{
		CollectorsFile:             cliCtx.String(CLIFieldsFile),
		Address:                    cliCtx.String(CLIAddress),
		CollectInterval:            cliCtx.Int(CLICollectInterval),
		Kubernetes:                 cliCtx.Bool(CLIKubernetes),
		KubernetesGPUIdType:        KubernetesGPUIDType(cliCtx.String(CLIKubernetesGPUIDType)),
		CollectDCP:                 true,
		UseOldNamespace:            cliCtx.Bool(CLIUseOldNamespace),
		UseRemoteHE:                cliCtx.IsSet(CLIRemoteHEInfo),
		RemoteHEInfo:               cliCtx.String(CLIRemoteHEInfo),
		GPUDevices:                 gOpt,
		SwitchDevices:              sOpt,
		CPUDevices:                 cOpt,
		NoHostname:                 cliCtx.Bool(CLINoHostname),
		UseFakeGPUs:                cliCtx.Bool(CLIUseFakeGPUs),
		ConfigMapData:              cliCtx.String(CLIConfigMapData),
		WebSystemdSocket:           cliCtx.Bool(CLIWebSystemdSocket),
		WebConfigFile:              cliCtx.String(CLIWebConfigFile),
		XIDCountWindowSize:         cliCtx.Int(CLIXIDCountWindowSize),
		ReplaceBlanksInModelName:   cliCtx.Bool(CLIReplaceBlanksInModelName),
		Debug:                      cliCtx.Bool(CLIDebugMode),
		ClockEventsCountWindowSize: cliCtx.Int(CLIClockEventsCountWindowSize),
		EnableDCGMLog:              cliCtx.Bool(CLIEnableDCGMLog),
		DCGMLogLevel:               dcgmLogLevel,
	}

	return nil
}

func parseDeviceOptions(devices string) (DeviceOptions, error) {
	var dOpt DeviceOptions

	letterAndRange := strings.Split(devices, ":")
	count := len(letterAndRange)
	if count > 2 {
		return dOpt, fmt.Errorf("Invalid ranged device option '%s': there can only be one specified range", devices)
	}

	letter := letterAndRange[0]
	if letter == FlexKey {
		dOpt.Flex = true
		if count > 1 {
			return dOpt, fmt.Errorf("no range can be specified with the flex option 'f'")
		}
	} else if letter == MajorKey || letter == MinorKey {
		var indices []int
		if count == 1 {
			// No range means all present devices of the type
			indices = append(indices, -1)
		} else {
			numbers := strings.Split(letterAndRange[1], ",")
			for _, numberOrRange := range numbers {
				rangeTokens := strings.Split(numberOrRange, "-")
				rangeTokenCount := len(rangeTokens)
				if rangeTokenCount > 2 {
					return dOpt, fmt.Errorf("range can only be '<number>-<number>', but found '%s'", numberOrRange)
				} else if rangeTokenCount == 1 {
					number, err := strconv.Atoi(rangeTokens[0])
					if err != nil {
						return dOpt, err
					}
					indices = append(indices, number)
				} else {
					start, err := strconv.Atoi(rangeTokens[0])
					if err != nil {
						return dOpt, err
					}
					end, err := strconv.Atoi(rangeTokens[1])
					if err != nil {
						return dOpt, err
					}

					// Add the range to the indices
					for i := start; i <= end; i++ {
						indices = append(indices, i)
					}
				}
			}
		}

		if letter == MajorKey {
			dOpt.MajorRange = indices
		} else {
			dOpt.MinorRange = indices
		}
	} else {
		return dOpt, fmt.Errorf("the only valid options preceding ':<range>' are 'g' or 'i', but found '%s'", letter)
	}

	return dOpt, nil
}

type Counter struct {
	FieldID   dcgm.Short
	FieldName string
	PromType  string
	Help      string
}

// CounterSet return
type CounterSet struct {
	DCGMCounters     []Counter
	ExporterCounters []Counter
}
