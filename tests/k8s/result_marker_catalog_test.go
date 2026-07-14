//go:build e2e

package k8s

import (
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

func TestResultMarkerCatalogDrift(t *testing.T) {
	names := scenario.MarkerBaseNames(scenario.Catalog, scenario.SuiteK8s)
	for _, entry := range scenario.Catalog {
		if entry.Suite != scenario.SuiteK8s {
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
