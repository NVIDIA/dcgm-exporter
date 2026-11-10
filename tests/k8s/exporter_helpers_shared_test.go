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

package k8s

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Paths of the files the chart mounts. Kubernetes tests share these.
const (
	// chartFilesDirectory holds the config files the chart mounts.
	chartFilesDirectory = "/etc/dcgm-exporter/chart"
	// chartConfigDirectory is where the chart mounts its config ConfigMap.
	chartConfigDirectory = chartFilesDirectory + "/config"
	// chartConfigFile is the config file there with the default configMap.key.
	chartConfigFile = chartConfigDirectory + "/config.yaml"
	// chartMetricsDirectory is where the chart mounts its metrics list ConfigMap.
	chartMetricsDirectory = chartFilesDirectory + "/metrics"
)

const (
	debugDumpRootDirectory = "/tmp/dcgm-exporter-debug/e2e"
	debugDumpReaderMount   = "/debug-dumps"
)

// debugDumpDirectoryForRun returns the host directory owned by a Kubernetes test run.
func debugDumpDirectoryForRun(runIdentifier string) string {
	return filepath.Join(debugDumpRootDirectory, runIdentifier)
}

// debugDumpReaderDirectory maps a run-owned host directory into the reader pod.
func debugDumpReaderDirectory(debugDumpDirectory, runIdentifier string) (string, error) {
	relativePath, err := filepath.Rel(debugDumpRootDirectory, debugDumpDirectory)
	if err != nil {
		return "", fmt.Errorf("get debug dump path relative to test root: %w", err)
	}
	if relativePath != runIdentifier || filepath.IsAbs(relativePath) {
		return "", errors.New("debug dump cleanup may only target this test run directory")
	}
	return filepath.Join(debugDumpReaderMount, relativePath), nil
}

// singleNodeName returns one usable node name and rejects ambiguous selections.
func singleNodeName(nodes []corev1.Node) (string, error) {
	if len(nodes) != 1 {
		return "", fmt.Errorf("debug-dump tests require exactly one selected node, found %d", len(nodes))
	}
	if nodes[0].Name == "" {
		return "", errors.New("selected node has no name")
	}
	return nodes[0].Name, nil
}

// defaultExporterArguments returns the exporter arguments every scenario
// starts from: a 1000 ms collect interval. The chart's config names the
// metrics list. Each call returns a new slice that callers may append to.
func defaultExporterArguments() []string {
	return []string{"-c=1000"}
}

// helmArgumentList formats exporter arguments as a Helm --set list, {a,b}.
// Helm splits a list at every comma, so a comma inside one argument (such as
// the two links in --switch-devices=g:1+i:71,72) is escaped as \, to keep the
// argument whole.
func helmArgumentList(args []string) string {
	escaped := make([]string, len(args))
	for i, arg := range args {
		escaped[i] = strings.ReplaceAll(arg, ",", `\,`)
	}

	return "{" + strings.Join(escaped, ",") + "}"
}
