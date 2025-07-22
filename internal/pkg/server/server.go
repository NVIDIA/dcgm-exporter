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
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/exporter-toolkit/web"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/debug"
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

	// Initialize file dumper
	fileDumper := debug.NewFileDumper(c.DumpConfig)

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
		fileDumper:             fileDumper,
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

	var podMapper *transformation.PodMapper
	for _, t := range serverv1.transformations {
		if pm, ok := t.(*transformation.PodMapper); ok {
			podMapper = pm
			break
		}
	}

	cleanup := func() {
		if podMapper != nil && c.KubernetesEnableDRA && podMapper.ResourceSliceManager != nil {
			slog.Info("Stopping ResourceSliceManager")
			podMapper.ResourceSliceManager.Stop()
		}
	}

	return serverv1, cleanup, nil
}

func (s *MetricsServer) Run(ctx context.Context, stop chan interface{}, wg *sync.WaitGroup) {
	defer wg.Done()

	var httpwg sync.WaitGroup
	httpwg.Add(1)
	go func() {
		defer httpwg.Done()
		slog.Info("Starting webserver")

		// Log dump configuration information
		if s.config.DumpConfig.Enabled {
			slog.Info("Debug dumps enabled - runtime objects may be written to files for troubleshooting",
				slog.String("dump_directory", s.config.DumpConfig.Directory),
				slog.Int("retention_hours", s.config.DumpConfig.Retention),
				slog.Bool("compression_enabled", s.config.DumpConfig.Compression),
				slog.String("note", "Debug files may be created during operation and cleaned up automatically"))
		} else {
			slog.Debug("Debug dumps disabled - use --dump-enabled flag to enable file-based debugging")
		}

		if err := web.ListenAndServe(s.server, s.webConfig, slog.Default()); err != nil && err != http.ErrServerClosed {
			slog.Error("Failed to Listen and Server HTTP server.", slog.String(logging.ErrorKey, err.Error()))
			os.Exit(1)
		}
	}()

	httpwg.Add(1)
	go func() {
		defer httpwg.Done()
		// Cleanup old debug files periodically
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.fileDumper != nil {
					if err := s.fileDumper.CleanupOldFiles(); err != nil {
						slog.Warn("Failed to cleanup old debug files", slog.String(logging.ErrorKey, err.Error()))
					}
				}
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

			// Write debug files and log references
			var metricsFile, deviceInfoFile string
			var err error

			if s.fileDumper != nil {
				metricsFile, err = s.fileDumper.DumpToFile(metrics, "metrics", group.String())
				if err != nil {
					slog.Warn("Failed to write metrics debug file",
						slog.String(logging.ErrorKey, err.Error()),
						slog.String(logging.FieldEntityGroupKey, group.String()))
				}

				deviceInfoFile, err = s.fileDumper.DumpToFile(deviceWatchList.DeviceInfo(), "deviceinfo", group.String())
				if err != nil {
					slog.Warn("Failed to write device info debug file",
						slog.String(logging.ErrorKey, err.Error()),
						slog.String(logging.FieldEntityGroupKey, group.String()))
				}
			}

			// Log summary information with file references
			slog.Debug("Applying transformations",
				slog.String(logging.FieldEntityGroupKey, group.String()),
				slog.Int("metrics_count", len(metrics)),
				slog.Int("transformations_count", len(s.transformations)),
				slog.String("metrics_debug_file", metricsFile),
				slog.String("deviceinfo_debug_file", deviceInfoFile),
			)

			for _, transformation := range s.transformations {
				transformErr := transformation.Process(metrics, deviceWatchList.DeviceInfo())
				if transformErr != nil {
					slog.LogAttrs(context.Background(), slog.LevelError, "Failed to apply transformations on metrics",
						slog.String(logging.ErrorKey, transformErr.Error()),
						slog.String(logging.FieldEntityGroupKey, group.String()),
						slog.String("transformation", transformation.Name()),
						slog.Int("metrics_count", len(metrics)),
						slog.String("metrics_debug_file", metricsFile),
						slog.String("deviceinfo_debug_file", deviceInfoFile),
					)
					return transformErr
				}
			}
			slog.Debug("Rendering metrics",
				slog.String(logging.FieldEntityGroupKey, group.String()),
				slog.Int("metrics_count", len(metrics)),
				slog.String("metrics_debug_file", metricsFile))
			err = rendermetrics.RenderGroup(w, group, metrics)
			if err != nil {
				slog.LogAttrs(context.Background(), slog.LevelError, "Failed to renderGroup metrics",
					slog.String(logging.ErrorKey, err.Error()),
					slog.String(logging.FieldEntityGroupKey, group.String()),
					slog.Int("metrics_count", len(metrics)),
					slog.String("metrics_debug_file", metricsFile),
					slog.String("deviceinfo_debug_file", deviceInfoFile),
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

// DumpMetricsToJSON is a helper function for debugging that dumps all metrics to JSON
func (s *MetricsServer) DumpMetricsToJSON() ([]byte, error) {
	metricGroups, err := s.registry.Gather()
	if err != nil {
		return nil, err
	}

	// Marshal the entire metricGroups slice to include all metric groups
	if len(metricGroups) == 0 {
		return json.Marshal(map[string]any{"error": "no metrics found"})
	}

	return json.MarshalIndent(metricGroups, "", "  ")
}
