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

package static_test

import (
	"os"
	"strings"
	"testing"
)

func TestPackageSystemdUnitContract(t *testing.T) {
	data, err := os.ReadFile(repoPath(t, "packaging", "config-files", "systemd", "nvidia-dcgm-exporter.service"))
	if err != nil {
		t.Fatalf("read systemd unit: %v", err)
	}
	unit := string(data)

	for _, want := range []string{
		"Wants=nvidia-dcgm.service",
		"After=nvidia-dcgm.service",
		"StartLimitIntervalSec=0",
		"User=root",
		"PrivateTmp=false",
		"ExecStart=/usr/bin/dcgm-exporter -f /etc/dcgm-exporter/default-counters.csv",
		"Restart=on-failure",
		"RestartSec=10s",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("systemd unit missing %q", want)
		}
	}
	if strings.Contains(unit, "EnvironmentFile=") {
		t.Fatal("systemd unit references an environment file that is not packaged")
	}
	if strings.Contains(unit, "--web-systemd-socket") {
		t.Fatal("packaged service must use the normal HTTP listener; socket activation is covered by the host systemdSocket scenario")
	}
}
