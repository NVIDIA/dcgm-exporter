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
	"fmt"
	"math/rand"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
)

func NewGroup() (dcgm.GroupHandle, func(), error) {
	group, err := dcgm.NewDefaultGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
	if err != nil {
		return dcgm.GroupHandle{}, func() {}, err
	}

	return group, func() { dcgm.DestroyGroup(group) }, nil
}

func NewDeviceFields(counters []Counter) []dcgm.Short {
	deviceFields := make([]dcgm.Short, len(counters))
	for i, f := range counters {
		deviceFields[i] = f.FieldID
	}

	return deviceFields
}

func NewFieldGroup(deviceFields []dcgm.Short) (dcgm.FieldHandle, func(), error) {
	name := fmt.Sprintf("gpu-collector-fieldgroup-%d", rand.Uint64())
	fieldGroup, err := dcgm.FieldGroupCreate(name, deviceFields)
	if err != nil {
		return dcgm.FieldHandle{}, func() {}, err
	}

	return fieldGroup, func() { dcgm.FieldGroupDestroy(fieldGroup) }, nil
}

func WatchFieldGroup(group dcgm.GroupHandle, field dcgm.FieldHandle) error {
	err := dcgm.WatchFieldsWithGroup(field, group)
	if err != nil {
		return err
	}

	return nil
}

func SetupDcgmFieldsWatch(deviceFields []dcgm.Short) ([]func(), error) {
	var err error
	var cleanups []func()
	var cleanup func()
	var group dcgm.GroupHandle
	var fieldGroup dcgm.FieldHandle

	group, cleanup, err = NewGroup()
	if err != nil {
		goto fail
	}

	cleanups = append(cleanups, cleanup)

	fieldGroup, cleanup, err = NewFieldGroup(deviceFields)
	if err != nil {
		goto fail
	}

	cleanups = append(cleanups, cleanup)

	err = WatchFieldGroup(group, fieldGroup)
	if err != nil {
		goto fail
	}

	return cleanups, nil

fail:
	for _, f := range cleanups {
		f()
	}

	return nil, err
}
