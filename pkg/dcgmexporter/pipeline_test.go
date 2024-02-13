/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmexporter

import (
	"github.com/sirupsen/logrus"
	"os"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	cleanup, err := dcgm.Init(dcgm.Embedded)
	require.NoError(t, err)
	defer cleanup()

	c, cleanup := testDCGMGPUCollector(t, sampleCounters)
	defer cleanup()

	p, cleanup, err := NewMetricsPipelineWithGPUCollector(&Config{}, c)
	require.NoError(t, err)
	defer cleanup()
	require.NoError(t, err)

	out, err := p.run()
	require.NoError(t, err)
	require.NotEmpty(t, out)

	// Note it is pretty difficult to make non superficial tests without
	// writting a full blown parser, always look at the results
	// We'll be testing them more throughly in the e2e tests (e.g: by running prometheus).
	t.Logf("Pipeline result is:\n%v", out)
}

func testNewDCGMCollector(t *testing.T,
	counter *int, enabledCollector map[dcgm.Field_Entity_Group]struct{},
) DCGMCollectorConstructor {
	t.Helper()
	return func(c []Counter,
		hostname string,
		fieldEntityGroupTypeSystemInfo FieldEntityGroupTypeSystemInfoItem) (*DCGMCollector, func()) {
		// should always create GPU Collector
		if fieldEntityGroupTypeSystemInfo.SystemInfo.InfoType != dcgm.FE_GPU {
			if _, ok := enabledCollector[fieldEntityGroupTypeSystemInfo.SystemInfo.InfoType]; !ok {
				t.Errorf("collector '%s' should not be created", fieldEntityGroupTypeSystemInfo.SystemInfo.InfoType)
				return nil, func() {}
			}
		}

		collector := &DCGMCollector{}
		cleanups := []func(){
			func() {
				*counter++
			},
		}
		collector.Cleanups = cleanups

		return collector, func() { collector.Cleanup() }
	}
}

func TestCountPipelineCleanup(t *testing.T) {
	f, err := os.CreateTemp("", "empty.*.csv")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	defer f.Close()

	for _, c := range []struct {
		name             string
		enabledCollector map[dcgm.Field_Entity_Group]struct{}
	}{{
		name: "only_gpu",
		enabledCollector: map[dcgm.Field_Entity_Group]struct{}{
			dcgm.FE_GPU: struct{}{},
		},
	}, {
		name: "gpu_switch",
		enabledCollector: map[dcgm.Field_Entity_Group]struct{}{
			dcgm.FE_SWITCH: struct{}{},
		},
	}, {
		name: "gpu_link",
		enabledCollector: map[dcgm.Field_Entity_Group]struct{}{
			dcgm.FE_LINK: struct{}{},
		},
	}, {
		name: "gpu_cpu",
		enabledCollector: map[dcgm.Field_Entity_Group]struct{}{
			dcgm.FE_CPU: struct{}{},
		},
	}, {
		name: "gpu_core",
		enabledCollector: map[dcgm.Field_Entity_Group]struct{}{
			dcgm.FE_CPU_CORE: struct{}{},
		},
	}, {
		name: "gpu_switch_link",
		enabledCollector: map[dcgm.Field_Entity_Group]struct{}{
			dcgm.FE_SWITCH: struct{}{},
			dcgm.FE_LINK:   struct{}{},
		},
	}, {
		name: "gpu_cpu_core",
		enabledCollector: map[dcgm.Field_Entity_Group]struct{}{
			dcgm.FE_CPU:      struct{}{},
			dcgm.FE_CPU_CORE: struct{}{},
		},
	}, {
		name: "all",
		enabledCollector: map[dcgm.Field_Entity_Group]struct{}{
			dcgm.FE_SWITCH:   struct{}{},
			dcgm.FE_LINK:     struct{}{},
			dcgm.FE_CPU:      struct{}{},
			dcgm.FE_CPU_CORE: struct{}{},
		},
	}} {

		t.Run(c.name, func(t *testing.T) {
			cleanupCounter := 0

			config := &Config{
				Kubernetes:     false,
				ConfigMapData:  undefinedConfigMapData,
				CollectorsFile: f.Name(),
			}

			cc, err := GetCounterSet(config)
			if err != nil {
				logrus.Fatal(err)
			}

			fieldEntityGroupTypeSystemInfo := NewEntityGroupTypeSystemInfo(cc.DCGMCounters, config)

			for egt := range c.enabledCollector {
				// We inject system info for unit test purpose
				fieldEntityGroupTypeSystemInfo.items[egt] = FieldEntityGroupTypeSystemInfoItem{
					SystemInfo: SystemInfo{
						InfoType: egt,
					},
					Config: config,
				}
			}

			_, cleanup, err := NewMetricsPipeline(config,
				cc.DCGMCounters,
				"",
				testNewDCGMCollector(t, &cleanupCounter, c.enabledCollector),
				fieldEntityGroupTypeSystemInfo)
			require.NoError(t, err, "case: %s failed", c.name)

			cleanup()
			require.Equal(t, len(c.enabledCollector), cleanupCounter, "case: %s failed", c.name)
		})
	}
}
