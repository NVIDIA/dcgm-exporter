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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultConfigBuildsAFreshValueEachTime(t *testing.T) {
	first := DefaultConfig()
	first.GPUDeviceOptions.Flex = false
	first.Address = ":1"

	second := DefaultConfig()
	assert.True(t, second.GPUDeviceOptions.Flex)
	assert.Equal(t, ":9400", second.Address)
	assert.Equal(t, DCGMDbgLvlNone, second.DCGMLogLevel)
	assert.Equal(t, MetricSourceFile, second.MetricSource.Kind)
	assert.Equal(t, DefaultCollectorsFile, second.MetricSource.File)
}

func TestDefaultConfigPassesItsOwnValueChecks(t *testing.T) {
	config := DefaultConfig()
	assert.Empty(t, ValidateValues(config, func(string) string { return "default" }))
	assert.Empty(t, ValidateCombinations(config, func(string) string { return "default" }))
	assert.NoError(t, ValidateWatchGroupPatterns(config.WatchGroups))
}
