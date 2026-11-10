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
package k8s

import "testing"

// deploymentProfile identifies whether a scenario owns or shares its exporter deployment.
type deploymentProfile string

const (
	dedicatedDeployment         deploymentProfile = "dedicated"
	hardwareBasicMetricsProfile deploymentProfile = "hardware-basic-metrics"
)

// scenarioDeploymentProfiles maps k8s scenario names to deployment ownership.
// Catalog coverage is asserted in internal/e2e/scenario.
var scenarioDeploymentProfiles = map[string]deploymentProfile{
	"default":                      dedicatedDeployment,
	"configMatrix":                 dedicatedDeployment,
	"configMapData":                dedicatedDeployment,
	"yamlConfig":                   dedicatedDeployment,
	"oldNamespace":                 dedicatedDeployment,
	"clockEventsCounters":          dedicatedDeployment,
	"tls":                          dedicatedDeployment,
	"basicAuth":                    dedicatedDeployment,
	"tlsBasicAuth":                 dedicatedDeployment,
	"defaultChartMounts":           dedicatedDeployment,
	"chartExistingConfig":          dedicatedDeployment,
	"chartUpgrade":                 dedicatedDeployment,
	"serviceAccess":                dedicatedDeployment,
	"metricsNetworkPolicy":         dedicatedDeployment,
	"podHardening":                 dedicatedDeployment,
	"podLabels":                    dedicatedDeployment,
	"podUID":                       dedicatedDeployment,
	"podLabelAllowlist":            dedicatedDeployment,
	"kubernetesGpuId":              dedicatedDeployment,
	"noHostname":                   dedicatedDeployment,
	"modelName":                    dedicatedDeployment,
	"pprof":                        dedicatedDeployment,
	"debugDump":                    dedicatedDeployment,
	"hpcJobMapping":                dedicatedDeployment,
	"customResourceNames":          dedicatedDeployment,
	"profiling":                    hardwareBasicMetricsProfile,
	"p2pStatus":                    hardwareBasicMetricsProfile,
	"fieldUnsupported":             hardwareBasicMetricsProfile,
	"nvlinkFabric":                 dedicatedDeployment,
	"nvlink":                       hardwareBasicMetricsProfile,
	"nvlinkIntegrity":              hardwareBasicMetricsProfile,
	"mig":                          dedicatedDeployment,
	"migFullGpuSelection":          dedicatedDeployment,
	"migInstanceSelection":         dedicatedDeployment,
	"migSpecificInstanceSelection": dedicatedDeployment,
	"migCombinedDeviceSelection":   dedicatedDeployment,
	"migPodAttribution":            dedicatedDeployment,
	"dra":                          dedicatedDeployment,
	"sharedGpu":                    dedicatedDeployment,
	"nvswitch":                     dedicatedDeployment,
	"nvswitchLink":                 dedicatedDeployment,
	"nvswitchThroughput":           dedicatedDeployment,
	"graceCpu":                     dedicatedDeployment,
	"c2c":                          dedicatedDeployment,
	"watchRetention":               dedicatedDeployment,
	"remoteDcgm":                   dedicatedDeployment,
	"remoteDcgmRestart":            dedicatedDeployment,
	"remoteDcgmIPv6Hostname":       dedicatedDeployment,
	"gpuOperatorChart":             dedicatedDeployment,
	"gpuOperatorExporter":          dedicatedDeployment,
	"gpuOperatorSharedGpu":         dedicatedDeployment,
	"gpuOperatorMig":               dedicatedDeployment,
	"gpuOperatorDRA":               dedicatedDeployment,
	"gpuOperatorIPv6":              dedicatedDeployment,
	"gpuOperatorMainEmbedded":      dedicatedDeployment,
	"gpuOperatorMainRemote":        dedicatedDeployment,
	"gpuOperatorMainDRA":           dedicatedDeployment,
}

// TestHardwareBasicMetricsSharedProfiles keeps compatible scenarios on one deployment.
func TestHardwareBasicMetricsSharedProfiles(t *testing.T) {
	wantShared := map[string]struct{}{
		"profiling":        {},
		"nvlink":           {},
		"nvlinkIntegrity":  {},
		"p2pStatus":        {},
		"fieldUnsupported": {},
	}
	for name, profile := range scenarioDeploymentProfiles {
		_, shouldShare := wantShared[name]
		if shouldShare && profile != hardwareBasicMetricsProfile {
			t.Fatalf("scenario %q profile = %q, want %q", name, profile, hardwareBasicMetricsProfile)
		}
		if !shouldShare && profile != dedicatedDeployment {
			t.Fatalf("scenario %q profile = %q, want %q", name, profile, dedicatedDeployment)
		}
	}
}
