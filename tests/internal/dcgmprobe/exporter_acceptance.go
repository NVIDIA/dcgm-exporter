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

// ExporterTarget pairs native identity with the independently observed labels
// required in actual HTTP exposition. Unselected targets must emit no samples.
type ExporterTarget struct {
	Entity   Entity            `json:"entity"`
	Selected bool              `json:"selected"`
	Labels   map[string]string `json:"labels"`
}

// ExporterAcceptance records one fake-topology run through real exporter startup.
// The caller records the exact source revision and binary checksum separately.
type ExporterAcceptance struct {
	Version          int              `json:"version"`
	Mode             string           `json:"mode"`
	Scenario         string           `json:"scenario"`
	Topology         string           `json:"topology"`
	Selector         string           `json:"selector"`
	CSV              string           `json:"csv"`
	Targets          []ExporterTarget `json:"targets"`
	Injected         []HistorySample  `json:"injected"`
	History          []HistorySample  `json:"history"`
	SecondHistory    []HistorySample  `json:"secondHistory"`
	HistorySinceUsec int64            `json:"historySinceUsec"`
	TotalSinceUsec   int64            `json:"totalSinceUsec"`
	WindowSizeMS     int              `json:"windowSizeMs"`
	FirstScrape      string           `json:"firstScrape"`
	SecondScrape     string           `json:"secondScrape"`
	FirstScrapeUsec  int64            `json:"firstScrapeUsec"`
	SecondScrapeUsec int64            `json:"secondScrapeUsec"`
}

// ExporterHTTPResponse retains the last received response, including an invalid one.
type ExporterHTTPResponse struct {
	Body          string `json:"body"`
	TimestampUsec int64  `json:"timestampUsec"`
	Status        int    `json:"status"`
	Truncated     bool   `json:"truncated"`
	Error         string `json:"error,omitempty"`
}

// ExporterFailure is diagnostic evidence from an unsuccessful native attempt.
type ExporterFailure struct {
	Version               int                  `json:"version"`
	Outcome               string               `json:"outcome"`
	Stage                 string               `json:"stage"`
	Error                 string               `json:"error"`
	Report                ExporterAcceptance   `json:"report"`
	HistoryVerified       bool                 `json:"historyVerified"`
	SecondHistoryVerified bool                 `json:"secondHistoryVerified"`
	HistoryTruncated      bool                 `json:"historyTruncated"`
	Response              ExporterHTTPResponse `json:"response"`
	Cleanup               string               `json:"cleanup"`
	CleanupErrors         []string             `json:"cleanupErrors,omitempty"`
}
