/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmexporter

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

func NewMetricsServer(c *Config, metrics chan string, registry *Registry) (*MetricsServer, func(), error) {
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
		metricsChan: metrics,
		metrics:     "",
		registry:    registry,
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
			logrus.WithError(err).Error("Failed to write response.")
			http.Error(w, "failed to write response", http.StatusInternalServerError)
			return
		}
	})

	router.HandleFunc("/health", serverv1.Health)
	router.HandleFunc("/metrics", serverv1.Metrics)

	return serverv1, func() {}, nil
}

func (s *MetricsServer) Run(stop chan interface{}, wg *sync.WaitGroup) {
	defer wg.Done()
	// Wrap the logrus logger with the LogrusAdapter
	logger := logging.NewLogrusAdapter(logrus.StandardLogger())

	var httpwg sync.WaitGroup
	httpwg.Add(1)
	go func() {
		defer httpwg.Done()
		logrus.Info("Starting webserver")
		if err := web.ListenAndServe(s.server, s.webConfig, logger); err != nil && err != http.ErrServerClosed {
			logrus.WithError(err).Fatal("Failed to Listen and Server HTTP server.")
		}
	}()

	httpwg.Add(1)
	go func() {
		defer httpwg.Done()
		for {
			select {
			case <-stop:
				return
			case m := <-s.metricsChan:
				s.updateMetrics(m)
			}
		}
	}()

	<-stop
	if err := s.server.Shutdown(context.Background()); err != nil {
		logrus.WithError(err).Fatal("Failed to shutdown HTTP server.")
	}

	if err := WaitWithTimeout(&httpwg, 3*time.Second); err != nil {
		logrus.WithError(err).Fatal("Failed waiting for HTTP server to shutdown.")
	}
}

func (s *MetricsServer) Metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(s.getMetrics()))
	if err != nil {
		logrus.WithError(err).Error("Failed to write response.")
		http.Error(w, "failed to write response", http.StatusInternalServerError)
		return
	}
	metrics, err := s.registry.Gather()
	if err != nil {
		logrus.WithError(err).Error("Failed to write response.")
		http.Error(w, "failed to write response", http.StatusInternalServerError)
		return
	}
	err = encodeExpMetrics(w, metrics)
	if err != nil {
		http.Error(w, "failed to write response", http.StatusInternalServerError)
		return
	}
}

func (s *MetricsServer) Health(w http.ResponseWriter, r *http.Request) {
	if s.getMetrics() == "" {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, err := w.Write([]byte("KO"))
		if err != nil {
			logrus.WithError(err).Error("Failed to write response.")
			http.Error(w, "failed to write response", http.StatusInternalServerError)
		}
	} else {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("OK"))
		if err != nil {
			logrus.WithError(err).Error("Failed to write response.")
			http.Error(w, "failed to write response", http.StatusInternalServerError)
		}
	}
}

func (s *MetricsServer) updateMetrics(m string) {
	s.Lock()
	defer s.Unlock()

	s.metrics = m
}

func (s *MetricsServer) getMetrics() string {
	s.Lock()
	defer s.Unlock()

	return s.metrics
}
