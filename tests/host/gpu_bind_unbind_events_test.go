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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// The scenario polls every bindUnbindPollInterval. After an unbind or a bind
// the series must change within one poll plus bindUnbindReloadMargin, which
// covers the exporter's reload.
const (
	bindUnbindPollInterval = 2 * time.Second
	bindUnbindReloadMargin = 30 * time.Second
	bindUnbindDeadline     = bindUnbindPollInterval + bindUnbindReloadMargin
	bindUnbindScrapeEvery  = time.Second
)

// Unbinding waits for every open handle on the GPU, so one sysfs write is
// stopped after sysfsWriteTimeout (and killed sysfsKillAfter later if it
// ignores the stop). After a failed unbind the scenario waits up to
// unbindSettleTimeout for the GPU to leave the driver before it decides what
// state the GPU is in.
const (
	sysfsWriteTimeout   = time.Minute
	sysfsKillAfter      = 5 * time.Second
	sysfsWaitDelay      = 10 * time.Second
	unbindSettleTimeout = 30 * time.Second
	unbindSettleEvery   = time.Second
)

// minDriverMajor is the first driver release that detaches a GPU that is
// still open instead of waiting for every handle to close.
const minDriverMajor = 590

// nvidiaDriverDir is the sysfs directory of the nvidia PCI driver. Writing a
// PCI address to its unbind or bind file detaches or attaches that device.
const nvidiaDriverDir = "/sys/bus/pci/drivers/nvidia"

// allowGPUUnbindEnv must be set to 1 for the scenario to run. A plain
// "go test ./tests/host" must never unbind a GPU, so the e2e CLI sets it only
// when the scenario is named.
const allowGPUUnbindEnv = "E2E_ALLOW_GPU_UNBIND"

// Services that must not be running while the scenario unbinds a GPU.
const (
	fabricManagerService = "nvidia-fabricmanager"
	persistencedService  = "nvidia-persistenced"
)

// Exporter log lines that show the watch saw an event and the exporter
// recovered from it.
const (
	lifecycleEventLog  = "GPU lifecycle event detected"
	lifecycleResumeLog = "GPU metrics resumed after lifecycle reset"
)

// pciBusIDPattern matches the bus ID nvidia-smi prints: a 4 to 8 digit
// domain, bus, device and a function from 0 to 7.
var pciBusIDPattern = regexp.MustCompile(`^([0-9a-fA-F]{4,8}):([0-9a-fA-F]{2}):([0-9a-fA-F]{2})\.([0-7])$`)

// runGPUBindUnbindEvents unbinds one idle GPU from the nvidia driver while the
// exporter watches bind and unbind events, checks its series disappear while
// every other GPU keeps its series, then binds it again and checks they return.
// Each step must also add at least one lifecycle event and one recovery line
// to the exporter log. It rebinds the GPU on every exit path. It fails before
// unbinding when the host cannot take the unbind safely: see requireSafeToUnbind.
func runGPUBindUnbindEvents(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	t.Helper()
	if os.Getenv(allowGPUUnbindEnv) != "1" {
		t.Skipf("%s=1 is not set; this scenario unbinds a GPU from the driver and runs only when named explicitly", allowGPUUnbindEnv)
	}

	gpu, others, address := chooseGPUToUnbind(t)
	requireSafeToUnbind(t, gpu, address)
	driver := newNvidiaDriver()
	require.Truef(t, driver.bound(address), "GPU %s (%s) is not bound to the nvidia driver at %s", gpu.UUID, address, driver.dir)

	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	process, _ := startExporterAndWaitForFamilies(
		t,
		metricsURL,
		nil,
		[]string{gpuUtilFamily},
		"--collectors", writeScenarioCounters(t, gpuUtilFamily+", gauge, GPU utilization as a ratio."),
		"--address", fmt.Sprintf("127.0.0.1:%d", port),
		"--collect-interval", "1000",
		"--enable-gpu-bind-unbind-watch",
		"--gpu-bind-unbind-poll-interval", bindUnbindPollInterval.String(),
	)
	scrape := scrapeGPUMetrics(t, metricsURL, process)
	for _, uuid := range append([]string{gpu.UUID}, others...) {
		require.Lenf(t, scrape.gpusWithUUID(gpuUtilFamily, uuid), 1,
			"the exporter must report one %s sample for GPU %s before the unbind", gpuUtilFamily, uuid)
	}

	// Registered before the unbind so a failed assertion or a failed unbind
	// still leaves the GPU bound. It runs before the exporter is stopped.
	t.Cleanup(func() {
		if err := driver.bind(address); err != nil {
			t.Errorf("rebinding GPU %s (%s) failed: %v; rebind it by hand with: echo %s | sudo tee %s/bind",
				gpu.UUID, address, err, address, driver.dir)
		}
	})

	events, resumed := lifecycleLogCounts(process.output.String())

	t.Logf("unbinding GPU %s (%s)", gpu.UUID, address)
	unbindGPU(t, driver, address)
	events, resumed = waitForGPUState(t, metricsURL, process, gpuState{uuid: gpu.UUID, others: others, present: false, events: events + 1, resumed: resumed + 1})

	t.Logf("binding GPU %s (%s)", gpu.UUID, address)
	require.NoError(t, driver.bind(address), "bind GPU %s", gpu.UUID)
	waitForGPUState(t, metricsURL, process, gpuState{uuid: gpu.UUID, others: others, present: true, events: events + 1, resumed: resumed + 1})
}

// chooseGPUToUnbind returns the GPU to unbind, the UUIDs of the other GPUs
// and the GPU's sysfs PCI address. It fails the test when nvidia-smi cannot
// name a GPU that is safe to unbind.
func chooseGPUToUnbind(t testing.TB) (nvidiasmi.GPU, []string, string) {
	t.Helper()
	ctx := context.Background()
	inventory, err := hostInventory.Identity(ctx)
	require.NoError(t, err)
	busy, err := nvidiasmi.QueryComputeAppUUIDs(ctx, nvidiasmi.LocalRunner)
	require.NoError(t, err)

	gpu, err := chooseUnbindGPU(inventory, busy)
	require.NoError(t, err)
	address, err := sysfsPCIAddress(gpu.PCIBusID)
	require.NoError(t, err)

	others := reportingGPUsBesides(inventory, gpu.UUID)
	require.NotEmptyf(t, others, "refusing to unbind: no other GPU with MIG off stays bound to show the watch leaves it alone")
	return gpu, others, address
}

// reportingGPUsBesides returns the UUIDs of the GPUs, other than uuid, that
// report series under their own UUID. A GPU with MIG on reports one series per
// instance and never one for the GPU, so it is left out.
func reportingGPUsBesides(inventory []nvidiasmi.GPU, uuid string) []string {
	var others []string
	for _, gpu := range inventory {
		if gpu.UUID != uuid && !gpu.MIGEnabled {
			others = append(others, gpu.UUID)
		}
	}
	return others
}

// chooseUnbindGPU picks the GPU to unbind: the highest-index GPU that has no
// compute process and MIG off. It refuses a host with one GPU, because
// unbinding the only GPU leaves the exporter nothing to report.
func chooseUnbindGPU(inventory []nvidiasmi.GPU, busyUUIDs []string) (nvidiasmi.GPU, error) {
	if len(inventory) < 2 {
		return nvidiasmi.GPU{}, fmt.Errorf("refusing to unbind: the host has %d GPU(s); the scenario needs at least two so one stays bound", len(inventory))
	}

	excluded, err := busyOrMIGGPUUUIDs(inventory, busyUUIDs)
	if err != nil {
		return nvidiasmi.GPU{}, err
	}

	candidates := slices.DeleteFunc(slices.Clone(inventory), func(gpu nvidiasmi.GPU) bool {
		return slices.Contains(excluded, gpu.UUID) || gpu.PCIBusID == ""
	})
	if len(candidates) == 0 {
		return nvidiasmi.GPU{}, errors.New("refusing to unbind: every GPU has MIG on, a compute process or no PCI bus ID")
	}

	return slices.MaxFunc(candidates, func(a, b nvidiasmi.GPU) int { return a.Index - b.Index }), nil
}

// busyOrMIGGPUUUIDs returns the UUIDs of the GPUs that are not free to use:
// those with a compute process and those with MIG on. A GPU with MIG on is
// never free, because a process on one of its MIG devices is reported with
// the MIG device UUID, which cannot be mapped to its GPU. A MIG device process
// with no GPU in MIG mode is an error, because the busy GPU is unknown.
func busyOrMIGGPUUUIDs(inventory []nvidiasmi.GPU, busyUUIDs []string) ([]string, error) {
	excluded := slices.Clone(busyUUIDs)
	migGPUs := 0
	for _, gpu := range inventory {
		if gpu.MIGEnabled {
			excluded = append(excluded, gpu.UUID)
			migGPUs++
		}
	}

	if migGPUs == 0 && slices.ContainsFunc(busyUUIDs, nvidiasmi.IsMIGDeviceUUID) {
		return nil, fmt.Errorf("nvidia-smi reports a compute process on a MIG device (%v) but no GPU with MIG on", busyUUIDs)
	}

	return excluded, nil
}

// sysfsPCIAddress converts the bus ID nvidia-smi prints, such as
// "00000000:3B:00.0", to the form sysfs uses, such as "0000:3b:00.0". A domain
// above four digits keeps its value ("00010000:3B:00.0" is "10000:3b:00.0").
// It rejects any other text, so the result is safe in a sysfs path.
func sysfsPCIAddress(busID string) (string, error) {
	match := pciBusIDPattern.FindStringSubmatch(strings.TrimSpace(busID))
	if match == nil {
		return "", fmt.Errorf("PCI bus ID %q is not <domain>:<bus>:<device>.<function> in hexadecimal", busID)
	}

	domain, err := strconv.ParseUint(match[1], 16, 32)
	if err != nil {
		return "", fmt.Errorf("PCI bus ID %q has a domain that is not a number: %w", busID, err)
	}

	return strings.ToLower(fmt.Sprintf("%04x:%s:%s.%s", domain, match[2], match[3], match[4])), nil
}

// requireSafeToUnbind fails the test unless the host can take the unbind
// without hanging or disturbing a fabric. The driver must detach a GPU that
// is in use (release 590 or later), persistence mode and nvidia-persistenced
// must be off, no process may hold the GPU's device file, and the GPU must not
// be part of an NVLink fabric.
func requireSafeToUnbind(t testing.TB, gpu nvidiasmi.GPU, address string) {
	t.Helper()
	ctx := context.Background()

	ok, err := driverMajorAtLeast(gpu.DriverVersion, minDriverMajor)
	require.NoError(t, err)
	require.Truef(t, ok, "refusing to unbind: driver %s is older than %d, and an older driver can hang an unbind while the GPU is open", gpu.DriverVersion, minDriverMajor)

	requireServiceNotActive(t, persistencedService, "it holds the GPU open")
	requireServiceNotActive(t, fabricManagerService, "unbinding a GPU on an NVSwitch fabric disturbs the fabric")

	persistence, err := nvidiasmi.LocalRunner(ctx, "--query-gpu=uuid,persistence_mode", "--format=csv,noheader")
	require.NoError(t, err, "query persistence mode")
	on, err := persistenceModeOn(string(persistence), gpu.UUID)
	require.NoError(t, err)
	require.Falsef(t, on, "refusing to unbind: persistence mode is on for GPU %s and keeps it open; turn it off with nvidia-smi -pm 0", gpu.UUID)

	requireNoNVLinkFabric(t)
	requireNoDeviceHolder(t, address)
}

// driverMajorAtLeast reports whether a driver version such as "590.48.01" has
// a major release of at least floor.
func driverMajorAtLeast(version string, floor int) (bool, error) {
	major, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	number, err := strconv.Atoi(major)
	if err != nil {
		return false, fmt.Errorf("driver version %q does not start with a release number: %w", version, err)
	}

	return number >= floor, nil
}

// persistenceModeOn reads the output of
// "nvidia-smi --query-gpu=uuid,persistence_mode --format=csv,noheader" and
// reports whether persistence mode is on for uuid. It fails when the GPU has no row.
func persistenceModeOn(output, uuid string) (bool, error) {
	for _, line := range strings.Split(output, "\n") {
		id, mode, found := strings.Cut(line, ",")
		if found && strings.TrimSpace(id) == uuid {
			return strings.EqualFold(strings.TrimSpace(mode), "Enabled"), nil
		}
	}

	return false, fmt.Errorf("nvidia-smi listed no persistence mode for GPU %s", uuid)
}

// requireServiceNotActive fails the test when the systemd unit is running.
// An unknown state, or a systemctl that cannot run, fails the test too,
// because the unit's state is then not known.
func requireServiceNotActive(t testing.TB, unit, why string) {
	t.Helper()

	// systemctl exits non-zero for a unit that is not active and prints its state.
	output, err := exec.Command("systemctl", "is-active", unit).Output() // #nosec G204 -- the unit name is a constant.
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("systemctl is-active %s: %v", unit, err)
	}

	busy, known := serviceBusy(string(output))
	if !known {
		t.Fatalf("systemctl is-active %s printed %q, which is not a state this scenario knows", unit, strings.TrimSpace(string(output)))
	}
	if busy {
		t.Fatalf("refusing to unbind: %s is %s; %s", unit, strings.TrimSpace(string(output)), why)
	}
}

// serviceBusy reads the state "systemctl is-active" printed. It reports
// whether the unit is running or starting, and whether the state is one
// systemctl documents.
func serviceBusy(state string) (busy, known bool) {
	switch strings.TrimSpace(state) {
	case "active", "activating", "reloading", "deactivating":
		return true, true
	case "inactive", "failed", "unknown":
		return false, true
	default:
		return false, false
	}
}

// requireNoNVLinkFabric fails the test when nvidia-smi shows a fabric
// (a Fabric section with a state or a non-zero cluster UUID) or an active
// NVLink. GB200 and GB300 NVLink domains can form a fabric without running
// Fabric Manager, and unbinding a member disturbs it.
func requireNoNVLinkFabric(t testing.TB) {
	t.Helper()
	ctx := context.Background()

	query, err := nvidiasmi.LocalRunner(ctx, "-q")
	require.NoError(t, err, "nvidia-smi -q")
	reported, _, evidence := nvidiasmi.FabricHealth(string(query))
	require.Falsef(t, reported, "refusing to unbind: nvidia-smi reports a fabric (%s)", evidence)
	require.Falsef(t, clusterUUIDSet(string(query)), "refusing to unbind: nvidia-smi reports a fabric cluster UUID")

	status, err := nvidiasmi.LocalRunner(ctx, "nvlink", "-s")
	require.NoError(t, err, "nvidia-smi nvlink -s")
	links, err := nvidiasmi.ActiveNVLinks(string(status))
	require.NoError(t, err, "nvidia-smi nvlink -s output:\n%s", status)
	require.Emptyf(t, links, "refusing to unbind: %d NVLink(s) are active, so the GPUs may be part of an NVLink fabric", len(links))
}

// clusterUUIDSet reports whether "nvidia-smi -q" output has a Cluster UUID
// line with a value other than empty, N/A or all zeros.
func clusterUUIDSet(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) != "Cluster UUID" {
			continue
		}

		value = strings.TrimSpace(value)
		if value != "" && !strings.EqualFold(value, "N/A") && strings.Trim(value, "0-") != "" {
			return true
		}
	}
	return false
}

// minorPattern reads the device minor from /proc/driver/nvidia/gpus/<address>/information.
var minorPattern = regexp.MustCompile(`(?m)^Device Minor:\s*([0-9]+)\s*$`)

// requireNoDeviceHolder fails the test when a process holds the GPU's device
// file open. It runs before the exporter starts, so any holder is another
// process. Holders block an unbind on drivers that wait for handles to close.
func requireNoDeviceHolder(t testing.TB, address string) {
	t.Helper()
	info, err := os.ReadFile(filepath.Join("/proc/driver/nvidia/gpus", address, "information"))
	require.NoErrorf(t, err, "read the device minor of GPU %s", address)
	match := minorPattern.FindSubmatch(info)
	require.NotNilf(t, match, "no Device Minor in the information of GPU %s:\n%s", address, info)

	device := "/dev/nvidia" + string(match[1])
	_, err = exec.LookPath("fuser")
	require.NoError(t, err, "refusing to unbind: fuser (psmisc) is needed to check who holds %s", device)

	command, args := "fuser", []string{device}
	if os.Geteuid() != 0 {
		command, args = "sudo", []string{"-n", "fuser", device}
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(command, args...) // #nosec G204 -- the device path is built from digits read from the driver.
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	holders, err := classifyFuser(runErr, stdout.String(), stderr.String())
	require.NoErrorf(t, err, "refusing to unbind: could not check who holds %s (needs passwordless sudo and fuser)", device)
	require.Emptyf(t, holders, "refusing to unbind: process(es) %s hold %s open; stop them first, because they can block the unbind", strings.Join(holders, " "), device)
}

// classifyFuser reads the result of "fuser <device>" and returns the PIDs that
// hold the device. "No holder" is accepted only when fuser itself ran and
// exited 1 with nothing on stderr. Sudo also exits 1 when it needs a password,
// and then its message is on stderr, so any other failure is an error.
func classifyFuser(runErr error, stdout, stderr string) ([]string, error) {
	if runErr == nil {
		return strings.Fields(stdout), nil
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 && strings.TrimSpace(stderr) == "" && strings.TrimSpace(stdout) == "" {
		return nil, nil
	}

	return nil, fmt.Errorf("fuser failed: %w (stderr: %s)", runErr, strings.TrimSpace(stderr))
}

// pciDriver reads and changes which PCI devices one kernel driver owns.
type pciDriver struct {
	// dir is the driver's sysfs directory.
	dir string
	// write puts value into the named file with the privileges sysfs needs.
	write func(file, value string) error
}

// newNvidiaDriver returns the nvidia driver, written through sudo or as root.
func newNvidiaDriver() pciDriver {
	return pciDriver{dir: nvidiaDriverDir, write: writeSysfsAsRoot}
}

// writeSysfsAsRoot writes value to a sysfs file as root, directly or through
// sudo that never asks for a password, and stops the write after
// sysfsWriteTimeout (and kills it sysfsKillAfter later).
func writeSysfsAsRoot(file, value string) error {
	args := []string{
		"timeout", "-k", strconv.Itoa(int(sysfsKillAfter.Seconds())), strconv.Itoa(int(sysfsWriteTimeout.Seconds())),
		"tee", file,
	}
	if os.Geteuid() != 0 {
		args = append([]string{"sudo", "-n"}, args...)
	}

	var output bytes.Buffer
	cmd := exec.Command(args[0], args[1:]...) // #nosec G204 -- the command and sysfs file are fixed by the scenario.
	cmd.Stdin = strings.NewReader(value)

	// A write stuck in the kernel keeps the output pipes open after timeout
	// kills tee; WaitDelay makes Run return anyway.
	cmd.WaitDelay = sysfsWaitDelay
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write %q to %s: %w (output: %s)", value, file, err, strings.TrimSpace(output.String()))
	}
	return nil
}

// bound reports whether the driver owns the device. The driver directory has
// an entry named for each device it owns.
func (d pciDriver) bound(address string) bool {
	_, err := os.Lstat(filepath.Join(d.dir, address))
	return err == nil
}

// unbind detaches the device from the driver.
func (d pciDriver) unbind(address string) error {
	return d.write(filepath.Join(d.dir, "unbind"), address)
}

// bind attaches the device to the driver. It does nothing when the driver
// already owns the device, so it is safe to call again on every exit path.
func (d pciDriver) bind(address string) error {
	if d.bound(address) {
		return nil
	}
	return d.write(filepath.Join(d.dir, "bind"), address)
}

// waitUnbound polls until the driver no longer owns the device and reports
// whether that happened within timeout.
func (d pciDriver) waitUnbound(address string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for d.bound(address) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(unbindSettleEvery)
	}
	return true
}

// rebindWhenUnbound waits up to timeout for the driver to release the device
// and binds it again if it does. It does nothing when the device stays bound.
func (d pciDriver) rebindWhenUnbound(address string, timeout time.Duration) error {
	if !d.waitUnbound(address, timeout) {
		return nil
	}
	return d.bind(address)
}

// unbindGPU detaches the GPU and fails the test when that does not work. After
// a failed or timed-out unbind it waits for the GPU to leave the driver. If it
// did, the cleanup rebinds it. If it is still bound, the unbind may still be
// running in the kernel, so the failure names the recovery steps.
func unbindGPU(t testing.TB, driver pciDriver, address string) {
	t.Helper()
	err := driver.unbind(address)
	if err == nil {
		return
	}

	if driver.waitUnbound(address, unbindSettleTimeout) {
		t.Fatalf("unbind of %s failed (%v) but the GPU is now unbound; the cleanup rebinds it", address, err)
	}

	// The kernel may finish the unbind later; bind the GPU again if it does.
	t.Cleanup(func() {
		if err := driver.rebindWhenUnbound(address, unbindSettleTimeout); err != nil {
			t.Errorf("rebinding GPU %s after a late unbind failed: %v; rebind it by hand with: echo %s | sudo tee %s/bind", address, err, address, driver.dir)
		}
	})

	t.Fatalf("unbind of %s failed (%v) and the GPU is still bound after %s, so the unbind may still be running in the kernel; "+
		"check dmesg and nvidia-smi, and if the GPU is unbound rebind it with: echo %s | sudo tee %s/bind",
		address, err, unbindSettleTimeout, address, driver.dir)
}

// gpuState is what waitForGPUState waits for: the GPU's series present or
// gone, every other GPU still reporting, and the exporter log holding at least
// the given number of lifecycle event and recovery lines.
type gpuState struct {
	uuid    string
	others  []string
	present bool
	events  int
	resumed int
}

// ready reports whether a scrape and the exporter log show the state.
func (s gpuState) ready(scrape gpuScrape, log string) bool {
	if (len(scrape.gpusWithUUID(gpuUtilFamily, s.uuid)) > 0) != s.present {
		return false
	}

	for _, other := range s.others {
		if len(scrape.gpusWithUUID(gpuUtilFamily, other)) == 0 {
			return false
		}
	}

	events, resumed := lifecycleLogCounts(log)
	return events >= s.events && resumed >= s.resumed
}

// lifecycleLogCounts counts the lifecycle event and recovery lines in the
// exporter log.
func lifecycleLogCounts(log string) (events, resumed int) {
	return strings.Count(log, lifecycleEventLog), strings.Count(log, lifecycleResumeLog)
}

// waitForGPUState scrapes until the state holds and returns the lifecycle log
// counts then. It fails the test when that does not happen within bindUnbindDeadline.
func waitForGPUState(t testing.TB, metricsURL string, process *hostExporterProcess, state gpuState) (int, int) {
	t.Helper()
	deadline := time.Now().Add(bindUnbindDeadline)
	for {
		require.NoError(t, process.running(), "the exporter stopped\n%s", process.output.String())

		// A scrape can fail while the exporter reloads; the deadline covers it.
		// A scrape that returns after the deadline does not count, because the
		// state must be reached within it.
		families, _, err := fetchMetricFamilies(metricsURL)
		log := process.output.String()
		late := time.Now().After(deadline)
		if err == nil && !late && state.ready(gpuScrape(families), log) {
			return lifecycleLogCounts(log)
		}

		if late {
			t.Fatalf("after %s the state was not reached: GPU %s series present = %t, other GPUs %v still reporting, at least %d %q and %d %q lines (last scrape error: %v)\n%s",
				bindUnbindDeadline, state.uuid, state.present, state.others, state.events, lifecycleEventLog, state.resumed, lifecycleResumeLog, err, log)
		}
		time.Sleep(bindUnbindScrapeEvery)
	}
}

// TestSysfsPCIAddress checks the nvidia-smi bus ID converts to the sysfs form
// and anything else is rejected.
func TestSysfsPCIAddress(t *testing.T) {
	tests := []struct {
		name    string
		busID   string
		want    string
		wantErr bool
	}{
		{name: "eight digit domain", busID: "00000000:3B:00.0", want: "0000:3b:00.0"},
		{name: "four digit domain", busID: "0000:AF:00.0", want: "0000:af:00.0"},
		{name: "domain above four digits", busID: "00010000:3B:00.0", want: "10000:3b:00.0"},
		{name: "surrounding space", busID: " 00000001:01:00.0 ", want: "0001:01:00.0"},
		{name: "function 7", busID: "0000:3b:00.7", want: "0000:3b:00.7"},
		{name: "function 8", busID: "0000:3b:00.8", wantErr: true},
		{name: "path separator", busID: "0000:3b:00.0/../x", wantErr: true},
		{name: "parent directory", busID: "..", wantErr: true},
		{name: "slash in domain", busID: "00/0:3b:00.0", wantErr: true},
		{name: "no colon", busID: "3B.00.0", wantErr: true},
		{name: "missing device", busID: "00000000:3B", wantErr: true},
		{name: "short domain", busID: "000:3b:00.0", wantErr: true},
		{name: "long domain", busID: "000000000:3b:00.0", wantErr: true},
		{name: "empty", busID: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sysfsPCIAddress(tt.busID)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestChooseUnbindGPU checks the scenario refuses a lone GPU and a host with no
// free GPU, and otherwise picks the highest-index free GPU.
func TestChooseUnbindGPU(t *testing.T) {
	gpu := func(index int, uuid string, mig bool) nvidiasmi.GPU {
		return nvidiasmi.GPU{Index: index, UUID: uuid, PCIBusID: fmt.Sprintf("00000000:0%d:00.0", index), MIGEnabled: mig}
	}

	tests := []struct {
		name      string
		inventory []nvidiasmi.GPU
		busy      []string
		wantUUID  string
		wantErr   string
	}{
		{name: "one GPU", inventory: []nvidiasmi.GPU{gpu(0, "GPU-a", false)}, wantErr: "at least two"},
		{name: "highest free GPU", inventory: []nvidiasmi.GPU{gpu(0, "GPU-a", false), gpu(1, "GPU-b", false)}, wantUUID: "GPU-b"},
		{name: "skips busy GPU", inventory: []nvidiasmi.GPU{gpu(0, "GPU-a", false), gpu(1, "GPU-b", false)}, busy: []string{"GPU-b"}, wantUUID: "GPU-a"},
		{name: "skips MIG GPU", inventory: []nvidiasmi.GPU{gpu(0, "GPU-a", false), gpu(1, "GPU-b", true)}, wantUUID: "GPU-a"},
		{name: "MIG device process with no MIG GPU", inventory: []nvidiasmi.GPU{gpu(0, "GPU-a", false), gpu(1, "GPU-b", false)}, busy: []string{"MIG-1"}, wantErr: "no GPU with MIG on"},
		{name: "all busy", inventory: []nvidiasmi.GPU{gpu(0, "GPU-a", false), gpu(1, "GPU-b", false)}, busy: []string{"GPU-a", "GPU-b"}, wantErr: "every GPU"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chooseUnbindGPU(tt.inventory, tt.busy)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantUUID, got.UUID)
		})
	}
}

// TestPCIDriverBindIsIdempotent checks bind writes only when the driver does
// not own the device, so the cleanup can run after a successful rebind.
func TestPCIDriverBindIsIdempotent(t *testing.T) {
	const address = "0000:3b:00.0"
	dir := t.TempDir()
	var writes []string
	driver := pciDriver{dir: dir, write: func(file, value string) error {
		writes = append(writes, filepath.Base(file)+" "+value)

		// A bind makes the driver own the device; an unbind releases it.
		entry := filepath.Join(dir, value)
		if filepath.Base(file) == "bind" {
			return os.WriteFile(entry, nil, 0o600)
		}
		return os.Remove(entry)
	}}

	// Bound: bind does nothing.
	require.NoError(t, os.WriteFile(filepath.Join(dir, address), nil, 0o600))
	require.NoError(t, driver.bind(address))
	require.Empty(t, writes)

	// Unbound: bind writes once, and a second call does nothing.
	require.NoError(t, driver.unbind(address))
	require.False(t, driver.bound(address))
	require.NoError(t, driver.bind(address))
	require.NoError(t, driver.bind(address))
	require.True(t, driver.bound(address))
	require.Equal(t, []string{"unbind " + address, "bind " + address}, writes)
}

// TestPCIDriverBindReportsWriteFailure checks a failed bind is returned, so
// the cleanup can tell the operator how to rebind by hand.
func TestPCIDriverBindReportsWriteFailure(t *testing.T) {
	writeErr := errors.New("write refused")
	driver := pciDriver{dir: t.TempDir(), write: func(string, string) error { return writeErr }}

	require.ErrorIs(t, driver.bind("0000:3b:00.0"), writeErr)
}

// TestPCIDriverWaitUnbound checks waitUnbound returns at once for an unbound
// device and gives up on a device that stays bound.
func TestPCIDriverWaitUnbound(t *testing.T) {
	dir := t.TempDir()
	driver := pciDriver{dir: dir}
	require.True(t, driver.waitUnbound("0000:3b:00.0", time.Second))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "0000:3b:00.0"), nil, 0o600))
	require.False(t, driver.waitUnbound("0000:3b:00.0", 10*time.Millisecond))
}

// TestDriverMajorAtLeast checks the driver release gate and its errors.
func TestDriverMajorAtLeast(t *testing.T) {
	tests := []struct {
		version string
		want    bool
		wantErr bool
	}{
		{version: "590.48.01", want: true},
		{version: "595.1", want: true},
		{version: "590", want: true},
		{version: "580.105.08", want: false},
		{version: "", wantErr: true},
		{version: "[N/A]", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got, err := driverMajorAtLeast(tt.version, 590)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestPersistenceModeOn checks the persistence mode rows of one GPU are read.
func TestPersistenceModeOn(t *testing.T) {
	output := "GPU-a, Enabled\nGPU-b, Disabled\n"

	on, err := persistenceModeOn(output, "GPU-a")
	require.NoError(t, err)
	require.True(t, on)

	on, err = persistenceModeOn(output, "GPU-b")
	require.NoError(t, err)
	require.False(t, on)

	_, err = persistenceModeOn(output, "GPU-c")
	require.Error(t, err)
}

// TestServiceBusy checks which systemctl states stop the scenario.
func TestServiceBusy(t *testing.T) {
	for state, want := range map[string][2]bool{
		"active\n":   {true, true},
		"inactive\n": {false, true},
		"failed\n":   {false, true},
		"unknown\n":  {false, true},
		"":           {false, false},
		"weird\n":    {false, false},
	} {
		busy, known := serviceBusy(state)
		require.Equal(t, want, [2]bool{busy, known}, "state %q", state)
	}
}

// TestClusterUUIDSet checks a fabric cluster UUID is told from none.
func TestClusterUUIDSet(t *testing.T) {
	tests := map[string]bool{
		"        Cluster UUID                      : 00000000-0000-0000-0000-000000000000": false,
		"        Cluster UUID                      : N/A":                                  false,
		"        Cluster UUID                      : 1f2e3d4c-0000-0000-0000-00000000abcd": true,
		"        Clique ID                         : 4":                                    false,
		"": false,
	}
	for line, want := range tests {
		require.Equal(t, want, clusterUUIDSet(line), line)
	}
}

// TestGPUStateReady checks the wait needs the series state, the other GPUs and
// the log lines together.
func TestGPUStateReady(t *testing.T) {
	scrape := func(uuids ...string) gpuScrape {
		var rows []string
		for i, uuid := range uuids {
			rows = append(rows, fmt.Sprintf(`%s{gpu="%d",UUID="%s"} 1`, gpuUtilFamily, i, uuid))
		}
		return parseTestScrape(t, "# TYPE "+gpuUtilFamily+" gauge\n"+strings.Join(rows, "\n")+"\n")
	}
	log := lifecycleEventLog + "\n" + lifecycleResumeLog + "\n"
	gone := gpuState{uuid: "GPU-b", others: []string{"GPU-a"}, present: false, events: 1, resumed: 1}
	back := gpuState{uuid: "GPU-b", others: []string{"GPU-a"}, present: true, events: 2, resumed: 2}

	tests := []struct {
		name   string
		state  gpuState
		scrape gpuScrape
		log    string
		want   bool
	}{
		{name: "gone", state: gone, scrape: scrape("GPU-a"), log: log, want: true},
		{name: "the GPU still reports", state: gone, scrape: scrape("GPU-a", "GPU-b"), log: log},
		{name: "the other GPU lost its series", state: gone, scrape: scrape("GPU-b"), log: log},
		{name: "no recovery line yet", state: gone, scrape: scrape("GPU-a"), log: lifecycleEventLog + "\n"},
		{name: "no event line yet", state: gone, scrape: scrape("GPU-a"), log: ""},
		{name: "extra lines still count", state: gone, scrape: scrape("GPU-a"), log: log + log, want: true},
		{name: "back, old lines do not count", state: back, scrape: scrape("GPU-a", "GPU-b"), log: log},
		{name: "back", state: back, scrape: scrape("GPU-a", "GPU-b"), log: log + log, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.state.ready(tt.scrape, tt.log))
		})
	}
}

// exitStatusError runs a shell that exits with code and returns its error.
func exitStatusError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run() // #nosec G204 -- code is an integer chosen by the test.
	require.Error(t, err)
	return err
}

// TestClassifyFuser checks "no holder" is accepted only when fuser ran and
// found none, not when sudo or a missing fuser failed with the same exit status.
func TestClassifyFuser(t *testing.T) {
	tests := []struct {
		name        string
		runErr      error
		stdout      string
		stderr      string
		wantHolders []string
		wantErr     bool
	}{
		{name: "holders", stdout: " 123 456\n", stderr: "/dev/nvidia1: ", wantHolders: []string{"123", "456"}},
		{name: "no holder", runErr: exitStatusError(t, 1)},
		{name: "sudo needs a password", runErr: exitStatusError(t, 1), stderr: "sudo: a password is required", wantErr: true},
		{name: "fuser not found", runErr: exitStatusError(t, 1), stderr: "sudo: fuser: command not found", wantErr: true},
		{name: "other exit status", runErr: exitStatusError(t, 2), wantErr: true},
		{name: "could not start", runErr: exec.ErrNotFound, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			holders, err := classifyFuser(tt.runErr, tt.stdout, tt.stderr)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantHolders, holders)
		})
	}
}

// TestReportingGPUsBesides checks the other GPUs exclude the chosen one and
// any GPU with MIG on.
func TestReportingGPUsBesides(t *testing.T) {
	inventory := []nvidiasmi.GPU{{UUID: "GPU-a"}, {UUID: "GPU-b"}, {UUID: "GPU-c", MIGEnabled: true}, {UUID: "GPU-d"}}

	require.Equal(t, []string{"GPU-a", "GPU-d"}, reportingGPUsBesides(inventory, "GPU-b"))
	require.Empty(t, reportingGPUsBesides(inventory[:1], "GPU-a"))
}

// TestPCIDriverRebindWhenUnbound checks a late unbind is followed by a bind, and
// a device that stays bound is left alone.
func TestPCIDriverRebindWhenUnbound(t *testing.T) {
	const address = "0000:3b:00.0"
	var writes []string
	record := func(file, value string) error {
		writes = append(writes, filepath.Base(file)+" "+value)
		return nil
	}

	unbound := pciDriver{dir: t.TempDir(), write: record}
	require.NoError(t, unbound.rebindWhenUnbound(address, time.Second))
	require.Equal(t, []string{"bind " + address}, writes)

	writes = nil
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, address), nil, 0o600))
	stillBound := pciDriver{dir: dir, write: record}
	require.NoError(t, stillBound.rebindWhenUnbound(address, 10*time.Millisecond))
	require.Empty(t, writes)
}
