//go:build docker

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

package docker

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

func dockerAvailable() bool {
	cmd := exec.Command("docker", "version")
	err := cmd.Run()
	return err == nil
}

// getFreePort finds and returns an available TCP port
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
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

func imageExists(ctx context.Context, imageName string) (bool, error) {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", imageName)
	fmt.Printf("→ Checking image: %s\n", imageName)
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			fmt.Printf("  ✗ Image not found\n")
			return false, nil
		}
		return false, err
	}
	fmt.Printf("  ✓ Image exists\n")
	return true, nil
}

func startContainer(ctx context.Context, imageName string, port int) (string, error) {
	containerName := fmt.Sprintf("dcgm-exporter-test-%d", time.Now().UnixNano())

	fmt.Printf("→ Starting container: %s\n", imageName)
	fmt.Printf("  docker run -d --gpus all --cap-add SYS_ADMIN --name %s -p %d:9400 %s\n", containerName, port, imageName)
	fmt.Printf("  🔍 DCGM debug logging enabled\n")

	cmd := exec.CommandContext(ctx, "docker", "run", "-d",
		"--gpus", "all",
		"--cap-add", "SYS_ADMIN",
		"-e", "DCGM_EXPORTER_DEBUG=true", // Enable dcgm-exporter debug output
		"-e", "DCGM_EXPORTER_ENABLE_DCGM_LOG=true", // Enable DCGM logs to stdout
		"-e", "DCGM_EXPORTER_DCGM_LOG_LEVEL=DEBUG", // Set DCGM log level to DEBUG
		"--name", containerName,
		"-p", fmt.Sprintf("%d:9400", port),
		imageName)

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

func containerIsRunning(ctx context.Context, containerID string) bool {
	cmd := exec.CommandContext(ctx, "docker", "inspect",
		"-f", "{{.State.Running}}", containerID)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "true"
}

func getContainerLogs(ctx context.Context, containerID string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "logs", containerID)
	output, err := cmd.Output()
	return string(output), err
}

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

func cleanupContainer(ctx context.Context, containerID string) error {
	fmt.Printf("→ Cleaning up container: %s\n", containerID[:12])
	_ = exec.CommandContext(ctx, "docker", "stop", "-t", "5", containerID).Run()
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", containerID)
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
		cmd := exec.CommandContext(ctx, "nvidia-smi", "--query-compute-apps=pid", "--format=csv,noheader")
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
