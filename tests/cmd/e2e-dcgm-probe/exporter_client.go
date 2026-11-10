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
package main

import (
	"errors"
	"sync/atomic"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// acceptanceClient delegates the existing native API, retaining real cleanup
// until the oracle has closed and the application has retired its collectors.
// Application Cleanup is the existing signal that native retirement is safe.
type acceptanceClient struct {
	dcgmprovider.DCGM
	cleanupRequested atomic.Bool
	released         bool
}

func (c *acceptanceClient) Cleanup() { c.cleanupRequested.Store(true) }

func (c *acceptanceClient) finish(app *acceptanceExporter) error {
	if app == nil {
		return c.release(true, false)
	}
	err := app.stop()
	return errors.Join(err, c.release(app.completed(), !app.finishedBeforeAction()))
}

// release never tears down native state when the application cannot establish
// safe collector retirement. A failed probe must then exit its owned process;
// the standalone caller must also stop its isolated hostengine.
func (c *acceptanceClient) release(appFinished, actionEntered bool) error {
	if c.released {
		return nil
	}
	if !appFinished || (actionEntered && !c.cleanupRequested.Load()) {
		return errors.New("native cleanup withheld: exporter did not establish safe retirement; exit owned probe process and stop owned standalone backend")
	}
	c.DCGM.Cleanup()
	c.released = true
	return nil
}
