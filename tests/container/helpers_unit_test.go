//go:build container

package container

import "testing"

func TestLocalHTTPURLUsesIPv4Loopback(t *testing.T) {
	if got := localHTTPURL(9400, "/metrics"); got != "http://127.0.0.1:9400/metrics" {
		t.Fatalf("localHTTPURL() = %q", got)
	}
}
