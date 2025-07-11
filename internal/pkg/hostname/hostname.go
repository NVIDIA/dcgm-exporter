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

package hostname

import (
	"net"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	osinterface "github.com/NVIDIA/dcgm-exporter/internal/pkg/os"
)

var os osinterface.OS = osinterface.RealOS{}

// GetHostname return a hostname where metric was collected.
func GetHostname(config *appconfig.Config) (string, error) {
	if config.Kubernetes {
		/* in kubernetes, the remote hostname is generic and local, so it's not useful */
		return getLocalHostname()
	}
	if config.UseRemoteHE {
		return parseRemoteHostname(config)
	}
	return getLocalHostname()
}

func parseRemoteHostname(config *appconfig.Config) (string, error) {
	// Extract the hostname or IP address part from the appconfig.RemoteHEInfo
	// This handles inputs like "localhost:5555", "example.com:5555", or "192.168.1.1:5555"
	host, _, err := net.SplitHostPort(config.RemoteHEInfo)
	if err != nil {
		// If there's an error, it might be because there's no port in the appconfig.RemoteHEInfo
		// In that case, use the appconfig.RemoteHEInfo as is
		host = config.RemoteHEInfo
	}
	return host, nil
}

func getLocalHostname() (string, error) {
	if nodeName := os.Getenv("NODE_NAME"); nodeName != "" {
		return nodeName, nil
	}
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	return hostname, nil
}
