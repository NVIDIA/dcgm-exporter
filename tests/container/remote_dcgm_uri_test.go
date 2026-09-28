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

// Container runtime tests for DCGM connection-string (dcgmConnect_v3) remote DCGM
// support: tcp://, unix://, and vsock:// URIs. These run each image and scrape
// its metrics while exercising the URI connection strings that
// select dcgmConnect_v3 inside go-dcgm (a bare host:port uses the legacy
// dcgmConnect_v2 path and is covered elsewhere).
//
// The remote DCGM endpoint runs from DCGM_IMAGE, normally the matching DCGM
// image or the default ubuntu26.04 exporter image. The exporter side is
// validated against every configured exporter image, such as the shipped
// distroless variant. Both connect over the host network so no inter-container
// DNS is needed. Requires a GPU host and an exporter image built with VSOCK
// support (libdcgm.so.4 exports dcgmConnect_v3).
//
// Env:
//   - E2E_REQUIRE_VSOCK=1  fail (instead of skip) the vsock specs if /dev/vsock is
//     absent, so a pipeline that must prove VSOCK cannot go green by skipping.
//
// Run:
//
//	make -C tests/container container-test     # or:
//	DCGM_IMAGE=nvcr.io/nvidia/cloud-native/dcgm:<dcgm-ver>-1-ubuntu24.04 \
//	EXPORTER_DISTROLESS_IMAGE=nvidia/dcgm-exporter:<ver>-distroless \
//	  go test -tags container ./tests/container/ -run TestDockerImages \
//	  -args --ginkgo.focus="dcgmConnect_v3 URI schemes"
package container

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

const countersFile = "/etc/dcgm-exporter/dcp-metrics-included.csv"

var hostnameLabelRE = regexp.MustCompile(`hostname="([^"]*)"`)

// vsockDockerArgs returns the docker run flags required for AF_VSOCK inside a
// container: the vsock device plus seccomp=unconfined. Docker's default seccomp
// profile blocks the AF_VSOCK socket family, so both the hostengine (server) and
// the exporter (client) containers need this to use vsock.
func vsockDockerArgs() []string {
	return []string{"--net", "host", "--device", "/dev/vsock", "--security-opt", "seccomp=unconfined"}
}

// requireVsockOrSkip is called only by the vsock specs: it skips when /dev/vsock
// is absent so the tcp/unix specs still run, but fails when E2E_REQUIRE_VSOCK=1 — so a
// pipeline that must prove VSOCK cannot go green by silently skipping vsock. It is
// deliberately NOT used in BeforeEach/BeforeSuite, which would abort tcp/unix too.
func requireVsockOrSkip() {
	if _, err := os.Stat("/dev/vsock"); err == nil {
		return
	}
	if os.Getenv("E2E_REQUIRE_VSOCK") == "1" {
		Fail("/dev/vsock not present but E2E_REQUIRE_VSOCK=1; run: sudo modprobe vsock_loopback")
	}
	Skip("/dev/vsock not present; run: sudo modprobe vsock_loopback")
}

var (
	allocatedPortsMu sync.Mutex
	allocatedPorts   = map[int]struct{}{}

	allocatedVsockPortsMu sync.Mutex
	allocatedVsockPort    = 38999
)

// mustFreePort returns a free TCP port this helper has not already handed out in
// the current process. getFreePort alone can re-hand a just-closed port, which
// would collide when a spec needs several ports (e.g. the hostengine port and the
// exporter's HTTP port, which share the TCP namespace under --net host).
func mustFreePort() int {
	allocatedPortsMu.Lock()
	defer allocatedPortsMu.Unlock()
	for {
		port, err := getFreePort()
		Expect(err).NotTo(HaveOccurred(), "should find a free port")
		if _, used := allocatedPorts[port]; used {
			continue
		}
		allocatedPorts[port] = struct{}{}
		return port
	}
}

// mustVsockPort returns a deterministic VSOCK port from the test-owned range.
// VSOCK ports do not share the host TCP namespace, and GB200 nv-hostengine
// startup is unreliable with arbitrary ephemeral TCP ports used as VSOCK ports.
func mustVsockPort() int {
	allocatedVsockPortsMu.Lock()
	defer allocatedVsockPortsMu.Unlock()

	allocatedVsockPort++
	if allocatedVsockPort > 39199 {
		allocatedVsockPort = 39000
	}
	return allocatedVsockPort
}

// startHostEngineContainer starts nv-hostengine from a DCGM-capable image with
// the given transport args and waits until it reports ready.
func startHostEngineContainer(ctx context.Context, image string, dockerArgs []string, heArgs ...string) string {
	var lastDiagnostics string
	for attempt := 1; attempt <= 2; attempt++ {
		id := startHostEngineContainerAttempt(ctx, image, dockerArgs, heArgs...)
		logs, ready := waitForHostEngineReady(ctx, id)
		if ready {
			DeferCleanup(func(ctx context.Context) { _ = cleanupContainer(ctx, id) })
			return id
		}

		state := inspectContainerState(ctx, id)
		lastDiagnostics = strings.TrimSpace(fmt.Sprintf("%s\n%s", state, logs))
		_ = cleanupContainer(ctx, id)
		if attempt < 2 {
			By(fmt.Sprintf("Hostengine did not report ready; retrying startup (state/logs: %s)", lastDiagnostics))
			time.Sleep(2 * time.Second)
		}
	}

	Fail(fmt.Sprintf("hostengine should start; last container state/logs:\n%s", lastDiagnostics))
	return ""
}

// startVsockHostEngineContainer starts nv-hostengine on a fresh VSOCK port. Some
// GB200/DCGM combinations do not emit the usual readiness log line for VSOCK
// mode, so readiness is proven by the exporter scrape instead of container logs.
func startVsockHostEngineContainer(ctx context.Context, image string) int {
	var lastDiagnostics string
	for attempt := 1; attempt <= 3; attempt++ {
		port := mustVsockPort()
		id := startHostEngineContainerAttempt(ctx, image, vsockDockerArgs(),
			"-c", "1", "-p", fmt.Sprintf("%d", port))
		time.Sleep(time.Second)
		if containerIsRunning(ctx, id) {
			DeferCleanup(func(ctx context.Context) { _ = cleanupContainer(ctx, id) })
			return port
		}

		logs, _ := containerLogsCombined(ctx, id)
		state := inspectContainerState(ctx, id)
		lastDiagnostics = strings.TrimSpace(fmt.Sprintf("port=%d\n%s\n%s", port, state, logs))
		_ = cleanupContainer(ctx, id)
		if attempt < 3 {
			By(fmt.Sprintf("VSOCK hostengine did not report ready; retrying with a fresh port (state/logs: %s)", lastDiagnostics))
			time.Sleep(2 * time.Second)
		}
	}

	Fail(fmt.Sprintf("vsock hostengine should start; last container state/logs:\n%s", lastDiagnostics))
	return 0
}

// startHostEngineContainerAttempt starts one nv-hostengine container and returns its ID.
func startHostEngineContainerAttempt(ctx context.Context, image string, dockerArgs []string, heArgs ...string) string {
	name := fmt.Sprintf("dcgm-exporter-test-he-%d", time.Now().UnixNano())

	runArgs := []string{"run", "-d", "--name", name, "--gpus", "all", "--privileged"}
	runArgs = append(runArgs, dockerArgs...)
	// -n keeps nv-hostengine in the foreground so the container stays alive.
	runArgs = append(runArgs, "--entrypoint", "nv-hostengine", image, "-n")
	runArgs = append(runArgs, heArgs...)

	By(fmt.Sprintf("Starting hostengine container (%v)", heArgs))
	out, err := exec.CommandContext(ctx, "docker", runArgs...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "start hostengine container: %s", out)
	id := strings.TrimSpace(string(out))
	return id
}

// waitForHostEngineReady polls Docker logs until nv-hostengine reports readiness.
func waitForHostEngineReady(ctx context.Context, id string) (string, bool) {
	By("Waiting for hostengine to report ready")
	deadline := time.Now().Add(startupTimeout)
	var logs string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return logs, false
		default:
		}

		currentLogs, err := containerLogsCombined(ctx, id)
		if err == nil {
			logs = currentLogs
			if strings.Contains(logs, "Started host engine") {
				return logs, true
			}
		}
		if !containerIsRunning(ctx, id) {
			return logs, false
		}
		time.Sleep(time.Second)
	}
	return logs, false
}

// containerLogsCombined returns both stdout and stderr of a container. The
// exporter logs to stderr, which `docker logs` writes to its own stderr, so
// the stdout-only getContainerLogs helper would miss it.
func containerLogsCombined(ctx context.Context, id string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "logs", id).CombinedOutput()
	return string(out), err
}

// inspectContainerState returns the Docker state line for startup diagnostics.
func inspectContainerState(ctx context.Context, id string) string {
	out, err := exec.CommandContext(ctx, "docker", "inspect", id,
		"--format", "running={{.State.Running}} exit={{.State.ExitCode}} error={{.State.Error}} oom={{.State.OOMKilled}}").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("inspect failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}

// runExporterContainer starts the exporter image with the given docker and
// exporter args. Unlike startContainer it does not fail if the container exits
// quickly, so it can also be used by the negative scenarios.
func runExporterContainer(ctx context.Context, image string, dockerArgs []string, exporterArgs ...string) string {
	name := fmt.Sprintf("dcgm-exporter-test-exp-%d", time.Now().UnixNano())

	// --uts=host shares the host UTS namespace so os.Hostname() inside the exporter
	// equals the test host's hostname, making the assertHostnameLabel equality an
	// explicit contract rather than relying on Docker's --net host hostname defaulting.
	runArgs := []string{"run", "-d", "--name", name, "--uts", "host", "--gpus", "all", "--privileged"}
	runArgs = append(runArgs, dockerArgs...)
	runArgs = append(runArgs, image)
	runArgs = append(runArgs, exporterArgs...)

	By(fmt.Sprintf("Starting exporter container (%v)", exporterArgs))
	out, err := exec.CommandContext(ctx, "docker", runArgs...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "start exporter container: %s", out)
	id := strings.TrimSpace(string(out))
	DeferCleanup(func(ctx context.Context) { _ = cleanupContainer(ctx, id) })
	return id
}

// fetchValidMetrics polls /metrics until it returns valid Prometheus data with
// at least one real DCGM GPU metric family, and returns the body.
func fetchValidMetrics(ctx context.Context, port int) string {
	var body string
	Eventually(ctx, func(ctx context.Context) error {
		resp, err := (&http.Client{Timeout: httpClientTimeout}).Get(localHTTPURL(port, "metrics"))
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status: %d", resp.StatusCode)
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read failed: %w", err)
		}
		families, err := metriccontract.ParseText(raw)
		if err != nil {
			return fmt.Errorf("parse metrics: %w", err)
		}
		if err := metriccontract.ValidateAtLeastOneDCGMGPUFamily(families); err != nil {
			return err
		}
		body = string(raw)
		return nil
	}).WithTimeout(metricsTimeout).WithPolling(time.Second).Should(Succeed(),
		"exporter should serve real DCGM GPU metrics over the remote DCGM endpoint")
	return body
}

// assertHostnameLabel proves the hostname label is present, non-empty, not a raw
// connection URI, and equals the local hostname. The exporter containers run with
// --uts=host (see runExporterContainer), so os.Hostname() inside the container
// equals the test host's hostname — making the equality an explicit contract.
func assertHostnameLabel(body string) {
	m := hostnameLabelRE.FindStringSubmatch(body)
	Expect(m).NotTo(BeNil(), "metrics must include a hostname label")
	hostname := m[1]
	Expect(hostname).NotTo(BeEmpty(), "hostname label must be non-empty")
	Expect(hostname).NotTo(MatchRegexp(`(?i)^(vsock|tcp|unix)://`),
		"hostname label must not be a raw connection URI")
	if local, err := os.Hostname(); err == nil && local != "" {
		Expect(hostname).To(Equal(local), "hostname label should be the local (host) hostname")
	}
}

// validateRemoteExporter runs one exporter image against a remote DCGM URI
// and asserts it serves GPU metrics with a correct, non-leaked hostname label.
func validateRemoteExporter(ctx context.Context, img ImageInfo, dockerArgs []string, uri string) {
	port := mustFreePort()
	runExporterContainer(ctx, img.FullName, dockerArgs,
		"-r", uri, "-a", fmt.Sprintf(":%d", port), "-f", countersFile)
	assertHostnameLabel(fetchValidMetrics(ctx, port))
}

// validateExporterImagesWithArgs runs every configured exporter image with the
// provided exporter arguments and asserts it serves valid remote DCGM metrics.
func validateExporterImagesWithArgs(ctx context.Context, dockerArgs []string, exporterArgs ...string) {
	ran := 0
	for _, img := range testConfig.Images {
		exists, err := imageExists(ctx, img.FullName)
		Expect(err).NotTo(HaveOccurred())
		if !exists {
			continue
		}
		By(fmt.Sprintf("Validating exporter image [%s]", img.Variant))
		port := mustFreePort()
		args := append([]string{}, exporterArgs...)
		args = append(args, "-a", fmt.Sprintf(":%d", port), "-f", countersFile)
		runExporterContainer(ctx, img.FullName, dockerArgs, args...)
		assertHostnameLabel(fetchValidMetrics(ctx, port))
		ran++
	}
	Expect(ran).To(BeNumerically(">", 0), "no configured exporter images available; run 'make local'")
}

// validateExporterImages runs validateRemoteExporter for every configured image
// that exists locally, so each exporter image variant is exercised.
func validateExporterImages(ctx context.Context, dockerArgs []string, uri string) {
	validateExporterImagesWithArgs(ctx, dockerArgs, "-r", uri)
}

// expectExporterFailsToConnect asserts one exporter image exits instead of
// serving metrics and logs a connection failure for an unreachable DCGM URI.
func expectExporterFailsToConnect(ctx context.Context, img ImageInfo, dockerArgs []string, uri string) {
	id := runExporterContainer(ctx, img.FullName, dockerArgs,
		"-r", uri, "-a", fmt.Sprintf(":%d", mustFreePort()), "-f", countersFile)

	By("Exporter should exit instead of serving metrics")
	Eventually(ctx, func(ctx context.Context) bool {
		return containerIsRunning(ctx, id)
	}).WithTimeout(startupTimeout).WithPolling(time.Second).Should(BeFalse(),
		"exporter must not stay up when the DCGM endpoint is unreachable")

	// Primary signal is the behavior above (exporter exits, serves nothing). The
	// log check is loosened to stable tokens so it tolerates message rewording
	// rather than coupling to an exact phrase.
	logs, err := containerLogsCombined(ctx, id)
	Expect(err).NotTo(HaveOccurred())
	Expect(logs).To(MatchRegexp(`(?i)connect.*hostengine`),
		"logs should indicate a hostengine connection failure")
}

// expectExporterImagesFailToConnect runs every configured exporter image against
// an unreachable remote DCGM URI and verifies each image exits cleanly.
func expectExporterImagesFailToConnect(ctx context.Context, dockerArgs []string, uri string) {
	ran := 0
	for _, img := range testConfig.Images {
		exists, err := imageExists(ctx, img.FullName)
		Expect(err).NotTo(HaveOccurred())
		if !exists {
			continue
		}
		By(fmt.Sprintf("Validating exporter image [%s] failure path", img.Variant))
		expectExporterFailsToConnect(ctx, img, dockerArgs, uri)
		ran++
	}
	Expect(ran).To(BeNumerically(">", 0), "no configured exporter images available; run 'make local'")
}

var _ = Describe("Remote DCGM dcgmConnect_v3 URI schemes", Serial, Label("remoteDcgmUri"), func() {
	var dcgmImage string

	BeforeEach(func(ctx context.Context) {
		dcgmImage = testConfig.DCGMImage
		if dcgmImage == "" {
			Skip("DCGM_IMAGE is not configured")
		}
		exists, err := imageExists(ctx, dcgmImage)
		Expect(err).NotTo(HaveOccurred())
		if !exists {
			Skip(fmt.Sprintf("DCGM image not found: %s", dcgmImage))
		}
	})

	It("connects over a tcp:// URI", func(ctx context.Context) {
		hePort := mustFreePort()
		startHostEngineContainer(ctx, dcgmImage, []string{"--net", "host"},
			"-b", "127.0.0.1", "-p", fmt.Sprintf("%d", hePort))

		validateExporterImages(ctx, []string{"--net", "host"},
			fmt.Sprintf("tcp://127.0.0.1:%d", hePort))
	})

	It("connects over a unix:// URI", func(ctx context.Context) {
		sockDir, err := os.MkdirTemp("", "dcgm-he-")
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Chmod(sockDir, 0o777)).To(Succeed())
		DeferCleanup(func() { _ = os.RemoveAll(sockDir) })
		socket := sockDir + "/he.sock"

		mount := []string{"--net", "host", "-v", fmt.Sprintf("%s:%s", sockDir, sockDir)}
		startHostEngineContainer(ctx, dcgmImage, mount, "-d", socket)

		validateExporterImages(ctx, mount, "unix://"+socket)
	})

	It("connects over a vsock:// URI", func(ctx context.Context) {
		requireVsockOrSkip()
		hePort := startVsockHostEngineContainer(ctx, dcgmImage)

		validateExporterImages(ctx, vsockDockerArgs(), fmt.Sprintf("vsock://1:%d", hePort))
	})

	It("honors DCGM_REMOTE_HOSTENGINE_INFO with a vsock:// URI", func(ctx context.Context) {
		requireVsockOrSkip()
		hePort := startVsockHostEngineContainer(ctx, dcgmImage)

		dockerArgs := append(vsockDockerArgs(),
			"-e", fmt.Sprintf("DCGM_REMOTE_HOSTENGINE_INFO=vsock://1:%d", hePort))
		validateExporterImagesWithArgs(ctx, dockerArgs)
	})

	DescribeTable("fails gracefully when the DCGM endpoint is unreachable",
		func(ctx context.Context, scheme string) {
			switch scheme {
			case "tcp":
				expectExporterImagesFailToConnect(ctx, []string{"--net", "host"},
					fmt.Sprintf("tcp://127.0.0.1:%d", mustFreePort()))
			case "unix":
				expectExporterImagesFailToConnect(ctx, []string{"--net", "host"},
					fmt.Sprintf("unix:///tmp/dcgm-missing-%d.sock", mustFreePort()))
			case "vsock":
				requireVsockOrSkip()
				expectExporterImagesFailToConnect(ctx, vsockDockerArgs(),
					fmt.Sprintf("vsock://1:%d", mustFreePort()))
			}
		},
		Entry("dead tcp endpoint", "tcp"),
		Entry("missing unix socket", "unix"),
		Entry("unreachable vsock", "vsock"),
	)
})
