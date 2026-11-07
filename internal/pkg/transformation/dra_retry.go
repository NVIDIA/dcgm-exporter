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

package transformation

import (
	"log/slog"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

// draRetryInterval is how long the pod mapper waits before it tries again to
// set up DRA discovery after a failure, such as a forbidden or unreachable API.
var draRetryInterval = time.Minute

// draManager returns the DRA discovery that is running now, or nil while DRA
// setup has not succeeded.
func (p *PodMapper) draManager() *DRAResourceSliceManager {
	p.draMu.RLock()
	defer p.draMu.RUnlock()

	return p.ResourceSliceManager
}

// retryDRAIfEnabled starts the background DRA retry when DRA labels are on, so a
// setup failure of any kind, including missing in-cluster credentials, is
// retried instead of leaving DRA off until restart.
func (p *PodMapper) retryDRAIfEnabled(onDRAChange func()) {
	if p.Config.KubernetesEnableDRA {
		go p.retryDRA(onDRAChange)
	}
}

// retryDRA keeps trying to start DRA discovery after the first attempt failed,
// until it works or the mapper stops. DRA labels appear once it works. The
// failure is logged at most every sourceWarningInterval.
func (p *PodMapper) retryDRA(onDRAChange func()) {
	var warnings warningLimiter

	ticker := time.NewTicker(draRetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopChan:
			return
		case <-ticker.C:
		}

		manager, err := newDRAResourceSliceManagerFunc(p.ctx, onDRAChange)
		if err != nil {
			if warnings.allow() {
				slog.Warn("DRA pod labels are still not available; the exporter keeps trying",
					slog.String(logging.ErrorKey, err.Error()))
			}

			continue
		}

		if !p.installDRAManager(manager) {
			return
		}

		slog.Info("Started DRAResourceSliceManager after an earlier failure; DRA pod labels are now available")
		return
	}
}

// installDRAManager publishes a manager that started late. A mapper that was
// stopped in the meantime stops the manager instead and reports false.
func (p *PodMapper) installDRAManager(manager *DRAResourceSliceManager) bool {
	p.draMu.Lock()
	defer p.draMu.Unlock()

	select {
	case <-p.stopChan:
		manager.Stop()
		return false
	default:
	}

	p.ResourceSliceManager = manager

	return true
}
