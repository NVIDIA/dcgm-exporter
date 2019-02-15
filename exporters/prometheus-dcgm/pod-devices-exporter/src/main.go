// Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.

package main

import (
	"flag"
	"syscall"
	"time"

	"github.com/golang/glog"
	"gopkg.in/fsnotify/fsnotify.v1"
)

const (
	socketPath        = "/var/lib/kubelet/pod-resources/kubelet.sock"
	gpuMetricsPath    = "/run/prometheus/"
	gpuMetrics        = gpuMetricsPath + "dcgm.prom"
	gpuPodMetricsPath = "/run/dcgm/"
	gpuPodMetrics     = gpuPodMetricsPath + "dcgm-pod.prom"
)

func main() {
	defer glog.Flush()
	flag.Parse()

	glog.Info("Starting FS watcher.")
	watcher, err := watchDir(gpuMetricsPath)
	if err != nil {
		glog.Fatal(err)
	}
	defer watcher.Close()

	glog.Info("Starting OS watcher.")
	sigs := sigWatcher(syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	// create gpuPodMetrics dir
	err = createMetricsDir(gpuPodMetricsPath)
	if err != nil {
		glog.Fatal(err)
	}

	for {
		select {
		case event := <-watcher.Events:
			if event.Name == gpuMetrics && event.Op&fsnotify.Create == fsnotify.Create {
				glog.V(1).Infof("inotify: %s created, now adding device pod information.", gpuMetrics)
				podMap, err := getDevicePodInfo(socketPath)
				if err != nil {
					glog.Error(err)
					return
				}
				err = addPodInfoToMetrics(gpuPodMetricsPath, gpuMetrics, gpuPodMetrics, podMap)
				if err != nil {
					glog.Error(err)
					return
				}
			}

		case err := <-watcher.Errors:
			glog.Errorf("inotify: %s", err)

		// exit if there are no events for 20 seconds.
		case <-time.After(time.Second * 20):
			glog.Fatal("No events received. Make sure \"dcgm-exporter\" is running")
			return

		case sig := <-sigs:
			glog.V(2).Infof("Received signal \"%v\", shutting down.", sig)
			return
		}
	}
}
