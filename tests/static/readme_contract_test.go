/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package static_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	installGuideURL    = "https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html"
	prometheusGuideURL = "https://docs.nvidia.com/datacenter/dcgm/latest/learn/" +
		"getting-started-for-system-administrators/configure-prometheus-for-dcgm-exporter.html"
	commandReferenceURL = "https://docs.nvidia.com/datacenter/dcgm/latest/reference/" +
		"command-line-reference/dcgm-exporter.html"
	metricsReferenceURL  = "https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html"
	releaseNotesURL      = "https://docs.nvidia.com/datacenter/dcgm/latest/release-notes/dcgm-exporter.html"
	enterpriseSupportURL = "https://www.nvidia.com/en-us/data-center/products/" +
		"ai-enterprise-suite/support/"
)

// TestReadmeDocumentationContract checks the root and Helm READMEs retain their required links and scope.
func TestReadmeDocumentationContract(t *testing.T) {
	rootReadme := readRootReadme(t)

	for _, required := range []string{
		installGuideURL,
		prometheusGuideURL,
		commandReferenceURL,
		metricsReferenceURL,
		releaseNotesURL,
		"deployment/README.md",
		"CONTRIBUTING.md",
		"tests/k8s/README.md",
		"tests/container/README.md",
		"etc/default-counters.csv",
	} {
		require.Contains(t, rootReadme, required)
	}

	for _, forbidden := range []string{
		"```",
		"docker run",
		"kubectl ",
		"helm install",
		"task build:binary",
		"sync:docker-run-example",
		"/gpu-telemetry/dcgm-exporter",
	} {
		require.NotContains(t, rootReadme, forbidden)
	}

	helmReadme := readRepositoryFile(t, "deployment", "README.md")
	for _, required := range []string{
		installGuideURL,
		prometheusGuideURL,
		commandReferenceURL,
		metricsReferenceURL,
		releaseNotesURL,
		enterpriseSupportURL,
		"task helm:package",
		"dist/dcgm-exporter.tgz",
		"core.symlinks=false",
	} {
		require.Contains(t, helmReadme, required)
	}
	for _, obsolete := range []string{
		"make helm-chart",
		"bazel-bin/deployment/dcgm-exporter.tgz",
	} {
		require.NotContains(t, helmReadme, obsolete)
	}
	require.NotContains(t, helmReadme, "/gpu-telemetry/dcgm-exporter")
}

// readRootReadme selects the repository README or its validation-package copy.
func readRootReadme(t *testing.T) string {
	t.Helper()

	workingDir := mustGetwd(t)
	packageRoot, err := isValidationPackageRoot(workingDir)
	require.NoError(t, err)
	if packageRoot {
		return readRepositoryFile(t, "etc", "dcgm-exporter-source-README.md")
	}
	return readRepositoryFile(t, "README.md")
}

// readRepositoryFile reads a repository asset and trims surrounding whitespace.
func readRepositoryFile(t *testing.T, elems ...string) string {
	t.Helper()

	data, err := os.ReadFile(repoPath(t, elems...))
	require.NoError(t, err)

	return strings.TrimSpace(string(data))
}
