package host

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/stretchr/testify/require"
)

// runStartWithTLSEnabledAndBasicAuth verifies host-mode TLS and basic-auth behavior.
func runStartWithTLSEnabledAndBasicAuth(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	port := getRandomAvailablePort(t)
	startExporterProcess(
		t,
		"--collectors", "./testdata/default-counters.csv",
		"--address", fmt.Sprintf(":%d", port),
		"--web-config-file", "./testdata/web-config.yml",
	)

	status, err := retry.DoWithData(
		func() (int, error) {
			_, status, err := httpGet(t, fmt.Sprintf("http://localhost:%d/metrics", port))
			if err != nil {
				return -1, err
			}
			return status, nil
		},
		retry.Attempts(10),
		retry.MaxDelay(10*time.Second),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				//nolint:gosec // The test connects to a temporary self-signed local server.
				InsecureSkipVerify: true,
			},
		},
	}
	status, err = retry.DoWithData(
		func() (int, error) {
			req := newRequestWithBasicAuth(t, "alice", "password", http.MethodGet,
				fmt.Sprintf("https://localhost:%d/metrics", port), nil)
			resp, err := client.Do(req)
			if err != nil {
				return -1, err
			}
			defer resp.Body.Close()
			return resp.StatusCode, nil
		},
		retry.Attempts(10),
		retry.MaxDelay(10*time.Second),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	status, err = retry.DoWithData(
		func() (int, error) {
			req := newRequestWithBasicAuth(t, "alice", "bad password", http.MethodGet,
				fmt.Sprintf("https://localhost:%d/metrics", port), nil)
			resp, err := client.Do(req)
			if err != nil {
				return -1, err
			}
			defer resp.Body.Close()
			return resp.StatusCode, nil
		},
		retry.Attempts(10),
		retry.MaxDelay(10*time.Second),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, status)
}
