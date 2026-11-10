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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
)

// errGPUDetachedDuringBuild permits a fresh startup snapshot only when DCGM
// confirms that a GPU detached after discovery. The native error is retained.
var errGPUDetachedDuringBuild = errors.New("GPU detached during registry construction")

// buildRegistry creates a complete candidate registry for the current topology.
// Startup and GPU recovery publish it only after construction succeeds. File
// and DRA reloads keep the current registry and topology if construction fails.
// A context canceled after the collectors exist still returns the candidate;
// the caller checks the context and retires it with its own cleanup timeout.
func buildRegistry(
	ctx context.Context,
	config *appconfig.Config,
	dcp counters.DCPCapabilities,
) (*registry.Registry, devicewatchlistmanager.Manager, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("build registry before loading counters: %w", err)
	}

	slog.Info("Building registry for current GPU topology")

	cs, err := getCountersFunc(ctx, config, dcp)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get counters: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("build registry after loading counters: %w", err)
	}

	watchConfig := config
	if config.EnableOTLP {
		watchConfig = config.Clone()
		watchConfig.OTLPWatchCollections = counters.FilterWatchSources(config.OTLPWatchCollections, dcp)
	}
	deviceWatchListManager, err := startWatchListManagerFunc(cs, watchConfig)
	if err != nil {
		return nil, nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("build registry after discovering devices: %w", err)
	}

	hostName := ""
	if !config.NoHostname {
		var err error
		hostName, err = getHostnameFunc(config)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get hostname: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("build registry after resolving hostname: %w", err)
	}

	cf := initCollectorFactoryFunc(cs, deviceWatchListManager, hostName, config)

	cRegistry := registry.NewRegistry()
	count, err := populateRegistry(cf, cRegistry)
	if err != nil {
		cRegistry.Cleanup()
		err = fmt.Errorf("populate collector registry: %w", err)
		if gpuDetachedAfterDiscovery(deviceWatchListManager) {
			return nil, nil, errors.Join(errGPUDetachedDuringBuild, err)
		}

		return nil, nil, err
	}

	slog.Info("Registry built successfully", slog.Int("collector_count", count))

	return cRegistry, deviceWatchListManager, nil
}

// gpuDetachedAfterDiscovery checks whether a failed build used an outdated
// GPU snapshot. An unknown current status does not establish a topology change.
func gpuDetachedAfterDiscovery(manager devicewatchlistmanager.Manager) bool {
	client := dcgmprovider.Client()
	if client == nil {
		return false
	}

	for _, entityType := range []dcgm.Field_Entity_Group{dcgm.FE_GPU, dcgm.FE_LINK} {
		watchList, exists := manager.EntityWatchList(entityType)
		if !exists {
			continue
		}

		info := watchList.DeviceInfo()
		for i := uint(0); i < info.GPUCount(); i++ {
			gpu := info.GPU(i)
			if gpu.Status != dcgm.EntityStatusDetached && client.GetGPUStatus(gpu.DeviceInfo.GPU) == dcgm.EntityStatusDetached {
				return true
			}
		}
	}
	return false
}

// collectorRegisterer is the registration operation needed by populateRegistry.
// Production passes a registry; rollback tests can supply a rejecting registerer.
type collectorRegisterer interface {
	Register(collector.EntityCollectorTuple) error
}

// populateRegistry calls the factory once and registers its collectors for
// startup or reload. A second unregistered batch would leak DCGM watches.
func populateRegistry(cf collector.Factory, r collectorRegisterer) (int, error) {
	collectors, err := cf.NewCollectors()
	if err != nil {
		return 0, err
	}

	for index, entityCollector := range collectors {
		err := r.Register(entityCollector)
		if err == nil {
			continue
		}

		registrationErr := fmt.Errorf("register collector: %w", err)
		completionSignals, cleanupErr := cleanupUnregisteredCollectors(collectors[index:], index)
		if len(completionSignals) != 0 || cleanupErr != nil {
			return 0, collectorRegistrationError{
				err:               registrationErr,
				cleanupErr:        cleanupErr,
				completionSignals: completionSignals,
			}
		}
		return 0, registrationErr
	}

	return len(collectors), nil
}

// cleanupUnregisteredCollectors releases every rejected candidate in reverse
// order and returns their completion signals for the bounded rollback wait.
// populateRegistry passes the rejected collector and all later candidates;
// firstIndex keeps cleanup errors tied to their original construction order.
func cleanupUnregisteredCollectors(
	collectors []collector.EntityCollectorTuple,
	firstIndex int,
) ([]<-chan struct{}, error) {
	var completionSignals []<-chan struct{}
	var cleanupErrors []error

	for offset := len(collectors) - 1; offset >= 0; offset-- {
		candidate := collectors[offset].Collector()
		completion, err := cleanupUnregisteredCollector(candidate, firstIndex+offset)
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if _, completionAware := candidate.(collector.CleanupCompleter); completionAware {
			completionSignals = append(completionSignals, completion)
		}
	}

	return completionSignals, errors.Join(cleanupErrors...)
}

// cleanupUnregisteredCollector requests cleanup and captures its completion
// signal without letting either call abort rollback of the remaining candidates.
func cleanupUnregisteredCollector(
	candidate collector.Collector,
	index int,
) (completion <-chan struct{}, err error) {
	operation := "cleanup"
	defer func() {
		if recovered := recover(); recovered != nil {
			if panicErr, ok := recovered.(error); ok {
				err = fmt.Errorf("unregistered collector %d %s panicked: %w", index, operation, panicErr)
				return
			}
			err = fmt.Errorf("unregistered collector %d %s panicked: %v", index, operation, recovered)
		}
	}()

	candidate.Cleanup()
	if completionAware, ok := candidate.(collector.CleanupCompleter); ok {
		operation = "cleanup completion lookup"
		completion = completionAware.CleanupComplete()
	}
	return completion, nil
}

// hotReloadCounter assigns a monotonically increasing ID to each reload for
// correlating logs across the reload paths.
var hotReloadCounter atomic.Uint64

// logTopologyInfo records the device counts for a newly published registry.
// Startup and reload paths call it after a registry is safe to serve metrics.
func logTopologyInfo(reloadID uint64, deviceWatchListMgr devicewatchlistmanager.Manager, duration time.Duration) {
	var gpuCount, switchCount, cpuCount uint

	// Count GPUs
	if gpuWatchList, exists := deviceWatchListMgr.EntityWatchList(dcgm.FE_GPU); exists {
		gpuCount = gpuWatchList.DeviceInfo().GPUCount()
	}

	// Count Switches
	if switchWatchList, exists := deviceWatchListMgr.EntityWatchList(dcgm.FE_SWITCH); exists {
		switchCount = uint(len(switchWatchList.DeviceInfo().Switches()))
	}

	// Count CPUs
	if cpuWatchList, exists := deviceWatchListMgr.EntityWatchList(dcgm.FE_CPU); exists {
		cpuCount = uint(len(cpuWatchList.DeviceInfo().CPUs()))
	}

	slog.Info("System running with new topology",
		slog.Uint64("reload_id", reloadID),
		slog.Duration("reload_duration", duration),
		slog.Uint64("gpus", uint64(gpuCount)),
		slog.Uint64("switches", uint64(switchCount)),
		slog.Uint64("cpus", uint64(cpuCount)))
}

// startDeviceWatchListManager creates this registry generation's device watches.
// Registry construction calls it before collector creation so every collector
// shares the same owned watch-list manager.
func startDeviceWatchListManager(
	cs *counters.CounterSet, config *appconfig.Config,
) (devicewatchlistmanager.Manager, error) {
	if err := devicewatchlistmanager.CheckOverrides(config, cs); err != nil {
		return nil, err
	}

	deviceWatchListManager := devicewatchlistmanager.NewWatchListManager(cs.WatchCounters(), config)
	// Capture this registry generation's client before discovery creates any
	// groups or field watches. Its cleanup closures retain the same client even
	// if a later GPU lifecycle reset publishes a replacement.
	deviceWatcher := devicewatcher.NewDeviceWatcherWithClient(dcgmprovider.Client())

	if err := createEntityWatchLists(deviceWatchListManager, deviceWatcher, int64(config.CollectInterval)); err != nil {
		return nil, err
	}

	return deviceWatchListManager, nil
}

// createEntityWatchLists builds the requested watch-list descriptions for one
// registry generation. It skips only CPU and NVSwitch topologies that do not
// exist on this host; all other discovery failures reject the candidate so
// selected-device configuration cannot silently drop metrics. Device discovery
// validates explicit selectors before returning an optional-topology error.
func createEntityWatchLists(
	manager devicewatchlistmanager.Manager,
	watcher devicewatcher.WatchInstaller,
	collectInterval int64,
) error {
	for _, deviceType := range devicewatchlistmanager.DeviceTypesToWatch {
		if err := manager.CreateEntityWatchList(deviceType, watcher, collectInterval); err != nil {
			if deviceinfo.IsOptionalTopologyError(err) {
				slog.Info(fmt.Sprintf("Not collecting %s metrics; %s", deviceType.String(), err))
				continue
			}
			return fmt.Errorf("discover %s metrics: %w", deviceType.String(), err)
		}
	}
	return nil
}

// getCounters loads the selected metric source using the DCP fields supported
// by the active DCGM topology. Registry construction uses it before it creates
// watches, so unsupported profiling fields never reach DCGM.
func getCounters(
	ctx context.Context,
	config *appconfig.Config,
	dcp counters.DCPCapabilities,
) (*counters.CounterSet, error) {
	cs, err := counters.GetCounterSet(ctx, config, dcp)
	if err != nil {
		return nil, err
	}

	// Copy labels from DCGM Counters to ExporterCounters
	for i := range cs.DCGMCounters {
		if cs.DCGMCounters[i].PromType == "label" {
			cs.ExporterCounters = append(cs.ExporterCounters, cs.DCGMCounters[i])
		}
	}
	return cs, nil
}
