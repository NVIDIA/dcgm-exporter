/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package dcgmexporter

import (
	"fmt"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

type CollectorFactory interface {
	Register()
}

// ToDo: When we move collectors to dedicated package like "collectors", we can replace the factory with something like "collectors.Register". Today, the collectorFactory plays a role of a package.
type collectorFactory struct {
	counterSet             *CounterSet
	deviceWatchListManager devicewatchlistmanager.Manager
	hostname               string
	config                 *appconfig.Config
	cRegistry              *Registry
}

func InitCollectorFactory(
	counterSet *CounterSet,
	deviceWatchListManager devicewatchlistmanager.Manager,
	hostname string,
	config *appconfig.Config,
	cRegistry *Registry,
) CollectorFactory {
	return &collectorFactory{
		counterSet:             counterSet,
		deviceWatchListManager: deviceWatchListManager,
		hostname:               hostname,
		config:                 config,
		cRegistry:              cRegistry,
	}
}

func (cf *collectorFactory) Register() {
	logrus.WithField(logging.DumpKey, fmt.Sprintf("%+v", cf.counterSet.DCGMCounters)).Debug("Counters are initialized")

	cf.enableDCGMCollector(dcgm.FE_GPU)
	cf.enableDCGMCollector(dcgm.FE_SWITCH)
	cf.enableDCGMCollector(dcgm.FE_LINK)
	cf.enableDCGMCollector(dcgm.FE_CPU)
	cf.enableDCGMCollector(dcgm.FE_CPU_CORE)
	cf.enableDCGMExpClockEventsCount()
	cf.enableDCGMExpXIDErrorsCountCollector()
}

func (cf *collectorFactory) enableDCGMCollector(group dcgm.Field_Entity_Group) {
	if len(cf.counterSet.DCGMCounters) > 0 {
		if item, exists := cf.deviceWatchListManager.EntityWatchList(group); exists {
			collector, err := NewDCGMCollector(cf.counterSet.DCGMCounters, cf.hostname, cf.config, item)
			if err != nil {
				logrus.Fatalf("Cannot create DCGMCollector for %s: %s", group.String(), err)
			}
			cf.cRegistry.Register(group, collector)
		}
	}
}

func (cf *collectorFactory) enableDCGMExpClockEventsCount() {
	if IsDCGMExpClockEventsCountEnabled(cf.counterSet.ExporterCounters) {
		item, exists := cf.deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)
		if !exists {
			logrus.Fatalf("%s collector cannot be initialized", DCGMClockEventsCount.String())
		}

		clocksThrottleReasonsCollector, err := NewClockEventsCollector(cf.counterSet.ExporterCounters,
			cf.hostname,
			cf.config, item)
		if err != nil {
			logrus.Fatalf("%s collector cannot be initialized. Error: %s", DCGMClockEventsCount.String(), err)
		}

		cf.cRegistry.Register(dcgm.FE_GPU, clocksThrottleReasonsCollector)

		logrus.Infof("%s collector initialized", DCGMClockEventsCount.String())
	}
}

func (cf *collectorFactory) enableDCGMExpXIDErrorsCountCollector() {
	if IsDCGMExpXIDErrorsCountEnabled(cf.counterSet.ExporterCounters) {
		item, exists := cf.deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)
		if !exists {
			logrus.Fatalf("%s collector cannot be initialized", DCGMXIDErrorsCount.String())
		}

		xidCollector, err := NewXIDCollector(cf.counterSet.ExporterCounters, cf.hostname, cf.config,
			item)
		if err != nil {
			logrus.Fatal(err)
		}

		cf.cRegistry.Register(dcgm.FE_GPU, xidCollector)

		logrus.Infof("%s collector initialized", DCGMXIDErrorsCount.String())
	}
}
