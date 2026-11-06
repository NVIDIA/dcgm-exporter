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

package imageref_test

import (
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/imageref"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestParse checks tags, digests, registry ports, and whitespace normalization.
func TestParse(t *testing.T) {
	// Cases bind each accepted reference to its repository, tag, and digest.
	tests := []struct {
		name       string
		value      string
		repository string
		tag        string
		digest     string
	}{
		{name: "familiar tagged", value: "busybox:1.36.1", repository: "busybox", tag: "1.36.1"},
		{name: "qualified tagged", value: "registry.example/team/image:dev", repository: "registry.example/team/image", tag: "dev"},
		{name: "registry port", value: "localhost:5000/team/image:dev", repository: "localhost:5000/team/image", tag: "dev"},
		{name: "digest", value: "registry.example/team/image@" + testDigest, repository: "registry.example/team/image", digest: testDigest},
		{name: "tag and digest", value: "registry.example/team/image:readable@" + testDigest, repository: "registry.example/team/image", tag: "readable", digest: testDigest},
		{name: "trim whitespace", value: "  busybox:1.36.1  ", repository: "busybox", tag: "1.36.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := imageref.Parse(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			if got.Repository() != tt.repository || got.Tag() != tt.tag || got.Digest() != tt.digest {
				t.Fatalf("Parse(%q) = repository %q, tag %q, digest %q", tt.value, got.Repository(), got.Tag(), got.Digest())
			}
		})
	}
}

// TestParseRejectsIncompleteAndMalformedReferences rejects name-only and malformed image references.
func TestParseRejectsIncompleteAndMalformedReferences(t *testing.T) {
	for _, value := range []string{"", "busybox", "registry.example/team/image", "https://registry.example/team/image:tag", "image/UPPERCASE:tag", "image@sha256:abc"} {
		t.Run(value, func(t *testing.T) {
			if _, err := imageref.Parse(value); err == nil {
				t.Fatalf("Parse(%q) error = nil", value)
			}
		})
	}
}
