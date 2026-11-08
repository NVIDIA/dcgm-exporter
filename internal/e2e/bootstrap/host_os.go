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
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// osReleasePath is the standard file used to identify the host operating system.
const osReleasePath = "/etc/os-release"

// HostOS identifies the operating system used for native package operations.
type HostOS struct {
	Distribution         string
	VersionID            string
	Architecture         string
	NVIDIARepositoryArch string
}

// HostOSDetector detects the host operating system without changing it.
type HostOSDetector func() (HostOS, error)

// DetectHostOS reads and parses the current host's os-release file.
func DetectHostOS() (HostOS, error) {
	content, err := os.Open(osReleasePath)
	if err != nil {
		return HostOS{}, fmt.Errorf("read %s: %w", osReleasePath, err)
	}
	defer content.Close()

	return ParseHostOS(content, runtime.GOARCH)
}

// ParseHostOS parses os-release data and maps the CPU name to the repository name.
func ParseHostOS(content io.Reader, architecture string) (HostOS, error) {
	values, err := parseOSRelease(content)
	if err != nil {
		return HostOS{}, err
	}

	distribution := packageDistribution(values["ID"], values["ID_LIKE"])
	version := strings.TrimSpace(values["VERSION_ID"])
	if distribution == "" || version == "" {
		return HostOS{}, fmt.Errorf("os-release must define ID and VERSION_ID")
	}

	nvidiaRepositoryArch, err := nvidiaRepositoryArch(architecture)
	if err != nil {
		return HostOS{}, err
	}
	return HostOS{
		Distribution:         distribution,
		VersionID:            version,
		Architecture:         architecture,
		NVIDIARepositoryArch: nvidiaRepositoryArch,
	}, nil
}

// packageDistribution returns the supported package family named by os-release.
func packageDistribution(id, idLike string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "ubuntu" || id == "rhel" {
		return id
	}

	for _, family := range strings.Fields(strings.ToLower(idLike)) {
		if family == "ubuntu" || family == "rhel" {
			return family
		}
	}

	return id
}

// parseOSRelease parses literal key/value records without executing shell input.
func parseOSRelease(content io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(content)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid os-release record %q", line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `'`) {
			if len(value) < 2 || !strings.HasSuffix(value, `'`) {
				return nil, fmt.Errorf("invalid os-release value for %s", key)
			}
			value = strings.TrimSuffix(strings.TrimPrefix(value, `'`), `'`)
		} else if strings.HasPrefix(value, `"`) {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("invalid os-release value for %s: %w", key, err)
			}
			value = unquoted
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan os-release: %w", err)
	}
	return values, nil
}

// nvidiaRepositoryArch maps Go architecture names to NVIDIA repository paths.
func nvidiaRepositoryArch(architecture string) (string, error) {
	switch architecture {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "sbsa", nil
	default:
		return "", fmt.Errorf("unsupported host architecture %q", architecture)
	}
}

// validateUbuntuHostOS accepts only Ubuntu tuples covered by repository fixtures.
func validateUbuntuHostOS(host HostOS) (HostOS, error) {
	if host.Distribution != "ubuntu" {
		return HostOS{}, fmt.Errorf("unsupported host distribution %q", host.Distribution)
	}
	switch host.VersionID {
	case "22.04", "24.04", "26.04":
	default:
		return HostOS{}, fmt.Errorf("unsupported Ubuntu version %q; supported versions are 22.04, 24.04, and 26.04", host.VersionID)
	}
	expectedArchitecture, err := nvidiaRepositoryArch(host.Architecture)
	if err != nil {
		return HostOS{}, err
	}
	if host.NVIDIARepositoryArch != expectedArchitecture {
		return HostOS{}, fmt.Errorf("ubuntu repository architecture %q does not match host architecture %q", host.NVIDIARepositoryArch, host.Architecture)
	}
	return host, nil
}
