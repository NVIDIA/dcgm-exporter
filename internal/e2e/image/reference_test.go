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

package image

import "testing"

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		repository string
		tag        string
		digest     string
		pull       string
		tagged     string
	}{
		{name: "familiar tagged", value: "busybox:1.36.1", repository: "busybox", tag: "1.36.1", pull: "busybox:1.36.1", tagged: "busybox:1.36.1"},
		{name: "qualified tagged", value: "registry.example/team/image:dev", repository: "registry.example/team/image", tag: "dev", pull: "registry.example/team/image:dev", tagged: "registry.example/team/image:dev"},
		{name: "registry port", value: "localhost:5000/team/image:dev", repository: "localhost:5000/team/image", tag: "dev", pull: "localhost:5000/team/image:dev", tagged: "localhost:5000/team/image:dev"},
		{name: "digest", value: "registry.example/team/image@" + testDigest, repository: "registry.example/team/image", digest: testDigest, pull: "registry.example/team/image@" + testDigest, tagged: "registry.example/team/image@" + testDigest},
		{name: "tag and digest", value: "registry.example/team/image:readable@" + testDigest, repository: "registry.example/team/image", tag: "readable", digest: testDigest, pull: "registry.example/team/image@" + testDigest, tagged: "registry.example/team/image:readable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			if got.Repository() != tt.repository || got.Tag() != tt.tag || got.Digest() != tt.digest {
				t.Fatalf("Parse(%q) = repository %q, tag %q, digest %q", tt.value, got.Repository(), got.Tag(), got.Digest())
			}
			if got.Pull() != tt.pull {
				t.Fatalf("Pull() = %q, want %q", got.Pull(), tt.pull)
			}
			if got.Tagged() != tt.tagged {
				t.Fatalf("Tagged() = %q, want %q", got.Tagged(), tt.tagged)
			}
			if got.String() != tt.value {
				t.Fatalf("String() = %q, want %q", got.String(), tt.value)
			}
		})
	}
}

func TestParseRejectsIncompleteAndMalformedReferences(t *testing.T) {
	for _, value := range []string{"", "busybox", "registry.example/team/image", "https://registry.example/team/image:tag", "image/UPPERCASE:tag", "image@sha256:abc"} {
		t.Run(value, func(t *testing.T) {
			if _, err := Parse(value); err == nil {
				t.Fatalf("Parse(%q) error = nil", value)
			}
		})
	}
}
