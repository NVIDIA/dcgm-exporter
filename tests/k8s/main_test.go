//go:build k8s

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
package k8s

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var runID = uuid.New()

const defaultTestRunName = "dcgm-exporter"

var testContext = testContextType{}

// envBool parses boolean environment variables used to enable optional suite behavior.
func envBool(names ...string) bool {
	for _, name := range names {
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		switch strings.ToLower(value) {
		case "1", "true", "yes", "y", "on":
			return true
		}
		return false
	}
	return false
}

// TestMain registers Kubernetes integration flags before running the Ginkgo suite.
func TestMain(m *testing.M) {
	runNameDefault := envString("E2E_RUN_NAME", defaultTestRunName)
	namespaceDefault := envString("E2E_HELM_NAMESPACE", runNameDefault)
	dcgmNamespaceDefault := envString("E2E_DCGM_NAMESPACE", namespaceDefault+"-dcgm")

	flag.StringVar(&testContext.kubeconfig,
		"kubeconfig",
		"~/.kube/config",
		"path to the kubeconfig file.")

	flag.StringVar(&testContext.namespace,
		"namespace",
		namespaceDefault,
		"Namespace name to use for the DCGM-exporter deployment")

	flag.StringVar(&testContext.runName,
		"run-name",
		runNameDefault,
		"Run name used to identify namespaces owned by this test run")

	flag.StringVar(&testContext.chart,
		"chart",
		"",
		"Helm chart to use")

	flag.StringVar(&testContext.exporterImage,
		"exporter-image",
		"",
		"Complete DCGM-exporter image reference")

	flag.StringVar(&testContext.gpuOperatorImage,
		"gpu-operator-exporter-image",
		os.Getenv("E2E_GPU_OPERATOR_EXPORTER_IMAGE"),
		"DCGM-exporter image reference expected in GPU Operator-managed pods")

	flag.StringVar(&testContext.k8sImagePullSecret,
		"k8s-image-pull-secret",
		"",
		"Kubernetes image pull secret used for private DCGM-exporter images")

	flag.StringVar(&testContext.arguments,
		"arguments",
		helmArgumentList(defaultExporterArguments()),
		`DCGM-exporter command line arguments as a Helm list. Example: -arguments="{-c=1000,--debug}"`)

	flag.BoolVar(&testContext.noCleanup,
		"no-cleanup",
		false,
		`Skip clean up after tests execution`)

	runIDValue := runID.String()
	flag.StringVar(&runIDValue,
		"run-id",
		runIDValue,
		"Unique e2e invocation identifier used to isolate test artifacts")

	flag.StringVar(&testContext.runtimeClass,
		"runtime-class",
		"",
		"Runtime Class to use for the DCGM-exporter deployment and workload pods")

	flag.StringVar(&testContext.nvidiaVisibleDevices,
		"nvidia-visible-devices",
		"",
		"NVIDIA_VISIBLE_DEVICES value to use for the DCGM-exporter deployment")

	flag.StringVar(&testContext.nodeSelectorKey,
		"node-selector-key",
		"",
		"Node selector label key to use for the DCGM-exporter deployment and workload pods")

	flag.StringVar(&testContext.nodeSelectorVal,
		"node-selector-value",
		"",
		"Node selector label value to use for the DCGM-exporter deployment and workload pods")

	flag.StringVar(&testContext.dcgmProbeBinary,
		"dcgm-probe-binary",
		"",
		"Host path to the e2e DCGM probe binary")

	flag.StringVar(&testContext.remoteDcgm,
		"remote-dcgm",
		"",
		"Remote DCGM host:port used by standalone DCGM Kubernetes scenarios")

	flag.StringVar(&testContext.dcgmImage,
		"dcgm-image",
		os.Getenv("E2E_DCGM_IMAGE"),
		"DCGM image used for field-injection scenarios")

	flag.StringVar(&testContext.busyboxImage,
		"busybox-image",
		os.Getenv("E2E_BUSYBOX_IMAGE"),
		"BusyBox image used for e2e helper pods")

	flag.StringVar(&testContext.cudaWorkloadImage,
		"cuda-workload-image",
		os.Getenv("E2E_CUDA_WORKLOAD_IMAGE"),
		"CUDA image used for workload pods")

	flag.StringVar(&testContext.dcgmNS,
		"dcgm-namespace",
		dcgmNamespaceDefault,
		"Namespace containing the standalone DCGM Service")

	flag.StringVar(&testContext.dcgmName,
		"dcgm-name",
		os.Getenv("E2E_DCGM_NAME"),
		"Standalone DCGM Service name")

	flag.StringVar(&testContext.dcgmPort,
		"dcgm-port",
		os.Getenv("E2E_DCGM_PORT"),
		"Standalone DCGM Service port")

	flag.StringVar(&testContext.migEntityID,
		"mig-instance-entity-id",
		os.Getenv("E2E_MIG_INSTANCE_ENTITY_ID"),
		"DCGM GPU instance entity ID used by specific-MIG-instance selection tests")

	flag.StringVar(&testContext.migNVMLID,
		"mig-instance-nvml-id",
		os.Getenv("E2E_MIG_INSTANCE_NVML_ID"),
		"NVML GPU instance ID expected in exporter labels for specific-MIG-instance selection tests")

	flag.StringVar(&testContext.unsupportedField,
		"unsupported-field-candidate",
		os.Getenv("E2E_UNSUPPORTED_FIELD_CANDIDATE"),
		"DCGM field proven by the e2e probe to return unsupported or blank samples")

	flag.BoolVar(&testContext.ownedLocalCluster,
		"owned-local-cluster",
		false,
		"The cluster is the local k3d cluster the e2e CLI created on this host, so host tools see its GPUs")

	flag.BoolVar(&testContext.resultMarkers,
		"result-markers",
		envBool("E2E_SUITE_RESULT_MARKERS", "E2E_RESULT_MARKERS"),
		"Emit reserved result marker lines for each Ginkgo spec")

	flag.Parse()
	if !runNamePattern.MatchString(testContext.runName) {
		fmt.Fprintf(os.Stderr, "invalid --run-name %q: must be a lowercase DNS label of at most 53 characters\n", testContext.runName)
		os.Exit(2)
	}
	setFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
	applyNamespaceDefaults(setFlags)
	testRunLabels[e2eRunNameLabel] = testContext.runName
	parsedRunID, err := uuid.Parse(runIDValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --run-id %q: %v\n", runIDValue, err)
		os.Exit(2)
	}
	runID = parsedRunID

	os.Exit(m.Run())
}

// envString returns a nonempty environment value or its fallback.
func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// runNamePattern matches the e2e CLI rule: a lowercase DNS label that stays
// valid after the derived "-dcgm" namespace suffix and ownership labels.
var runNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,51}[a-z0-9])?$`)

// TestRunNamePatternMatchesCLIRule keeps the harness run-name check aligned with the CLI rule.
func TestRunNamePatternMatchesCLIRule(t *testing.T) {
	for name, want := range map[string]bool{
		"dcgm-exporter":         true,
		"a":                     true,
		strings.Repeat("a", 53): true,
		strings.Repeat("a", 54): false,
		"":                      false,
		"Upper":                 false,
		"-leading":              false,
		"trailing-":             false,
		"under_score":           false,
	} {
		if got := runNamePattern.MatchString(name); got != want {
			t.Errorf("runNamePattern.MatchString(%q) = %t, want %t", name, got, want)
		}
	}
}

// applyNamespaceDefaults derives unset namespaces from the run name and Helm namespace.
func applyNamespaceDefaults(setFlags map[string]bool) {
	if !setFlags["namespace"] && os.Getenv("E2E_HELM_NAMESPACE") == "" {
		testContext.namespace = testContext.runName
	}
	if !setFlags["dcgm-namespace"] && os.Getenv("E2E_DCGM_NAMESPACE") == "" {
		testContext.dcgmNS = testContext.namespace + "-dcgm"
	}
}

// TestKubernetesIntegration runs the Kubernetes integration Ginkgo suite.
func TestKubernetesIntegration(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	// Run tests through the Ginkgo runner with output to console + JUnit for Jenkins
	suiteConfig, reporterConfig := createGinkgoConfig()
	ginkgo.RunSpecs(t, "DCGM-exporter Kubernetes integration suite", suiteConfig, reporterConfig)
}
