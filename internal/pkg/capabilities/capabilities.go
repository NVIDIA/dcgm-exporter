/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package capabilities provides Linux capability checking functionality for dcgm-exporter.
// It detects if CAP_SYS_ADMIN is available for profiling metrics collection.
package capabilities

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const (
	// CAP_SYS_ADMIN is required for profiling metrics (DCP/DCGM_FI_PROF_*)
	CAP_SYS_ADMIN = 21
)

// HasCapability checks if the current process has the specified Linux capability.
// It reads from /proc/self/status to check the effective capabilities.
func HasCapability(cap int) (bool, error) {
	// Validate capability number is in valid range (0-63)
	if cap < 0 || cap > 63 {
		return false, fmt.Errorf("invalid capability number: %d (must be 0-63)", cap)
	}

	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false, fmt.Errorf("failed to read /proc/self/status: %w", err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			// Extract the hex capability mask
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}

			capHex := fields[1]
			capMask, err := strconv.ParseUint(capHex, 16, 64)
			if err != nil {
				return false, fmt.Errorf("failed to parse capability mask: %w", err)
			}

			// Check if the specific capability bit is set
			// cap is validated to be 0-63, so conversion to uint is safe
			// #nosec G115 -- cap range validated above
			return (capMask & (1 << uint(cap))) != 0, nil
		}
	}

	return false, fmt.Errorf("could not find CapEff in /proc/self/status")
}

// CheckSysAdmin checks if the process has CAP_SYS_ADMIN capability.
// Returns true if the capability is present, false otherwise.
func CheckSysAdmin() bool {
	has, err := HasCapability(CAP_SYS_ADMIN)
	if err != nil {
		slog.Warn("Failed to check for CAP_SYS_ADMIN capability",
			slog.String("error", err.Error()))
		return false
	}
	return has
}

// WarnIfMissingProfilingCapabilities checks if profiling metrics are being collected
// and warns if CAP_SYS_ADMIN is not available.
// This replicates the behavior of the old entrypoint script.
func WarnIfMissingProfilingCapabilities(hasProfilingMetrics bool) {
	if !hasProfilingMetrics {
		// No profiling metrics configured, capability not needed
		return
	}

	if CheckSysAdmin() {
		// Capability present - profiling metrics will work
		return
	}

	// Capability missing but profiling metrics requested
	// This matches the warning from the old entrypoint script
	slog.Warn("dcgm-exporter doesn't have sufficient privileges to expose profiling metrics. To get profiling metrics with dcgm-exporter, use --cap-add SYS_ADMIN")
}

// GetCurrentCapabilities returns a human-readable string of current effective capabilities.
// Useful for debugging.
func GetCurrentCapabilities() string {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Sprintf("error reading capabilities: %v", err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				capHex := fields[1]
				capMask, err := strconv.ParseUint(capHex, 16, 64)
				if err != nil {
					return fmt.Sprintf("error parsing: %v", err)
				}

				// Decode common capabilities
				caps := []string{}
				capNames := map[int]string{
					0:  "CAP_CHOWN",
					1:  "CAP_DAC_OVERRIDE",
					2:  "CAP_DAC_READ_SEARCH",
					3:  "CAP_FOWNER",
					4:  "CAP_FSETID",
					5:  "CAP_KILL",
					6:  "CAP_SETGID",
					7:  "CAP_SETUID",
					8:  "CAP_SETPCAP",
					9:  "CAP_LINUX_IMMUTABLE",
					10: "CAP_NET_BIND_SERVICE",
					11: "CAP_NET_BROADCAST",
					12: "CAP_NET_ADMIN",
					13: "CAP_NET_RAW",
					14: "CAP_IPC_LOCK",
					15: "CAP_IPC_OWNER",
					16: "CAP_SYS_MODULE",
					17: "CAP_SYS_RAWIO",
					18: "CAP_SYS_CHROOT",
					19: "CAP_SYS_PTRACE",
					20: "CAP_SYS_PACCT",
					21: "CAP_SYS_ADMIN",
					22: "CAP_SYS_BOOT",
					23: "CAP_SYS_NICE",
					24: "CAP_SYS_RESOURCE",
					25: "CAP_SYS_TIME",
					26: "CAP_SYS_TTY_CONFIG",
				}

				for cap := 0; cap < 64; cap++ {
					// cap is in range 0-63, so conversion to uint is safe
					// #nosec G115 -- cap range enforced by loop bounds
					if (capMask & (1 << uint(cap))) != 0 {
						if name, ok := capNames[cap]; ok {
							caps = append(caps, name)
						} else {
							caps = append(caps, fmt.Sprintf("CAP_%d", cap))
						}
					}
				}

				if len(caps) == 0 {
					return "no capabilities"
				}
				return strings.Join(caps, ", ")
			}
		}
	}

	return "unknown"
}

// DropAllCapabilitiesExcept drops all capabilities except the specified ones.
// This is a placeholder for future implementation if needed.
// Note: Dropping capabilities requires CAP_SETPCAP.
func DropAllCapabilitiesExcept(keep []int) error {
	// This would require using the cap library or syscalls
	// For now, we rely on container runtime to handle capability dropping
	// via securityContext.capabilities.drop in Kubernetes
	slog.Debug("Capability dropping is handled by container runtime",
		slog.String("method", "securityContext.capabilities.drop"))
	return nil
}

// LogCapabilityInfo logs information about current capabilities.
// Useful for debugging permission issues.
func LogCapabilityInfo() {
	uid := os.Getuid()
	euid := os.Geteuid()
	gid := os.Getgid()
	egid := os.Getegid()

	caps := GetCurrentCapabilities()

	slog.Debug("Process capability information",
		slog.Int("uid", uid),
		slog.Int("euid", euid),
		slog.Int("gid", gid),
		slog.Int("egid", egid),
		slog.String("capabilities", caps),
		slog.Bool("has_cap_sys_admin", CheckSysAdmin()))
}

// IsRunningAsRoot returns true if the effective UID is 0 (root).
func IsRunningAsRoot() bool {
	return syscall.Geteuid() == 0
}
