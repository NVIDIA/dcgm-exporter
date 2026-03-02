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
	debugelf "debug/elf"
	"fmt"
	"log/slog"
	"strings"
)

const (
	libdcgmco     = "libdcgm.so.4"
	procSelfExe   = "/proc/self/exe"
	ldconfig      = "ldconfig"
	ldconfigParam = "-p"
)

type dcgmLibExistsRule struct{}

// Validate checks if libdcgm.so.4 exists and matches with the machine architecture.
func (c dcgmLibExistsRule) Validate() error {
	// On Ubuntu, ldconfig is a wrapper around ldconfig.real
	ldconfigPath := fmt.Sprintf("/sbin/%s.real", ldconfig)
	if _, err := os.Stat(ldconfigPath); err != nil {
		ldconfigPath = "/sbin/" + ldconfig
	}
	// Get list of shared libraries. See: man ldconfig
	out, err := exec.Command(ldconfigPath, ldconfigParam).Output()
	if err != nil {
		return err
	}

	for _, match := range rxLDCacheEntry.FindAllSubmatch(out, -1) {
		libName := strings.TrimSpace(string(match[1]))
		if libName == libdcgmco {
			libPath := strings.TrimSpace(string(match[2]))
			selfMachine, err := c.readELF(procSelfExe)
			if err != nil {
				return err
			}
			libMachine, err := c.readELF(libPath)
			if err != nil {
				// When datacenter-gpu-manager uninstalled, the ldconfig -p may return that the libdcgm.so.4 is present,
				// but the library file was removed.
				slog.Error(err.Error())
				return errLibdcgmNotFound
			}

			if selfMachine != libMachine {
				return fmt.Errorf("the %s library architecture mismatch with the system; wanted: %s, received: %s",
					libdcgmco, selfMachine, libMachine)
			}

			return nil
		}
	}

	return errLibdcgmNotFound
}

func (c dcgmLibExistsRule) readELF(name string) (debugelf.Machine, error) {
	elfFile, err := elf.Open(name)
	if err != nil {
		return 0, fmt.Errorf("could not open %s: %v", name, err)
	}
	if err := elfFile.Close(); err != nil {
		slog.Warn(fmt.Sprintf("could not close ELF: %v", err))
	}

	return elfFile.Machine, nil
}
