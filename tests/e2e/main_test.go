//go:build e2e

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
package e2e

import (
	"flag"
	"os"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/suite"

	"github.com/google/uuid"
)

var runID = uuid.New()

var log *logrus.Entry

var suiteCfg = suiteConfig{}

func TestMain(m *testing.M) {
	// Create a new logger instance
	logrus.SetOutput(os.Stdout)
	logrus.SetFormatter(&logrus.TextFormatter{})
	logrus.SetLevel(logrus.InfoLevel)
	log = logrus.WithField("e2eRunID", runID)

	flag.StringVar(&suiteCfg.kubeconfig,
		"kubeconfig",
		"~/.kube/config",
		"path to the kubeconfig file.")

	flag.StringVar(&suiteCfg.namespace,
		"namespace",
		"dcgm-exporter",
		"Namespace name to use for the DCGM-exporter deployment")

	flag.StringVar(&suiteCfg.chart,
		"chart",
		"",
		"Helm chart to use")

	flag.StringVar(&suiteCfg.imageRepository,
		"image-repository",
		"",
		"DCGM-exporter image repository")

	flag.StringVar(&suiteCfg.imageTag,
		"image-tag",
		"",
		"DCGM-exporter image tag to use")

	flag.StringVar(&suiteCfg.arguments,
		"arguments",
		"",
		`DCGM-exporter command line parameters. Example: -arguments={-f=/etc/dcgm-exporter/default-counters.csv}`)

	flag.Parse()
	os.Exit(m.Run())
}

// TestRunSuite will be run by the 'go test' command
func TestRunSuite(t *testing.T) {
	suite.Run(t, NewSuite())
}
