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

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

type CollectorFactory interface {
	Register(
		cs *CounterSet,
		fieldEntityGroupTypeSystemInfo *FieldEntityGroupTypeSystemInfo,
		hostname string,
		config *appconfig.Config,
		cRegistry *Registry,
	)
}

// ToDo: When we move collectors to dedicated package like "collectors", we can replace the factory with something like "collectors.Register". Today, the collectorFactory plays a role of a package.
type collectorFactory struct{}

func InitCollectorFactory() CollectorFactory {
	return &collectorFactory{}
}

func (cf *collectorFactory) Register(cs *CounterSet,
	fieldEntityGroupTypeSystemInfo *FieldEntityGroupTypeSystemInfo,
	hostname string,
	config *appconfig.Config,
	cRegistry *Registry,
) {
	logrus.WithField(logging.DumpKey, fmt.Sprintf("%+v", cs.DCGMCounters)).Debug("Counters are initialized")

	cf.enableDCGMCollector(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry, dcgm.FE_GPU)
	cf.enableDCGMCollector(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry, dcgm.FE_SWITCH)
	cf.enableDCGMCollector(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry, dcgm.FE_LINK)
	cf.enableDCGMCollector(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry, dcgm.FE_CPU)
	cf.enableDCGMCollector(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry, dcgm.FE_CPU_CORE)
	cf.enableDCGMExpClockEventsCount(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry)
	cf.enableDCGMExpXIDErrorsCountCollector(cs, fieldEntityGroupTypeSystemInfo, hostname, config, cRegistry)
}

func (*collectorFactory) enableDCGMCollector(
	cs *CounterSet,
	fieldEntityGroupTypeSystemInfo *FieldEntityGroupTypeSystemInfo,
	hostname string,
	config *appconfig.Config,
	cRegistry *Registry,
	group dcgm.Field_Entity_Group,
) {
	if len(cs.DCGMCounters) > 0 {
		if item, exists := fieldEntityGroupTypeSystemInfo.Get(group); exists {
			collector, err := NewDCGMCollector(cs.DCGMCounters, hostname, config, item)
			if err != nil {
				logrus.Fatalf("Cannot create DCGMCollector for %s: %s", group.String(), err)
			}
			cRegistry.Register(group, collector)
		}
	}
}

func (*collectorFactory) enableDCGMExpClockEventsCount(
	cs *CounterSet,
	fieldEntityGroupTypeSystemInfo *FieldEntityGroupTypeSystemInfo,
	hostname string,
	config *appconfig.Config,
	cRegistry *Registry,
) {
	if IsDCGMExpClockEventsCountEnabled(cs.ExporterCounters) {
		item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)
		if !exists {
			logrus.Fatalf("%s collector cannot be initialized", DCGMClockEventsCount.String())
		}
		clocksThrottleReasonsCollector, err := NewClockEventsCollector(
			cs.ExporterCounters, hostname, config, item)
		if err != nil {
			logrus.Fatalf("%s collector cannot be initialized. Error: %s", DCGMClockEventsCount.String(), err)
		}

		cRegistry.Register(dcgm.FE_GPU, clocksThrottleReasonsCollector)

		logrus.Infof("%s collector initialized", DCGMClockEventsCount.String())
	}
}

func (*collectorFactory) enableDCGMExpXIDErrorsCountCollector(
	cs *CounterSet,
	fieldEntityGroupTypeSystemInfo *FieldEntityGroupTypeSystemInfo,
	hostname string,
	config *appconfig.Config,
	cRegistry *Registry,
) {
	if IsDCGMExpXIDErrorsCountEnabled(cs.ExporterCounters) {
		item, exists := fieldEntityGroupTypeSystemInfo.Get(dcgm.FE_GPU)
		if !exists {
			logrus.Fatalf("%s collector cannot be initialized", DCGMXIDErrorsCount.String())
		}

		xidCollector, err := NewXIDCollector(cs.ExporterCounters, hostname, config, item)
		if err != nil {
			logrus.Fatal(err)
		}

		cRegistry.Register(dcgm.FE_GPU, xidCollector)

		logrus.Infof("%s collector initialized", DCGMXIDErrorsCount.String())
	}
}
