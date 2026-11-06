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
	"math"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// Families the GPU load scenario reads.
const (
	gpuUtilFamily      = "DCGM_FI_DEV_GPU_UTIL_RATIO"
	grEngineUtilFamily = "DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO"
	gpuEnergyFamily    = "DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL"
)

// A GPU under full load reports utilization near 1 and an idle GPU near 0, so
// the scenario expects a loaded GPU above loadedUtilMin and an idle GPU below
// idleUtilMax. A loaded GPU draws clearly more power than an idle one, so over
// the same time it must use at least minEnergyRatio times the energy of the
// busiest idle GPU.
const (
	loadedUtilMin  = 0.5
	idleUtilMax    = 0.2
	minEnergyRatio = 1.2
)

// The load must show within loadStartTimeout and is then measured for
// loadMeasurePeriod. It runs for loadDuration, which covers both with a
// margin, and is stopped as soon as the measurement ends.
const (
	loadStartTimeout  = time.Minute
	loadMeasurePeriod = 20 * time.Second
	loadDuration      = loadStartTimeout + loadMeasurePeriod + 30*time.Second
)

// dcgmImageEnv names the DCGM image that provides dcgmproftester. The e2e CLI
// sets it from --dcgm-image.
const dcgmImageEnv = "DCGM_IMAGE"

// gpuLoadCounters enables utilization and energy, and graphics engine
// activity when DCGM profiling is supported.
func gpuLoadCounters(profiling bool) []string {
	rows := []string{
		gpuUtilFamily + ", gauge, GPU utilization as a ratio.",
		gpuEnergyFamily + ", counter, GPU energy consumed since the driver was reloaded in joules.",
	}
	if profiling {
		rows = append(rows, grEngineUtilFamily+", gauge, Ratio of time the graphics engine is active.")
	}
	return rows
}

// loadGPU is one GPU the scenario loads or keeps idle: its exporter gpu label
// and its UUID.
type loadGPU struct {
	index string
	uuid  string
}

// runGPULoadUtilization loads the highest-ID idle GPU with dcgmproftester and
// checks that only that GPU's utilization rises and that it uses more energy
// than every other idle GPU. It skips when the host has fewer than two idle
// GPUs or a CUDA version without a matching dcgmproftester.
func runGPULoadUtilization(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	image := strings.TrimSpace(os.Getenv(dcgmImageEnv))
	require.NotEmptyf(t, image, "%s must name the DCGM image that provides dcgmproftester; the e2e CLI sets it from --dcgm-image", dcgmImageEnv)
	tester := requireProfTester(t, image)
	engine := startUnixHostEngine(t)

	// Graphics engine activity is checked only when DCGM lists the profiling field.
	profileOut, profileOK := engine.dcgmi(t, "profile", "--list")
	profiling := profilingSupported(profileOut, profileOK)
	if profiling {
		t.Logf("DCGM lists %s; checking it too", grEngineUtilFamily)
	} else {
		t.Logf("DCGM shows no profiling support; not checking %s: %s", grEngineUtilFamily, strings.TrimSpace(profileOut))
	}

	families := []string{gpuUtilFamily, gpuEnergyFamily}
	if profiling {
		families = append(families, grEngineUtilFamily)
	}
	process, metricsURL := startFastExporterOnHostEngine(t, engine, gpuLoadCounters(profiling), families)

	// Load the highest-ID idle GPU; every other idle GPU is a control.
	idle := exporterGPUs(t, scrapeGPUMetrics(t, metricsURL, process), queryIdleGPUs(t))
	loaded, controls := idle[len(idle)-1], idle[:len(idle)-1]
	t.Logf("loading GPU %s (%s) with %s; idle GPUs %v", loaded.index, loaded.uuid, tester, controls)

	load := startProfTesterLoad(t, image, tester, loaded.uuid)
	start := waitForLoad(t, metricsURL, process, loaded, load)
	measureLoad(t, loadGPUs{loaded: loaded, controls: controls}, metricsURL, process, load, profiling, start)
	load.stop(t)
}

// requireProfTester returns the dcgmproftester binary for the driver's CUDA
// version and fails the test when image does not have it. It skips when no
// dcgmproftester matches the CUDA version.
func requireProfTester(t testing.TB, image string) string {
	t.Helper()
	major, minor, err := nvidiasmi.QueryCUDAVersion(context.Background(), nvidiasmi.LocalRunner)
	require.NoError(t, err)

	tester, ok := profTesterForCUDA(major)
	if !ok {
		t.Skipf("no dcgmproftester matches CUDA %d.%d", major, minor)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	args := []string{"run", "--rm", "--entrypoint", "/usr/bin/test", image, "-x", "/usr/bin/" + tester}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput() // #nosec G204 G702 -- the image is the harness's --dcgm-image input and the tester name is a constant.
	require.NoErrorf(t, err, "DCGM image %s has no /usr/bin/%s for CUDA %d.%d: %s", image, tester, major, minor, out)

	return tester
}

// profTesterForCUDA returns the dcgmproftester built for a driver that
// supports CUDA major. A newer driver runs the newest tester.
func profTesterForCUDA(major int) (string, bool) {
	switch {
	case major >= 13:
		return "dcgmproftester13", true
	case major == 12:
		return "dcgmproftester12", true
	default:
		return "", false
	}
}

// queryIdleGPUs returns the GPUs nvidia-smi reports with MIG off, no compute
// process and utilization below idleUtilMax, in index order. It skips the
// test when there are fewer than two.
func queryIdleGPUs(t testing.TB) []nvidiasmi.GPUUtilization {
	t.Helper()
	ctx := context.Background()
	utilization, err := nvidiasmi.QueryGPUUtilization(ctx, nvidiasmi.LocalRunner)
	require.NoError(t, err)
	inventory, err := hostInventory.Identity(ctx)
	require.NoError(t, err)
	busy, err := nvidiasmi.QueryComputeAppUUIDs(ctx, nvidiasmi.LocalRunner)
	require.NoError(t, err)

	idle, err := idleGPUs(utilization, inventory, busy)
	require.NoError(t, err)
	if len(idle) < 2 {
		t.Skipf("needs two idle GPUs (MIG off, no compute process, utilization.gpu below %g%%); nvidia-smi found %d of %d",
			idleUtilMax*100, len(idle), len(utilization))
	}
	return idle
}

// idleGPUs returns the GPUs with a reported utilization below idleUtilMax, no
// compute process and MIG off, in index order. A GPU with MIG on never counts
// as idle, because a process on one of its MIG devices is reported with the
// MIG device UUID, which this query cannot map to its GPU. A MIG device
// process with no GPU in MIG mode is an error, because the busy GPU is unknown.
func idleGPUs(utilization []nvidiasmi.GPUUtilization, inventory []nvidiasmi.GPU, busyUUIDs []string) ([]nvidiasmi.GPUUtilization, error) {
	excluded, err := busyOrMIGGPUUUIDs(inventory, busyUUIDs)
	if err != nil {
		return nil, err
	}

	var idle []nvidiasmi.GPUUtilization
	for _, gpu := range utilization {
		reading := gpu.UtilizationPercent
		if !reading.Reported || reading.Value >= idleUtilMax*100 || slices.Contains(excluded, gpu.UUID) {
			continue
		}

		idle = append(idle, gpu)
	}

	slices.SortFunc(idle, func(a, b nvidiasmi.GPUUtilization) int { return a.Index - b.Index })
	return idle, nil
}

// exporterGPUs maps each idle GPU to its exporter gpu label and fails the test
// unless the exporter has exactly one utilization sample for each UUID.
func exporterGPUs(t testing.TB, scrape gpuScrape, idle []nvidiasmi.GPUUtilization) []loadGPU {
	t.Helper()
	gpus, problems := matchExporterGPUs(scrape, idle)
	require.Empty(t, problems, "the exporter's %s samples do not match the idle GPUs nvidia-smi reports:\n%s",
		gpuUtilFamily, strings.Join(problems, "\n"))
	return gpus
}

// matchExporterGPUs maps each idle GPU to the gpu label of its one
// utilization sample, and lists the GPUs without exactly one sample.
func matchExporterGPUs(scrape gpuScrape, idle []nvidiasmi.GPUUtilization) ([]loadGPU, []string) {
	var gpus []loadGPU
	var problems []string
	for _, gpu := range idle {
		labels := scrape.gpusWithUUID(gpuUtilFamily, gpu.UUID)
		if len(labels) != 1 {
			problems = append(problems, fmt.Sprintf("GPU %d (%s) has %d %s samples, want 1", gpu.Index, gpu.UUID, len(labels), gpuUtilFamily))
			continue
		}

		gpus = append(gpus, loadGPU{index: labels[0], uuid: gpu.UUID})
	}
	return gpus, problems
}

// loadGPUs is the GPU under load and the idle GPUs it is compared with.
type loadGPUs struct {
	loaded   loadGPU
	controls []loadGPU
}

// measureLoad scrapes through the measure period and checks the utilization
// of every GPU at every scrape and their energy use over the whole period. The
// load must still run at every scrape.
func measureLoad(
	t testing.TB, gpus loadGPUs, metricsURL string, process *hostExporterProcess, load *profTesterLoad,
	profiling bool, start gpuScrape,
) {
	t.Helper()
	loaded, controls := gpus.loaded, gpus.controls
	utilFamilies := []string{gpuUtilFamily}
	if profiling {
		utilFamilies = append(utilFamilies, grEngineUtilFamily)
	}

	end := start
	for deadline := time.Now().Add(loadMeasurePeriod); time.Now().Before(deadline); {
		time.Sleep(2 * time.Second)
		require.Truef(t, load.running(), "the load stopped during the measurement:\n%s", load.output.String())
		end = scrapeGPUMetrics(t, metricsURL, process)
		problems := loadUtilProblems(end, loaded, controls, utilFamilies)
		require.Empty(t, problems, "during the load:\n%s", strings.Join(problems, "\n"))
	}

	loadedEnergy := energyUsed(t, start, end, loaded)
	busiestControl, controlEnergy := controls[0], energyUsed(t, start, end, controls[0])
	for _, control := range controls[1:] {
		if used := energyUsed(t, start, end, control); used > controlEnergy {
			busiestControl, controlEnergy = control, used
		}
	}

	t.Logf("over %s: loaded GPU %s used %.0f J; the busiest idle GPU, %s, used %.0f J",
		loadMeasurePeriod, loaded.index, loadedEnergy, busiestControl.index, controlEnergy)
	require.Truef(t, energyExceeds(loadedEnergy, controlEnergy),
		"over %s the loaded GPU %s used %.0f J and idle GPU %s used %.0f J; want the loaded GPU to use at least %g times as much",
		loadMeasurePeriod, loaded.index, loadedEnergy, busiestControl.index, controlEnergy, minEnergyRatio)
}

// waitForLoad scrapes until the loaded GPU's utilization is above
// loadedUtilMin and returns that scrape. It fails when the load ends first or
// does not show within loadStartTimeout.
func waitForLoad(t testing.TB, metricsURL string, process *hostExporterProcess, loaded loadGPU, load *profTesterLoad) gpuScrape {
	t.Helper()
	deadline := time.Now().Add(loadStartTimeout)
	for {
		scrape := scrapeGPUMetrics(t, metricsURL, process)
		if util, ok := scrape.value(gpuUtilFamily, loaded.index); ok && util > loadedUtilMin {
			return scrape
		}

		require.Truef(t, load.running(), "the load stopped before it showed:\n%s", load.output.String())
		require.Truef(t, time.Now().Before(deadline), "GPU %s %s stayed at or below %g for %s of load\n%s",
			loaded.index, gpuUtilFamily, loadedUtilMin, loadStartTimeout, load.output.String())
		time.Sleep(time.Second)
	}
}

// loadUtilProblems lists utilization samples that do not fit a load on
// loaded: each family must be above loadedUtilMin on loaded and below
// idleUtilMax on every control GPU.
func loadUtilProblems(scrape gpuScrape, loaded loadGPU, controls []loadGPU, families []string) []string {
	var problems []string
	for _, family := range families {
		if util, ok := scrape.value(family, loaded.index); !ok || !isFinite(util) || util <= loadedUtilMin {
			problems = append(problems, fmt.Sprintf("loaded GPU %s %s = %g (present %t), want > %g", loaded.index, family, util, ok, loadedUtilMin))
		}

		for _, control := range controls {
			if util, ok := scrape.value(family, control.index); !ok || !isFinite(util) || util >= idleUtilMax {
				problems = append(problems, fmt.Sprintf("idle GPU %s %s = %g (present %t), want < %g", control.index, family, util, ok, idleUtilMax))
			}
		}
	}
	return problems
}

// isFinite reports whether v is a number, not NaN or an infinity, which would
// pass or fail a threshold comparison for the wrong reason.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// energyUsed returns how many joules gpu used between two scrapes.
func energyUsed(t testing.TB, start, end gpuScrape, gpu loadGPU) float64 {
	t.Helper()
	before, ok := start.value(gpuEnergyFamily, gpu.index)
	require.Truef(t, ok, "GPU %s has no %s sample at the start of the load", gpu.index, gpuEnergyFamily)
	after, ok := end.value(gpuEnergyFamily, gpu.index)
	require.Truef(t, ok, "GPU %s has no %s sample at the end of the load", gpu.index, gpuEnergyFamily)
	return after - before
}

// energyExceeds reports whether a loaded GPU's energy use is positive and at
// least minEnergyRatio times the control GPU's.
func energyExceeds(loaded, control float64) bool {
	return loaded > 0 && loaded >= minEnergyRatio*control
}

// profilingEvidence matches a dcgmi profile --list line that lists graphics
// engine activity, by field ID 1001 or by name.
var profilingEvidence = regexp.MustCompile(`(?i)\|\s*1001\s*\||gr_engine_active`)

// profilingSupported reads dcgmi profile --list output the way the e2e
// capability probe does: profiling is supported only when the command
// succeeded and a line that does not say "not supported" lists graphics
// engine activity. Anything else counts as unsupported.
func profilingSupported(output string, succeeded bool) bool {
	if !succeeded {
		return false
	}

	for _, line := range strings.Split(output, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "not supported") || strings.Contains(lower, "unsupported") {
			continue
		}

		if profilingEvidence.MatchString(line) {
			return true
		}
	}
	return false
}

// profTesterLoad is a dcgmproftester container that loads one GPU. The test
// owns the docker run process through cancel, so the container cannot be
// created after the test removed it.
type profTesterLoad struct {
	name     string
	cancel   context.CancelFunc
	done     chan error
	output   *lockedBuffer
	stopOnce sync.Once
}

// startProfTesterLoad runs tester from image on the GPU with uuid for
// loadDuration at its highest graphics activity, and stops it when the test
// ends.
func startProfTesterLoad(t testing.TB, image, tester, uuid string) *profTesterLoad {
	t.Helper()
	name := fmt.Sprintf("dcgm-exporter-e2e-load-%d-%d", os.Getpid(), time.Now().UnixNano())
	args := profTesterArgs(name, image, tester, uuid, loadDuration)

	ctx, cancel := context.WithCancel(context.Background())
	// #nosec G204 -- docker is found on PATH and the arguments are built by this test.
	cmd := exec.CommandContext(ctx, "docker", args...)
	output := &lockedBuffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start docker %v: %v", args, err)
	}

	load := &profTesterLoad{name: name, cancel: cancel, done: make(chan error, 1), output: output}
	go func() { load.done <- cmd.Wait() }()
	t.Cleanup(func() { load.stop(t) })
	return load
}

// profTesterArgs returns the docker arguments that run tester on one GPU for
// duration, rounded up to whole seconds. Without --target-max-value the
// tester ramps activity from zero, so the GPU would be busy only part of the time.
func profTesterArgs(name, image, tester, uuid string, duration time.Duration) []string {
	seconds := int((duration + time.Second - 1) / time.Second)
	return []string{
		"run", "--rm", "--name", name,
		"--gpus", "device=" + uuid,
		"--entrypoint", "/usr/bin/" + tester,
		image,
		"--no-dcgm-validation", "--target-max-value",
		"-t", "1001",
		"-d", strconv.Itoa(seconds),
	}
}

// running reports whether the load container is still running.
func (l *profTesterLoad) running() bool {
	select {
	case err := <-l.done:
		l.done <- err
		return false
	default:
		return true
	}
}

// stop ends the load once: it removes the container, which ends the tester,
// and waits up to a minute for docker run to return. If docker run is still
// running, it is killed and awaited. Only then is the container removed again,
// so a container that docker run created late cannot keep running.
func (l *profTesterLoad) stop(t testing.TB) {
	t.Helper()
	l.stopOnce.Do(func() {
		l.removeContainer(t, false)

		select {
		case err := <-l.done:
			// Removing the container ends the tester early, so its exit status is only logged.
			t.Logf("docker run for container %s returned: %v", l.name, err)
		case <-time.After(time.Minute):
			t.Logf("docker run for container %s did not return a minute after removal; killing it:\n%s", l.name, l.output.String())
			l.cancel()
			t.Logf("docker run for container %s returned after kill: %v", l.name, <-l.done)
		}

		l.cancel()
		l.removeContainer(t, true)
	})
}

// removeContainer force-removes the load container. Before docker run has
// exited, a failure is only logged, because docker run may still create the
// container. After it has exited (runExited), a missing container is expected
// and any other failure fails the test, so a load container cannot outlive it.
func (l *profTesterLoad) removeContainer(t testing.TB, runExited bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// #nosec G204 -- docker is found on PATH and the container name was created by this test.
	out, err := exec.CommandContext(ctx, "docker", "rm", "-f", l.name).CombinedOutput()
	if err == nil || (runExited && strings.Contains(string(out), "No such container")) {
		return
	}

	if runExited {
		t.Errorf("remove load container %s after docker run exited: %v: %s", l.name, err, out)
		return
	}

	t.Logf("remove load container %s before docker run exited: %v: %s", l.name, err, out)
}

// TestIdleGPUsSkipsBusyAndLoadedGPUs checks that GPUs with a compute process,
// high or unreported utilization are not chosen, and that the rest come in index order.
func TestIdleGPUsSkipsBusyAndLoadedGPUs(t *testing.T) {
	reported := func(value float64) nvidiasmi.Reading { return nvidiasmi.Reading{Value: value, Reported: true} }
	utilization := []nvidiasmi.GPUUtilization{
		{Index: 3, UUID: "GPU-d", UtilizationPercent: reported(0)},
		{Index: 0, UUID: "GPU-a", UtilizationPercent: reported(0)},
		{Index: 1, UUID: "GPU-b", UtilizationPercent: reported(90)},
		{Index: 2, UUID: "GPU-c", UtilizationPercent: reported(5)},
		{Index: 4, UUID: "GPU-e", UtilizationPercent: nvidiasmi.Reading{Raw: "[N/A]"}},
		{Index: 5, UUID: "GPU-f", UtilizationPercent: reported(20)},
		{Index: 6, UUID: "GPU-g", UtilizationPercent: reported(0)},
	}
	inventory := []nvidiasmi.GPU{{Index: 6, UUID: "GPU-g", MIGEnabled: true}, {Index: 3, UUID: "GPU-d"}}

	tests := []struct {
		name    string
		busy    []string
		want    []nvidiasmi.GPUUtilization
		wantErr string
	}{
		{name: "GPU process and MIG on", busy: []string{"GPU-a"}, want: []nvidiasmi.GPUUtilization{utilization[3], utilization[0]}},
		{name: "MIG device process", busy: []string{"MIG-1"}, want: []nvidiasmi.GPUUtilization{utilization[1], utilization[3], utilization[0]}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := idleGPUs(utilization, inventory, tc.busy)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	_, err := idleGPUs(utilization, inventory[1:], []string{"MIG-1"})
	require.ErrorContains(t, err, "no GPU with MIG on", "a MIG device process with no MIG GPU leaves the busy GPU unknown")
}

// TestMatchExporterGPUs checks that each idle GPU needs exactly one exporter
// utilization sample with its UUID.
func TestMatchExporterGPUs(t *testing.T) {
	scrape := parseTestScrape(t, `# TYPE DCGM_FI_DEV_GPU_UTIL_RATIO gauge
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="0",UUID="GPU-a"} 0
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="1",UUID="GPU-b"} 0
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="2",UUID="GPU-b"} 0
`)
	idle := []nvidiasmi.GPUUtilization{{Index: 0, UUID: "GPU-a"}, {Index: 1, UUID: "GPU-b"}, {Index: 7, UUID: "GPU-z"}}
	gpus, problems := matchExporterGPUs(scrape, idle)
	require.Equal(t, []loadGPU{{index: "0", uuid: "GPU-a"}}, gpus)
	require.Equal(t, []string{
		"GPU 1 (GPU-b) has 2 DCGM_FI_DEV_GPU_UTIL_RATIO samples, want 1",
		"GPU 7 (GPU-z) has 0 DCGM_FI_DEV_GPU_UTIL_RATIO samples, want 1",
	}, problems)
}

// TestProfilingSupported checks that only positive evidence from a
// successful dcgmi profile --list counts as support.
func TestProfilingSupported(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		succeeded bool
		want      bool
	}{
		{name: "lists the field ID", output: "| A.0            | 1001     | gr_engine_active |", succeeded: true, want: true},
		{name: "lists the field name", output: "gr_engine_active", succeeded: true, want: true},
		{name: "not supported", output: "Error: Profiling is not supported for gr_engine_active on this GPU.", succeeded: true},
		{name: "command failed", output: "| A.0 | 1001 | gr_engine_active |"},
		{name: "connection error", output: "Error: unable to establish a connection to the specified host"},
		{name: "other fields only", output: "| A.0 | 1002 | sm_active |", succeeded: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, profilingSupported(tc.output, tc.succeeded))
		})
	}
}

// TestLoadUtilProblems checks the utilization limits for the loaded GPU and
// every idle GPU, including an absent sample.
func TestLoadUtilProblems(t *testing.T) {
	loaded, controls := loadGPU{index: "0"}, []loadGPU{{index: "1"}, {index: "2"}}
	scrape := parseTestScrape(t, `# TYPE DCGM_FI_DEV_GPU_UTIL_RATIO gauge
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="0"} 0.99
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="1"} 0
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="2"} 0.1
# TYPE DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO gauge
DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO{gpu="0"} 0.5
DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO{gpu="1"} 0.3
`)
	require.Empty(t, loadUtilProblems(scrape, loaded, controls, []string{gpuUtilFamily}))
	require.Equal(t, []string{
		"loaded GPU 0 DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO = 0.5 (present true), want > 0.5",
		"idle GPU 1 DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO = 0.3 (present true), want < 0.2",
		"idle GPU 2 DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO = 0 (present false), want < 0.2",
	}, loadUtilProblems(scrape, loaded, controls, []string{grEngineUtilFamily}))
	require.Equal(t, []string{
		"loaded GPU 3 DCGM_FI_DEV_GPU_UTIL_RATIO = 0 (present false), want > 0.5",
	}, loadUtilProblems(scrape, loadGPU{index: "3"}, controls, []string{gpuUtilFamily}))

	// NaN and infinity fail both comparisons the wrong way, so they are problems.
	invalid := parseTestScrape(t, `# TYPE DCGM_FI_DEV_GPU_UTIL_RATIO gauge
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="0"} NaN
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="1"} NaN
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="2"} -Inf
`)
	require.Len(t, loadUtilProblems(invalid, loaded, controls, []string{gpuUtilFamily}), 3)
}

// TestEnergyExceeds checks the energy ratio between the loaded and an idle GPU.
func TestEnergyExceeds(t *testing.T) {
	require.True(t, energyExceeds(6000, 4000))
	require.True(t, energyExceeds(4800, 4000))
	require.False(t, energyExceeds(4700, 4000))
	require.False(t, energyExceeds(0, 0), "a loaded GPU must use some energy")
}

// TestProfTesterForCUDA checks which dcgmproftester each CUDA major version uses.
func TestProfTesterForCUDA(t *testing.T) {
	tests := []struct {
		major int
		want  string
		ok    bool
	}{
		{major: 14, want: "dcgmproftester13", ok: true},
		{major: 13, want: "dcgmproftester13", ok: true},
		{major: 12, want: "dcgmproftester12", ok: true},
		{major: 11},
	}
	for _, tc := range tests {
		got, ok := profTesterForCUDA(tc.major)
		require.Equal(t, tc.want, got, "CUDA %d", tc.major)
		require.Equal(t, tc.ok, ok, "CUDA %d", tc.major)
	}
}

// TestProfTesterArgs checks that the load runs on the named GPU at constant
// maximum activity for the whole duration.
func TestProfTesterArgs(t *testing.T) {
	require.Equal(t, []string{
		"run", "--rm", "--name", "load", "--gpus", "device=GPU-a", "--entrypoint", "/usr/bin/dcgmproftester12", "dcgm:4",
		"--no-dcgm-validation", "--target-max-value", "-t", "1001", "-d", "111",
	}, profTesterArgs("load", "dcgm:4", "dcgmproftester12", "GPU-a", 110*time.Second+time.Millisecond))
	args := profTesterArgs("load", "dcgm:4", "dcgmproftester13", "GPU-a", loadDuration)
	require.Equal(t, "110", args[len(args)-1])
}
