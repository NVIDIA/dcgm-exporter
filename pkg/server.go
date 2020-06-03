/*
 * Copyright (c) 2020, NVIDIA CORPORATION.  All rights reserved.
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

package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

func NewMetricsServer(c *Config, metrics chan string) (*MetricsServer, func(), error) {
	router := mux.NewRouter()
	serverv1 := &MetricsServer{
		server: http.Server{
			Addr:         fmt.Sprintf(":%d", c.Port),
			Handler:      router,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
		},
		metricsChan: metrics,
		metrics:     "",
	}

	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
			<head><title>GPU Exporter</title></head>
			<body>
			<h1>GPU Exporter</h1>
			<p><a href="./metrics">Metrics</a></p>
			</body>
			</html>`))
	})

	router.HandleFunc("/health", serverv1.Health)
	router.HandleFunc("/metrics", serverv1.Metrics)

	return serverv1, func() {}, nil
}

func (s *MetricsServer) Run(stop chan interface{}, wg *sync.WaitGroup) {
	defer wg.Done()

	var httpwg sync.WaitGroup
	httpwg.Add(1)
	go func() {
		defer httpwg.Done()
		logrus.Info("Starting webserver")
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.Fatalf("Failed to Listen and Server HTTP server with err: `%v`", err)
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
		logrus.Fatalf("Failed to shutdown HTTP server, with err: `%v`", err)
	}

	if err := WaitWithTimeout(&httpwg, 3*time.Second); err != nil {
		logrus.Fatalf("Failed waiting for HTTP server to shutdown, with err: `%v`", err)
	}
}

func (s *MetricsServer) Metrics(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(s.getMetrics()))
}

func (s *MetricsServer) Health(w http.ResponseWriter, r *http.Request) {
	if s.getMetrics() == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("KO"))
	} else {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
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
