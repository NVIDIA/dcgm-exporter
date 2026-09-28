//go:build container

/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const localLoopbackHost = "127.0.0.1"

// dockerAvailable reports whether the Docker CLI can reach a running daemon.
func dockerAvailable() bool {
	cmd := exec.Command("docker", "version")
	err := cmd.Run()
	return err == nil
}

// getFreePort finds and returns an available TCP port.
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(localLoopbackHost, "0"))
	if err != nil {
		return 0, err
	}

	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port, nil
}

func localHTTPURL(port int, path string) string {
	return fmt.Sprintf("http://%s/%s", net.JoinHostPort(localLoopbackHost, fmt.Sprintf("%d", port)), strings.TrimPrefix(path, "/"))
}

// imageExists checks whether a container image is already present locally.
func imageExists(ctx context.Context, imageName string) (bool, error) {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", imageName)
	fmt.Printf("→ Checking image: %s\n", imageName)
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			if os.Getenv("E2E_REQUIRE_CONTAINER_IMAGES") != "1" {
				fmt.Printf("  ✗ Image not found\n")
				return false, nil
			}
			return pullImage(ctx, imageName)
		}
		return false, err
	}
	fmt.Printf("  ✓ Image exists\n")
	return true, nil
}

func pullImage(ctx context.Context, imageName string) (bool, error) {
	fmt.Printf("  → Pulling required image\n")
	cmd := exec.CommandContext(ctx, "docker", "pull", imageName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("  ✗ Image pull failed\n")
		return false, fmt.Errorf("pull image %s: %w (output: %s)", imageName, err, strings.TrimSpace(string(output)))
	}
	fmt.Printf("  ✓ Image pulled\n")
	return true, nil
}

// runLshwJSON runs the distroless image's lshw binary and returns its JSON output.
func runLshwJSON(ctx context.Context, imageName string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"--entrypoint", "/usr/bin/lshw",
		imageName,
		"-json")

	stdout, err := cmd.Output()
	stderr := ""
	if exitErr, ok := err.(*exec.ExitError); ok {
		stderr = string(exitErr.Stderr)
	}
	if err != nil {
		return string(stdout), fmt.Errorf("failed to run /usr/bin/lshw -json: %w (stderr: %s)",
			err, strings.TrimSpace(stderr))
	}

	return string(stdout), nil
}

// startContainer starts a dcgm-exporter container with GPU access on a host port.
func startContainer(ctx context.Context, imageName string, port int) (string, error) {
	containerName := fmt.Sprintf("dcgm-exporter-test-%d", time.Now().UnixNano())

	fmt.Printf("→ Starting container: %s\n", imageName)
	fmt.Printf("  docker run -d --gpus all --privileged --net host --name %s %s -a :%d\n", containerName, imageName, port)

	cmd := exec.CommandContext(ctx, "docker", "run", "-d",
		"--gpus", "all",
		"--privileged",
		"--net", "host",
		"-e", "DCGM_EXPORTER_DEBUG=true", // Enable dcgm-exporter debug output
		"--name", containerName,
		imageName,
		"-a", fmt.Sprintf(":%d", port))

	output, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		fmt.Printf("  ✗ Failed to start: %s\n", stderr)
		return "", fmt.Errorf("failed to start container: %w (stderr: %s)", err, stderr)
	}

	containerID := strings.TrimSpace(string(output))
	fmt.Printf("  ✓ Container started: %s\n", containerID[:12])

	// Wait a moment and check if container is still running (quick failure detection)
	time.Sleep(2 * time.Second)
	if !containerIsRunning(ctx, containerID) {
		logs, _ := getContainerLogs(ctx, containerID)
		fmt.Printf("  ✗ Container exited immediately!\n")
		fmt.Printf("  📋 Container logs:\n%s\n", logs)
		return containerID, fmt.Errorf("container exited immediately after start")
	}

	return containerID, nil
}

// containerIsRunning reports whether Docker still considers the container active.
func containerIsRunning(ctx context.Context, containerID string) bool {
	cmd := exec.CommandContext(ctx, "docker", "inspect",
		"-f", "{{.State.Running}}", containerID)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "true"
}

// getContainerLogs returns the stdout stream from a container's Docker logs.
func getContainerLogs(ctx context.Context, containerID string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "logs", containerID)
	output, err := cmd.Output()
	return string(output), err
}

// stopContainer stops a container with the requested graceful shutdown timeout.
func stopContainer(ctx context.Context, containerID string, timeout time.Duration) error {
	timeoutSecs := int(timeout.Seconds())
	fmt.Printf("→ Stopping container: %s (timeout: %ds)\n", containerID[:12], timeoutSecs)
	cmd := exec.CommandContext(ctx, "docker", "stop", "-t", fmt.Sprintf("%d", timeoutSecs), containerID)
	err := cmd.Run()
	if err != nil {
		fmt.Printf("  ✗ Failed to stop\n")
	} else {
		fmt.Printf("  ✓ Container stopped\n")
	}
	return err
}

// cleanupContainer removes one test container and waits briefly for GPU handles to drain.
func cleanupContainer(_ context.Context, containerID string) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("→ Cleaning up container: %s\n", containerID[:12])
	_ = exec.CommandContext(cleanupCtx, "docker", "stop", "-t", "5", containerID).Run()
	cmd := exec.CommandContext(cleanupCtx, "docker", "rm", "-f", containerID)
	err := cmd.Run()
	if err != nil {
		fmt.Printf("  ✗ Failed to cleanup\n")
		return err
	}

	fmt.Printf("  ✓ Container removed\n")
	fmt.Printf("  ⏳ Waiting for GPU to be released...\n")

	// Wait for GPU to be fully released by checking nvidia-smi
	// This ensures DCGM has released the GPU before the next test
	gpuReleased := false
	for i := 0; i < 10; i++ { // Try for up to 10 seconds
		cmd := exec.CommandContext(cleanupCtx, "nvidia-smi", "--query-compute-apps=pid", "--format=csv,noheader")
		output, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(output))) == 0 {
			gpuReleased = true
			fmt.Printf("  ✓ GPU released\n")
			break
		}
		time.Sleep(1 * time.Second)
	}

	if !gpuReleased {
		fmt.Printf("  ⚠ GPU may still have processes, continuing anyway\n")
	}

	return nil
}

// cleanupTestContainers removes stale dcgm-exporter test containers before or after the suite.
func cleanupTestContainers(ctx context.Context) {
	fmt.Printf("→ Cleaning up leftover dcgm-exporter-test containers...\n")

	// First, stop all test containers
	_ = exec.CommandContext(ctx, "sh", "-c",
		"docker ps --filter 'name=dcgm-exporter-test-' --format '{{.ID}}' | xargs -r docker stop -t 5").Run()

	// Then remove them
	cmd := exec.CommandContext(ctx, "sh", "-c",
		"docker ps -a --filter 'name=dcgm-exporter-test-' --format '{{.ID}}' | xargs -r docker rm -f")
	output, err := cmd.CombinedOutput()
	if err == nil && len(output) > 0 {
		fmt.Printf("  ✓ Removed containers: %s\n", strings.TrimSpace(string(output)))

		// Wait for GPU to be released
		fmt.Printf("  ⏳ Waiting for GPU to be released...\n")
		for i := 0; i < 10; i++ {
			cmd := exec.CommandContext(ctx, "nvidia-smi", "--query-compute-apps=pid", "--format=csv,noheader")
			output, err := cmd.Output()
			if err == nil && len(strings.TrimSpace(string(output))) == 0 {
				fmt.Printf("  ✓ GPU released\n")
				return
			}
			time.Sleep(1 * time.Second)
		}
	}
}
