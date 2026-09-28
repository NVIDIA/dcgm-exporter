#!/usr/bin/env bash
# Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Derive CI image references used by build, manifest, and e2e package jobs.

set -euo pipefail

: "${ARCHITECTURE:?ARCHITECTURE is required}"
: "${CONTAINER_REGISTRY:?CONTAINER_REGISTRY is required}"
: "${CONTAINER_IMAGE_NAME:?CONTAINER_IMAGE_NAME is required}"

IMAGE_REPOSITORY="${CONTAINER_REGISTRY}/${CONTAINER_IMAGE_NAME}"
IMAGE_VARIANT="${IMAGE_VARIANT:-distroless}"
LATEST_TAG=""

if [ -n "${CI_COMMIT_TAG:-}" ]; then
    IMAGE_VERSION="${CI_COMMIT_TAG#v}"
elif [ "${CI_COMMIT_BRANCH:-}" = "${CI_DEFAULT_BRANCH:-}" ]; then
    : "${CI_COMMIT_REF_SLUG:?CI_COMMIT_REF_SLUG is required}"
    : "${CI_PIPELINE_IID:?CI_PIPELINE_IID is required}"
    : "${CI_COMMIT_SHORT_SHA:?CI_COMMIT_SHORT_SHA is required}"
    IMAGE_VERSION="${CI_COMMIT_REF_SLUG}-${CI_PIPELINE_IID}-${CI_COMMIT_SHORT_SHA}"
    LATEST_TAG="${IMAGE_REPOSITORY}:latest-${IMAGE_VARIANT}-${ARCHITECTURE}"
else
    : "${CI_COMMIT_REF_SLUG:?CI_COMMIT_REF_SLUG is required}"
    : "${CI_PIPELINE_IID:?CI_PIPELINE_IID is required}"
    IMAGE_VERSION="${CI_COMMIT_REF_SLUG}-${CI_PIPELINE_IID}"
fi

IMAGE_TAG="${IMAGE_VERSION}-${IMAGE_VARIANT}-${ARCHITECTURE}"
IMAGE_REF="${IMAGE_REPOSITORY}:${IMAGE_TAG}"

export IMAGE_REPOSITORY IMAGE_VARIANT IMAGE_TAG IMAGE_REF LATEST_TAG
