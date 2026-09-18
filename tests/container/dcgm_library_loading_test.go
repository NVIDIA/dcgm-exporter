//go:build container

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

package container

import (
	"context"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DCGM library load failures", Serial, Label("dcgmLibraryFailure"), func() {
	DescribeTable("exits cleanly when no usable library is available",
		func(ctx context.Context, launch string) {
			img, found := findImageByVariant("ubuntu26.04")
			if !found {
				Skip("ubuntu26.04 image not configured")
			}
			exists, err := imageExists(ctx, img.FullName)
			Expect(err).NotTo(HaveOccurred())
			if !exists {
				Skip("ubuntu26.04 image is unavailable")
			}

			const isolateAndLaunch = `
set -euo pipefail
mkdir -p /opt/empty-dcgm
find /usr/lib /lib \( -name 'libdcgm.so.4' -o -name 'libdcgm.so.4.*' \) -print0 | xargs -0 -r rm -f
ldconfig
if ldconfig -p | grep -q 'libdcgm.so.4'; then
  echo 'test isolation failed: libdcgm.so.4 remains in the loader cache' >&2
  exit 99
fi
exec `
			cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--entrypoint", "/bin/bash",
				img.FullName, "-ceu", isolateAndLaunch+launch)
			output, err := cmd.CombinedOutput()
			Expect(err).To(HaveOccurred(), "exporter unexpectedly started:\n%s", output)
			exitErr, ok := err.(*exec.ExitError)
			Expect(ok).To(BeTrue(), "docker run returned %T: %v", err, err)
			Expect(exitErr.ExitCode()).To(Equal(1), "unexpected exit:\n%s", output)

			logs := string(output)
			Expect(logs).To(ContainSubstring("library=libdcgm.so.4"))
			Expect(logs).To(ContainSubstring("compatible DCGM library"))
			Expect(logs).To(ContainSubstring("LD_LIBRARY_PATH"))
			Expect(strings.ToLower(logs)).NotTo(ContainSubstring("panic"))
		},
		Entry("LD_LIBRARY_PATH is unset", "env -u LD_LIBRARY_PATH /usr/bin/dcgm-exporter"),
		Entry("LD_LIBRARY_PATH points to an empty directory", "env LD_LIBRARY_PATH=/opt/empty-dcgm /usr/bin/dcgm-exporter"),
		Entry("LD_LIBRARY_PATH contains an unusable library", "sh -c 'printf invalid > /opt/empty-dcgm/libdcgm.so.4; exec env LD_LIBRARY_PATH=/opt/empty-dcgm /usr/bin/dcgm-exporter'"),
	)
})
