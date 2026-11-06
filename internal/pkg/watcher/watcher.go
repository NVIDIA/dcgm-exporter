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

// Watcher monitors a resource and calls its callback when that resource changes.
// Implementations observe context cancellation at their wait or read boundaries;
// cancellation does not promise to interrupt an operation already in progress.
type Watcher interface {
	// Watch monitors until it observes cancellation or a monitoring error.
	// It reports cancellation as ctx.Err() and returns other monitoring failures directly.
	Watch(ctx context.Context, onChange func()) error
}
