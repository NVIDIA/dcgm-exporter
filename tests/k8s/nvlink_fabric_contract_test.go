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
	"strconv"
	"strings"

	dto "github.com/prometheus/client_model/go"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// nvlinkFabricLinkFamilies are the DCGM fields that DCGM reads per GPU link.
// The profiling pair needs DCGM profiling support; DCGM documents the byte
// totals as NVLink 5 only. Any one of them proves the exporter reports a link.
var nvlinkFabricLinkFamilies = []string{
	"DCGM_FI_PROF_NVLINK_TX_BYTES",
	"DCGM_FI_PROF_NVLINK_RX_BYTES",
	"DCGM_FI_DEV_NVLINK_TX_BYTES_TOTAL",
	"DCGM_FI_DEV_NVLINK_RX_BYTES_TOTAL",
}

// nvlinkFabricCustomMetrics returns the CSV rows the nvlinkFabric deployment
// collects: one plain GPU field plus every per-link NVLink field.
func nvlinkFabricCustomMetrics() []string {
	return []string{
		"DCGM_FI_DEV_GPU_TEMP_CELSIUS, gauge, GPU temperature (in C).",
		"DCGM_FI_PROF_NVLINK_TX_BYTES, gauge, NVLink transmit bytes per second.",
		"DCGM_FI_PROF_NVLINK_RX_BYTES, gauge, NVLink receive bytes per second.",
		"DCGM_FI_DEV_NVLINK_TX_BYTES_TOTAL, counter, NVLink transmitted bytes.",
		"DCGM_FI_DEV_NVLINK_RX_BYTES_TOTAL, counter, NVLink received bytes.",
	}
}

// nvlinkFabricLinkKey names one GPU link by GPU UUID and link index label.
type nvlinkFabricLinkKey struct {
	uuid string
	link string
}

// nvlinkFabricContract checks that the exporter reports every link that
// `nvidia-smi nvlink -s` lists as active. A link counts as reported when any
// per-link NVLink family has a sample with the GPU's UUID in gpu_uuid and the
// link index in nvlink. All DCGM samples must also pass the shared sample
// checks.
func nvlinkFabricContract(metrics []byte, active []nvidiasmi.NVLink) error {
	if len(active) == 0 {
		return errors.New("nvidia-smi nvlink -s reported no active NVLink")
	}

	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return fmt.Errorf("parse nvlinkFabric metrics: %w", err)
	}

	reported := reportedNVLinks(families)

	errs := []error{metriccontract.ValidateDCGMSamples(families)}
	for _, link := range active {
		key := nvlinkFabricLinkKey{uuid: link.GPUUUID, link: strconv.Itoa(link.Link)}
		if reported[key] {
			continue
		}
		errs = append(errs, fmt.Errorf(
			"GPU %d (%s) link %d: nvidia-smi nvlink -s reports it active, but no %s sample has gpu_uuid=%q and nvlink=%q",
			link.GPU, link.GPUUUID, link.Link, strings.Join(nvlinkFabricLinkFamilies, " or "), key.uuid, key.link,
		))
	}

	return errors.Join(errs...)
}

// reportedNVLinks returns the links that have at least one sample in a
// per-link NVLink family, keyed by the gpu_uuid and nvlink labels.
func reportedNVLinks(families map[string]*dto.MetricFamily) map[nvlinkFabricLinkKey]bool {
	reported := map[nvlinkFabricLinkKey]bool{}
	for _, name := range nvlinkFabricLinkFamilies {
		for _, metric := range families[name].GetMetric() {
			var key nvlinkFabricLinkKey
			for _, label := range metric.GetLabel() {
				switch label.GetName() {
				case "gpu_uuid":
					key.uuid = label.GetValue()
				case "nvlink":
					key.link = label.GetValue()
				}
			}

			if key.uuid != "" && key.link != "" {
				reported[key] = true
			}
		}
	}

	return reported
}
