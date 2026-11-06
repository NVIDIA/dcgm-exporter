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
	"os"
	"strings"
	"testing"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

func TestImportImageIfPresentDetectsK3dImportErrors(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"k3d image import dcgm:test -c dcgm-exporter-gpu": {
			Stderr: []byte("ERRO failed to import images in node 'k3d-dcgm-exporter-gpu-agent-0'\n"),
		},
	}}
	var out bytes.Buffer

	imported, err := ImportImageIfPresentWithResult(context.Background(), runner, &out, DefaultConfig(), "dcgm:test", "k3d_import_dcgm_image")
	if !imported {
		t.Fatal("ImportImageIfPresentWithResult() imported = false, want true")
	}
	if err == nil || !strings.Contains(err.Error(), "reported image import errors") {
		t.Fatalf("ImportImageIfPresentWithResult() error = %v, want import error", err)
	}
	if !strings.Contains(out.String(), "failed to import images") {
		t.Fatalf("import log did not include k3d error output:\n%s", out.String())
	}
}

func TestImportImageIfPresentWithResultVerifiesEveryK3dNode(t *testing.T) {
	image := "nvidia/dcgm-exporter:dev"
	normalizedImage := "docker.io/nvidia/dcgm-exporter:dev"
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"docker ps -a --format {{.Names}}": {
			Stdout: []byte("k3d-dcgm-exporter-gpu-agent-0\nk3d-other-server-0\nk3d-dcgm-exporter-gpu-server-0\n"),
		},
		"docker exec k3d-dcgm-exporter-gpu-agent-0 ctr --namespace k8s.io images check --quiet name==" + normalizedImage: {
			Stdout: []byte(normalizedImage + "\n"),
		},
		"docker exec k3d-dcgm-exporter-gpu-server-0 ctr --namespace k8s.io images check --quiet name==" + normalizedImage: {
			Stdout: []byte(normalizedImage + "\n"),
		},
	}}
	var out bytes.Buffer

	imported, err := ImportImageIfPresentWithResult(context.Background(), runner, &out, DefaultConfig(), image, "k3d_import_test_image")
	if err != nil {
		t.Fatalf("ImportImageIfPresentWithResult() error = %v", err)
	}
	if !imported {
		t.Fatal("ImportImageIfPresentWithResult() imported = false, want true")
	}
	for _, node := range []string{"k3d-dcgm-exporter-gpu-server-0", "k3d-dcgm-exporter-gpu-agent-0"} {
		if !runner.has("docker", "exec", node, "ctr", "--namespace", "k8s.io", "images", "check", "--quiet", "name=="+normalizedImage) {
			t.Fatalf("image was not checked on %s: %#v", node, runner.commands)
		}
		if !strings.Contains(out.String(), "node="+node+" image="+normalizedImage) {
			t.Fatalf("image readiness was not logged for %s:\n%s", node, out.String())
		}
	}
	if runner.has("docker", "exec", "k3d-other-server-0") {
		t.Fatalf("image was checked on a node from another cluster: %#v", runner.commands)
	}
}

// TestImportImageIfPresentUsesPlatformArchiveForImageIndex saves only the inspected platform and verifies the imported tag on cluster nodes.
func TestImportImageIfPresentUsesPlatformArchiveForImageIndex(t *testing.T) {
	image := "nvidia/dcgm-exporter:dev"
	normalizedImage := "docker.io/nvidia/dcgm-exporter:dev"
	runner := newImageArchiveTestRunner(image, []byte(`[{"Os":"linux","Architecture":"arm64","Variant":"v8","Descriptor":{"mediaType":"application/vnd.oci.image.index.v1+json"}}]`))
	var out bytes.Buffer

	imported, err := ImportImageIfPresentWithResult(context.Background(), runner, &out, DefaultConfig(), image, "k3d_import_test_image")
	if err != nil {
		t.Fatalf("ImportImageIfPresentWithResult() error = %v", err)
	}
	if !imported {
		t.Fatal("ImportImageIfPresentWithResult() imported = false, want true")
	}
	if !runner.archivePresentAtImport {
		t.Fatal("platform archive was not available when k3d imported it")
	}
	if !runner.has("docker", "image", "save", "--platform", "linux/arm64/v8", "--output", runner.archivePath, image) {
		t.Fatalf("image was not saved for its inspected platform and local tag: %#v", runner.commands)
	}
	if !runner.has("k3d", "image", "import", runner.archivePath, "-c", DefaultConfig().ClusterName) {
		t.Fatalf("k3d did not import the platform archive: %#v", runner.commands)
	}
	if runner.has("k3d", "image", "import", image, "-c", DefaultConfig().ClusterName) {
		t.Fatalf("k3d imported the unfiltered image index: %#v", runner.commands)
	}
	for _, node := range []string{"k3d-dcgm-exporter-gpu-server-0", "k3d-dcgm-exporter-gpu-agent-0"} {
		if !runner.has("docker", "exec", node, "ctr", "--namespace", "k8s.io", "images", "check", "--quiet", "name=="+normalizedImage) {
			t.Fatalf("imported tag was not verified on %s: %#v", node, runner.commands)
		}
	}
	requireImageArchiveRemoved(t, runner.archivePath)
}

// TestImportImageIfPresentKeepsSingleManifestImportPath retains the direct import path for a single-platform image.
func TestImportImageIfPresentKeepsSingleManifestImportPath(t *testing.T) {
	image := "nvidia/dcgm-exporter:dev"
	normalizedImage := "docker.io/nvidia/dcgm-exporter:dev"
	runner := newImageArchiveTestRunner(image, []byte(`[{"Os":"linux","Architecture":"amd64"}]`))
	runner.results["docker ps -a --format {{.Names}}"] = e2eexec.Result{Stdout: []byte("k3d-dcgm-exporter-gpu-server-0\n")}
	runner.results["docker exec k3d-dcgm-exporter-gpu-server-0 ctr --namespace k8s.io images check --quiet name=="+normalizedImage] = e2eexec.Result{Stdout: []byte(normalizedImage + "\n")}

	imported, err := ImportImageIfPresentWithResult(context.Background(), runner, io.Discard, DefaultConfig(), image, "k3d_import_test_image")
	if err != nil || !imported {
		t.Fatalf("ImportImageIfPresentWithResult() = (%t, %v), want (true, nil)", imported, err)
	}
	if runner.has("docker", "image", "save") {
		t.Fatalf("single-manifest image unexpectedly used a platform archive: %#v", runner.commands)
	}
	if !runner.has("k3d", "image", "import", image, "-c", DefaultConfig().ClusterName) {
		t.Fatalf("single-manifest image did not use the existing import path: %#v", runner.commands)
	}
}

// TestImportImageIfPresentRejectsUnreadableImageInspection stops before import when Docker inspection output is invalid.
func TestImportImageIfPresentRejectsUnreadableImageInspection(t *testing.T) {
	image := "nvidia/dcgm-exporter:dev"
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + image: {Stdout: []byte("not JSON\n")},
	}}

	imported, err := ImportImageIfPresentWithResult(context.Background(), runner, io.Discard, DefaultConfig(), image, "k3d_import_test_image")
	if imported || err == nil || !strings.Contains(err.Error(), "decode docker image inspect output") {
		t.Fatalf("ImportImageIfPresentWithResult() = (%t, %v), want inspection error without import", imported, err)
	}
	if runner.has("k3d", "image", "import") {
		t.Fatalf("image was imported after inspection output could not be decoded: %#v", runner.commands)
	}
}

// TestImportImageIfPresentCleansPlatformArchiveOnSaveOrImportError removes the temporary archive after save or k3d import failures.
func TestImportImageIfPresentCleansPlatformArchiveOnSaveOrImportError(t *testing.T) {
	image := "nvidia/dcgm-exporter:dev"
	tests := []struct {
		name         string
		saveResult   e2eexec.Result
		importResult e2eexec.Result
		wantImported bool
		wantError    string
	}{
		{
			name:       "platform save unsupported",
			saveResult: e2eexec.Result{ExitCode: 1, Stderr: []byte("unknown flag: --platform")},
			wantError:  "unknown flag: --platform",
		},
		{
			name:         "k3d import failed",
			importResult: e2eexec.Result{ExitCode: 1, Stderr: []byte("ctr image import failed")},
			wantImported: true,
			wantError:    "ctr image import failed",
		},
		{
			name:         "k3d reports import failure with zero exit code",
			importResult: e2eexec.Result{Stderr: []byte("ERRO failed to import images in node 'k3d-test-agent-0'\n")},
			wantImported: true,
			wantError:    "reported image import errors",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newImageArchiveTestRunner(image, []byte(`[{"Os":"linux","Architecture":"amd64","Descriptor":{"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json"}}]`))
			runner.saveResult = tt.saveResult
			runner.importResult = tt.importResult

			imported, err := ImportImageIfPresentWithResult(context.Background(), runner, io.Discard, DefaultConfig(), image, "k3d_import_test_image")
			if imported != tt.wantImported {
				t.Fatalf("ImportImageIfPresentWithResult() imported = %t, want %t", imported, tt.wantImported)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("ImportImageIfPresentWithResult() error = %v, want containing %q", err, tt.wantError)
			}
			requireImageArchiveRemoved(t, runner.archivePath)
			if runner.has("k3d", "image", "import") != imported {
				t.Fatalf("k3d import attempt = %t, want %t: %#v", runner.has("k3d", "image", "import"), imported, runner.commands)
			}
			if runner.archivePresentAtImport != imported {
				t.Fatalf("archive present during import = %t, want %t", runner.archivePresentAtImport, imported)
			}
		})
	}
}

// TestImportImageIfPresentCleansPlatformArchiveOnNodeVerificationFailure removes the archive after import succeeds but node verification fails.
func TestImportImageIfPresentCleansPlatformArchiveOnNodeVerificationFailure(t *testing.T) {
	image := "nvidia/dcgm-exporter:dev"
	normalizedImage := "docker.io/nvidia/dcgm-exporter:dev"
	runner := newImageArchiveTestRunner(image, []byte(`[{"Os":"linux","Architecture":"amd64","Descriptor":{"mediaType":"application/vnd.oci.image.index.v1+json"}}]`))
	runner.results["docker exec k3d-dcgm-exporter-gpu-server-0 ctr --namespace k8s.io images check --quiet name=="+normalizedImage] = e2eexec.Result{}

	imported, err := ImportImageIfPresentWithResult(context.Background(), runner, io.Discard, DefaultConfig(), image, "k3d_import_test_image")
	if !imported || err == nil || !strings.Contains(err.Error(), "does not have usable content") {
		t.Fatalf("ImportImageIfPresentWithResult() = (%t, %v), want failed node verification after import", imported, err)
	}
	if !runner.archivePresentAtImport {
		t.Fatal("platform archive was not present during k3d import")
	}
	requireImageArchiveRemoved(t, runner.archivePath)
}

// requireImageArchiveRemoved fails when the temporary platform archive remains after an import attempt.
func requireImageArchiveRemoved(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary platform archive still exists: path=%s err=%v", path, err)
	}
}

func TestVerifyImageOnNodesRejectsMissingOrIncompleteImport(t *testing.T) {
	image := "registry.example/dcgm-exporter:dev"
	tests := []struct {
		name    string
		results map[string]e2eexec.Result
		want    string
	}{
		{
			name: "no cluster nodes",
			results: map[string]e2eexec.Result{
				"docker ps -a --format {{.Names}}": {},
			},
			want: "has no server or agent nodes",
		},
		{
			name: "image content is incomplete",
			results: map[string]e2eexec.Result{
				"docker ps -a --format {{.Names}}": {
					Stdout: []byte("k3d-test-server-0\n"),
				},
				"docker exec k3d-test-server-0 ctr --namespace k8s.io images check --quiet name==" + image: {},
			},
			want: "does not have usable content",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := verifyImageOnNodes(context.Background(), &recordingRunner{results: test.results}, io.Discard, "test", image)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("verifyImageOnNodes() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestResolveLocalK3DImageCreatesAnImportableDigestAlias(t *testing.T) {
	for _, digest := range []string{clusterTestDigest, clusterTestSHA512Digest} {
		t.Run(digest[:6], func(t *testing.T) {
			image, err := ResolveLocalK3DImage("registry.example/cuda@" + digest)
			if err != nil {
				t.Fatal(err)
			}
			if image.Source != "registry.example/cuda@"+digest {
				t.Fatalf("source = %q", image.Source)
			}
			if image.Local != "registry.example/cuda:dcgm-exporter-e2e-aaaaaaaaaaaa" {
				t.Fatalf("k3d image = %q", image.Local)
			}
		})
	}
}

func TestTagLocalK3DImageUsesResolvedDigestAlias(t *testing.T) {
	image, err := ResolveLocalK3DImage("registry.example/cuda@" + clusterTestDigest)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}

	if err := TagLocalK3DImage(context.Background(), runner, io.Discard, image, "docker_tag_test_image", nil); err != nil {
		t.Fatal(err)
	}
	if !runner.has("docker", "tag", image.Source, image.Local) {
		t.Fatalf("digest alias was not tagged before import: %#v", runner.commands)
	}
}

// imageArchiveTestRunner extends recordingRunner to emulate temporary platform archive save and k3d import.
type imageArchiveTestRunner struct {
	recordingRunner
	saveResult             e2eexec.Result
	importResult           e2eexec.Result
	archivePath            string
	archivePresentAtImport bool
}

// newImageArchiveTestRunner sets image inspection and cluster-node responses for platform archive tests.
func newImageArchiveTestRunner(image string, inspectOutput []byte) *imageArchiveTestRunner {
	return &imageArchiveTestRunner{recordingRunner: recordingRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + image: {
			Stdout: inspectOutput,
		},
		"docker ps -a --format {{.Names}}": {
			Stdout: []byte("k3d-dcgm-exporter-gpu-server-0\nk3d-dcgm-exporter-gpu-agent-0\n"),
		},
		"docker exec k3d-dcgm-exporter-gpu-server-0 ctr --namespace k8s.io images check --quiet name==docker.io/nvidia/dcgm-exporter:dev": {
			Stdout: []byte("docker.io/nvidia/dcgm-exporter:dev\n"),
		},
		"docker exec k3d-dcgm-exporter-gpu-agent-0 ctr --namespace k8s.io images check --quiet name==docker.io/nvidia/dcgm-exporter:dev": {
			Stdout: []byte("docker.io/nvidia/dcgm-exporter:dev\n"),
		},
	}}}
}

// Run emulates saving an image archive and importing it, recording whether the archive exists during import.
func (r *imageArchiveTestRunner) Run(ctx context.Context, command e2eexec.Command) e2eexec.Result {
	if command.Name == "docker" && len(command.Args) >= 2 && command.Args[0] == "image" && command.Args[1] == "save" {
		r.commands = append(r.commands, command)
		for i, arg := range command.Args {
			if arg == "--output" && i+1 < len(command.Args) {
				r.archivePath = command.Args[i+1]
				break
			}
		}
		if r.archivePath == "" {
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("image save did not specify an output path")}
		}
		if err := os.WriteFile(r.archivePath, []byte("test image archive"), 0o600); err != nil {
			return e2eexec.Result{ExitCode: 1, Err: err}
		}
		return streamResult(command, r.saveResult)
	}
	if command.Name == "k3d" && len(command.Args) >= 3 && command.Args[0] == "image" && command.Args[1] == "import" {
		r.commands = append(r.commands, command)
		_, err := os.Stat(command.Args[2])
		r.archivePresentAtImport = err == nil
		return streamResult(command, r.importResult)
	}
	return r.recordingRunner.Run(ctx, command)
}
