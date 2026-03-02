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

package prerequisites

import (
	"fmt"
	"regexp"

	elfinterface "github.com/NVIDIA/dcgm-exporter/internal/pkg/elf"
	execinterface "github.com/NVIDIA/dcgm-exporter/internal/pkg/exec"
	osinterface "github.com/NVIDIA/dcgm-exporter/internal/pkg/os"
)

var (
	os osinterface.OS = osinterface.RealOS{}

	exec execinterface.Exec = execinterface.RealExec{}

	elf elfinterface.ELF = elfinterface.RealELF{}

	// rxLDCacheEntry matches the following library strings:
	//	libdcgm.so.4 (libc6,x86-64) => /lib/x86_64-linux-gnu/libdcgm.so.4
	//	ld-linux.so.2 (ELF) => /lib/ld-linux.so.2
	// ld-linux-x86-64.so.2 (libc6,x86-64) => /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2
	rxLDCacheEntry = regexp.MustCompile(`(?m)^(.*)\s*\(.*\)\s*=>\s*(.*)$`)

	errLibdcgmNotFound = fmt.Errorf("the %s library was not found. Install Data Center GPU Manager (DCGM).", libdcgmco)
)
