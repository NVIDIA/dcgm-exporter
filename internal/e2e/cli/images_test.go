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

package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// TestCheckSelectedContainerImagesUsesLocalImage verifies a local image avoids a registry request and a pull.
func TestCheckSelectedContainerImagesUsesLocalImage(t *testing.T) {
	image := "registry.example/exporter:dev"
	runner := &fakeRunner{}
	requirements := []scenario.SelectedRequirement{{
		Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageExporter},
		RequiredBy:  []string{"container/imageStartup"},
	}}

	err := checkSelectedContainerImages(context.Background(), runner, config.TestOptions{ExporterImage: image}, requirements)
	if err != nil {
		t.Fatalf("checkSelectedContainerImages() error = %v", err)
	}
	if _, ok := runner.commandWithArgs("docker", "image", "inspect", image); !ok {
		t.Fatalf("local image inspection was not run: %#v", runner.commands)
	}
	if runner.hasCommandArg("manifest") || runner.hasCommandArg("pull") {
		t.Fatalf("local image check contacted the registry or pulled: %#v", runner.commands)
	}
}

func TestCheckSelectedContainerImagesUsesGPUOperatorDCGMImage(t *testing.T) {
	defaultImage := "registry.example/dcgm:default"
	operatorImage := "registry.example/dcgm:4.7.0"
	runner := &fakeRunner{}
	requirements := []scenario.SelectedRequirement{{
		Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageGPUOperatorDCGM},
		RequiredBy:  []string{"k8s/gpuOperatorMainRemote"},
	}}

	err := checkSelectedContainerImages(context.Background(), runner, config.TestOptions{
		DCGMImage:            defaultImage,
		GPUOperatorDCGMImage: operatorImage,
	}, requirements)
	if err != nil {
		t.Fatalf("checkSelectedContainerImages() error = %v", err)
	}
	if _, ok := runner.commandWithArgs("docker", "image", "inspect", operatorImage); !ok {
		t.Fatalf("GPU Operator DCGM image inspection was not run: %#v", runner.commands)
	}
	if _, ok := runner.commandWithArgs("docker", "image", "inspect", defaultImage); ok {
		t.Fatalf("default DCGM image was inspected instead of GPU Operator DCGM image: %#v", runner.commands)
	}
}

func TestConfiguredContainerImageUsesGPUOperatorDCGMDefault(t *testing.T) {
	image, err := configuredContainerImage(config.TestOptions{}, scenario.ContainerImageGPUOperatorDCGM)
	if err != nil {
		t.Fatalf("configuredContainerImage() error = %v", err)
	}
	if image != cluster.DefaultGPUOperatorMainDCGMImage {
		t.Fatalf("GPU Operator DCGM image = %q, want %q", image, cluster.DefaultGPUOperatorMainDCGMImage)
	}
}

// TestCheckSelectedContainerImagesUsesManifestWithoutPull verifies a missing local image is checked at the registry without downloading it.
func TestCheckSelectedContainerImagesUsesManifestWithoutPull(t *testing.T) {
	image := "registry.example/exporter:dev"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + image:    {ExitCode: 1, Stderr: []byte("no such image")},
		"docker manifest inspect " + image: {Stdout: hostPlatformManifest()},
	}}
	requirements := []scenario.SelectedRequirement{{
		Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageExporter},
		RequiredBy:  []string{"container/imageStartup"},
	}}

	err := checkSelectedContainerImages(context.Background(), runner, config.TestOptions{ExporterImage: image}, requirements)
	if err != nil {
		t.Fatalf("checkSelectedContainerImages() error = %v", err)
	}
	if _, ok := runner.commandWithArgs("docker", "manifest", "inspect", image); !ok {
		t.Fatalf("registry manifest inspection was not run: %#v", runner.commands)
	}
	local := runner.commandIndex("docker", "image", "inspect", image)
	remote := runner.commandIndex("docker", "manifest", "inspect", image)
	if local < 0 || remote < 0 || local > remote {
		t.Fatalf("image checks ran in the wrong order: %#v", runner.commands)
	}
	if runner.hasCommandArg("pull") {
		t.Fatalf("registry image check pulled an image: %#v", runner.commands)
	}
}

// TestCheckSelectedContainerImagesRejectsUnsupportedHostPlatform verifies a remote image index fails before a scenario pulls it.
func TestCheckSelectedContainerImagesRejectsUnsupportedHostPlatform(t *testing.T) {
	image := "registry.example/dcgm:dev"
	unsupportedArchitecture := "amd64"
	if runtime.GOARCH == unsupportedArchitecture {
		unsupportedArchitecture = "arm64"
	}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + image: {ExitCode: 1, Stderr: []byte("local image missing")},
		"docker manifest inspect " + image: {
			Stdout: []byte(`{"manifests":[{"platform":{"os":"` + runtime.GOOS + `","architecture":"` + unsupportedArchitecture + `"}}]}`),
		},
	}}
	requirements := []scenario.SelectedRequirement{{
		Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageDCGM},
		RequiredBy:  []string{"container/remoteDcgmUri"},
	}}

	err := checkSelectedContainerImages(context.Background(), runner, config.TestOptions{DCGMImage: image}, requirements)
	if err == nil {
		t.Fatal("checkSelectedContainerImages() error = nil")
	}
	for _, want := range []string{"local image missing", "image is not published for " + runtime.GOOS + "/" + runtime.GOARCH} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("checkSelectedContainerImages() error = %v, missing %q", err, want)
		}
	}
	if runner.hasCommandArg("pull") {
		t.Fatalf("unsupported platform check pulled an image: %#v", runner.commands)
	}
}

// TestCheckSelectedContainerImagesAcceptsSingleManifest verifies a non-index manifest is accepted without a pull.
func TestCheckSelectedContainerImagesAcceptsSingleManifest(t *testing.T) {
	image := "registry.example/exporter:dev"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + image:    {ExitCode: 1, Stderr: []byte("no such image")},
		"docker manifest inspect " + image: {Stdout: []byte(`{"schemaVersion":2}`)},
	}}
	requirements := []scenario.SelectedRequirement{{
		Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageExporter},
		RequiredBy:  []string{"container/imageStartup"},
	}}

	if err := checkSelectedContainerImages(context.Background(), runner, config.TestOptions{ExporterImage: image}, requirements); err != nil {
		t.Fatalf("checkSelectedContainerImages() error = %v", err)
	}
	if runner.hasCommandArg("pull") {
		t.Fatalf("single manifest check pulled an image: %#v", runner.commands)
	}
}

// TestCheckSelectedContainerImagesRejectsEmptyManifestList verifies an empty manifest list does not pass as a single-image manifest.
func TestCheckSelectedContainerImagesRejectsEmptyManifestList(t *testing.T) {
	image := "registry.example/exporter:dev"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + image:    {ExitCode: 1, Stderr: []byte("no such image")},
		"docker manifest inspect " + image: {Stdout: []byte(`{"manifests":[]}`)},
	}}
	requirements := []scenario.SelectedRequirement{{
		Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageExporter},
		RequiredBy:  []string{"container/imageStartup"},
	}}

	err := checkSelectedContainerImages(context.Background(), runner, config.TestOptions{ExporterImage: image}, requirements)
	if err == nil {
		t.Fatal("checkSelectedContainerImages() error = nil")
	}
	if !strings.Contains(err.Error(), "image manifest list contains no platform entries") {
		t.Fatalf("checkSelectedContainerImages() error = %v", err)
	}
}

// TestCheckSelectedContainerImagesKeepsMalformedManifestDetails verifies a bad registry response keeps Docker evidence.
func TestCheckSelectedContainerImagesKeepsMalformedManifestDetails(t *testing.T) {
	image := "registry.example/exporter:dev"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + image:    {ExitCode: 1, Stderr: []byte("no such image")},
		"docker manifest inspect " + image: {Stdout: []byte("not JSON")},
	}}
	requirements := []scenario.SelectedRequirement{{
		Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageExporter},
		RequiredBy:  []string{"container/imageStartup"},
	}}

	err := checkSelectedContainerImages(context.Background(), runner, config.TestOptions{ExporterImage: image}, requirements)
	if err == nil {
		t.Fatal("checkSelectedContainerImages() error = nil")
	}
	for _, want := range []string{"no such image", "not JSON", "decode manifest JSON"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("checkSelectedContainerImages() error = %v, missing %q", err, want)
		}
	}
}

// TestCheckSelectedContainerImagesKeepsFailureDetails verifies an unavailable image reports its name, users, and both Docker errors.
func TestCheckSelectedContainerImagesKeepsFailureDetails(t *testing.T) {
	image := "registry.example/dcgm:dev"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + image:    {ExitCode: 1, Stderr: []byte("local image missing")},
		"docker manifest inspect " + image: {ExitCode: 1, Stderr: []byte("registry denied")},
	}}
	requirements := []scenario.SelectedRequirement{{
		Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageDCGM},
		RequiredBy:  []string{"container/remoteDcgmUri", "k8s/remoteDcgm"},
	}}

	err := checkSelectedContainerImages(context.Background(), runner, config.TestOptions{DCGMImage: image}, requirements)
	if err == nil {
		t.Fatal("checkSelectedContainerImages() error = nil")
	}
	for _, want := range []string{image, "container/remoteDcgmUri, k8s/remoteDcgm", "local image missing", "registry denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("checkSelectedContainerImages() error = %v, missing %q", err, want)
		}
	}
}

// TestCheckSelectedContainerImagesChecksEachImageOnce verifies duplicate selected requirements do not repeat Docker calls.
func TestCheckSelectedContainerImagesChecksEachImageOnce(t *testing.T) {
	image := "registry.example/exporter:dev"
	runner := &fakeRunner{}
	requirements := []scenario.SelectedRequirement{
		{
			Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageExporter},
			RequiredBy:  []string{"container/imageStartup"},
		},
		{
			Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageExporter},
			RequiredBy:  []string{"k8s/default"},
		},
	}

	err := checkSelectedContainerImages(context.Background(), runner, config.TestOptions{ExporterImage: image}, requirements)
	if err != nil {
		t.Fatalf("checkSelectedContainerImages() error = %v", err)
	}
	if count := runner.commandCount("docker", "image", "inspect", image); count != 1 {
		t.Fatalf("docker image inspect count = %d, want 1; commands = %#v", count, runner.commands)
	}
}

// TestCheckSelectedContainerImagesRetainsCallerDockerConfig verifies the check reads caller-owned credentials without changing the directory.
func TestCheckSelectedContainerImagesRetainsCallerDockerConfig(t *testing.T) {
	image := "registry.example/exporter:dev"
	dockerConfig := t.TempDir()
	configPath := filepath.Join(dockerConfig, "config.json")
	if err := os.WriteFile(configPath, []byte("caller credentials\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	requirements := []scenario.SelectedRequirement{{
		Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageExporter},
		RequiredBy:  []string{"container/imageStartup"},
	}}

	err := checkSelectedContainerImages(context.Background(), runner, config.TestOptions{
		ExporterImage:   image,
		DockerConfigDir: dockerConfig,
	}, requirements)
	if err != nil {
		t.Fatalf("checkSelectedContainerImages() error = %v", err)
	}
	command, ok := runner.commandWithArgs("docker", "image", "inspect", image)
	if !ok || !hasArg(command.Env, "DOCKER_CONFIG="+dockerConfig) {
		t.Fatalf("image inspection did not use caller Docker config: %#v", runner.commands)
	}
	content, err := os.ReadFile(configPath)
	if err != nil || string(content) != "caller credentials\n" {
		t.Fatalf("caller Docker config changed: content=%q err=%v", content, err)
	}
}

func TestTagLocalK3DImageRetainsCallerDockerConfig(t *testing.T) {
	dockerConfig := t.TempDir()
	image := "registry.example/exporter@" + testImageDigest
	runner := &fakeRunner{}

	err := tagLocalK3dImage(context.Background(), runner, io.Discard, config.TestOptions{
		ExporterImage:   image,
		DockerConfigDir: dockerConfig,
	})
	if err != nil {
		t.Fatalf("tagLocalK3dImage() error = %v", err)
	}
	command, ok := runner.commandWithArgs("docker", "tag", image, "registry.example/exporter:dcgm-exporter-e2e-aaaaaaaaaaaa")
	if !ok || !hasArg(command.Env, "DOCKER_CONFIG="+dockerConfig) {
		t.Fatalf("image tag did not use caller Docker config: %#v", runner.commands)
	}
}

// hostPlatformManifest returns a manifest index containing an image for the platform running this test.
func hostPlatformManifest() []byte {
	return []byte(`{"manifests":[{"platform":{"os":"` + runtime.GOOS + `","architecture":"` + runtime.GOARCH + `"}}]}`)
}

// TestWriteSelectedImageReferencesDeduplicatesMixedTargets deduplicates references across target types.
func TestWriteSelectedImageReferencesDeduplicatesMixedTargets(t *testing.T) {
	selected := []scenario.Scenario{
		scenario.MustFind(scenario.Catalog, scenario.SuiteContainer, "imageStartup"),
		scenario.MustFind(scenario.Catalog, scenario.SuiteContainer, "remoteDcgmUri"),
		scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "default"),
		scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "remoteDcgm"),
	}
	requirements := scenario.RequirementsFor(selected, scenario.RunConditions{})
	opts := config.TestOptions{
		ExporterImage:     "registry.example/exporter:dev",
		CUDAWorkloadImage: "registry.example/cuda:dev",
		DCGMImage:         "registry.example/dcgm:dev",
	}
	var out bytes.Buffer
	if err := writeSelectedImageReferences(&out, opts, requirements); err != nil {
		t.Fatal(err)
	}
	want := "[e2e] image exporter: registry.example/exporter:dev\n" +
		"[e2e] image dcgm: registry.example/dcgm:dev\n" +
		"[e2e] image cuda-workload: registry.example/cuda:dev\n"
	if got := out.String(); got != want {
		t.Fatalf("image references = %q, want %q", got, want)
	}
}
