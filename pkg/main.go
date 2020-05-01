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
	"os"
	"syscall"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
)

var (
	BuildVersion = "Filled by the build system"

	CLIFieldsFile = "collectors"
	CLIPort = "port"
	CLICollectInterval = "collect-interval"
	CLIKubernetes = "kubernetes"
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
		&cli.IntFlag{
			Name:    CLIPort,
			Aliases: []string{"p"},
			Value:   8080,
			Usage:   "Port",
			EnvVars: []string{"DCGM_EXPORTER_PORT"},
		},
		&cli.IntFlag{
			Name:    CLICollectInterval,
			Aliases: []string{"c"},
			Value:   2000,
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
	config := contextToConfig(c)

	cleanup, err := dcgm.Init(dcgm.Embedded)
	defer cleanup()
	if err != nil {
		logrus.Fatal(err)
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
			err := WaitWithTimeout(&wg, time.Second * 2)
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
	return &Config {
		CollectorsFile: c.String(CLIFieldsFile),
		Port: c.Int(CLIPort),
		CollectInterval: c.Int(CLICollectInterval),
		Kubernetes: c.Bool(CLIKubernetes),
	}
}
