package integration

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/pkg/cmd"
)

func TestStartWithTLSEnabledAndBasicAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	app := cmd.NewApp()
	args := os.Args[0:1]
	args = append(args, "-f=./testdata/default-counters.csv") // Append a file with default counters
	port := getRandomAvailablePort(t)
	args = append(args, fmt.Sprintf("-a=:%d", port))
	args = append(args, "--web-config-file=./testdata/web-config.yml")
	ctx, cancel := context.WithCancel(context.Background())
	go func(ctx context.Context) {
		err := app.Run(args)
		require.NoError(t, err)
	}(ctx)

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
	cancel()
}
