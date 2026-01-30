package integration

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/pkg/cmd"
)

func TestStartWithTLSEnabledAndBasicAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	port := getRandomAvailablePort(t)

	// Create test signal source for proper cleanup
	testSigs := cmd.NewTestSignalSource()

	// Create CLI context with TLS config
	app := cmd.NewApp()
	set := flag.NewFlagSet("test", 0)
	for _, f := range app.Flags {
		switch flag := f.(type) {
		case *cli.StringFlag:
			set.String(flag.Name, flag.Value, flag.Usage)
		case *cli.BoolFlag:
			set.Bool(flag.Name, flag.Value, flag.Usage)
		case *cli.IntFlag:
			set.Int(flag.Name, flag.Value, flag.Usage)
		}
	}
	require.NoError(t, set.Set("collectors", "./testdata/default-counters.csv"))
	require.NoError(t, set.Set("address", fmt.Sprintf(":%d", port)))
	require.NoError(t, set.Set("web-config-file", "./testdata/web-config.yml"))
	cliCtx := cli.NewContext(app, set, nil)

	// Run exporter with test signal source in goroutine
	appDone := make(chan error, 1)
	go func() {
		err := cmd.StartDCGMExporterWithSignalSource(cliCtx, testSigs)
		appDone <- err
	}()

	// Ensure cleanup happens even if test fails
	defer func() {
		t.Log("Sending termination signal for cleanup...")
		testSigs.SendSignal(syscall.SIGTERM)
		select {
		case <-appDone:
			t.Log("App shutdown completed")
		case <-time.After(10 * time.Second):
			t.Log("Warning: App did not shutdown within timeout")
		}
	}()

	t.Run("server returns 400 if request uses HTTP and TLS enabled on the server",
		func(t *testing.T) {
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
		})

	t.Run("server returns 200 when request uses HTTPS and valid password", func(t *testing.T) {
		// Create a custom client with TLS configuration
		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		}
		status, err := retry.DoWithData(
			func() (int, error) {
				req := newRequestWithBasicAuth(t, "alice", "password", http.MethodGet,
					fmt.Sprintf("https://localhost:%d/metrics", port), nil)
				resp, err := client.Do(req)
				if err != nil {
					return -1, err
				}
				return resp.StatusCode, nil
			},
			retry.Attempts(10),
			retry.MaxDelay(10*time.Second),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
	})

	t.Run("server returns 401 when request uses HTTPS and password is invalid", func(t *testing.T) {
		// Create a custom client with TLS configuration
		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		}
		status, err := retry.DoWithData(
			func() (int, error) {
				req := newRequestWithBasicAuth(t, "alice", "bad password", http.MethodGet,
					fmt.Sprintf("https://localhost:%d/metrics", port), nil)
				resp, err := client.Do(req)
				if err != nil {
					return -1, err
				}
				return resp.StatusCode, nil
			},
			retry.Attempts(10),
			retry.MaxDelay(10*time.Second),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, status)
	})
}
