#!/bin/bash
# Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

set -e

TARGETOS=${TARGETOS:-linux}
TARGETARCH=${TARGETARCH:-amd64}

# Configure cross-compilation based on target architecture
if [ "$TARGETARCH" = "arm64" ]; then
    export CC=aarch64-linux-gnu-gcc
    export LD_LIBRARY_PATH=/usr/aarch64-linux-gnu/lib:$LD_LIBRARY_PATH
else
    export CC=gcc
fi

echo "Building dcgm-exporter for $TARGETOS/$TARGETARCH using CC=$CC"

# Execute build with all necessary environment variables
GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=1 CC=$CC make install

echo "Build completed successfully"

