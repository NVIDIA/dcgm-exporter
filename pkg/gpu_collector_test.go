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
	"fmt"
	"testing"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
	"github.com/stretchr/testify/require"
)

/*
func TestGetMetrics(t *testing.T) {
	err := dcgm.Init(dcgm.Embedded);
	require.NoError(t, err)

	collector := NewDCGMCollector()
	out, err := collector.GetMetrics()

	require.NoError(t, err)
	require.NotEmpty(t, out)
	t.Log(out)
}
*/

func TestFieldGroup(t *testing.T) {
	cleanup, err := dcgm.Init(dcgm.Embedded);
	require.NoError(t, err)
	defer cleanup()

	c, cleanup, err := NewDCGMCollector([]DCGMField{
		{dcgm.DCGM_FI_DEV_GPU_TEMP, "DCGM_FI_DEV_GPU_TEMP", "gauge", "Renaud"},
		{dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", "gauge", "Renaud"},
		{dcgm.DCGM_FI_DEV_POWER_USAGE, "DCGM_FI_DEV_POWER_USAGE", "gauge", "Renaud"},
	})
	require.NoError(t, err)
	defer cleanup()

	out, err := c.GetMetrics()
	require.NoError(t, err)
	fmt.Println(out)
}
