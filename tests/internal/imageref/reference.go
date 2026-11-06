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

// Package imageref parses Docker/OCI image references for test suites.
// Thin copy kept here so tests/k8s does not import internal/e2e.
package imageref

import (
	_ "crypto/sha256"
	"fmt"
	"strings"

	distributionreference "github.com/distribution/reference"
)

// Reference is a validated Docker/OCI image reference with a tag, digest, or both.
type Reference struct {
	repository string
	tag        string
	digest     string
}

// Parse validates a complete image reference. Name-only references are rejected.
func Parse(value string) (Reference, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Reference{}, fmt.Errorf("image reference is empty")
	}
	named, err := distributionreference.ParseNormalizedNamed(value)
	if err != nil {
		return Reference{}, fmt.Errorf("invalid image reference %q: %w", value, err)
	}
	if distributionreference.IsNameOnly(named) {
		return Reference{}, fmt.Errorf("image reference %q requires a tag or digest", value)
	}

	parsed := Reference{repository: distributionreference.FamiliarName(named)}
	if tagged, ok := named.(distributionreference.NamedTagged); ok {
		parsed.tag = tagged.Tag()
	}
	if digested, ok := named.(distributionreference.Digested); ok {
		parsed.digest = digested.Digest().String()
	}
	return parsed, nil
}

// Repository returns the image repository without tag or digest.
func (r Reference) Repository() string { return r.repository }

// Tag returns the optional image tag.
func (r Reference) Tag() string { return r.tag }

// Digest returns the optional image digest.
func (r Reference) Digest() string { return r.digest }
