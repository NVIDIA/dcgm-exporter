package host

import (
	"fmt"
	"net/http"
	"path/filepath"
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
		"--collectors", defaultCollectorsFile(t),
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

	client := newTLSTestClient()
	status, err = retry.DoWithData(
		func() (int, error) {
			req := newRequestWithBasicAuth(t, webConfigUser, webConfigPassword, http.MethodGet,
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
			req := newRequestWithBasicAuth(t, webConfigUser, "bad password", http.MethodGet,
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

// runTLSEffect runs the exporter with a web config file, as the flag and as
// the config key, and checks each run refuses plain HTTP, serves the baseline
// families over HTTPS with basic auth, and matches the other form.
func runTLSEffect(t testing.TB) {
	base := startSettingBaseline(t)
	runSettingForms(t, base, []settingForm{
		{
			name: "--web-config-file",
			args: func(t testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--web-config-file", webConfigPath(t))
			},
			check:  checkTLSServesBaseline,
			target: tlsTarget,
		},
		{
			name: "server.web_config_file",
			args: func(t testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--config-file", writeSettingConfig(t, map[string]any{
					"version": 2,
					"server":  map[string]any{"web_config_file": webConfigPath(t)},
				}))
			},
			check:  checkTLSServesBaseline,
			target: tlsTarget,
		},
	})
}

// checkTLSServesBaseline checks plain HTTP on the run's port gets 400, which
// the baseline's plain scrape never does, and the HTTPS scrape has the
// baseline's DCGM families.
func checkTLSServesBaseline(t testing.TB, base *settingBaseline, run settingRun) {
	t.Helper()
	_, status, err := fetchRaw(plainTarget(run.port).url)
	require.NoError(t, err)
	require.Equalf(t, http.StatusBadRequest, status, "plain HTTP on port %d must get 400 once TLS is on", run.port)

	checkSameDCGMShapeAsBaseline(t, base, run)
}

// webConfigPath is the absolute path of the TLS and basic auth web config
// that host/startupTLS also uses.
func webConfigPath(t testing.TB) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", "web-config.yml"))
	require.NoError(t, err)
	return path
}
