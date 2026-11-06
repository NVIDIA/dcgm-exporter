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

package appconfig

import (
	"strings"
	"time"
)

const (
	// FieldDCGMCleanupTimeout is the origin-tracking name of the DCGM cleanup timeout.
	FieldDCGMCleanupTimeout = "DCGMCleanupTimeout"
	// DCGMCleanupTimeoutKey is the version 2 key of the DCGM cleanup timeout.
	DCGMCleanupTimeoutKey = "sources.dcgm.cleanup_timeout"
)

// applyCleanupTimeout applies sources.dcgm.cleanup_timeout. An empty value
// changes nothing.
func (s *YAMLDCGMSource) applyCleanupTimeout(config *Config, rec originRecorder) error {
	value := strings.TrimSpace(s.CleanupTimeout)
	if value == "" {
		return nil
	}

	timeout, err := parsePositiveDuration(value, DCGMCleanupTimeoutKey)
	if err != nil {
		return err
	}

	config.DCGMCleanupTimeout = timeout
	rec.record(FieldDCGMCleanupTimeout, DCGMCleanupTimeoutKey)

	return nil
}

// EffectiveDCGMCleanupTimeout returns the DCGM cleanup timeout to wait with.
// configured is false when c is nil or holds no positive timeout, as a config
// built without validation can; the default is returned then.
func (c *Config) EffectiveDCGMCleanupTimeout() (timeout time.Duration, configured bool) {
	if c == nil || c.DCGMCleanupTimeout <= 0 {
		return DefaultDCGMCleanupTimeout, false
	}

	return c.DCGMCleanupTimeout, true
}
