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
	"testing"
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

func TestWriteRowsMatchesCatalogFixture(t *testing.T) {
	want, err := os.ReadFile("testdata/catalog.txt")
	if err != nil {
		t.Fatal(err)
	}

	var got bytes.Buffer
	if err := WriteRows(&got); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("catalog rows drifted from fixture\nwant:\n%s\ngot:\n%s", want, got.Bytes())
	}
}

func TestCatalogSelectorsAreUnique(t *testing.T) {
	seen := make(map[string]struct{}, len(Catalog))
	for _, entry := range Catalog {
		selector := entry.Selector()
		if _, ok := seen[selector]; ok {
			t.Fatalf("duplicate scenario selector %q", selector)
		}
		seen[selector] = struct{}{}
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

func TestFailureNvlinkHealthUsesFieldSpecificGate(t *testing.T) {
	for _, entry := range Catalog {
		if entry.Name != "failureNvlinkHealth" {
			continue
		}
		if len(entry.ExtraGates) != 0 {
			t.Fatalf("failureNvlinkHealth ExtraGates = %v, want none", entry.ExtraGates)
		}
		if len(entry.Capabilities) != 2 || entry.Capabilities[0] != "dcgm:failure_injection_nvlink_crc" || entry.Capabilities[1] != "cluster:standalone_dcgm_resources" {
			t.Fatalf("failureNvlinkHealth Capabilities = %v", entry.Capabilities)
		}
		return
	}
	t.Fatal("failureNvlinkHealth scenario not found")
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
