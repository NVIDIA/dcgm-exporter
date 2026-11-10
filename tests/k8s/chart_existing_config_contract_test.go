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

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// existingConfigKey is the key of the config file in the scenario's ConfigMap;
// it differs from the chart's default so the scenario checks configMap.key.
const existingConfigKey = "exporter.yaml"

// existingConfigPodError reports when the exporter container of pod does not
// read its config file from the chart's config directory under key.
func existingConfigPodError(pod *corev1.Pod, key string) error {
	container := findContainer(pod.Spec.Containers, exporterContainerName)
	if container == nil {
		return fmt.Errorf("pod %s has no %q container", pod.Name, exporterContainerName)
	}

	want := chartConfigDirectory + "/" + key
	for _, env := range container.Env {
		if env.Name == "DCGM_EXPORTER_CONFIG_FILE" {
			if env.Value != want {
				return fmt.Errorf("DCGM_EXPORTER_CONFIG_FILE = %q, want %q", env.Value, want)
			}

			return nil
		}
	}

	return fmt.Errorf("pod %s does not set DCGM_EXPORTER_CONFIG_FILE", pod.Name)
}

// existingConfigMetricsError reports when a scrape does not come from
// existingConfigDocument: it must have the two listed fields and the go_*
// metrics, and none of the default list's other fields.
func existingConfigMetricsError(metrics []byte) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}

	var missing []string
	for _, name := range []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_FB_USED_BYTES", "go_goroutines"} {
		if family := families[name]; family == nil || len(family.GetMetric()) == 0 {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("the config file's metrics are missing: %v", missing)
	}

	if family := families["DCGM_FI_DEV_BOARD_POWER_WATTS"]; family != nil && len(family.GetMetric()) > 0 {
		return errors.New("DCGM_FI_DEV_BOARD_POWER_WATTS is exported, so the default list was used instead of the config file")
	}

	return nil
}
