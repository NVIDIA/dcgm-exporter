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

	corev1 "k8s.io/api/core/v1"
)

// chartMountsPodError reports where a running exporter pod installed by the
// chart mounts a file with subPath, does not mount the metrics list
// directory, or does not read its config file from the chart's config
// directory.
func chartMountsPodError(pod *corev1.Pod) error {
	container := findContainer(pod.Spec.Containers, exporterContainerName)
	if container == nil {
		return fmt.Errorf("pod %s has no %q container", pod.Name, exporterContainerName)
	}

	var errs []error
	metricsMounted := false
	for _, mount := range container.VolumeMounts {
		if mount.SubPath != "" {
			errs = append(errs, fmt.Errorf("volume %s is mounted at %s with subPath %s", mount.Name, mount.MountPath, mount.SubPath))
		}
		if mount.MountPath == chartMetricsDirectory {
			metricsMounted = true
		}
	}

	if !metricsMounted {
		errs = append(errs, fmt.Errorf("no volume is mounted at %s", chartMetricsDirectory))
	}

	configFile := ""
	for _, env := range container.Env {
		if env.Name == "DCGM_EXPORTER_CONFIG_FILE" {
			configFile = env.Value
		}
	}

	if configFile != chartConfigFile {
		errs = append(errs, fmt.Errorf("DCGM_EXPORTER_CONFIG_FILE = %q, want %q", configFile, chartConfigFile))
	}

	return errors.Join(errs...)
}

// readOnlyRootFilesystemError reports when the exporter container of pod
// does not run with a read-only root filesystem.
func readOnlyRootFilesystemError(pod *corev1.Pod) error {
	container := findContainer(pod.Spec.Containers, exporterContainerName)
	if container == nil {
		return fmt.Errorf("pod %s has no %q container", pod.Name, exporterContainerName)
	}

	sc := container.SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		return fmt.Errorf("exporter container of pod %s does not set readOnlyRootFilesystem: true", pod.Name)
	}

	return nil
}
