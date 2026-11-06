/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package bootstrap

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// failingReader supplies a deterministic I/O failure to parser tests.
type failingReader struct{}

// Read makes failingReader return a deterministic scan error.
func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("read failure")
}

// TestParseHostReadsLiteralOSRelease verifies quoted fields and architecture mapping.
func TestParseHostReadsLiteralOSRelease(t *testing.T) {
	host, err := ParseHostOS(strings.NewReader("NAME=Ubuntu\nID=ubuntu\nVERSION_ID=\"24.04\"\n"), "arm64")
	if err != nil {
		t.Fatal(err)
	}
	want := HostOS{
		Distribution:         "ubuntu",
		VersionID:            "24.04",
		Architecture:         "arm64",
		NVIDIARepositoryArch: "sbsa",
	}
	if host != want {
		t.Fatalf("ParseHostOS() = %#v, want %#v", host, want)
	}
}

// TestParseHostAcceptsSingleQuotedFields verifies literal single-quote handling.
func TestParseHostAcceptsSingleQuotedFields(t *testing.T) {
	host, err := ParseHostOS(strings.NewReader("# comment\nID='ubuntu'\nVERSION_ID='22.04'\n"), "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if host.Distribution != "ubuntu" || host.VersionID != "22.04" || host.NVIDIARepositoryArch != "x86_64" {
		t.Fatalf("ParseHostOS() = %#v", host)
	}
}

// TestParseHostUsesCompatiblePackageFamily verifies derivatives use the package family named by ID_LIKE.
func TestParseHostUsesCompatiblePackageFamily(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		distribution string
	}{
		{name: "Ubuntu derivative", content: "ID=pop\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=24.04\n", distribution: "ubuntu"},
		{name: "RHEL derivative", content: "ID=rocky\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID=8.10\n", distribution: "rhel"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host, err := ParseHostOS(strings.NewReader(test.content), "amd64")
			if err != nil {
				t.Fatal(err)
			}
			if host.Distribution != test.distribution {
				t.Fatalf("ParseHostOS() distribution = %q, want %q", host.Distribution, test.distribution)
			}
		})
	}
}

// TestParseHostPrefersExactSupportedID verifies ID wins over a different compatible family.
func TestParseHostPrefersExactSupportedID(t *testing.T) {
	host, err := ParseHostOS(strings.NewReader("ID=ubuntu\nID_LIKE=rhel\nVERSION_ID=24.04\n"), "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if host.Distribution != "ubuntu" {
		t.Fatalf("ParseHostOS() distribution = %q, want ubuntu", host.Distribution)
	}
}

// TestParseHostReportsReaderFailure verifies filesystem read errors retain context.
func TestParseHostReportsReaderFailure(t *testing.T) {
	_, err := ParseHostOS(io.Reader(failingReader{}), "amd64")
	if err == nil || !strings.Contains(err.Error(), "read failure") {
		t.Fatalf("ParseHostOS() error = %v, want reader failure", err)
	}
}

// TestParseHostRejectsMalformedInput verifies host data is never shell-evaluated or guessed.
func TestParseHostRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name    string
		content string
		arch    string
	}{
		{name: "missing version", content: "ID=ubuntu\n", arch: "amd64"},
		{name: "invalid record", content: "ID=ubuntu\nnot-a-record\nVERSION_ID=24.04\n", arch: "amd64"},
		{name: "invalid quote", content: "ID=ubuntu\nVERSION_ID=\"24.04\n", arch: "amd64"},
		{name: "unsupported architecture", content: "ID=ubuntu\nVERSION_ID=24.04\n", arch: "ppc64le"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseHostOS(strings.NewReader(test.content), test.arch); err == nil {
				t.Fatal("ParseHostOS() succeeded, want error")
			}
		})
	}
}

// TestValidateUbuntuHostOSUsesApprovedMatrix verifies explicit distribution tuples.
func TestValidateUbuntuHostOSUsesApprovedMatrix(t *testing.T) {
	for _, version := range []string{"22.04", "24.04", "26.04"} {
		for _, arch := range []struct{ goarch, repository string }{{"amd64", "x86_64"}, {"arm64", "sbsa"}} {
			host := HostOS{Distribution: "ubuntu", VersionID: version, Architecture: arch.goarch, NVIDIARepositoryArch: arch.repository}
			if _, err := validateUbuntuHostOS(host); err != nil {
				t.Fatalf("validateUbuntuHostOS(%s/%s) error = %v", version, arch.goarch, err)
			}
		}
	}

	for _, host := range []HostOS{
		{Distribution: "debian", VersionID: "12", Architecture: "amd64"},
		{Distribution: "ubuntu", VersionID: "20.04", Architecture: "amd64"},
		{Distribution: "ubuntu", VersionID: "20.04", Architecture: "arm64"},
		{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "sbsa"},
	} {
		if _, err := validateUbuntuHostOS(host); err == nil {
			t.Fatalf("validateUbuntuHostOS(%#v) succeeded, want error", host)
		}
	}
}
