/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/exporter-toolkit/web"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/rendermetrics"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/transformation"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

const internalServerError = "internal server error"

func NewMetricsServer(
	c *appconfig.Config,
	metrics chan string,
	deviceWatchListManager devicewatchlistmanager.Manager,
	registry *registry.Registry,
) (*MetricsServer, func(), error) {
	router := mux.NewRouter()
	serverv1 := &MetricsServer{
		server: &http.Server{
			Addr:         c.Address,
			Handler:      router,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
		},
		webConfig: &web.FlagConfig{
			WebListenAddresses: &[]string{c.Address},
			WebSystemdSocket:   &c.WebSystemdSocket,
			WebConfigFile:      &c.WebConfigFile,
		},
		metricsChan:            metrics,
		metrics:                "",
		registry:               registry,
		config:                 c,
		transformations:        transformation.GetTransformations(c),
		deviceWatchListManager: deviceWatchListManager,
	}

	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`<html>
			<head><title>GPU Exporter</title></head>
			<body>
			<h1>GPU Exporter</h1>
			<p><a href="./metrics">Metrics</a></p>
			</body>
			</html>`))
		if err != nil {
			slog.Error("Failed to write response.", slog.String(logging.ErrorKey, err.Error()))
			http.Error(w, internalServerError, http.StatusInternalServerError)
			return
		}
	})

	router.HandleFunc("/health", serverv1.Health)
	router.HandleFunc("/metrics", serverv1.Metrics)

	return serverv1, func() {}, nil
}

func (s *MetricsServer) Run(ctx context.Context, stop chan interface{}, wg *sync.WaitGroup) {
	defer wg.Done()

	var httpwg sync.WaitGroup
	httpwg.Add(1)
	go func() {
		defer httpwg.Done()
		slog.Info("Starting webserver")
		if err := web.ListenAndServe(s.server, s.webConfig, slog.Default()); err != nil && err != http.ErrServerClosed {
			slog.Error("Failed to Listen and Server HTTP server.", slog.String(logging.ErrorKey, err.Error()))
			os.Exit(1)
		}
	}()

	httpwg.Add(1)
	go func() {
		defer httpwg.Done()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	<-stop
	if err := s.server.Shutdown(ctx); err != nil {
		slog.Error("Failed to shutdown HTTP server.", slog.String(logging.ErrorKey, err.Error()))
		s.fatal()
	}

	if err := utils.WaitWithTimeout(&httpwg, 3*time.Second); err != nil {
		slog.Error("Failed waiting for HTTP server to shutdown.", slog.String(logging.ErrorKey, err.Error()))
		s.fatal()
	}
}

func (s *MetricsServer) fatal() {
	os.Exit(1)
}

func (s *MetricsServer) Metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	metricGroups, err := s.registry.Gather()
	if err != nil {
		slog.Error("Failed to gather metrics from collectors", slog.String(logging.ErrorKey, err.Error()))
		http.Error(w, internalServerError, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	err = s.render(&buf, metricGroups)
	if err != nil {
		http.Error(w, internalServerError, http.StatusInternalServerError)
		return
	}
	_, err = w.Write(buf.Bytes())
	if err != nil {
		slog.Error("Failed to write response.", slog.String(logging.ErrorKey, err.Error()))
		http.Error(w, "failed to write response", http.StatusInternalServerError)
		return
	}
}

func (s *MetricsServer) render(w io.Writer, metricGroups registry.MetricsByCounterGroup) error {
	for group, metrics := range metricGroups {
		deviceWatchList, exists := s.deviceWatchListManager.EntityWatchList(group)
		if exists {
			for _, transformation := range s.transformations {
				err := transformation.Process(metrics, deviceWatchList.DeviceInfo())
				if err != nil {
					slog.LogAttrs(context.Background(), slog.LevelError, "Failed to apply transformations on metrics",
						slog.String(logging.ErrorKey, err.Error()),
						slog.String(logging.FieldEntityGroupKey, group.String()),
						slog.Any(logging.MetricsKey, metrics),
						slog.Any(logging.DeviceInfoKey, deviceWatchList.DeviceInfo),
					)
					return err
				}
			}

			err := rendermetrics.RenderGroup(w, group, metrics)
			if err != nil {
				slog.LogAttrs(context.Background(), slog.LevelError, "Failed to renderGroup metrics",
					slog.String(logging.ErrorKey, err.Error()),
					slog.String(logging.FieldEntityGroupKey, group.String()),
					slog.Any(logging.MetricsKey, metrics),
					slog.Any(logging.DeviceInfoKey, deviceWatchList.DeviceInfo),
				)
				return err
			}
		}
	}
	return nil
}

func (s *MetricsServer) Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, err := w.Write([]byte("KO"))
	if err != nil {
		slog.Error("Failed to write response.", slog.String(logging.ErrorKey, err.Error()))
		http.Error(w, "failed to write response", http.StatusInternalServerError)
	}
}
