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
	"math/rand"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
)

const (
	LoggerErrorField   = "error"
	LoggerGroupIDField = "group_id"
	LoggerDumpField    = "dump"
)

func NewGroup() (dcgm.GroupHandle, func(), error) {
	group, err := dcgm.NewDefaultGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
	if err != nil {
		return dcgm.GroupHandle{}, func() {}, err
	}

	return group, func() {
		err := dcgm.DestroyGroup(group)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				LoggerErrorField: err,
			}).Warn("can not destroy field group")
		}
	}, nil
}

func NewDeviceFields(counters []Counter, entityType dcgm.Field_Entity_Group) []dcgm.Short {
	var deviceFields []dcgm.Short
	for _, f := range counters {
		meta := dcgm.FieldGetById(f.FieldID)

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
	name := fmt.Sprintf("gpu-collector-fieldgroup-%d", rand.Uint64())
	fieldGroup, err := dcgm.FieldGroupCreate(name, deviceFields)
	if err != nil {
		return dcgm.FieldHandle{}, func() {}, err
	}

	return fieldGroup, func() {
		err := dcgm.FieldGroupDestroy(fieldGroup)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				LoggerErrorField: err,
			}).Warn("can not destroy field group")
		}
	}, nil
}

func WatchFieldGroup(group dcgm.GroupHandle, field dcgm.FieldHandle, updateFreq int64, maxKeepAge float64, maxKeepSamples int32) error {
	err := dcgm.WatchFieldsWithGroupEx(field, group, updateFreq, maxKeepAge, maxKeepSamples)
	if err != nil {
		return err
	}

	return nil
}

func SetupDcgmFieldsWatch(deviceFields []dcgm.Short, sysInfo SystemInfo, collectIntervalUsec int64) ([]func(), error) {
	var err error
	var cleanups []func()
	var cleanup func()
	var groups []dcgm.GroupHandle
	var fieldGroup dcgm.FieldHandle

	if sysInfo.InfoType == dcgm.FE_LINK {
		/* one group per-nvswitch is created for nvlinks */
		groups, cleanups, err = CreateLinkGroupsFromSystemInfo(sysInfo)
	} else if sysInfo.InfoType == dcgm.FE_CPU_CORE {
		/* one group per-CPU is created for cpu cores */
		groups, cleanups, err = CreateCoreGroupsFromSystemInfo(sysInfo)
	} else {
		group, cleanup, err := CreateGroupFromSystemInfo(sysInfo)
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
