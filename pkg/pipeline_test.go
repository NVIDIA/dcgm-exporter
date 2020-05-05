/*
 * Copyright (c) 2020, NVIDIA CORPORATION.  All rights reserved.
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

package main

import (
	"testing"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	cleanup, err := dcgm.Init(dcgm.Embedded)
	require.NoError(t, err)
	defer cleanup()

	c, cleanup := testDCGMCollector(t, sampleCounters)
	defer cleanup()

	p, cleanup, err := NewMetricsPipelineWithGPUCollector(&Config{}, c)
	defer cleanup()

	out, err := p.run()
	require.NoError(t, err)
	require.NotEmpty(t, out)

	// Note it is pretty difficult to make non superficial tests without
	// writting a full blown parser, always look at the results
	// We'll be testing them more throughly in the e2e tests (e.g: by running prometheus).
	t.Logf("Pipeline result is:\n%v", out)
}
