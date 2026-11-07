/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
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

package appconfig

// SourceKind identifies the provider of a signal value.
type SourceKind string

// EntityScope identifies the hardware scope of a signal source.
type EntityScope string

const (
	SourceDCGM     SourceKind = "dcgm"
	SourceExporter SourceKind = "exporter"
)

const (
	EntityGPU        EntityScope = "gpu"
	EntityGPUOrMIGGI EntityScope = "gpu-or-mig-gi"
	EntityVGPU       EntityScope = "vgpu"
	EntityExporter   EntityScope = "exporter"
)

// Source identifies the DCGM field or exporter metric that supplies a signal.
type Source struct {
	Kind    SourceKind
	Name    string
	FieldID uint16
}

// WatchSource identifies a selected source and its hardware scope.
type WatchSource struct {
	Source Source
	Entity EntityScope
	// Metadata marks an auxiliary source for identity attributes, not a selected signal.
	Metadata bool
}
