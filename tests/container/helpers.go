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

package container

import (
	"fmt"
	"net"
	"strings"
)

const localLoopbackHost = "127.0.0.1"

// localHTTPURL returns a loopback URL that does not depend on hostname resolution.
func localHTTPURL(port int, path string) string {
	return fmt.Sprintf("http://%s/%s", net.JoinHostPort(localLoopbackHost, fmt.Sprintf("%d", port)), strings.TrimPrefix(path, "/"))
}

// imagePullUnavailableForPlatform reports a valid image that lacks the host architecture.
func imagePullUnavailableForPlatform(output []byte) bool {
	return strings.Contains(strings.ToLower(string(output)), "no matching manifest for linux/")
}

// localDockerEndpoint reports whether a Docker endpoint runs containers on
// this host. Only a unix socket counts: a TCP address, even a loopback one, can
// be a tunnel to a daemon on another host.
func localDockerEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "unix://")
}
