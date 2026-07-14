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
	"net/http/pprof"
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

const (
	contentTypeOptionsHeader  = "X-Content-Type-Options"
	failedWriteResponseError  = "Failed to write response."
	internalServerError       = "internal server error"
	prometheusTextContentType = "text/plain; version=0.0.4; charset=utf-8"
	shutdownWaitTimeout       = 3 * time.Second
)

func NewMetricsServer(
	c *appconfig.Config,
	deviceWatchListManager devicewatchlistmanager.Manager,
	registry *registry.Registry,
) (*MetricsServer, func(), error) {
	router := mux.NewRouter()

	// Initialize file dumper
	fileDumper := debug.NewFileDumper(c.DumpConfig)
	readTimeout := timeoutOrDefault(c.WebReadTimeout, appconfig.DefaultWebReadTimeout)
	writeTimeout := timeoutOrDefault(c.WebWriteTimeout, appconfig.DefaultWebWriteTimeout)

	serverv1 := &MetricsServer{
		server: &http.Server{
			Addr:         c.Address,
			Handler:      router,
			ReadTimeout:  readTimeout,
			WriteTimeout: writeTimeout,
		},
		webConfig: &web.FlagConfig{
			WebListenAddresses: &[]string{c.Address},
			WebSystemdSocket:   &c.WebSystemdSocket,
			WebConfigFile:      &c.WebConfigFile,
		},
		metrics:                "",
		config:                 c,
		transformations:        transformation.GetTransformations(c),
		deviceWatchListManager: deviceWatchListManager,
		fileDumper:             fileDumper,
	}

	serverv1.registry.Store(registry)
	serverv1.reloadInProgress.Store(false)
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(contentTypeOptionsHeader, "nosniff")
		pprofHTML := ""
		if c.EnablePprof {
			pprofHTML = `
			<h2>Profiling (pprof)</h2>
			<ul>
				<li><a href="./debug/pprof/">Index</a></li>
				<li><a href="./debug/pprof/heap">Heap</a> - Memory allocations</li>
				<li><a href="./debug/pprof/goroutine">Goroutines</a> - Active goroutines</li>
				<li><a href="./debug/pprof/allocs">Allocations</a> - All memory allocations</li>
			</ul>`
		}
		_, err := w.Write([]byte(`<html>
			<head><title>GPU Exporter</title></head>
			<body>
			<h1>GPU Exporter</h1>
			<p><a href="./metrics">Metrics</a></p>
			<p><a href="./health">Health</a></p>` + pprofHTML + `
			</body>
			</html>`))
		if err != nil {
			slog.Error(failedWriteResponseError, slog.String(logging.ErrorKey, err.Error()))
			http.Error(w, internalServerError, http.StatusInternalServerError)
			return
		}
	})

	router.HandleFunc("/health", serverv1.Health)
	router.HandleFunc("/metrics", serverv1.Metrics)

	if c.EnablePprof {
		router.HandleFunc("/debug/pprof/", pprof.Index)
		router.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		router.HandleFunc("/debug/pprof/profile", pprof.Profile)
		router.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		router.HandleFunc("/debug/pprof/trace", pprof.Trace)
		router.Handle("/debug/pprof/heap", pprof.Handler("heap"))
		router.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
		router.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
		router.Handle("/debug/pprof/block", pprof.Handler("block"))
		router.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
		router.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))

		slog.Info("Profiling endpoints enabled at /debug/pprof/")
	}

	var podMapper *transformation.PodMapper
	for _, t := range serverv1.transformations {
		if pm, ok := t.(*transformation.PodMapper); ok {
			podMapper = pm
			break
		}
	}

	if podMapper != nil {
		go podMapper.Run()
	}

	cleanup := func() {
		if podMapper != nil {
			slog.Info("Stopping PodMapper")
			podMapper.Stop()
		}
		if podMapper != nil && c.KubernetesEnableDRA && podMapper.ResourceSliceManager != nil {
			slog.Info("Stopping ResourceSliceManager")
			podMapper.ResourceSliceManager.Stop()
		}
	}

	return serverv1, cleanup, nil
}

func timeoutOrDefault(timeout, defaultTimeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultTimeout
	}
	return timeout
}

func shutdownTimeout(writeTimeout time.Duration) time.Duration {
	return timeoutOrDefault(writeTimeout, appconfig.DefaultWebWriteTimeout)
}

// ClearRegistry removes the current registry and returns it for cleanup.
// After calling this, /metrics will return empty responses until SetRegistry is called.
func (s *MetricsServer) ClearRegistry() *registry.Registry {
	s.Lock()
	defer s.Unlock()

	return s.registry.Swap(nil)
}

// SetRegistry sets the new registry to serve metrics from.
// /metrics will now serve metrics from the new registry.
func (s *MetricsServer) SetRegistry(newRegistry *registry.Registry) {
	s.Lock()
	defer s.Unlock()

	s.registry.Store(newRegistry)
}

// SwapMetricsRuntime atomically replaces the registry and device topology used
// to gather and render metrics. It waits for in-flight scrapes to finish and
// returns the previous registry for cleanup.
func (s *MetricsServer) SwapMetricsRuntime(
	newRegistry *registry.Registry,
	newDeviceWatchListManager devicewatchlistmanager.Manager,
) *registry.Registry {
	s.Lock()
	defer s.Unlock()

	oldRegistry := s.registry.Swap(newRegistry)
	s.deviceWatchListManager = newDeviceWatchListManager
	return oldRegistry
}

// GetRegistry returns the current registry (atomic read).
// Returns an empty registry if nil (during hot reload/bind/unbind).
func (s *MetricsServer) GetRegistry() *registry.Registry {
	s.RLock()
	defer s.RUnlock()

	reg := s.registry.Load()
	if reg == nil {
		// This is expected during hot reload, bind, or unbind
		return registry.NewRegistry()
	}
	return reg
}

// SetReloadInProgress marks whether a hot reload is currently happening
// This can be exposed via /health endpoint
func (s *MetricsServer) SetReloadInProgress(inProgress bool) {
	s.reloadInProgress.Store(inProgress)
}

// IsReloadInProgress returns whether a hot reload is in progress
func (s *MetricsServer) IsReloadInProgress() bool {
	return s.reloadInProgress.Load()
}

func (s *MetricsServer) Run(ctx context.Context, stop chan interface{}) {
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
	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout(s.server.WriteTimeout))
	defer shutdownCancel()
	if err := s.server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Failed to shutdown HTTP server.", slog.String(logging.ErrorKey, err.Error()))
		s.fatal()
	}

	if err := utils.WaitWithTimeout(&httpwg, shutdownWaitTimeout); err != nil {
		slog.Error("Failed waiting for HTTP server to shutdown.", slog.String(logging.ErrorKey, err.Error()))
		s.fatal()
	}
}

func (s *MetricsServer) fatal() {
	os.Exit(1)
}

// Metrics gathers the current registry and serves Prometheus text exposition.
func (s *MetricsServer) Metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", prometheusTextContentType)
	w.Header().Set(contentTypeOptionsHeader, "nosniff")

	var buf bytes.Buffer
	if err := s.gatherAndRenderMetrics(r.Context(), &buf); err != nil {
		http.Error(w, internalServerError, http.StatusInternalServerError)

		return
	}

	_, err := w.Write(buf.Bytes())
	if err != nil {
		slog.Error(failedWriteResponseError, slog.String(logging.ErrorKey, err.Error()))
		http.Error(w, "failed to write response", http.StatusInternalServerError)

		return
	}
}

func (s *MetricsServer) gatherAndRenderMetrics(ctx context.Context, w io.Writer) error {
	// Keep the registry and device topology paired for the whole scrape. This
	// lets reload cleanup wait until rendering no longer references the old
	// runtime.
	s.RLock()
	defer s.RUnlock()

	currentRegistry := s.registry.Load()
	if currentRegistry == nil {
		currentRegistry = registry.NewRegistry()
	}
	deviceWatchListManager := s.deviceWatchListManager
	metricGroups, err := currentRegistry.Gather()
	if err != nil {
		slog.Error("Failed to gather metrics from collectors", slog.String(logging.ErrorKey, err.Error()))
		return err
	}
	return s.render(ctx, w, deviceWatchListManager, metricGroups)
}

// render transforms all gathered groups before rendering one combined metrics document.
func (s *MetricsServer) render(
	ctx context.Context,
	w io.Writer,
	deviceWatchListManager devicewatchlistmanager.Manager,
	metricGroups registry.MetricsByCounterGroup,
) error {
	renderableGroups := registry.MetricsByCounterGroup{}

	for group, metrics := range metricGroups {
		deviceWatchList, exists := deviceWatchListManager.EntityWatchList(group)
		if !exists {
			continue
		}

		// Write debug files before transformations so failures can reference inputs.
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

		slog.Debug(
			"Applying transformations",
			slog.String(logging.FieldEntityGroupKey, group.String()),
			slog.Int("metrics_count", len(metrics)),
			slog.Int("transformations_count", len(s.transformations)),
			slog.String("metrics_debug_file", metricsFile),
			slog.String("deviceinfo_debug_file", deviceInfoFile),
		)

		for _, transformation := range s.transformations {
			transformErr := transformation.Process(metrics, deviceWatchList.DeviceInfo())
			if transformErr != nil {
				slog.LogAttrs(
					ctx, slog.LevelError, "Failed to apply transformations on metrics",
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

		slog.Debug("Prepared metrics for rendering",
			slog.String(logging.FieldEntityGroupKey, group.String()),
			slog.Int("metrics_count", len(metrics)),
			slog.String("metrics_debug_file", metricsFile))
		renderableGroups[group] = metrics
	}

	// Render once across all groups so shared families emit one HELP/TYPE block.
	if err := rendermetrics.Render(w, renderableGroups); err != nil {
		slog.LogAttrs(
			ctx, slog.LevelError, "Failed to render metrics",
			slog.String(logging.ErrorKey, err.Error()),
			slog.Int("metric_group_count", len(renderableGroups)),
		)

		return err
	}

	return nil
}

func (s *MetricsServer) Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set(contentTypeOptionsHeader, "nosniff")

	// If reload in progress or registry is nil (during hot reload/bind/unbind),
	// still return 200 OK to prevent Kubernetes pod termination
	// Use headers to communicate status to monitoring systems
	if s.IsReloadInProgress() {
		w.Header().Set("X-Reload-In-Progress", "true")
	}

	// Check the raw atomic value to see if registry is nil
	if s.registry.Load() == nil {
		w.Header().Set("X-Registry-Available", "false")
		w.Header().Set("X-Reload-In-Progress", "true")
		_, _ = w.Write([]byte("OK - reload in progress"))
		return
	}

	w.Header().Set("X-Registry-Available", "true")
	_, err := w.Write([]byte("OK"))
	if err != nil {
		slog.Error(failedWriteResponseError, slog.String(logging.ErrorKey, err.Error()))
		http.Error(w, "failed to write response", http.StatusInternalServerError)
		return
	}
}

// DumpMetricsToJSON is a helper function for debugging that dumps all metrics to JSON
func (s *MetricsServer) DumpMetricsToJSON() ([]byte, error) {
	currentRegistry := s.GetRegistry()

	metricGroups, err := currentRegistry.Gather()
	if err != nil {
		return nil, err
	}

	// Marshal the entire metricGroups slice to include all metric groups
	if len(metricGroups) == 0 {
		return json.Marshal(map[string]any{"error": "no metrics found"})
	}

	return json.MarshalIndent(metricGroups, "", "  ")
}
