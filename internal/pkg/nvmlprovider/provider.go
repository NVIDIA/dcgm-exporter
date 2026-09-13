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

package nvmlprovider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

type MIGDeviceInfo struct {
	ParentUUID        string
	GPUInstanceID     int
	ComputeInstanceID int
}

// Default retry parameters for Initialize. The GPU driver installer on GKE
// (and similar node-bootstrap setups) can still be running when the exporter
// pod starts, so nvml.Init briefly returns ERROR_LIBRARY_NOT_FOUND. These
// defaults give the driver installer up to ~1 minute to finish before giving up.
const (
	DefaultNVMLInitRetryAttempts = 5
	DefaultNVMLInitRetryBaseWait = 2 * time.Second
	DefaultNVMLInitRetryMaxWait  = 20 * time.Second
)

// maxJitterFraction is the maximum fraction of the backoff duration added as
// random jitter, so pods restarting together (e.g. after a node reboot) don't
// retry in lockstep against the same node.
const maxJitterFraction = 0.30

var nvmlInterface NVML

// nvmlInitFunc is a package-level indirection over nvml.Init so tests can
// substitute a fake without requiring real GPU hardware.
var nvmlInitFunc = nvml.Init

// nvmlInitError wraps the raw NVML return code from nvmlInitFunc so callers
// can distinguish transient failures (e.g. ERROR_LIBRARY_NOT_FOUND) from
// permanent ones without resorting to string matching.
type nvmlInitError struct {
	ret nvml.Return
}

func (e *nvmlInitError) Error() string {
	return nvml.ErrorString(e.ret)
}

// isLibraryNotFoundErr reports whether err represents nvml.ERROR_LIBRARY_NOT_FOUND,
// the transient error returned while the GPU driver installer hasn't finished yet.
func isLibraryNotFoundErr(err error) bool {
	var initErr *nvmlInitError
	if errors.As(err, &initErr) {
		return initErr.ret == nvml.ERROR_LIBRARY_NOT_FOUND
	}
	return false
}

// Initialize sets up the Singleton NVML interface, retrying on the transient
// ERROR_LIBRARY_NOT_FOUND error using sane default retry parameters.
func Initialize() error {
	return InitializeWithRetry(context.Background(), DefaultNVMLInitRetryAttempts, DefaultNVMLInitRetryBaseWait, DefaultNVMLInitRetryMaxWait)
}

// InitializeWithRetry sets up the Singleton NVML interface, retrying up to
// attempts times with exponential backoff (base, 2x, 4x, ... capped at
// maxWait, plus jitter) when nvml.Init fails with ERROR_LIBRARY_NOT_FOUND.
// Any other error is returned immediately without retrying, since retrying a
// permanent failure only delays an unavoidable error. attempts < 1 is treated
// as 1, so at least one init attempt always happens. If ctx is cancelled
// while waiting between attempts, InitializeWithRetry returns ctx.Err()
// immediately instead of sleeping out the remaining backoff, so a shutdown
// signal during startup isn't ignored until retries are exhausted.
func InitializeWithRetry(ctx context.Context, attempts int, baseWait, maxWait time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		var err error
		nvmlInterface, err = newNVMLProvider()
		if err == nil {
			return nil
		}
		lastErr = err

		if !isLibraryNotFoundErr(err) {
			return fmt.Errorf("failed to initialize NVML library: %w", err)
		}

		if attempt < attempts-1 {
			wait := backoffDuration(attempt, baseWait, maxWait)
			slog.Warn("NVML library not found yet (GPU driver may still be installing); retrying",
				slog.Int("attempt", attempt+1),
				slog.Int("maxAttempts", attempts),
				slog.Duration("wait", wait))

			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("NVML initialization cancelled after %d attempt(s): %w", attempt+1, ctx.Err())
			}
		}
	}

	return fmt.Errorf("failed to initialize NVML library after %d attempts, last error: %w", attempts, lastErr)
}

// backoffDuration computes the exponential backoff wait for the given attempt
// (0-indexed): baseWait * 2^attempt, capped at maxWait, plus up to
// maxJitterFraction of additional random jitter on top of the cap. The
// doubling is computed via repeated, overflow-checked multiplication rather
// than a bit shift so an unbounded attempt count (attempts is a
// user-configurable CLI value) can never wrap into a bogus small positive
// duration that defeats the cap.
func backoffDuration(attempt int, baseWait, maxWait time.Duration) time.Duration {
	wait := baseWait
	for i := 0; i < attempt; i++ {
		if wait >= maxWait || wait > maxWait/2 {
			wait = maxWait
			break
		}
		wait *= 2
	}
	if wait > maxWait {
		wait = maxWait
	}

	jitter := time.Duration(rand.Float64() * maxJitterFraction * float64(wait)) //nolint:gosec // #nosec G404 -- jitter only needs to desynchronize retries, not be cryptographically secure
	return wait + jitter
}

// reset clears the current NVML interface instance.
func reset() {
	nvmlInterface = nil
}

// Client retrieves the current NVML interface instance.
// Returns a non-initialized provider if NVML was never initialized.
func Client() NVML {
	if nvmlInterface == nil {
		// Return a non-initialized provider that will safely return errors
		return nvmlProvider{initialized: false}
	}
	return nvmlInterface
}

// SetClient sets the current NVML interface instance to the provided one.
func SetClient(n NVML) {
	nvmlInterface = n
}

// nvmlProvider implements NVML Interface
type nvmlProvider struct {
	initialized bool
}

func newNVMLProvider() (NVML, error) {
	// Check if a NVML client already exists and return it if so.
	if Client() != nil && Client().(nvmlProvider).initialized {
		slog.Info("NVML already initialized.")
		return Client(), nil
	}

	slog.Info("Attempting to initialize NVML library.")
	ret := nvmlInitFunc()
	if ret != nvml.SUCCESS {
		err := &nvmlInitError{ret: ret}
		slog.Error(fmt.Sprintf("Cannot init NVML library; err: %v", err))
		return nvmlProvider{initialized: false}, err
	}

	return nvmlProvider{initialized: true}, nil
}

func (n nvmlProvider) preCheck() error {
	if !n.initialized {
		return errors.New("NVML library not initialized")
	}

	return nil
}

// GetMIGDeviceInfoByID returns information about MIG DEVICE by ID
func (n nvmlProvider) GetMIGDeviceInfoByID(uuid string) (*MIGDeviceInfo, error) {
	if err := n.preCheck(); err != nil {
		return nil, fmt.Errorf("NVML not initialized (may need to enable Kubernetes mode): %w", err)
	}

	device, ret := nvml.DeviceGetHandleByUUID(uuid)
	if ret == nvml.SUCCESS {
		return getMIGDeviceInfoForNewDriver(device)
	}

	return getMIGDeviceInfoForOldDriver(uuid)
}

// getMIGDeviceInfoForNewDriver identifies MIG Device Information for drivers >= R470 (470.42.01+),
// each MIG device is assigned a GPU UUID starting with MIG-<UUID>.
func getMIGDeviceInfoForNewDriver(device nvml.Device) (*MIGDeviceInfo, error) {
	parentDevice, ret := device.GetDeviceHandleFromMigDeviceHandle()
	if ret != nvml.SUCCESS {
		return nil, errors.New(nvml.ErrorString(ret))
	}

	parentUUID, ret := parentDevice.GetUUID()
	if ret != nvml.SUCCESS {
		return nil, errors.New(nvml.ErrorString(ret))
	}

	gi, ret := device.GetGpuInstanceId()
	if ret != nvml.SUCCESS {
		return nil, errors.New(nvml.ErrorString(ret))
	}

	ci, ret := device.GetComputeInstanceId()
	if ret != nvml.SUCCESS {
		return nil, errors.New(nvml.ErrorString(ret))
	}

	return &MIGDeviceInfo{
		ParentUUID:        parentUUID,
		GPUInstanceID:     gi,
		ComputeInstanceID: ci,
	}, nil
}

// getMIGDeviceInfoForOldDriver identifies MIG Device Information for drivers < R470 (e.g. R450 and R460),
// each MIG device is enumerated by specifying the CI and the corresponding parent GI. The format follows this
// convention: MIG-<GPU-UUID>/<GPU instance ID>/<Compute instance ID>.
func getMIGDeviceInfoForOldDriver(uuid string) (*MIGDeviceInfo, error) {
	tokens := strings.SplitN(uuid, "-", 2)
	if len(tokens) != 2 || tokens[0] != "MIG" {
		return nil, fmt.Errorf("unable to parse '%s' as MIG device UUID", uuid)
	}

	gpuTokens := strings.SplitN(tokens[1], "/", 3)
	if len(gpuTokens) != 3 || !strings.HasPrefix(gpuTokens[0], "GPU-") {
		return nil, fmt.Errorf("invalid MIG device UUID '%s'", uuid)
	}

	gi, err := strconv.Atoi(gpuTokens[1])
	if err != nil {
		return nil, fmt.Errorf("invalid GPU instance ID '%s' for MIG device '%s'", gpuTokens[1], uuid)
	}

	ci, err := strconv.Atoi(gpuTokens[2])
	if err != nil {
		return nil, fmt.Errorf("invalid Compute instance ID '%s' for MIG device '%s'", gpuTokens[2], uuid)
	}

	return &MIGDeviceInfo{
		ParentUUID:        gpuTokens[0],
		GPUInstanceID:     gi,
		ComputeInstanceID: ci,
	}, nil
}

// GetDeviceProcessMemory returns memory usage for compute processes running on the GPU
func (n nvmlProvider) GetDeviceProcessMemory(gpuUUID string) (map[uint32]uint64, error) {
	if err := n.preCheck(); err != nil {
		return nil, fmt.Errorf("failed to get device process memory: %w", err)
	}

	device, ret := nvml.DeviceGetHandleByUUID(gpuUUID)
	if ret != nvml.SUCCESS {
		return nil, fmt.Errorf("failed to get device handle for UUID %s: %s", gpuUUID, nvml.ErrorString(ret))
	}

	processes, ret := device.GetComputeRunningProcesses()
	if ret != nvml.SUCCESS && ret != nvml.ERROR_NOT_SUPPORTED {
		return nil, fmt.Errorf("failed to get compute running processes: %s", nvml.ErrorString(ret))
	}

	result := make(map[uint32]uint64, len(processes))
	for _, p := range processes {
		result[p.Pid] = p.UsedGpuMemory
	}

	return result, nil
}

// GetDeviceProcessUtilization returns SM utilization for processes running on the GPU
func (n nvmlProvider) GetDeviceProcessUtilization(gpuUUID string) (map[uint32]uint32, error) {
	if err := n.preCheck(); err != nil {
		return nil, fmt.Errorf("failed to get device process utilization: %w", err)
	}

	device, ret := nvml.DeviceGetHandleByUUID(gpuUUID)
	if ret != nvml.SUCCESS {
		return nil, fmt.Errorf("failed to get device handle for UUID %s: %s", gpuUUID, nvml.ErrorString(ret))
	}

	samples, ret := device.GetProcessUtilization(0)
	if ret != nvml.SUCCESS {
		if ret == nvml.ERROR_NOT_SUPPORTED {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get process utilization: %s", nvml.ErrorString(ret))
	}

	result := make(map[uint32]uint32, len(samples))
	for _, s := range samples {
		result[s.Pid] = s.SmUtil
	}

	return result, nil
}

// GetAllMIGDevicesProcessMemory returns per-process memory usage for all MIG instances on a GPU.
// Returns map[gpuInstanceID (MIG instance)]map[PID]memoryBytes.
func (n nvmlProvider) GetAllMIGDevicesProcessMemory(parentGPUUUID string) (map[uint]map[uint32]uint64, error) {
	if err := n.preCheck(); err != nil {
		return nil, fmt.Errorf("failed to get MIG device process memory: %w", err)
	}

	parentDevice, ret := nvml.DeviceGetHandleByUUID(parentGPUUUID)
	if ret != nvml.SUCCESS {
		return nil, fmt.Errorf("failed to get parent device handle for UUID %s: %s", parentGPUUUID, nvml.ErrorString(ret))
	}

	migCount, ret := parentDevice.GetMaxMigDeviceCount()
	if ret != nvml.SUCCESS {
		return nil, fmt.Errorf("failed to get MIG device count for UUID %s: %s", parentGPUUUID, nvml.ErrorString(ret))
	}

	result := make(map[uint]map[uint32]uint64)

	for i := 0; i < migCount; i++ {
		migDevice, ret := parentDevice.GetMigDeviceHandleByIndex(i)
		if ret == nvml.ERROR_NOT_FOUND || ret == nvml.ERROR_INVALID_ARGUMENT {
			continue
		}
		if ret != nvml.SUCCESS {
			slog.Debug("Failed to get MIG device handle", "index", i, "error", nvml.ErrorString(ret))
			continue
		}

		giID, ret := migDevice.GetGpuInstanceId()
		if ret != nvml.SUCCESS {
			slog.Debug("Failed to get GPU instance ID for MIG device", "index", i, "error", nvml.ErrorString(ret))
			continue
		}

		processes, ret := migDevice.GetComputeRunningProcesses()
		if ret != nvml.SUCCESS && ret != nvml.ERROR_NOT_SUPPORTED {
			slog.Debug("Failed to get running processes for MIG device", "gpuInstanceID", giID, "error", nvml.ErrorString(ret))
			continue
		}

		if giID < 0 {
			slog.Debug("Skipping MIG device with negative GPU instance ID", "gpuInstanceID", giID)
			continue
		}

		pidToMemory := make(map[uint32]uint64, len(processes))
		for _, p := range processes {
			pidToMemory[p.Pid] = p.UsedGpuMemory
		}
		result[uint(giID)] = pidToMemory
	}

	return result, nil
}

// Cleanup performs cleanup operations for the NVML provider
func (n nvmlProvider) Cleanup() {
	if !n.initialized {
		slog.Info("NVML not initialized, skipping cleanup")
		return
	}

	slog.Info("Attempting to shutdown NVML library")
	ret := nvml.Shutdown()
	if ret != nvml.SUCCESS {
		slog.Error(fmt.Sprintf("Failed to shutdown NVML library: %v", nvml.ErrorString(ret)))
	}

	reset()
}
