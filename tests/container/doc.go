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

// Package container contains container-runtime integration tests.
//
// This file intentionally has no `container` build tag. The real tests are
// tag-gated because they require Docker and GPU container runtime access, but
// keeping one untagged package file lets `go test ./tests/container` report
// `[no test files]` instead of failing with "build constraints exclude all Go
// files" when the container suite is not selected.
package container
