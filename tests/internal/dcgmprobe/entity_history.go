/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package dcgmprobe

// Entity identifies a native DCGM entity independently of its metric labels.
type Entity struct {
	Group uint `json:"group"`
	ID    uint `json:"id"`
}

// HistorySample preserves the native status, type, timestamp and integer value.
type HistorySample struct {
	Entity        Entity `json:"entity"`
	FieldID       uint   `json:"fieldId"`
	FieldType     uint   `json:"fieldType"`
	Status        int    `json:"status"`
	TimestampUsec int64  `json:"timestampUsec"`
	Value         int64  `json:"value"`
}

// EntityHistory is a private native-feasibility report, not exporter acceptance.
// Standalone fake entities remain in the caller-owned backend until it exits.
type EntityHistory struct {
	Version           int             `json:"version"`
	Mode              string          `json:"mode"`
	Topology          string          `json:"topology"`
	Entities          []Entity        `json:"entities"`
	GPUInstanceID     uint            `json:"gpuInstanceId"`
	ComputeInstanceID uint            `json:"computeInstanceId"`
	SinceUsec         int64           `json:"sinceUsec"`
	WindowSinceUsec   int64           `json:"windowSinceUsec"`
	Expected          []HistorySample `json:"expected"`
	History           []HistorySample `json:"history"`
	WindowHistory     []HistorySample `json:"windowHistory"`
}
