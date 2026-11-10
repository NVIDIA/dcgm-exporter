/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// PackageRequirements lists the native package groups needed for a run.
type PackageRequirements struct {
	Docker bool
	// DockerSocketACL requires the acl package so bootstrap can repair user access to Docker's socket.
	DockerSocketACL  bool
	Buildx           bool
	ContainerToolkit bool
	HostDCGM         bool
	DCGMDevelopment  bool
	DCGMVersion      string
	// ContainerToolkitVersion pins every Toolkit package used by the local k3d node image.
	ContainerToolkitVersion string
}

// Empty reports whether no host packages are required.
func (r PackageRequirements) Empty() bool {
	return !r.Docker && !r.DockerSocketACL && !r.Buildx && !r.ContainerToolkit && !r.HostDCGM && !r.DCGMDevelopment
}

// String lists the required package groups for diagnostics.
func (r PackageRequirements) String() string {
	var names []string
	if r.Docker {
		names = append(names, "Docker")
	}
	if r.DockerSocketACL {
		names = append(names, "Docker socket ACL")
	}
	if r.Buildx {
		names = append(names, "Docker Buildx")
	}
	if r.ContainerToolkit {
		names = append(names, "NVIDIA Container Toolkit")
	}
	if r.HostDCGM {
		names = append(names, "host DCGM")
	}
	if r.DCGMDevelopment {
		names = append(names, "DCGM development files")
	}
	return strings.Join(names, ", ")
}

// PackageStatus records whether each requested native package is ready.
type PackageStatus struct {
	Docker bool
	// DockerSocketACL reports whether the acl package needed for Docker socket repair is installed.
	DockerSocketACL  bool
	Buildx           bool
	ContainerToolkit bool
	HostDCGM         bool
	DCGMDevelopment  bool
	// HostDCGMReason says why host DCGM or its development package is not ready; it is empty when both are ready or not checked.
	HostDCGMReason string
}

// MissingPackages returns required packages that are not ready.
func (s PackageStatus) MissingPackages(requirements PackageRequirements) PackageRequirements {
	return PackageRequirements{
		Docker:                  requirements.Docker && !s.Docker,
		DockerSocketACL:         requirements.DockerSocketACL && !s.DockerSocketACL,
		Buildx:                  requirements.Buildx && !s.Buildx,
		ContainerToolkit:        requirements.ContainerToolkit && !s.ContainerToolkit,
		HostDCGM:                requirements.HostDCGM && !s.HostDCGM,
		DCGMDevelopment:         requirements.DCGMDevelopment && !s.DCGMDevelopment,
		DCGMVersion:             requirements.DCGMVersion,
		ContainerToolkitVersion: requirements.ContainerToolkitVersion,
	}
}

// DCGM reasons shared by both package installers.
const (
	dcgmNotInstalledFormat         = "DCGM packages are not installed: %s"
	dcgmLibraryNotVisible          = "libdcgm.so.4 is not visible to the dynamic loader"
	dcgmInjectionLibraryNotVisible = "libnvml_injection.so is not visible to the dynamic loader"
)

// notReadyError reports a package condition that is not ready, as opposed to a failed inspection.
type notReadyError struct{ reason string }

// Error returns the reason the package is not ready.
func (e notReadyError) Error() string { return e.reason }

// notReady builds a notReadyError from a format string.
func notReady(format string, args ...any) error {
	return notReadyError{reason: fmt.Sprintf(format, args...)}
}

// notReadyReason turns a not-ready error into a status reason and keeps inspection failures as errors.
func notReadyReason(err error) (string, error) {
	var reason notReadyError
	if errors.As(err, &reason) {
		return reason.reason, nil
	}

	return "", err
}

// hostPackageInstaller checks and installs packages for one supported host OS.
type hostPackageInstaller interface {
	CheckPackages(context.Context, PackageRequirements) (PackageStatus, error)
	InstallPackages(context.Context, PackageRequirements) error
}

// newHostPackageInstaller validates the host OS and returns its package installer.
func newHostPackageInstaller(host HostOS, stdout io.Writer, runner e2eexec.Runner) (hostPackageInstaller, error) {
	switch host.Distribution {
	case "ubuntu":
		validated, err := validateUbuntuHostOS(host)
		if err != nil {
			return nil, err
		}
		return &ubuntuPackageInstaller{hostOS: validated, stdout: stdout, runner: runner}, nil
	case "rhel":
		validated, err := validateRHELHostOS(host)
		if err != nil {
			return nil, err
		}
		return &rhelPackageInstaller{hostOS: validated, stdout: stdout, runner: runner}, nil
	default:
		return nil, fmt.Errorf("unsupported host distribution %q", host.Distribution)
	}
}

// Compile-time assertions require both supported installers to implement hostPackageInstaller.
var (
	_ hostPackageInstaller = (*ubuntuPackageInstaller)(nil)
	_ hostPackageInstaller = (*rhelPackageInstaller)(nil)
)
