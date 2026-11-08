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

package scenario

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/k8smarkers"
)

func TestWriteListMatchesFixture(t *testing.T) {
	want, err := os.ReadFile("testdata/list-scenarios.txt")
	if err != nil {
		t.Fatal(err)
	}

	var got bytes.Buffer
	if err := WriteList(&got); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("--list-scenarios output drifted from fixture\nwant:\n%s\ngot:\n%s", want, got.Bytes())
	}
}

func TestCatalogSelectorsAreUnique(t *testing.T) {
	if err := ValidateCatalog(Catalog); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{}, len(Catalog))
	for _, entry := range Catalog {
		selector := entry.Selector()
		if _, ok := seen[selector]; ok {
			t.Fatalf("duplicate scenario selector %q", selector)
		}
		seen[selector] = struct{}{}
	}
}

func TestValidateCatalogRejectsInvalidDependencyGroups(t *testing.T) {
	for _, test := range []Scenario{
		{Suite: SuiteHost, Name: "empty", ResultName: "empty"},
		{Suite: SuiteHost, Name: "wrong-suite", Group: GroupContainerBaseline, ResultName: "wrong-suite"},
		{Suite: SuiteHost, Name: "unknown", Group: "future-group", ResultName: "unknown"},
	} {
		if err := ValidateCatalog([]Scenario{test}); err == nil {
			t.Fatalf("ValidateCatalog(%#v) succeeded, want error", test)
		}
	}
}

func TestValidateCatalogRejectsEmptyResultName(t *testing.T) {
	test := Scenario{Suite: SuiteHost, Name: "missing-result", Group: GroupHostBaseline}
	if err := ValidateCatalog([]Scenario{test}); err == nil {
		t.Fatalf("ValidateCatalog(%#v) succeeded, want error", test)
	}
}

func TestValidateCatalogRejectsInvalidSuiteAndEmptyName(t *testing.T) {
	for _, test := range []struct {
		name    string
		entry   Scenario
		wantErr string
	}{
		{
			name:    "invalid suite",
			entry:   Scenario{Suite: Suite("future"), Name: "invalid-suite", Group: GroupHostBaseline, ResultName: "invalid-suite"},
			wantErr: `uses unknown suite "future"`,
		},
		{
			name:    "empty name",
			entry:   Scenario{Suite: SuiteHost, Group: GroupHostBaseline, ResultName: "missing-name"},
			wantErr: "has an empty name",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCatalog([]Scenario{test.entry})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateCatalog(%#v) error = %v, want substring %q", test.entry, err, test.wantErr)
			}
		})
	}
}

func TestValidateCatalogRejectsDuplicateSelector(t *testing.T) {
	test := Scenario{Suite: SuiteHost, Name: "duplicate", Group: GroupHostBaseline, ResultName: "duplicate"}
	if err := ValidateCatalog([]Scenario{test, test}); err == nil {
		t.Fatalf("ValidateCatalog() accepted duplicate selector %s", test.Selector())
	}
}

func TestAllSuitesCoversCatalog(t *testing.T) {
	suites := map[Suite]struct{}{}
	for _, suite := range AllSuites() {
		if !ValidSuite(suite) {
			t.Fatalf("AllSuites returned invalid suite %q", suite)
		}
		suites[suite] = struct{}{}
	}
	for _, entry := range Catalog {
		if _, ok := suites[entry.Suite]; !ok {
			t.Fatalf("%s uses suite %q absent from AllSuites", entry.Selector(), entry.Suite)
		}
	}
}

func TestNVLinkScenariosSeparateFeatureAndApplicability(t *testing.T) {
	want := map[string][]string{
		"nvlinkFabric":    {"host:profiling"},
		"nvlink":          nil,
		"nvlinkIntegrity": {"host:nvlink5"},
	}
	for name, applicability := range want {
		entry := MustFind(Catalog, SuiteK8s, name)
		if len(entry.Features) != 1 || entry.Features[0] != "host:nvlink_fabric" {
			t.Fatalf("%s features = %v", entry.Selector(), entry.Features)
		}
		if !sameStrings(entry.Applicability, applicability) {
			t.Fatalf("%s applicability = %v, want %v", entry.Selector(), entry.Applicability, applicability)
		}
	}
}

func TestNVSwitchScenariosUseProviderApplicability(t *testing.T) {
	want := map[string]string{
		"nvswitch":           "host:nvswitch_local_telemetry",
		"nvswitchLink":       "host:nvswitch_nscq",
		"nvswitchThroughput": "host:nvswitch_nvsdm",
	}
	for _, entry := range Catalog {
		gate, ok := want[entry.Name]
		if !ok {
			continue
		}
		if len(entry.Features) != 1 || entry.Features[0] != "host:nvswitch" {
			t.Fatalf("%s features = %v", entry.Selector(), entry.Features)
		}
		if len(entry.Applicability) != 1 || entry.Applicability[0] != gate {
			t.Fatalf("%s applicability = %v, want %s", entry.Selector(), entry.Applicability, gate)
		}
		delete(want, entry.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing NVSwitch scenarios: %v", want)
	}
}

func TestSetupControlledScenariosAreNotCapabilityGated(t *testing.T) {
	for _, name := range []string{"watchRetention", "remoteDcgm", "gpuOperatorChart"} {
		entry := MustFind(Catalog, SuiteK8s, name)
		if len(entry.Features) != 0 || len(entry.Applicability) != 0 {
			t.Fatalf(
				"%s should be setup-controlled, got features=%v applicability=%v",
				entry.Selector(),
				entry.Features,
				entry.Applicability,
			)
		}
	}
}

func TestWatchRetentionPrecedesOtherStandaloneBaselineScenarios(t *testing.T) {
	var names []string
	for _, entry := range Catalog {
		if entry.Suite == SuiteK8s && entry.Group == "standalone-baseline" {
			names = append(names, entry.Name)
		}
	}
	want := []string{"watchRetention", "remoteDcgm", "remoteDcgmRestart"}
	if !sameStrings(names, want) {
		t.Fatalf("standalone-baseline scenarios = %v, want %v", names, want)
	}
	if entry := MustFind(Catalog, SuiteK8s, "watchRetention"); !entry.Remote {
		t.Fatal("watchRetention must request the standalone DCGM group setup")
	}
}

func TestGinkgoMarkerNamesComeFromCatalog(t *testing.T) {
	for _, entry := range Catalog {
		if entry.Suite == SuiteStatic {
			continue
		}
		got, ok := entry.MarkerBaseName()
		if !ok || got == "" {
			t.Fatalf("%s marker base is empty", entry.Selector())
		}
	}
}

func TestResultMarkerCatalogDrift(t *testing.T) {
	names := MarkerBaseNames(Catalog, SuiteK8s)
	for _, entry := range Catalog {
		if entry.Suite != SuiteK8s {
			continue
		}

		want, ok := entry.MarkerBaseName()
		if !ok {
			t.Fatalf("%s has no marker base", entry.Selector())
		}
		if got := names[entry.ResultName]; got != want {
			t.Fatalf("marker base for %q = %q, want catalog marker %q", entry.ResultName, got, want)
		}
	}
}

func TestK8sResultMarkerBasesMatchCatalog(t *testing.T) {
	got := MarkerBaseNames(Catalog, SuiteK8s)
	if len(got) != len(k8smarkers.ResultMarkerBaseNames) {
		t.Fatalf("k8s marker map size = %d, catalog MarkerBaseNames = %d; update tests/k8smarkers", len(k8smarkers.ResultMarkerBaseNames), len(got))
	}
	for label, want := range got {
		if have := k8smarkers.ResultMarkerBaseNames[label]; have != want {
			t.Fatalf("k8s marker for %q = %q, want catalog %q; update tests/k8smarkers/markers.go", label, have, want)
		}
	}
}

// k8sDeploymentProfiles mirrors tests/k8s scenarioDeploymentProfiles names.
// Update both maps when adding a SuiteK8s catalog entry.
var k8sDeploymentProfiles = map[string]string{
	"default":                      "dedicated",
	"configMatrix":                 "dedicated",
	"configMapData":                "dedicated",
	"yamlConfig":                   "dedicated",
	"oldNamespace":                 "dedicated",
	"clockEventsCounters":          "dedicated",
	"tls":                          "dedicated",
	"basicAuth":                    "dedicated",
	"tlsBasicAuth":                 "dedicated",
	"defaultChartMounts":           "dedicated",
	"chartExistingConfig":          "dedicated",
	"chartUpgrade":                 "dedicated",
	"serviceAccess":                "dedicated",
	"metricsNetworkPolicy":         "dedicated",
	"podHardening":                 "dedicated",
	"podLabels":                    "dedicated",
	"podUID":                       "dedicated",
	"podLabelAllowlist":            "dedicated",
	"kubernetesGpuId":              "dedicated",
	"noHostname":                   "dedicated",
	"modelName":                    "dedicated",
	"pprof":                        "dedicated",
	"debugDump":                    "dedicated",
	"hpcJobMapping":                "dedicated",
	"customResourceNames":          "dedicated",
	"profiling":                    "hardware-basic-metrics",
	"p2pStatus":                    "hardware-basic-metrics",
	"fieldUnsupported":             "hardware-basic-metrics",
	"nvlinkFabric":                 "dedicated",
	"nvlink":                       "hardware-basic-metrics",
	"nvlinkIntegrity":              "hardware-basic-metrics",
	"mig":                          "dedicated",
	"migFullGpuSelection":          "dedicated",
	"migInstanceSelection":         "dedicated",
	"migSpecificInstanceSelection": "dedicated",
	"migCombinedDeviceSelection":   "dedicated",
	"migPodAttribution":            "dedicated",
	"dra":                          "dedicated",
	"sharedGpu":                    "dedicated",
	"nvswitch":                     "dedicated",
	"nvswitchLink":                 "dedicated",
	"nvswitchThroughput":           "dedicated",
	"graceCpu":                     "dedicated",
	"c2c":                          "dedicated",
	"watchRetention":               "dedicated",
	"remoteDcgm":                   "dedicated",
	"remoteDcgmRestart":            "dedicated",
	"remoteDcgmIPv6Hostname":       "dedicated",
	"gpuOperatorChart":             "dedicated",
	"gpuOperatorExporter":          "dedicated",
	"gpuOperatorSharedGpu":         "dedicated",
	"gpuOperatorMig":               "dedicated",
	"gpuOperatorDRA":               "dedicated",
	"gpuOperatorIPv6":              "dedicated",
	"gpuOperatorMainEmbedded":      "dedicated",
	"gpuOperatorMainRemote":        "dedicated",
	"gpuOperatorMainDRA":           "dedicated",
}

func TestK8sDeploymentProfilesCoverCatalog(t *testing.T) {
	for _, entry := range Catalog {
		if entry.Suite != SuiteK8s {
			continue
		}
		if _, ok := k8sDeploymentProfiles[entry.Name]; !ok {
			t.Fatalf("k8s scenario %q has no deployment profile", entry.Name)
		}
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
