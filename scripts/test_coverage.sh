#!/bin/bash

#
# Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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
#

echo "Running unit tests..."
go test $(go list ./... | grep -v "/tests/e2e/") \
  -count=1 \
  -timeout 5m \
  -covermode=count \
  -coverprofile=unit_coverage.out \
  -json > test_results.json

if [ $? -ne 0 ]; then
  echo "Unit tests failed."
  exit 1
fi

echo "Running integration tests..."
go test ./internal/pkg/integration_test/... \
  -count=1 \
  -timeout 5m \
  -covermode=count \
  -coverpkg=./internal/pkg/... \
  -coverprofile=integration_coverage.out \
  -json >> test_results.json

if [ $? -ne 0 ]; then
  echo "Integration tests failed."
  exit 1
fi

echo "Merging coverage profiles..."
gocovmerge unit_coverage.out integration_coverage.out > combined_coverage.out.tmp

# Remove mocks from coverage
cat combined_coverage.out.tmp | grep -v "mock_" > tests.cov

# Cleanup
rm combined_coverage.out.tmp integration_coverage.out unit_coverage.out