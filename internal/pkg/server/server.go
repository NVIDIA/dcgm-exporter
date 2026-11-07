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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/gorilla/mux"
	"github.com/prometheus/exporter-toolkit/web"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/debug"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/rendermetrics"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/transformation"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

const (
	contentSecurityPolicy         = "default-src 'none'; frame-ancestors 'none'"
	contentSecurityPolicyHeader   = "Content-Security-Policy"
	contentTypeOptionsHeader      = "X-Content-Type-Options"
	failedWriteResponseError      = "Failed to write response."
	frameOptionsHeader            = "X-Frame-Options"
	internalServerError           = "internal server error"
	prometheusTextContentType     = "text/plain; version=0.0.4; charset=utf-8"
	scrapeCapacityExceededMessage = "scrape concurrency limit reached"
	shutdownWaitTimeout           = 3 * time.Second
)

// ErrActiveScrapeDrainTimeout marks a shutdown that left a native scrape
// running past the HTTP shutdown deadline. The command must avoid tearing down
// DCGM resources while that scrape can still use them.
var ErrActiveScrapeDrainTimeout = errors.New("active scrape producer did not drain before shutdown deadline")

// NewMetricsServer creates the HTTP server for a published registry and its
// matching device topology. The command layer starts Run and calls cleanup
// when application shutdown is complete.
func NewMetricsServer(
	c *appconfig.Config,
	deviceWatchListManager devicewatchlistmanager.Manager,
	registry *registry.Registry,
) (*MetricsServer, func(), error) {
	return NewMetricsServerWithDRAChangeCallback(c, deviceWatchListManager, registry, nil)
}

// NewMetricsServerWithDRAChangeCallback creates the HTTP server and wires an
// optional DRA topology notification into Kubernetes transformations. The
// command lifecycle provides the callback; other server users can call
// NewMetricsServer when they do not manage reloads.
func NewMetricsServerWithDRAChangeCallback(
	c *appconfig.Config,
	deviceWatchListManager devicewatchlistmanager.Manager,
	registry *registry.Registry,
	onDRAChange func(),
) (*MetricsServer, func(), error) {
	router := mux.NewRouter()

	var fileDumper *debug.FileDumper
	if c.DumpConfig.Enabled {
		fileDumper = debug.NewFileDumper(c.DumpConfig)
	}
	readTimeout := timeoutOrDefault(c.WebReadTimeout, appconfig.DefaultWebReadTimeout)
	writeTimeout := timeoutOrDefault(c.WebWriteTimeout, appconfig.DefaultWebWriteTimeout)
	maxConcurrentScrapes := c.MaxConcurrentScrapes
	if maxConcurrentScrapes <= 0 {
		return nil, nil, fmt.Errorf(
			"max concurrent scrapes must be greater than zero: %d",
			maxConcurrentScrapes,
		)
	}

	managedTransforms := transformation.NewManagedTransforms(c, onDRAChange)
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
		transformations:        managedTransforms.Transformations,
		deviceWatchListManager: deviceWatchListManager,
		fileDumper:             fileDumper,
		scrapes:                newScrapeCoordinator(maxConcurrentScrapes),
		terminalErrors:         make(chan error, 1),
	}

	serverv1.registry.Store(registry)
	serverv1.reloadInProgress.Store(false)
	metricsHandler := http.Handler(http.HandlerFunc(serverv1.Metrics))
	if c.EnableExporterMetrics {
		serverv1.exporterMetrics = newExporterMetrics(metricsHandler)
		metricsHandler = serverv1.exporterMetrics
	}
	metricsHTML := `<p><a href="./metrics">Metrics</a></p>`
	if c.DisablePrometheus {
		metricsHTML = ""
	}
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(contentTypeOptionsHeader, "nosniff")
		setPageSecurityHeaders(w.Header())
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
			` + metricsHTML + `
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
	if c.DisablePrometheus {
		router.Handle("/metrics", http.NotFoundHandler())
	} else {
		router.Handle("/metrics", metricsHandler)
	}

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

	managedTransforms.Start()

	cleanup := func() {
		managedTransforms.Cleanup()
	}

	return serverv1, cleanup, nil
}

// setPageSecurityHeaders stops browsers from framing the landing and health
// pages or loading anything into them. A header already set from the web
// config's http_server_config.headers is kept.
func setPageSecurityHeaders(header http.Header) {
	if header.Get(contentSecurityPolicyHeader) == "" {
		header.Set(contentSecurityPolicyHeader, contentSecurityPolicy)
	}

	if header.Get(frameOptionsHeader) == "" {
		header.Set(frameOptionsHeader, "DENY")
	}
}

// timeoutOrDefault selects configured timeout unless it is unset or invalid.
// NewMetricsServer and Run use it to keep HTTP timeouts predictable.
func timeoutOrDefault(timeout, defaultTimeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultTimeout
	}
	return timeout
}

// shutdownTimeout uses the effective write timeout as the graceful-shutdown
// deadline so an admitted scrape has the time it was promised.
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
// to gather and render metrics. Gather admission belongs to Registry, so this
// short swap never waits for a native scrape; its caller retires the returned
// registry and waits for its completion before releasing native providers.
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

// Run serves HTTP requests until stop, ctx, or a terminal server error ends
// the lifetime. It returns the first terminal error after attempting bounded
// shutdown so the command layer, rather than this package, chooses the exit.
func (s *MetricsServer) Run(ctx context.Context, stop <-chan struct{}) error {
	var serverWG sync.WaitGroup
	serverErrors := make(chan error, 1)
	serverWG.Add(1)
	go func() {
		defer serverWG.Done()
		slog.Info("Starting webserver")

		// Log dump configuration information
		if s.config.DumpConfig.Enabled {
			slog.Info("Debug dumps enabled - runtime objects may be written to files for troubleshooting",
				slog.String("dump_directory", s.config.DumpConfig.Directory),
				slog.String("retention", appconfig.FormatDuration(s.config.DumpConfig.Retention)),
				slog.Bool("compression_enabled", s.config.DumpConfig.Compression),
				slog.String("note", "Debug files may be created during operation and cleaned up automatically"))
		} else {
			slog.Debug("Debug dumps disabled - use --dump-enabled flag to enable file-based debugging")
		}

		if err := web.ListenAndServe(s.server, s.webConfig, slog.Default()); err != nil && err != http.ErrServerClosed {
			slog.Error("Failed to listen and serve HTTP server.", slog.String(logging.ErrorKey, err.Error()))
			serverErrors <- fmt.Errorf("listen and serve HTTP server: %w", err)
		}
	}()

	done := make(chan struct{})
	var maintenanceWG sync.WaitGroup
	if s.fileDumper != nil {
		maintenanceWG.Add(1)
		go func() {
			defer maintenanceWG.Done()
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()

			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					if err := s.fileDumper.CleanupOldFiles(); err != nil {
						slog.Warn("Failed to cleanup old debug files", slog.String(logging.ErrorKey, err.Error()))
					}
				}
			}
		}()
	}

	var runErr error
	select {
	case <-stop:
	case <-ctx.Done():
	case runErr = <-serverErrors:
	case runErr = <-s.terminalErrors:
	}
	close(done)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout(s.server.WriteTimeout))
	defer shutdownCancel()
	if err := s.server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Failed to shutdown HTTP server.", slog.String(logging.ErrorKey, err.Error()))
		runErr = errors.Join(runErr, fmt.Errorf("shutdown HTTP server: %w", err))
	}

	if err := s.scrapes.wait(shutdownCtx); err != nil {
		slog.Error("Failed waiting for active scrape producer.", slog.String(logging.ErrorKey, err.Error()))
		runErr = errors.Join(runErr, fmt.Errorf("%w: %w", ErrActiveScrapeDrainTimeout, err))
	}

	if err := utils.WaitWithTimeout(&maintenanceWG, shutdownWaitTimeout); err != nil {
		slog.Error("Failed waiting for HTTP server to shutdown.", slog.String(logging.ErrorKey, err.Error()))
		runErr = errors.Join(runErr, fmt.Errorf("wait for HTTP maintenance: %w", err))
	}
	if err := utils.WaitWithTimeout(&serverWG, shutdownWaitTimeout); err != nil {
		slog.Error("Failed waiting for HTTP listener to shutdown.", slog.String(logging.ErrorKey, err.Error()))
		runErr = errors.Join(runErr, fmt.Errorf("wait for HTTP listener: %w", err))
	}
	runErr = joinAvailableServerError(runErr, serverErrors)
	if terminalErr := s.TerminalError(); terminalErr != nil && !errors.Is(runErr, terminalErr) {
		runErr = errors.Join(runErr, terminalErr)
	}
	return runErr
}

// joinAvailableServerError preserves a listener failure that arrived while
// Run selected a simultaneous stop or context signal. The listener wait has
// already completed before this helper runs, so the non-blocking read cannot
// wait for a future error or delay orderly shutdown.
func joinAvailableServerError(runErr error, serverErrors <-chan error) error {
	select {
	case serverErr := <-serverErrors:
		return errors.Join(runErr, serverErr)
	default:
		return runErr
	}
}

// reportTerminalError records only the first fatal scrape error. Metrics can
// be called concurrently, so a buffered non-blocking send prevents duplicate
// reports from blocking or replacing the application result.
func (s *MetricsServer) reportTerminalError(err error) {
	s.terminalErrorOnce.Do(func() {
		s.terminalErrorMu.Lock()
		s.terminalError = err
		s.terminalErrorMu.Unlock()
		select {
		case s.terminalErrors <- err:
		default:
		}
	})
}

// ReportTerminalError asks Run to stop with err after its bounded HTTP
// shutdown. Runtime owners use it when a registry cannot retire safely, so the
// command layer returns a nonzero result without closing native clients below
// still-running work.
func (s *MetricsServer) ReportTerminalError(err error) {
	if err == nil {
		return
	}
	s.reportTerminalError(err)
}

// TerminalError returns the first fatal error reported to this server. The
// command layer checks it after watcher shutdown so a concurrent stop signal
// cannot turn an unsafe native retirement into a successful process exit.
func (s *MetricsServer) TerminalError() error {
	s.terminalErrorMu.Lock()
	defer s.terminalErrorMu.Unlock()
	return s.terminalError
}

// Metrics gathers the current registry and serves Prometheus text exposition.
func (s *MetricsServer) Metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", prometheusTextContentType)
	w.Header().Set(contentTypeOptionsHeader, "nosniff")

	if r.Context().Err() != nil {
		return
	}

	if !s.scrapes.tryAcquire() {
		http.Error(w, scrapeCapacityExceededMessage, http.StatusServiceUnavailable)

		return
	}
	defer s.scrapes.release()

	producerContext := context.WithoutCancel(r.Context())
	response, err := s.scrapes.do(r.Context(), func() ([]byte, error) {
		var buf bytes.Buffer
		if err := s.gatherAndRenderMetrics(producerContext, &buf); err != nil {
			if dcgmerrors.IsFatalScrapeError(err) {
				s.reportTerminalError(fmt.Errorf("fatal metrics scrape: %w", err))
			}
			return nil, err
		}

		return buf.Bytes(), nil
	})
	requestErr := r.Context().Err()
	if errors.Is(requestErr, context.Canceled) || errors.Is(requestErr, context.DeadlineExceeded) {
		return
	}
	if err != nil {
		http.Error(w, internalServerError, http.StatusInternalServerError)

		return
	}

	_, err = w.Write(response)
	if err != nil {
		slog.Error(failedWriteResponseError, slog.String(logging.ErrorKey, err.Error()))
		http.Error(w, "failed to write response", http.StatusInternalServerError)

		return
	}
}

// gatherAndRenderMetrics renders DCGM metrics and appends enabled exporter metrics to one response.
func (s *MetricsServer) gatherAndRenderMetrics(ctx context.Context, w *bytes.Buffer) error {
	// Snapshot and admit one matching runtime while the swap lock is held. After
	// that the server lock can be released for native gather and rendering; a
	// swap cannot retire this registry until the matching EndUse below.
	s.RLock()
	currentRegistry := s.registry.Load()
	deviceWatchListManager := s.deviceWatchListManager
	if currentRegistry == nil {
		currentRegistry = registry.NewRegistry()
	}
	if err := currentRegistry.BeginUse(); err != nil {
		s.RUnlock()
		return err
	}
	s.RUnlock()
	defer currentRegistry.EndUse()
	metricGroups, err := currentRegistry.GatherAdmitted()
	if err != nil {
		slog.Error("Failed to gather metrics from collectors", slog.String(logging.ErrorKey, err.Error()))
		return err
	}
	if err := s.render(ctx, w, deviceWatchListManager, metricGroups); err != nil {
		return err
	}
	if err := s.exporterMetrics.appendTo(w); err != nil {
		slog.Warn(
			"Failed to append exporter metrics",
			slog.String(logging.ErrorKey, err.Error()),
		)
	}

	return nil
}

// dumpMetrics writes untransformed metrics for one group to the optional debug
// dumper and returns the path used when reporting downstream failures. Metrics
// with no DCGM entity (FE_NONE) are written under the "Host" name.
func (s *MetricsServer) dumpMetrics(metrics collector.MetricsByCounter, group dcgm.Field_Entity_Group) string {
	if s.fileDumper == nil {
		return ""
	}

	suffix := group.String()
	if group == dcgm.FE_NONE {
		suffix = "Host"
	}

	metricsFile, err := s.fileDumper.DumpToFile(metrics, "metrics", suffix)
	if err != nil {
		slog.Warn("Failed to write metrics debug file",
			slog.String(logging.ErrorKey, err.Error()),
			slog.String(logging.FieldEntityGroupKey, suffix))

		return ""
	}

	return metricsFile
}

// dumpDeviceInfo writes one group's device information to the optional debug
// dumper and returns the path used when reporting downstream failures.
func (s *MetricsServer) dumpDeviceInfo(deviceWatchList devicewatchlistmanager.WatchList, group dcgm.Field_Entity_Group) string {
	if s.fileDumper == nil {
		return ""
	}

	deviceInfoFile, err := s.fileDumper.DumpToFile(deviceWatchList.DeviceInfo(), "deviceinfo", group.String())
	if err != nil {
		slog.Warn("Failed to write device info debug file",
			slog.String(logging.ErrorKey, err.Error()),
			slog.String(logging.FieldEntityGroupKey, group.String()))

		return ""
	}

	return deviceInfoFile
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
		// Host-level exporter metrics (field policy, system health) have no DCGM
		// entity, so they skip watch lists and transformations.
		if group == dcgm.FE_NONE {
			s.dumpMetrics(metrics, group)
			renderableGroups[group] = metrics
			continue
		}

		deviceWatchList, exists := deviceWatchListManager.EntityWatchList(group)
		if !exists {
			continue
		}

		// Write debug files before transformations so failures can reference inputs.
		metricsFile := s.dumpMetrics(metrics, group)
		deviceInfoFile := s.dumpDeviceInfo(deviceWatchList, group)

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
	setPageSecurityHeaders(w.Header())

	// If reload in progress or registry is nil (during hot reload/bind/unbind),
	// still return 200 OK to prevent Kubernetes pod termination
	// Use headers to communicate status to monitoring systems
	if s.IsReloadInProgress() {
		w.Header().Set("X-Reload-In-Progress", "true")
	}

	// Check the raw atomic value to see if registry is nil
	reg := s.registry.Load()
	if reg == nil {
		w.Header().Set("X-Registry-Available", "false")
		w.Header().Set("X-Reload-In-Progress", "true")
		_, _ = w.Write([]byte("OK - reload in progress"))
		return
	}

	w.Header().Set("X-Registry-Available", "true")

	// A registry built while the hostengine had no GPUs visible carries no GPU
	// collector, and is only rebuilt on reload or a bind event, so on its own the
	// exporter serves an empty metrics page while still reporting healthy.
	// Reporting that as unhealthy lets a liveness probe already pointed at /health
	// restart the pod so it re-enumerates once the hostengine itself is sighted.
	// While the hostengine stays blind this is a restart loop rather than a silent
	// blind pod - the loud failure is the point.
	//
	// Note this counts collectors registered under FE_GPU, which is a proxy for
	// GPU visibility: a counters file contributing no FE_GPU fields also yields
	// zero. See the flag's usage text.
	if s.config != nil && s.config.HealthRequireGPUs && !s.IsReloadInProgress() {
		// reg is non-nil: the nil case returned above. Load once so an unbind
		// swapping it to nil between loads cannot skip the check.
		if reg.CollectorCount(dcgm.FE_GPU) == 0 {
			w.Header().Set("X-GPU-Collectors", "0")
			http.Error(w, "KO - no GPU collector registered", http.StatusServiceUnavailable)
			return
		}
	}

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
