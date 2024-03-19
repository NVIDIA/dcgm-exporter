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

package dcgmprovider

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/pkg/common"
)

type DcgmGroupType int

const (
	DCGM_GROUP_DEFAULT DcgmGroupType = iota
	DCGM_GROUP_EMPTY
	DCGM_GROUP_DEFAULT_NVSWITCHESll
	DCGM_GROUP_DEFAULT_INSTANCESnces
	DCGM_GROUP_DEFAULT_COMPUTE_INSTANCES
	DCGM_GROUP_DEFAULT_EVERYTHING
)

// DcgmGroupManager source: https://github.com/NVIDIA/DCGM/blob/master/dcgmlib/src/DcgmGroupManager.h
type DcgmGroupManager struct {
	groupIDSequence uint32
	numGroups       uint
	groupIDMap      map[uint]*DcgmGroup
}

type DcgmGroup struct {
	groupId    uint
	name       string
	entityList []dcgm.GroupEntityPair
}

func (d *DcgmGroupManager) AddNewGroup(groupName string, groupType DcgmGroupType) (uint, error) {

	newGroupID := uint(atomic.AddUint32(&d.groupIDSequence, 1))

	newGroup := DcgmGroup{
		groupId:    newGroupID,
		name:       groupName,
		entityList: make([]dcgm.GroupEntityPair, 0),
	}

	if d.groupIDMap == nil {
		d.groupIDMap = make(map[uint]*DcgmGroup)
	}
	d.groupIDMap[newGroupID] = &newGroup

	return newGroupID, nil
}

func (d *DcgmGroupManager) RemoveGroup(groupID uint) error {
	if _, exists := d.groupIDMap[groupID]; !exists {
		return fmt.Errorf("group ID does not exist")
	}

	delete(d.groupIDMap, groupID)
	atomic.AddUint32(&d.groupIDSequence, ^uint32(0))
	return nil
}

func (d *DcgmGroupManager) GetGroupById(groupID uint) *DcgmGroup {
	if group, exists := d.groupIDMap[groupID]; exists {
		return group
	}

	return nil
}

func (d *DcgmGroup) AddEntityToGroup(entityGroupId dcgm.Field_Entity_Group, entityId uint) error {
	newPair := dcgm.GroupEntityPair{
		EntityGroupId: entityGroupId,
		EntityId:      entityId,
	}
	d.entityList = append(d.entityList, newPair)
	return nil
}

func (d *DcgmGroup) RemoveEntityFromGroup(entityGroupId dcgm.Field_Entity_Group, entityId uint) error {
	for i, pair := range d.entityList {
		if pair.EntityGroupId == entityGroupId && pair.EntityId == entityId {
			d.entityList = append(d.entityList[:i], d.entityList[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("entity not found")
}

func (d *DcgmGroup) GetGroupId() uint {
	return d.groupId
}

func (d *DcgmGroup) GetEntities() ([]dcgm.GroupEntityPair, error) {
	return d.entityList, nil
}

type FakeDCGMProvider struct {
	dcgmGroupManager   DcgmGroupManager
	fakeAll            bool
	fakeFuncs          map[string]bool
	realDCGMProvider   DCGMProvider
	shutdownRealClient bool
}

func NewFakeDCGMProvider(config *common.Config, fakeAll bool) FakeDCGMProvider {

	fakeClient := FakeDCGMProvider{
		dcgmGroupManager: DcgmGroupManager{},
		fakeAll:          fakeAll,
	}

	funcs := reflect.TypeOf((*DCGMProvider)(nil)).Elem()
	fakeClient.fakeFuncs = make(map[string]bool)
	for i := 0; i < funcs.NumMethod(); i++ {
		fakeClient.fakeFuncs[funcs.Method(i).Name] = false
	}

	if !fakeAll {
		if realDCGMProvider := Client(); realDCGMProvider != nil {
			fmt.Println("Using existing client")
			fakeClient.realDCGMProvider = realDCGMProvider
		} else {
			fmt.Println("Using new client")
			fakeClient.realDCGMProvider = newDCGMProvider(config)
			fakeClient.shutdownRealClient = true
		}
	}

	return fakeClient
}

func (f *FakeDCGMProvider) FakeFunc(funcName string) {
	f.fakeFuncs[funcName] = true
}

func (f *FakeDCGMProvider) FakeFuncs(funcNames []string) {
	for _, funcName := range funcNames {
		f.FakeFunc(funcName)
	}
}

func (f *FakeDCGMProvider) AddEntityToGroup(
	groupId dcgm.GroupHandle, entityGroupId dcgm.Field_Entity_Group, entityId uint,
) error {
	if f.useFake(getFunctionName()) {
		if group := f.dcgmGroupManager.GetGroupById(uint(groupId.GetHandle())); group != nil {
			return group.AddEntityToGroup(entityGroupId, entityId)
		}
		return fmt.Errorf("group not found")
	} else {
		return f.realDCGMProvider.AddEntityToGroup(groupId, entityGroupId, entityId)
	}
}

func (f *FakeDCGMProvider) AddLinkEntityToGroup(groupId dcgm.GroupHandle, index uint, parentId uint) error {
	if f.useFake(getFunctionName()) {
		slice := []byte{uint8(dcgm.FE_SWITCH), uint8(index), uint8(parentId), 0}

		entityId := binary.LittleEndian.Uint32(slice)

		if group := f.dcgmGroupManager.GetGroupById(uint(groupId.GetHandle())); group != nil {
			return group.AddEntityToGroup(dcgm.FE_LINK, uint(entityId))
		}

		return fmt.Errorf("group not found")
	} else {
		return f.realDCGMProvider.AddLinkEntityToGroup(groupId, index, parentId)
	}
}

func (f *FakeDCGMProvider) CreateGroup(groupName string) (dcgm.GroupHandle, error) {
	if f.useFake(getFunctionName()) {
		groupID, err := f.dcgmGroupManager.AddNewGroup(groupName, DCGM_GROUP_DEFAULT)
		if err != nil {
			return dcgm.GroupHandle{}, err
		}

		groupHandle := dcgm.GroupHandle{}
		groupHandle.SetHandle(uintptr(groupID))

		return groupHandle, nil
	} else {
		return f.realDCGMProvider.CreateGroup(groupName)
	}
}

func (f *FakeDCGMProvider) DestroyGroup(groupId dcgm.GroupHandle) error {
	if f.useFake(getFunctionName()) {
		return f.dcgmGroupManager.RemoveGroup(uint(groupId.GetHandle()))
	} else {
		return f.realDCGMProvider.DestroyGroup(groupId)
	}
}

func (f *FakeDCGMProvider) EntitiesGetLatestValues(
	entities []dcgm.GroupEntityPair, fields []dcgm.Short,
	flags uint,
) ([]dcgm.FieldValue_v2, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMProvider.EntitiesGetLatestValues(entities, fields, flags)
	// }
}

func (f *FakeDCGMProvider) EntityGetLatestValues(
	entityGroup dcgm.Field_Entity_Group, entityId uint,
	fields []dcgm.Short,
) ([]dcgm.FieldValue_v1, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.EntityGetLatestValues(entityGroup, entityId, fields)
	//  }
}

func (f *FakeDCGMProvider) FieldGetById(fieldId dcgm.Short) dcgm.FieldMeta {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMProvider.FieldGetById(fieldId)
	// }
}

func (f *FakeDCGMProvider) FieldGroupCreate(fieldsGroupName string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMProvider.FieldGroupCreate(fieldsGroupName, fields)
	// }
}

func (f *FakeDCGMProvider) FieldGroupDestroy(fieldsGroup dcgm.FieldHandle) error {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMProvider.FieldGroupDestroy(fieldsGroup)
	// }
}

func (f *FakeDCGMProvider) GetAllDeviceCount() (uint, error) {
	if f.useFake(getFunctionName()) {
		// TODO (temp)
		return 1, nil
	} else {
		return f.realDCGMProvider.GetAllDeviceCount()
	}
}

func (f *FakeDCGMProvider) GetCpuHierarchy() (dcgm.CpuHierarchy_v1, error) {
	if f.useFake(getFunctionName()) {
		// TODO (temp)
		CPU := dcgm.CpuHierarchyCpu_v1{
			CpuId:      0,
			OwnedCores: []uint64{0},
		}
		hierarchy := dcgm.CpuHierarchy_v1{
			Version: 0,
			NumCpus: 1,
			Cpus:    [dcgm.MAX_NUM_CPUS]dcgm.CpuHierarchyCpu_v1{CPU},
		}

		return hierarchy, nil
	} else {
		return f.realDCGMProvider.GetCpuHierarchy()
	}
}

func (f *FakeDCGMProvider) GetDeviceInfo(gpuId uint) (dcgm.Device, error) {
	if f.useFake(getFunctionName()) {
		// TODO (temp)
		dev := dcgm.Device{
			GPU:  0,
			UUID: fmt.Sprintf("fake%d", gpuId),
		}

		return dev, nil
	} else {
		return f.realDCGMProvider.GetDeviceInfo(gpuId)
	}
}

func (f *FakeDCGMProvider) GetEntityGroupEntities(entityGroup dcgm.Field_Entity_Group) ([]uint, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMProvider.GetEntityGroupEntities(entityGroup)
	// }
}

func (f *FakeDCGMProvider) GetGpuInstanceHierarchy() (dcgm.MigHierarchy_v2, error) {
	if f.useFake(getFunctionName()) {
		// TODO (temp)
		hierarchy := dcgm.MigHierarchy_v2{
			Count: 0,
		}
		return hierarchy, nil
	} else {
		return f.realDCGMProvider.GetGpuInstanceHierarchy()
	}
}

func (f *FakeDCGMProvider) GetNvLinkLinkStatus() ([]dcgm.NvLinkStatus, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMProvider.GetNvLinkLinkStatus()
	// }
}

func (f *FakeDCGMProvider) GetSupportedDevices() ([]uint, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.GetSupportedDevices()
	//  }
}

func (f *FakeDCGMProvider) GetSupportedMetricGroups(gpuId uint) ([]dcgm.MetricGroup, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMProvider.GetSupportedMetricGroups(gpuId)
	// }
}

func (f *FakeDCGMProvider) LinkGetLatestValues(index uint, parentId uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
	error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.LinkGetLatestValues(index, parentId, fields)
	// }
}

func (f *FakeDCGMProvider) GetValuesSince(
	gpuGroup dcgm.GroupHandle, fieldGroup dcgm.FieldHandle, sinceTime time.Time,
) ([]dcgm.FieldValue_v2, time.Time, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.GetValuesSince(gpuGroup, fieldGroup, sinceTime)
	// }
}

func (f *FakeDCGMProvider) GroupAllGPUs() dcgm.GroupHandle {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.GroupAllGPUs()
	//  }
}

func (f *FakeDCGMProvider) NewDefaultGroup(groupName string) (dcgm.GroupHandle, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMProvider.NewDefaultGroup(groupName)
	// }
}

func (f *FakeDCGMProvider) UpdateAllFields() error {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.UpdateAllFields()
	// }
}

func (f *FakeDCGMProvider) WatchFieldsWithGroupEx(
	fieldsGroup dcgm.FieldHandle, group dcgm.GroupHandle, updateFreq int64, maxKeepAge float64,
	maxKeepSamples int32,
) error {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMProvider.WatchFieldsWithGroupEx(fieldsGroup, group, updateFreq, maxKeepAge, maxKeepSamples)
	// }
}

func (f *FakeDCGMProvider) Cleanup() {
	if f.shutdownRealClient {
		f.realDCGMProvider.Cleanup()
	}
}

func (f *FakeDCGMProvider) useFake(funcName string) bool {
	return f.fakeAll || f.fakeFuncs[funcName]
}

func getFunctionName() string {
	pc, _, _, _ := runtime.Caller(1)
	funcNamePath := fmt.Sprintf("%s", runtime.FuncForPC(pc).Name())

	elements := strings.Split(funcNamePath, ".")
	return elements[len(elements)-1]
}
