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

	"github.com/google/uuid"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	"github.com/onsi/gomega"
)

var runID = uuid.New()

var testContext = testContextType{}

func TestMain(m *testing.M) {
	flag.StringVar(&testContext.kubeconfig,
		"kubeconfig",
		"~/.kube/config",
		"path to the kubeconfig file.")

	flag.StringVar(&testContext.namespace,
		"namespace",
		"dcgm-exporter",
		"Namespace name to use for the DCGM-exporter deployment")

	flag.StringVar(&testContext.chart,
		"chart",
		"",
		"Helm chart to use")

	flag.StringVar(&testContext.imageRepository,
		"image-repository",
		"",
		"DCGM-exporter image repository")

	flag.StringVar(&testContext.imageTag,
		"image-tag",
		"",
		"DCGM-exporter image tag to use")

	flag.StringVar(&testContext.arguments,
		"arguments",
		"",
		`DCGM-exporter command line arguments. Example: -arguments="{-f=/etc/dcgm-exporter/default-counters.csv}"`)

	flag.BoolVar(&testContext.noCleanup,
		"no-cleanup",
		false,
		`Skip clean up after tests execution`)

	flag.StringVar(&testContext.runtimeClass,
		"runtime-class",
		"",
		"Runtime Class to use for the DCGM-exporter deployment and workload pods")

	flag.Parse()

	os.Exit(m.Run())
}

func createGinkgoConfig() (types.SuiteConfig, types.ReporterConfig) {
	// fetch the current config
	suiteConfig, reporterConfig := ginkgo.GinkgoConfiguration()
	// Randomize specs as well as suites
	suiteConfig.RandomizeAllSpecs = true
	return suiteConfig, reporterConfig
}

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	// Run tests through the Ginkgo runner with output to console + JUnit for Jenkins
	suiteConfig, reporterConfig := createGinkgoConfig()
	ginkgo.RunSpecs(t, "DCGM-exporter e2e suite", suiteConfig, reporterConfig)
}
