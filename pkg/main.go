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
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
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
)

func main() {
	c := cli.NewApp()
	c.Name = "DCGM Exporter"
	c.Usage = "Generates GPU metrics in the prometheus format"
	c.Version = BuildVersion

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
			Name:    CLIRemoteHEInfo,
			Aliases: []string{"r"},
			Value:   "localhost:5555",
			Usage:   "Connect to remote hostengine at <HOST>:<PORT>",
			EnvVars: []string{"DCGM_REMOTE_HOSTENGINE_INFO"},
		},
		&cli.StringFlag{
			Name:    CLIKubernetesGPUIDType,
			Value:   string(GPUUID),
			Usage:   fmt.Sprintf("Choose Type of GPU ID to use to map kubernetes resources to pods. Possible values: '%s', '%s'", GPUUID, DeviceName),
			EnvVars: []string{"DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE"},
		},
	}

	c.Action = func(c *cli.Context) error {
		return Run(c)
	}

	if err := c.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}
}

func Run(c *cli.Context) error {
restart:

	logrus.Info("Starting dcgm-exporter")
	config := contextToConfig(c)

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

	_, err := dcgm.GetSupportedMetricGroups(0)
	if err != nil {
		config.CollectDCP = false
		logrus.Info("Not collecting DCP metrics: ", err)
	} else {
		logrus.Info("Collecting DCP Metrics")
	}

	ch := make(chan string, 10)
	pipeline, cleanup, err := NewMetricsPipeline(config)
	defer cleanup()
	if err != nil {
		logrus.Fatal(err)
	}

	server, cleanup, err := NewMetricsServer(config, ch)
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
			err := WaitWithTimeout(&wg, time.Second*2)
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

func contextToConfig(c *cli.Context) *Config {
	return &Config{
		CollectorsFile:      c.String(CLIFieldsFile),
		Address:             c.String(CLIAddress),
		CollectInterval:     c.Int(CLICollectInterval),
		Kubernetes:          c.Bool(CLIKubernetes),
		KubernetesGPUIdType: KubernetesGPUIDType(c.String(CLIKubernetesGPUIDType)),
		CollectDCP:          true,
		UseOldNamespace:     c.Bool(CLIUseOldNamespace),
		UseRemoteHE:         c.IsSet(CLIRemoteHEInfo),
		RemoteHEInfo:        c.String(CLIRemoteHEInfo),
	}
}
