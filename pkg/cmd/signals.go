/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

package cmd

import "os"

// SignalSource provides signals that trigger reload or shutdown.
// This interface allows dependency injection for testing.
type SignalSource interface {
	// Signals returns the channel that receives signals
	Signals() <-chan os.Signal
	// Cleanup stops signal watching and cleans up resources
	Cleanup()
}

// OSSignalSource watches actual OS signals (production use)
type OSSignalSource struct {
	ch      chan os.Signal
	cleanup func()
}

// NewOSSignalSource creates a signal source that watches OS signals
func NewOSSignalSource(sigs ...os.Signal) *OSSignalSource {
	ch, cleanup := newOSWatcher(sigs...)
	return &OSSignalSource{ch: ch, cleanup: cleanup}
}

// Signals returns the channel that receives OS signals
func (s *OSSignalSource) Signals() <-chan os.Signal {
	return s.ch
}

// Cleanup stops watching OS signals and closes the channel
func (s *OSSignalSource) Cleanup() {
	s.cleanup()
}

// TestSignalSource allows programmatic signal injection for testing
type TestSignalSource struct {
	ch chan os.Signal
}

// NewTestSignalSource creates a signal source for testing
func NewTestSignalSource() *TestSignalSource {
	return &TestSignalSource{ch: make(chan os.Signal, 10)}
}

// Signals returns the channel that receives test signals
func (s *TestSignalSource) Signals() <-chan os.Signal {
	return s.ch
}

// Cleanup closes the signal channel
func (s *TestSignalSource) Cleanup() {
	close(s.ch)
}

// SendSignal injects a signal into the channel (test helper)
func (s *TestSignalSource) SendSignal(sig os.Signal) {
	s.ch <- sig
}
