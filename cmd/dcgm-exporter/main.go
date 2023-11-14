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

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

const (
	FlexKey                = "f" // Monitor all GPUs if MIG is disabled or all GPU instances if MIG is enabled
	MajorKey               = "g" // Monitor top-level entities: GPUs or NvSwitches
	MinorKey               = "i" // Monitor sub-level entities: GPU instances/NvLinks - cannot be specified if MIG is disabled
	undefinedConfigMapData = "none"
)

var (
	BuildVersion = "Filled by the build system"

	CLIFieldsFile          = "collectors"
	CLIAddress             = "address"
	CLICollectInterval     = "collect-interval"
	CLIKubernetes          = "kubernetes"
	CLIKubernetesGPUIDType = "kubernetes-gpu-id-type"
	CLIUseOldNamespace     = "use-old-namespace"
	CLIRemoteHEInfo        = "remote-hostengine-info"
	CLIGPUDevices          = "devices"
	CLISwitchDevices       = "switch-devices"
	CLINoHostname          = "no-hostname"
	CLIUseFakeGpus         = "fake-gpus"
	CLIConfigMapData       = "configmap-data"
	CLIWebSystemdSocket    = "web-systemd-socket"
	CLIWebConfigFile       = "web-config-file"
)

func main() {
	c := cli.NewApp()
	c.Name = "DCGM Exporter"
	c.Usage = "Generates GPU metrics in the prometheus format"
	c.Version = BuildVersion

	deviceUsageTemplate := `Specify which devices dcgm-exporter monitors.
	Possible values: {{.FlexKey}} or 
	                 {{.MajorKey}}[:id1[,-id2...] or 
	                 {{.MinorKey}}[:id1[,-id2...].
	If an id list is used, then devices with match IDs must exist on the system. For example:
		(default) = monitor all GPU instances in MIG mode, all GPUs if MIG mode is disabled. (See {{.FlexKey}})
		{{.MajorKey}} = Monitor all GPUs
		{{.MinorKey}} = Monitor all GPU instances
		{{.FlexKey}} = Monitor all GPUs if MIG is disabled, or all GPU instances if MIG is enabled.
                       Note: this rule will be applied to each GPU. If it has GPU instances, those
                             will be monitored. If it doesn't, then the GPU will be monitored.
                             This is our recommended option for single or mixed MIG Strategies.
		{{.MajorKey}}:0,1 = monitor GPUs 0 and 1
		{{.MinorKey}}:0,2-4 = monitor GPU instances 0, 2, 3, and 4.
	
	NOTE 1: -i cannot be specified unless MIG mode is enabled.
	NOTE 2: Any time indices are specified, those indices must exist on the system.	
	NOTE 3: In MIG mode, only -f or -i with a range can be specified. GPUs are not assigned to pods
		and therefore reporting must occur at the GPU instance level.`

	var deviceUsageBuffer bytes.Buffer
	t := template.Must(template.New("").Parse(deviceUsageTemplate))
	_ = t.Execute(&deviceUsageBuffer, map[string]string{"FlexKey": FlexKey, "MajorKey": MajorKey, "MinorKey": MinorKey})
	DeviceUsageStr := deviceUsageBuffer.String()

	c.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    CLIFieldsFile,
			Aliases: []string{"f"},
			Usage:   "Path to the file, that contains the DCGM fields to collect",
			Value:   "/etc/dcgm-exporter/default-counters.csv",
			EnvVars: []string{"DCGM_EXPORTER_COLLECTORS"},
		},
		&cli.StringFlag{
			Name:    CLIAddress,
			Aliases: []string{"a"},
			Value:   ":9400",
			Usage:   "Address",
			EnvVars: []string{"DCGM_EXPORTER_LISTEN"},
		},
		&cli.IntFlag{
			Name:    CLICollectInterval,
			Aliases: []string{"c"},
			Value:   30000,
			Usage:   "Interval of time at which point metrics are collected. Unit is milliseconds (ms).",
			EnvVars: []string{"DCGM_EXPORTER_INTERVAL"},
		},
		&cli.BoolFlag{
			Name:    CLIKubernetes,
			Aliases: []string{"k"},
			Value:   false,
			Usage:   "Enable kubernetes mapping metrics to kubernetes pods",
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES"},
		},
		&cli.BoolFlag{
			Name:    CLIUseOldNamespace,
			Aliases: []string{"o"},
			Value:   false,
			Usage:   "Use old 1.x namespace",
			EnvVars: []string{"DCGM_EXPORTER_USE_OLD_NAMESPACE"},
		},
		&cli.StringFlag{
			Name:    CLIConfigMapData,
			Aliases: []string{"m"},
			Value:   undefinedConfigMapData,
			Usage:   "ConfigMap <NAMESPACE>:<NAME> for metric data",
			EnvVars: []string{"DCGM_EXPORTER_CONFIGMAP_DATA"},
		},
		&cli.StringFlag{
			Name:    CLIRemoteHEInfo,
			Aliases: []string{"r"},
			Value:   "localhost:5555",
			Usage:   "Connect to remote hostengine at <HOST>:<PORT>",
			EnvVars: []string{"DCGM_REMOTE_HOSTENGINE_INFO"},
		},
		&cli.StringFlag{
			Name:  CLIKubernetesGPUIDType,
			Value: string(dcgmexporter.GPUUID),
			Usage: fmt.Sprintf("Choose Type of GPU ID to use to map kubernetes resources to pods. Possible values: '%s', '%s'",
				dcgmexporter.GPUUID, dcgmexporter.DeviceName),
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE"},
		},
		&cli.StringFlag{
			Name:    CLIGPUDevices,
			Aliases: []string{"d"},
			Value:   FlexKey,
			Usage:   DeviceUsageStr,
			EnvVars: []string{"DCGM_EXPORTER_DEVICES_STR"},
		},
		&cli.BoolFlag{
			Name:    CLINoHostname,
			Aliases: []string{"n"},
			Value:   false,
			Usage:   "Omit the hostname information from the output, matching older versions.",
			EnvVars: []string{"DCGM_EXPORTER_NO_HOSTNAME"},
		},
		&cli.StringFlag{
			Name:    CLISwitchDevices,
			Aliases: []string{"s"},
			Value:   FlexKey,
			Usage:   DeviceUsageStr,
			EnvVars: []string{"DCGM_EXPORTER_OTHER_DEVICES_STR"},
		},
		&cli.BoolFlag{
			Name:    CLIUseFakeGpus,
			Value:   false,
			Usage:   "Accept GPUs that are fake, for testing purposes only",
			EnvVars: []string{"DCGM_EXPORTER_USE_FAKE_GPUS"},
		},
		&cli.BoolFlag{
			Name:    CLIWebSystemdSocket,
			Value:   false,
			Usage:   "Use systemd socket activation listeners instead of port listeners (Linux only).",
			EnvVars: []string{"DCGM_EXPORTER_SYSTEMD_SOCKET"},
		},
		&cli.StringFlag{
			Name:    CLIWebConfigFile,
			Value:   "",
			Usage:   "TLS config file following webConfig spec.",
			EnvVars: []string{"DCGM_EXPORTER_WEB_CONFIG_FILE"},
		},
	}

	c.Action = func(c *cli.Context) error {
		return Run(c)
	}

	if err := c.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}
}

func newOSWatcher(sigs ...os.Signal) chan os.Signal {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, sigs...)

	return sigChan
}

func Run(c *cli.Context) error {
restart:

	logrus.Info("Starting dcgm-exporter")
	config, err := contextToConfig(c)
	if err != nil {
		return err
	}

	if config.UseRemoteHE {
		logrus.Info("Attemping to connect to remote hostengine at ", config.RemoteHEInfo)
		cleanup, err := dcgm.Init(dcgm.Standalone, config.RemoteHEInfo, "0")
		defer cleanup()
		if err != nil {
			logrus.Fatal(err)
		}
	} else {
		cleanup, err := dcgm.Init(dcgm.Embedded)
		defer cleanup()
		if err != nil {
			logrus.Fatal(err)
		}
	}
	logrus.Info("DCGM successfully initialized!")

	dcgm.FieldsInit()
	defer dcgm.FieldsTerm()

	var groups []dcgm.MetricGroup
	groups, err = dcgm.GetSupportedMetricGroups(0)
	if err != nil {
		config.CollectDCP = false
		logrus.Info("Not collecting DCP metrics: ", err)
	} else {
		logrus.Info("Collecting DCP Metrics")
		config.MetricGroups = groups
	}

	ch := make(chan string, 10)
	pipeline, cleanup, err := dcgmexporter.NewMetricsPipeline(config)
	defer cleanup()
	if err != nil {
		logrus.Fatal(err)
	}

	server, cleanup, err := dcgmexporter.NewMetricsServer(config, ch)
	defer cleanup()
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	stop := make(chan interface{})

	wg.Add(1)
	go pipeline.Run(ch, stop, &wg)

	wg.Add(1)
	go server.Run(stop, &wg)

	sigs := newOSWatcher(syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)
	for {
		select {
		case sig := <-sigs:
			close(stop)
			err := dcgmexporter.WaitWithTimeout(&wg, time.Second*2)
			if err != nil {
				logrus.Fatal(err)
			}

			if sig == syscall.SIGHUP {
				goto restart
			}

			return nil
		}
	}

	return nil
}

func parseDeviceOptionsToken(token string, dOpt *dcgmexporter.DeviceOptions) error {
	letterAndRange := strings.Split(token, ":")
	count := len(letterAndRange)
	if count > 2 {
		return fmt.Errorf("Invalid ranged device option '%s': there can only be one specified range", token)
	}

	letter := letterAndRange[0]
	if letter == FlexKey {
		dOpt.Flex = true
		if count > 1 {
			return fmt.Errorf("No range can be specified with the flex option 'f'")
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
					return fmt.Errorf("A range can only be '<number>-<number>', but found '%s'", numberOrRange)
				} else if rangeTokenCount == 1 {
					number, err := strconv.Atoi(rangeTokens[0])
					if err != nil {
						return err
					}
					indices = append(indices, number)
				} else {
					start, err := strconv.Atoi(rangeTokens[0])
					if err != nil {
						return err
					}
					end, err := strconv.Atoi(rangeTokens[1])
					if err != nil {
						return err
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
		return fmt.Errorf("The only valid options preceding ':<range>' are 'g' or 'i', but found '%s'", letter)
	}

	return nil
}

func parseDeviceOptions(devices string) (dcgmexporter.DeviceOptions, error) {
	var dOpt dcgmexporter.DeviceOptions

	letterAndRange := strings.Split(devices, ":")
	count := len(letterAndRange)
	if count > 2 {
		return dOpt, fmt.Errorf("Invalid ranged device option '%s': there can only be one specified range", devices)
	}

	letter := letterAndRange[0]
	if letter == FlexKey {
		dOpt.Flex = true
		if count > 1 {
			return dOpt, fmt.Errorf("No range can be specified with the flex option 'f'")
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
					return dOpt, fmt.Errorf("A range can only be '<number>-<number>', but found '%s'", numberOrRange)
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
		return dOpt, fmt.Errorf("The only valid options preceding ':<range>' are 'g' or 'i', but found '%s'", letter)
	}

	return dOpt, nil
}

func contextToConfig(c *cli.Context) (*dcgmexporter.Config, error) {
	gOpt, err := parseDeviceOptions(c.String(CLIGPUDevices))
	if err != nil {
		return nil, err
	}

	sOpt, err := parseDeviceOptions(c.String(CLISwitchDevices))
	if err != nil {
		return nil, err
	}

	return &dcgmexporter.Config{
		CollectorsFile:      c.String(CLIFieldsFile),
		Address:             c.String(CLIAddress),
		CollectInterval:     c.Int(CLICollectInterval),
		Kubernetes:          c.Bool(CLIKubernetes),
		KubernetesGPUIdType: dcgmexporter.KubernetesGPUIDType(c.String(CLIKubernetesGPUIDType)),
		CollectDCP:          true,
		UseOldNamespace:     c.Bool(CLIUseOldNamespace),
		UseRemoteHE:         c.IsSet(CLIRemoteHEInfo),
		RemoteHEInfo:        c.String(CLIRemoteHEInfo),
		GPUDevices:          gOpt,
		SwitchDevices:       sOpt,
		NoHostname:          c.Bool(CLINoHostname),
		UseFakeGpus:         c.Bool(CLIUseFakeGpus),
		ConfigMapData:       c.String(CLIConfigMapData),
		WebSystemdSocket:    c.Bool(CLIWebSystemdSocket),
		WebConfigFile:       c.String(CLIWebConfigFile),
	}, nil
}
