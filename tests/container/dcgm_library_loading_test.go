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
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// libraryFailureTimeout bounds one docker run so an exporter that starts instead of failing is
// reported as a failure rather than hanging the suite.
const libraryFailureTimeout = time.Minute

// containerCleanupTimeout bounds the removal of a library-failure test container.
const containerCleanupTimeout = 30 * time.Second

// isolateLibraryScript empties every shipped libdcgm.so.4 file and checks that the loader can no
// longer find a usable copy. The distroless image has no find, rm, mkdir, or grep, so it uses only
// bash built-ins. The image has no mkdir, so the docker run call mounts a tmpfs at /opt/empty-dcgm
// to provide the empty directory. The script ends with "exec ", and the caller must append the
// exporter launch command to it.
const isolateLibraryScript = `
set -euo pipefail
shopt -s nullglob
libs=(/usr/lib/*-linux-gnu/libdcgm.so.4*)
if (( ${#libs[@]} == 0 )); then
  echo 'test isolation failed: no libdcgm.so.4 found under /usr/lib' >&2
  exit 99
fi
for f in "${libs[@]}"; do : > "$f"; done
/usr/sbin/ldconfig
if [[ "$(/usr/sbin/ldconfig -p)" == *libdcgm.so.4* ]]; then
  echo 'test isolation failed: libdcgm.so.4 remains in the loader cache' >&2
  exit 99
fi
for f in "${libs[@]}"; do
  if [[ -s "$f" ]]; then
    echo "test isolation failed: $f is not empty" >&2
    exit 99
  fi
done
exec `

// unusableLibraryLaunch writes a file that is not a library at /opt/empty-dcgm/libdcgm.so.4 and
// starts the exporter with that directory in LD_LIBRARY_PATH. The exporter starts only if the
// write succeeds, so a failed write cannot turn this case into the empty-directory case.
const unusableLibraryLaunch = "/usr/bin/sh -c 'printf invalid > /opt/empty-dcgm/libdcgm.so.4 && " +
	"exec /usr/bin/env LD_LIBRARY_PATH=/opt/empty-dcgm /usr/bin/dcgm-exporter'"

// removeTestContainer force-removes a named test container. A container that is already gone is
// expected, because docker run --rm removes it. Any other failure is returned, so DeferCleanup
// fails the spec instead of leaving the container on the test host.
func removeTestContainer(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput()
	if err != nil && !strings.Contains(string(output), "No such container") {
		return fmt.Errorf("remove container %s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}

	return nil
}

var _ = Describe("DCGM library load failures", Serial, Label("dcgmLibraryFailure"), func() {
	DescribeTable("exits cleanly when no usable library is available",
		func(ctx context.Context, launch string) {
			image := requireExporterImage(ctx)

			// A named container lets cleanup stop an exporter that wrongly keeps running.
			name := testContainerName("library-failure")
			DeferCleanup(removeTestContainer, name)

			// Bound only the docker run; the image check above may pull a large image.
			runCtx, cancel := context.WithTimeout(ctx, libraryFailureTimeout)
			defer cancel()

			cmd := exec.CommandContext(runCtx, "docker", "run", "--rm", "--name", name, "--tmpfs", "/opt/empty-dcgm",
				"--entrypoint", "/usr/bin/bash", image, "-ceu", isolateLibraryScript+launch)
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
		Entry("LD_LIBRARY_PATH is unset",
			"/usr/bin/env -u LD_LIBRARY_PATH /usr/bin/dcgm-exporter"),
		Entry("LD_LIBRARY_PATH points to an empty directory",
			"/usr/bin/env LD_LIBRARY_PATH=/opt/empty-dcgm /usr/bin/dcgm-exporter"),
		Entry("LD_LIBRARY_PATH contains an unusable library",
			unusableLibraryLaunch),
	)
})
