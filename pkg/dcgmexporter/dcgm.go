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
	"fmt"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	. "github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

func NewDeviceFields(counters []Counter, entityType dcgm.Field_Entity_Group) []dcgm.Short {
	var deviceFields []dcgm.Short
	for _, f := range counters {
		meta := dcgmprovider.Client().FieldGetById(f.FieldID)

		if meta.EntityLevel == entityType || meta.EntityLevel == dcgm.FE_NONE {
			deviceFields = append(deviceFields, f.FieldID)
		} else if entityType == dcgm.FE_GPU && (meta.EntityLevel == dcgm.FE_GPU_CI || meta.EntityLevel == dcgm.FE_GPU_I || meta.EntityLevel == dcgm.FE_VGPU) {
			deviceFields = append(deviceFields, f.FieldID)
		} else if entityType == dcgm.FE_CPU && (meta.EntityLevel == dcgm.FE_CPU || meta.EntityLevel == dcgm.FE_CPU_CORE) {
			deviceFields = append(deviceFields, f.FieldID)
		}
	}

	return deviceFields
}

func NewFieldGroup(deviceFields []dcgm.Short) (dcgm.FieldHandle, func(), error) {
	newFieldGroupNumber, err := RandUint64()
	if err != nil {
		return dcgm.FieldHandle{}, func() {}, err
	}

	name := fmt.Sprintf("gpu-collector-fieldgroup-%d", newFieldGroupNumber)
	fieldGroup, err := dcgmprovider.Client().FieldGroupCreate(name, deviceFields)
	if err != nil {
		return dcgm.FieldHandle{}, func() {}, err
	}

	return fieldGroup, func() {
		err := dcgmprovider.Client().FieldGroupDestroy(fieldGroup)
		if err != nil {
			logrus.WithError(err).Warn("Cannot destroy field group.")
		}
	}, nil
}

func WatchFieldGroup(
	group dcgm.GroupHandle, field dcgm.FieldHandle, updateFreq int64, maxKeepAge float64, maxKeepSamples int32,
) error {
	err := dcgmprovider.Client().WatchFieldsWithGroupEx(field, group, updateFreq, maxKeepAge, maxKeepSamples)
	if err != nil {
		return err
	}

	return nil
}

func SetupDcgmFieldsWatch(
	deviceFields []dcgm.Short, deviceInfo deviceinfo.Provider,
	collectIntervalUsec int64,
) ([]func(),
	error,
) {
	var err error
	var cleanups []func()
	var cleanup func()
	var groups []dcgm.GroupHandle
	var fieldGroup dcgm.FieldHandle

	if deviceInfo.InfoType() == dcgm.FE_LINK {
		/* one group per-nvswitch is created for nvlinks */
		groups, cleanups, err = CreateLinkGroupsFromDeviceInfo(deviceInfo)
	} else if deviceInfo.InfoType() == dcgm.FE_CPU_CORE {
		/* one group per-CPU is created for cpu cores */
		groups, cleanups, err = CreateCoreGroupsFromDeviceInfo(deviceInfo)
	} else {
		var group dcgm.GroupHandle
		group, cleanup, err = CreateGroupFromDeviceInfo(deviceInfo)
		if err == nil {
			groups = append(groups, group)
			cleanups = append(cleanups, cleanup)
		}
	}

	if err != nil {
		goto fail
	}

	for _, gr := range groups {
		fieldGroup, cleanup, err = NewFieldGroup(deviceFields)
		if err != nil {
			goto fail
		}

		cleanups = append(cleanups, cleanup)

		err = WatchFieldGroup(gr, fieldGroup, collectIntervalUsec, 0.0, 1)
		if err != nil {
			goto fail
		}
	}

	return cleanups, nil

fail:
	for _, f := range cleanups {
		f()
	}

	return nil, err
}

func CreateGroupFromDeviceInfo(deviceInfo deviceinfo.Provider) (dcgm.GroupHandle, func(), error) {
	monitoringInfo := devicemonitoring.GetMonitoredEntities(deviceInfo)

	newGroupNumber, err := RandUint64()
	if err != nil {
		return dcgm.GroupHandle{}, func() {}, err
	}

	groupID, err := dcgmprovider.Client().CreateGroup(fmt.Sprintf("gpu-collector-group-%d", newGroupNumber))
	if err != nil {
		return dcgm.GroupHandle{}, func() {}, err
	}

	for _, mi := range monitoringInfo {
		err := dcgmprovider.Client().AddEntityToGroup(groupID, mi.Entity.EntityGroupId, mi.Entity.EntityId)
		if err != nil {
			return groupID, func() {
				err := dcgmprovider.Client().DestroyGroup(groupID)
				if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
					logrus.WithFields(logrus.Fields{
						GroupIDKey:      groupID,
						logrus.ErrorKey: err,
					}).Warn("can not destroy group")
				}
			}, err
		}
	}

	return groupID, func() {
		err := dcgmprovider.Client().DestroyGroup(groupID)
		if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
			logrus.WithFields(logrus.Fields{
				GroupIDKey:      groupID,
				logrus.ErrorKey: err,
			}).Warn("can not destroy group")
		}
	}, nil
}

func CreateCoreGroupsFromDeviceInfo(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, []func(), error) {
	var groups []dcgm.GroupHandle
	var cleanups []func()
	var groupID dcgm.GroupHandle
	var err error

	for _, cpu := range deviceInfo.CPUs() {
		if !deviceInfo.IsCPUWatched(cpu.EntityId) {
			continue
		}

		var groupCoreCount int
		for _, core := range cpu.Cores {
			if !deviceInfo.IsCoreWatched(core, cpu.EntityId) {
				continue
			}

			// Create per-cpu core groups or after max number of CPU cores have been added to current group
			if groupCoreCount%dcgm.DCGM_GROUP_MAX_ENTITIES == 0 {
				newGroupNumber, err := RandUint64()
				if err != nil {
					return nil, cleanups, err
				}

				groupID, err = dcgmprovider.Client().CreateGroup(fmt.Sprintf("gpu-collector-group-%d", newGroupNumber))
				if err != nil {
					return nil, cleanups, err
				}
				groups = append(groups, groupID)
			}

			groupCoreCount++

			err = dcgmprovider.Client().AddEntityToGroup(groupID, dcgm.FE_CPU_CORE, core)
			if err != nil {
				return groups, cleanups, err
			}

			cleanups = append(cleanups, func() {
				err := dcgmprovider.Client().DestroyGroup(groupID)
				if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
					logrus.WithFields(logrus.Fields{
						GroupIDKey:      groupID,
						logrus.ErrorKey: err,
					}).Warn("can not destroy group")
				}
			})
		}
	}

	return groups, cleanups, nil
}

func CreateLinkGroupsFromDeviceInfo(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, []func(), error) {
	var groups []dcgm.GroupHandle
	var cleanups []func()

	/* Create per-switch link groups */
	for _, sw := range deviceInfo.Switches() {
		if !deviceInfo.IsSwitchWatched(sw.EntityId) {
			continue
		}

		newGroupNumber, err := RandUint64()
		if err != nil {
			return nil, cleanups, err
		}

		groupID, err := dcgmprovider.Client().CreateGroup(fmt.Sprintf("gpu-collector-group-%d", newGroupNumber))
		if err != nil {
			return nil, cleanups, err
		}

		groups = append(groups, groupID)

		for _, link := range sw.NvLinks {
			if link.State != dcgm.LS_UP {
				continue
			}

			if !deviceInfo.IsLinkWatched(link.Index, sw.EntityId) {
				continue
			}

			err = dcgmprovider.Client().AddLinkEntityToGroup(groupID, link.Index, link.ParentId)
			if err != nil {
				return groups, cleanups, err
			}

			cleanups = append(cleanups, func() {
				err := dcgmprovider.Client().DestroyGroup(groupID)
				if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
					logrus.WithFields(logrus.Fields{
						GroupIDKey:      groupID,
						logrus.ErrorKey: err,
					}).Warn("can not destroy group")
				}
			})
		}
	}

	return groups, cleanups, nil
}
