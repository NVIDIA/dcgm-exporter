/*
 * Copyright (c) 2023, NVIDIA CORPORATION.  All rights reserved.
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

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/pkg/cmd"
)

func TestStartAndReadMetrics(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	app := cmd.NewApp()
	args := os.Args[0:1]
	args = append(args, "-f=./testdata/default-counters.csv") // Append a file with default counters
	port := getRandomAvailablePort(t)
	args = append(args, fmt.Sprintf("-a=:%d", port))
	ctx, cancel := context.WithCancel(context.Background())
	go func(ctx context.Context) {
		err := app.Run(args)
		require.NoError(t, err)
	}(ctx)

	t.Logf("Read metrics from http://localhost:%d/metrics", port)

	metricsResp, _ := retry.DoWithData(
		func() (string, error) {
			metricsResp, _, err := httpGet(t, fmt.Sprintf("http://localhost:%d/metrics", port))
			if err != nil {
				return "", err
			}

			if len(metricsResp) == 0 {
				return "", errors.New("empty response")
			}
			return metricsResp, nil
		},
		retry.Attempts(10),
		retry.MaxDelay(10*time.Second),
	)

	require.NotEmpty(t, metricsResp)
	var parser expfmt.TextParser
	mf, err := parser.TextToMetricFamilies(strings.NewReader(metricsResp))
	require.NoError(t, err)
	require.Greater(t, len(mf), 0, "expected number of metrics more than 0")
	cancel()
}
