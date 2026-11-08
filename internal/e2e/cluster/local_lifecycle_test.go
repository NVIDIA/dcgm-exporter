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

package cluster

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

func TestEnsureNodeImageBuildsFromMirroredK3SRepository(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + cfg.NodeImage + " --format={{index .Config.Labels \"" + containerToolkitVersionLabel + "\"}}": {ExitCode: 1, Stderr: []byte("no such image")},
	}}

	if err := ensureNodeImage(context.Background(), runner, io.Discard, cfg); err != nil {
		t.Fatalf("ensureNodeImage() error = %v", err)
	}
	if !runner.has("docker", "buildx", "version") {
		t.Fatalf("node image build did not verify Docker Buildx: %#v", runner.commands)
	}
	if !runner.has("docker", "buildx", "build", "--load") {
		t.Fatalf("node image build did not use buildx --load: %#v", runner.commands)
	}
	if !runner.has("docker", "--build-arg", "K3S_IMAGE=mirror.example/rancher/k3s:v1.35.3-k3s1") {
		t.Fatalf("Docker Buildx build missing mirrored K3S_IMAGE build arg: %#v", runner.commands)
	}
	if !runner.has("docker", "--build-arg", "NVIDIA_CONTAINER_TOOLKIT_VERSION=1.19.1-1") {
		t.Fatalf("Docker Buildx build missing pinned toolkit build arg: %#v", runner.commands)
	}
}

func TestEnsureNodeImageRequiresBuildxWhenImageIsMissing(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + cfg.NodeImage + " --format={{index .Config.Labels \"" + containerToolkitVersionLabel + "\"}}": {ExitCode: 1, Stderr: []byte("no such image")},
		"docker buildx version": {ExitCode: 1, Stderr: []byte("docker: 'buildx' is not a docker command")},
	}}

	err := ensureNodeImage(context.Background(), runner, io.Discard, cfg)
	if err == nil || !strings.Contains(err.Error(), "requires Docker Buildx") {
		t.Fatalf("ensureNodeImage() error = %v, want Buildx prerequisite error", err)
	}
	if runner.has("docker", "buildx", "build") {
		t.Fatalf("node image build ran without Buildx: %#v", runner.commands)
	}
}

func TestEnsureNodeImageDoesNotRequireBuildxWhenImageExists(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	runner := &recordingRunner{outputs: map[string]string{
		"docker image inspect " + cfg.NodeImage + " --format={{index .Config.Labels \"" + containerToolkitVersionLabel + "\"}}": cfg.ContainerToolkitVersion,
	}}

	if err := ensureNodeImage(context.Background(), runner, io.Discard, cfg); err != nil {
		t.Fatalf("ensureNodeImage() error = %v", err)
	}
	if runner.has("docker", "buildx") {
		t.Fatalf("cached node image unexpectedly required Buildx: %#v", runner.commands)
	}
}

func TestEnsureNodeImageRebuildsWhenToolkitVersionDoesNotMatch(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	runner := &recordingRunner{outputs: map[string]string{
		"docker image inspect " + cfg.NodeImage + " --format={{index .Config.Labels \"" + containerToolkitVersionLabel + "\"}}": "1.20.0-1",
	}}
	var out bytes.Buffer

	if err := ensureNodeImage(context.Background(), runner, &out, cfg); err != nil {
		t.Fatalf("ensureNodeImage() error = %v", err)
	}
	if !runner.has("docker", "buildx", "build") {
		t.Fatalf("mismatched node image was not rebuilt: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "has Container Toolkit \"1.20.0-1\", want \"1.19.1-1\"; rebuilding") {
		t.Fatalf("missing version mismatch diagnostic:\n%s", out.String())
	}
}

func TestCreateLocalClusterUsesWaitTimeout(t *testing.T) {
	runner := &clusterCreateTimeoutRunner{}
	cfg := defaultLocalConfigForTest(t)
	cfg.WaitTimeout = "1ms"

	err := createLocalCluster(context.Background(), runner, io.Discard, cfg, false)
	if err == nil || !strings.Contains(err.Error(), "k3d cluster creation timed out after 1ms") {
		t.Fatalf("createLocalCluster() error = %v, want bounded timeout", err)
	}
}

func TestCreateLocalClusterPreservesParentDeadlineAttribution(t *testing.T) {
	runner := &clusterCreateTimeoutRunner{}
	cfg := defaultLocalConfigForTest(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	err := createLocalCluster(ctx, runner, io.Discard, cfg, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("createLocalCluster() error = %v, want parent deadline", err)
	}
	if strings.Contains(err.Error(), "k3d cluster creation timed out after") {
		t.Fatalf("createLocalCluster() attributed parent deadline to the k3d timeout: %v", err)
	}
}

// clusterCreateTimeoutRunner blocks k3d cluster creation until its context ends, to check the create time limit.
type clusterCreateTimeoutRunner struct{}

// Run blocks a k3d command until its context ends and answers every other command at once.
func (*clusterCreateTimeoutRunner) Run(ctx context.Context, command e2eexec.Command) e2eexec.Result {
	if command.Name != "k3d" {
		return e2eexec.Result{}
	}
	select {
	case <-ctx.Done():
		return streamResult(command, e2eexec.Result{ExitCode: -1, Err: ctx.Err()})
	case <-time.After(time.Second):
		return streamResult(command, e2eexec.Result{ExitCode: -1, Err: errors.New("cluster create context was not bounded")})
	}
}

// TestLocalCgroupV1EnabledRejectsInvalidModeBeforeProbe verifies bad local configuration does not hide behind a host probe failure.
func TestLocalCgroupV1EnabledRejectsInvalidModeBeforeProbe(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"stat -fc %T /sys/fs/cgroup": {ExitCode: 1, Stderr: []byte("permission denied\n")},
	}}

	_, err := localCgroupV1Enabled(context.Background(), runner, io.Discard, LocalConfig{CgroupV1: "sometimes"})
	if err == nil || !strings.Contains(err.Error(), "unsupported local k3d cgroup v1 mode") {
		t.Fatalf("localCgroupV1Enabled() error = %v, want invalid mode error", err)
	}
	if runner.has("stat", "-fc", "%T", "/sys/fs/cgroup") {
		t.Fatalf("invalid mode ran the cgroup probe: %#v", runner.commands)
	}
}

func TestResolveLocalIPFamilyFallsBackForExistingIPv4Network(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.IPFamily = "auto"
	runner := &recordingRunner{
		results: map[string]e2eexec.Result{
			"docker network inspect " + cfg.NetworkName: {},
		},
		outputs: map[string]string{
			"docker network inspect " + cfg.NetworkName + " --format={{.EnableIPv6}}": "false\n",
		},
	}
	var out bytes.Buffer

	resolved, err := resolveLocalIPFamily(context.Background(), runner, &out, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.IPFamily != "ipv4" {
		t.Fatalf("resolveLocalIPFamily() IPFamily = %q, want ipv4", resolved.IPFamily)
	}
	if !strings.Contains(out.String(), "using IPv4 for the local k3d cluster") {
		t.Fatalf("missing IPv4 fallback explanation:\n%s", out.String())
	}
}

// TestEnsureDualStackNetworkSetsGateways verifies k3d receives a usable host network.
func TestEnsureDualStackNetworkSetsGateways(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"docker network inspect " + cfg.NetworkName: {
			ExitCode: 1,
			Stderr:   []byte("Error: No such network: " + cfg.NetworkName),
		},
	}}

	if err := ensureDualStackNetwork(context.Background(), runner, io.Discard, cfg); err != nil {
		t.Fatal(err)
	}
	if !runner.has("docker", "--gateway", cfg.DockerIPv4Gateway) ||
		!runner.has("docker", "--gateway", cfg.DockerIPv6Gateway) {
		t.Fatalf("dual-stack network command omitted a gateway: %#v", runner.commands)
	}
}
