// Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakePackageDatabase struct {
	owners   map[string]string
	metadata map[string]packageMetadata
}

func (database fakePackageDatabase) Owner(path string) (string, error) {
	owner := database.owners[path]
	if owner == "" {
		return "", fmt.Errorf("no package owns %s", path)
	}
	return owner, nil
}

func (database fakePackageDatabase) Metadata(binaryPackage string) (packageMetadata, error) {
	metadata, found := database.metadata[binaryPackage]
	if !found {
		return packageMetadata{}, fmt.Errorf("metadata not found for %s", binaryPackage)
	}
	return metadata, nil
}

func TestAssembleContainerNotices(t *testing.T) {
	t.Parallel()

	config := newContainerNoticeFixture(t)
	notices, err := assembleContainerNotices(config)
	if err != nil {
		t.Fatalf("assemble container notices: %v", err)
	}
	text := string(notices)
	for _, expected := range []string{
		"Architecture: linux/amd64",
		"THIRD-PARTY NOTICES FOR THE DCGM-EXPORTER BINARY",
		"Go terms  \r\n",
		"DCGM PACKAGE THIRD-PARTY NOTICES",
		"DCGM package terms  \r\n",
		"Component: runtime-tool 2.0",
		"runtime terms",
		"Common license: /usr/share/common-licenses/GPL-2",
		"complete common license text",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("container notices do not contain %q", expected)
		}
	}
	if strings.Contains(text, "NVIDIA DCGM package license") {
		t.Error("container notices include the first-party DCGM package license")
	}

	again, err := assembleContainerNotices(config)
	if err != nil {
		t.Fatalf("assemble container notices again: %v", err)
	}
	if string(again) != text {
		t.Error("container notices are not deterministic")
	}
}

func TestAssembleContainerNoticesRequiresDCGMInput(t *testing.T) {
	t.Parallel()

	config := newContainerNoticeFixture(t)
	if err := os.WriteFile(config.DCGMNoticesPath, nil, 0o600); err != nil {
		t.Fatalf("empty DCGM notices: %v", err)
	}
	if _, err := assembleContainerNotices(config); err == nil || !strings.Contains(err.Error(), "DCGM notices are empty") {
		t.Fatalf("error = %v, want empty DCGM notice failure", err)
	}
}

func TestAssembleContainerNoticesRequiresShippedDCGMPackage(t *testing.T) {
	t.Parallel()

	config := newContainerNoticeFixture(t)
	if err := os.WriteFile(config.RuntimeManifest, []byte("package\t/usr/bin/runtime-tool\t/usr/bin/runtime-tool\n"), 0o600); err != nil {
		t.Fatalf("write runtime manifest: %v", err)
	}
	if _, err := assembleContainerNotices(config); err == nil || !strings.Contains(err.Error(), "no DCGM package-owned files") {
		t.Fatalf("error = %v, want missing DCGM package failure", err)
	}
}

func TestReadRuntimeManifestRejectsDuplicateDestination(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime.tsv")
	if err := os.WriteFile(path, []byte(
		"package\t/usr/bin/one\t/usr/bin/tool\n"+
			"package\t/usr/bin/two\t/usr/bin/tool\n"), 0o600); err != nil {
		t.Fatalf("write runtime manifest: %v", err)
	}
	if _, err := readRuntimeManifest(path); err == nil || !strings.Contains(err.Error(), "duplicate destination") {
		t.Fatalf("error = %v, want duplicate destination failure", err)
	}
}

func TestParseDpkgOwner(t *testing.T) {
	t.Parallel()

	owner, err := parseDpkgOwner("/usr/bin/tool", []byte("runtime-tool: /usr/bin/tool\n"))
	if err != nil {
		t.Fatalf("parse package owner: %v", err)
	}
	if owner != "runtime-tool" {
		t.Errorf("owner = %q, want runtime-tool", owner)
	}
	if _, err := parseDpkgOwner("/usr/bin/tool", []byte("one, two: /usr/bin/tool\n")); err == nil {
		t.Error("ambiguous package ownership was accepted")
	}
}

func newContainerNoticeFixture(t *testing.T) containerNoticeConfig {
	t.Helper()

	root := t.TempDir()
	goNoticesPath := filepath.Join(t.TempDir(), "GO_THIRD_PARTY_NOTICES")
	dcgmNoticesPath := filepath.Join(t.TempDir(), "DCGM_THIRD_PARTY_NOTICES")
	manifestPath := filepath.Join(t.TempDir(), "runtime.tsv")
	if err := os.WriteFile(goNoticesPath, []byte("THIRD-PARTY NOTICES FOR THE DCGM-EXPORTER BINARY\n\nGo terms  \r\n"), 0o600); err != nil {
		t.Fatalf("write Go notices: %v", err)
	}
	if err := os.WriteFile(dcgmNoticesPath, []byte("DCGM package terms  \r\n"), 0o600); err != nil {
		t.Fatalf("write DCGM notices: %v", err)
	}
	writeFixtureFile(t, root, "/usr/share/doc/runtime-tool/copyright", "runtime terms\n/usr/share/common-licenses/GPL-2\n")
	writeFixtureFile(t, root, "/usr/share/common-licenses/GPL-2", "complete common license text\n")
	writeFixtureFile(t, root, "/usr/share/doc/datacenter-gpu-manager-4-core/copyright", "NVIDIA DCGM package license\n")
	if err := os.WriteFile(manifestPath, []byte(
		"package\t/usr/bin/runtime-tool\t/usr/bin/runtime-tool\n"+
			"package\t/usr/lib/libdcgm.so.4\t/usr/lib/libdcgm.so.4\n"), 0o600); err != nil {
		t.Fatalf("write runtime manifest: %v", err)
	}

	return containerNoticeConfig{
		Architecture:    "linux/amd64",
		GoNoticesPath:   goNoticesPath,
		DCGMNoticesPath: dcgmNoticesPath,
		RuntimeManifest: manifestPath,
		HelperRoot:      root,
		Packages: fakePackageDatabase{
			owners: map[string]string{
				"/usr/bin/runtime-tool": "runtime-tool",
				"/usr/lib/libdcgm.so.4": "datacenter-gpu-manager-4-core",
			},
			metadata: map[string]packageMetadata{
				"runtime-tool": {
					BinaryPackage: "runtime-tool",
					BinaryVersion: "2.0",
					SourcePackage: "runtime-source",
					SourceVersion: "2.0",
				},
				"datacenter-gpu-manager-4-core": {
					BinaryPackage: "datacenter-gpu-manager-4-core",
					BinaryVersion: "1:4.6.1-1",
					SourcePackage: "datacenter-gpu-manager",
					SourceVersion: "1:4.6.1-1",
				},
			},
		},
	}
}

func writeFixtureFile(t *testing.T, root, logicalPath, contents string) {
	t.Helper()
	path, err := rootedPath(root, logicalPath)
	if err != nil {
		t.Fatalf("resolve fixture path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", logicalPath, err)
	}
}
