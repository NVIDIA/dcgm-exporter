/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

// Package watcher provides interfaces and implementations for monitoring
// resources and triggering actions on changes. It supports file system
// watching and can be extended to monitor other resource types like metrics,
// endpoints, or configuration sources.
package watcher

import "context"

// Watcher monitors a resource and triggers callback on changes.
// Implementations must be context-aware and exit gracefully when context is cancelled.
type Watcher interface {
	// Watch starts monitoring the resource and blocks until ctx is cancelled.
	// Calls onChange() when the resource changes.
	// Returns nil on clean shutdown, or an error if monitoring fails.
	Watch(ctx context.Context, onChange func()) error
}
