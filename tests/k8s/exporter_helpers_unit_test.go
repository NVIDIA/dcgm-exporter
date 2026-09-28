//go:build e2e

package k8s

import "testing"

func TestLocalPortForwardURLUsesIPv4Loopback(t *testing.T) {
	if got := localPortForwardURL("https", 9400, "/metrics"); got != "https://127.0.0.1:9400/metrics" {
		t.Fatalf("localPortForwardURL() = %q", got)
	}
}
