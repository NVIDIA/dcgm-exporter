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

package mig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// ErrLayoutUnsupported reports a host that cannot take a MIG layout, such as
// one with too few GPUs or a GPU without MIG support. Nothing was changed.
var ErrLayoutUnsupported = errors.New("MIG layout unsupported")

// minLayoutGPUs is the number of GPUs the two-instances-per-GPU layout needs,
// so a check can tell one series per GPU from one series for the node.
const minLayoutGPUs = 2

// migProfileLineRE matches the first line of one profile in
// "nvidia-smi mig -lgip" output, for example
// "|   0  MIG 3g.40gb    9     2/2     39.25   No   60   3   0  |". It captures
// the name, profile ID, free and total instances, memory in GiB and SM count.
var migProfileLineRE = regexp.MustCompile(`^\|\s*\d+\s+MIG\s+(\S+)\s+(\d+)\s+(\d+)/(\d+)\s+([0-9.]+)\s+\S+\s+(\d+)`)

// migProfile is one GPU instance profile that "nvidia-smi mig -lgip" lists.
type migProfile struct {
	name      string
	id        string
	free      int
	memoryGiB float64
	sms       int
}

// PrepareHostLayout applies the MIG layout that the selected scenarios
// declare and returns the cleanup that restores MIG, with the environment that
// tells their tests the layout is in place. Without a declared layout it does
// nothing. A layout changes MIG on every GPU, so scenarios that do not declare
// it cannot run in the same suite run. A host that cannot take the layout gets
// an error wrapping ErrLayoutUnsupported and no change.
func PrepareHostLayout(
	ctx context.Context, stdout io.Writer, runner e2eexec.Runner, selected []scenario.Scenario,
) (cleanup func(context.Context) error, env []string, err error) {
	noop := func(context.Context) error { return nil }
	layout, err := declaredLayout(selected)
	if err != nil || layout == scenario.MIGLayoutNone {
		return noop, nil, err
	}

	cleanup, err = applyTwoInstancesPerGPU(ctx, stdout, runner, migLockPath, 0)
	if err != nil {
		return noop, nil, err
	}
	return cleanup, []string{scenario.MIGLayoutEnv + "=" + string(layout)}, nil
}

// declaredLayout returns the one MIG layout the selected scenarios declare, or
// MIGLayoutNone. It rejects a selection that mixes a layout with scenarios that
// need MIG left as it is.
func declaredLayout(selected []scenario.Scenario) (scenario.MIGLayout, error) {
	layout := scenario.MIGLayoutNone
	var without []string
	for _, entry := range selected {
		if entry.MIGLayout == scenario.MIGLayoutNone {
			without = append(without, entry.Selector())
			continue
		}
		layout = entry.MIGLayout
	}

	if layout != scenario.MIGLayoutNone && len(without) > 0 {
		return layout, fmt.Errorf("MIG layout %s changes every GPU; run it without %s", layout, strings.Join(without, ", "))
	}
	return layout, nil
}

// applyTwoInstancesPerGPU puts every GPU in MIG mode with two GPU instances,
// each with one compute instance, of the largest profile that fits twice on
// the GPU, under the MIG lock at lockPath. It returns a hook that removes the
// instances, turns MIG off again where it was off, restores the host CDI spec
// and releases the lock. A host with fewer than two GPUs, or a GPU without
// MIG, gets an error wrapping ErrLayoutUnsupported and no change.
func applyTwoInstancesPerGPU(
	ctx context.Context, stdout io.Writer, runner e2eexec.Runner, lockPath string, lockUID int,
) (restore func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	if runtime.GOOS != "linux" {
		return noop, errMIGRequiresLinux
	}

	// A read-only check first, so an unsupported host skips without the lock.
	gpus, err := gpuMIGModes(ctx, runner)
	if err != nil {
		return noop, err
	}
	if err := requireLayoutGPUs(gpus); err != nil {
		return noop, err
	}

	release, err := acquireMIGLock(ctx, stdout, runner, lockPath, lockUID)
	if err != nil {
		return noop, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, release())
		}
	}()

	// The modes restored afterwards are read under the lock.
	if gpus, err = gpuMIGModes(ctx, runner); err != nil {
		return noop, err
	}
	if err := refuseUnsafeMIGChange(ctx, runner); err != nil {
		return noop, err
	}
	cdiSpec, err := saveCDISpec(ctx, runner)
	if err != nil {
		return noop, err
	}
	if err := createTwoInstancesPerGPU(ctx, stdout, runner, gpus, cdiSpec); err != nil {
		return noop, err
	}

	var once sync.Once
	var restoreErr error
	return func(restoreCtx context.Context) error {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(restoreCtx), time.Duration(len(gpus))*migRollbackTimeout)
			defer cancel()
			restoreErr = errors.Join(restoreGPUs(ctx, stdout, runner, gpus, cdiSpec), release())
		})
		return restoreErr
	}, nil
}

// gpuMIGModes reads the index and current MIG mode of every GPU.
func gpuMIGModes(ctx context.Context, runner e2eexec.Runner) ([]gpuMIGMode, error) {
	output, err := runCommandText(ctx, runner, nvidiaSMI, "--query-gpu=index,mig.mode.current", "--format=csv,noheader")
	if err != nil {
		return nil, fmt.Errorf("query current MIG mode before MIG configuration: %w", err)
	}

	var gpus []gpuMIGMode
	for _, line := range strings.Split(output, "\n") {
		index, mode, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		gpus = append(gpus, gpuMIGMode{index: strings.TrimSpace(index), mode: strings.TrimSpace(mode)})
	}
	return gpus, nil
}

// requireLayoutGPUs returns an error wrapping ErrLayoutUnsupported unless there
// are at least two GPUs and every one reports MIG as enabled or disabled; any
// other mode, such as "[N/A]", means the GPU has no MIG.
func requireLayoutGPUs(gpus []gpuMIGMode) error {
	if len(gpus) < minLayoutGPUs {
		return fmt.Errorf("%w: %d GPUs, need at least %d", ErrLayoutUnsupported, len(gpus), minLayoutGPUs)
	}

	for _, gpu := range gpus {
		if !strings.EqualFold(gpu.mode, "enabled") && !strings.EqualFold(gpu.mode, "disabled") {
			return fmt.Errorf("%w: GPU %s has MIG mode %q", ErrLayoutUnsupported, gpu.index, gpu.mode)
		}
	}
	return nil
}

// createTwoInstancesPerGPU turns MIG on for every GPU, creates two GPU
// instances with compute instances of the largest profile that fits twice on
// each, and regenerates the host CDI spec. On failure it restores every GPU
// and cdiSpec before it returns.
func createTwoInstancesPerGPU(
	ctx context.Context, stdout io.Writer, runner e2eexec.Runner, gpus []gpuMIGMode, cdiSpec cdiSpecState,
) (err error) {
	// A failed command can still change MIG state, so every failure rolls back.
	defer func() {
		if err == nil {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(len(gpus))*migRollbackTimeout)
		defer cancel()
		if rollbackErr := restoreGPUs(rollbackCtx, stdout, runner, gpus, cdiSpec); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("restore MIG state after failed configuration: %w", rollbackErr))
		}
	}()

	for _, gpu := range gpus {
		if err := runInstallCommand(ctx, stdout, runner, "nvidia_smi_enable_mig", sudoCommand(nvidiaSMI, "-i", gpu.index, "-mig", "1")); err != nil {
			return err
		}
	}

	for _, gpu := range gpus {
		if err := createTwoInstances(ctx, stdout, runner, gpu.index); err != nil {
			return err
		}
	}

	return regenerateNvidiaCDISpec(ctx, stdout, runner)
}

// createTwoInstances creates two GPU instances, each with a compute instance,
// of the largest profile that fits twice on one GPU in MIG mode.
func createTwoInstances(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, index string) error {
	profiles, err := runCommandText(ctx, runner, nvidiaSMI, "mig", "-i", index, "-lgip")
	if err != nil {
		return fmt.Errorf("query MIG profiles of GPU %s: %w", index, err)
	}

	profile, ok := largestProfileFittingTwice(profiles)
	if !ok {
		return fmt.Errorf("GPU %s lists no MIG profile with two free instances:\n%s", index, profiles)
	}

	fmt.Fprintf(stdout, "[e2e] Creating two MIG instances of profile %s (ID %s) on GPU %s\n", profile.name, profile.id, index)
	return runInstallCommand(ctx, stdout, runner, "nvidia_smi_create_mig_instances",
		sudoCommand(nvidiaSMI, "mig", "-i", index, "-cgi", profile.id+","+profile.id, "-C"))
}

// largestProfileFittingTwice returns the profile with the most SMs, then the
// most memory, among those with at least two free instances in
// "nvidia-smi mig -lgip" output.
func largestProfileFittingTwice(output string) (migProfile, bool) {
	var best migProfile
	found := false
	for _, line := range strings.Split(output, "\n") {
		profile, ok := parseMIGProfileLine(line)
		if !ok || profile.free < 2 {
			continue
		}
		if !found || profile.sms > best.sms || (profile.sms == best.sms && profile.memoryGiB > best.memoryGiB) {
			best, found = profile, true
		}
	}
	return best, found
}

// parseMIGProfileLine reads the first line of one "nvidia-smi mig -lgip"
// profile. Other lines report false.
func parseMIGProfileLine(line string) (migProfile, bool) {
	match := migProfileLineRE.FindStringSubmatch(strings.TrimSpace(line))
	if match == nil {
		return migProfile{}, false
	}

	free, freeErr := strconv.Atoi(match[3])
	memory, memoryErr := strconv.ParseFloat(match[5], 64)
	sms, smsErr := strconv.Atoi(match[6])
	if freeErr != nil || memoryErr != nil || smsErr != nil {
		return migProfile{}, false
	}
	return migProfile{name: match[1], id: match[2], free: free, memoryGiB: memory, sms: sms}, true
}
