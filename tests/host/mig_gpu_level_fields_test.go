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

package host

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// The e2e CLI sets migLayoutEnv to migLayout after it has applied the MIG
// layout the catalog declares for this scenario. A plain "go test ./tests/host"
// leaves it unset, and the scenario skips, because it only checks that layout.
const (
	migLayoutEnv = "E2E_MIG_LAYOUT"
	migLayout    = "two-instances-per-gpu"
)

// The scenario scrapes every migScrapeEvery until migScrapeWindow has passed
// since the first full scrape, so the exporter runs through several of its
// default 30-second collections.
const (
	migScrapeWindow = 65 * time.Second
	migScrapeEvery  = 5 * time.Second
)

// migInstancesPerGPU is the number of GPU instances the layout creates on
// every GPU.
const migInstancesPerGPU = 2

// runMIGGPULevelFields runs the exporter with its defaults (flex device
// selection and the default CSV) on a host where every GPU has two MIG
// instances. Every scrape over a minute must succeed with the exporter still
// running, and every scrape must report each GPU-level family once per
// physical GPU without MIG labels, while instance-level families keep one
// series per instance.
func runMIGGPULevelFields(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	t.Helper()
	if os.Getenv(migLayoutEnv) != migLayout {
		t.Skipf("%s=%s is not set; the e2e CLI sets it after it puts every GPU in MIG mode with two instances", migLayoutEnv, migLayout)
	}

	gpus := hostGPUIndexes(t)

	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	process, _ := startExporterAndWaitForFamilies(
		t,
		metricsURL,
		nil,
		[]string{"DCGM_FI_DEV_BOARD_POWER_WATTS", "DCGM_FI_DEV_FB_USED_BYTES"},
		"--collectors", canonicalDefaultCollectorsFile(t),
		"--address", fmt.Sprintf("127.0.0.1:%d", port),
	)

	scrapes := 0
	for deadline := time.Now().Add(migScrapeWindow); ; scrapes++ {
		scrape := scrapeGPUMetrics(t, metricsURL, process)
		requireTwoInstancesPerGPU(t, scrape, gpus)
		require.NoErrorf(t, metriccontract.ValidateMIGSeriesShape(scrape), "scrape %d", scrapes+1)

		if time.Now().After(deadline) {
			break
		}
		time.Sleep(migScrapeEvery)
	}

	require.NoError(t, process.running())
	t.Logf("%d scrapes over %s served the MIG series shape", scrapes+1, migScrapeWindow)
}

// hostGPUIndexes returns the index of every GPU nvidia-smi lists, in order,
// as the exporter's gpu label writes them.
func hostGPUIndexes(t testing.TB) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	identities, err := nvidiasmi.QueryGPUIdentity(ctx, nvidiasmi.LocalRunner)
	require.NoError(t, err, "list the host GPUs with nvidia-smi")

	indexes := make([]string, 0, len(identities))
	for _, gpu := range identities {
		indexes = append(indexes, strconv.Itoa(gpu.Index))
	}
	slices.Sort(indexes)
	return indexes
}

// requireTwoInstancesPerGPU checks that the scrape shows the layout the CLI
// applied: every host GPU in gpus is a MIG GPU with two GPU instances, and
// each instance has exactly one framebuffer-used series.
func requireTwoInstancesPerGPU(t testing.TB, scrape gpuScrape, gpus []string) {
	t.Helper()
	instances := metriccontract.MIGInstances(scrape)
	require.Equal(t, gpus, slices.Sorted(maps.Keys(instances)), "MIG GPUs in the scrape")

	for gpu, ids := range instances {
		require.Lenf(t, ids, migInstancesPerGPU, "GPU instances on gpu=%s", gpu)
		fbSeries := map[string]int{}
		for _, metric := range scrape["DCGM_FI_DEV_FB_USED_BYTES"].GetMetric() {
			if labels := labelMap(metric); labels["gpu"] == gpu {
				fbSeries[labels["GPU_I_ID"]]++
			}
		}
		want := map[string]int{}
		for _, id := range ids {
			want[id] = 1
		}
		require.Equalf(t, want, fbSeries, "DCGM_FI_DEV_FB_USED_BYTES series per GPU_I_ID on gpu=%s", gpu)
	}
}
