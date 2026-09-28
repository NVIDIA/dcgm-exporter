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

// Package image parses complete Docker/OCI image references used by e2e.
package image

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

// Pull returns the immutable digest reference when present, otherwise the tagged reference.
func (r Reference) Pull() string {
	if r.digest != "" {
		return r.repository + "@" + r.digest
	}
	return r.Tagged()
}

// Tagged returns the tagged reference when present, otherwise the digest reference.
func (r Reference) Tagged() string {
	if r.tag != "" {
		return r.repository + ":" + r.tag
	}
	if r.digest != "" {
		return r.repository + "@" + r.digest
	}
	return r.repository
}

// String returns a complete reference, preserving both tag and digest when supplied.
func (r Reference) String() string {
	result := r.repository
	if r.tag != "" {
		result += ":" + r.tag
	}
	if r.digest != "" {
		result += "@" + r.digest
	}
	return result
}
