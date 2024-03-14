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

package dcgm_client

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

func (d *DcgmGroupManager) AddNewGroup(groupName string, groupType DcgmGroupType, groupID *uint) error {

	newGroupID := atomic.AddUint32(&d.groupIDSequence, 1)
	*groupID = uint(newGroupID)

	newGroup := DcgmGroup{
		groupId:    *groupID,
		name:       groupName,
		entityList: make([]dcgm.GroupEntityPair, 0),
	}

	d.groupIDMap[*groupID] = &newGroup

	return nil
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

type FakeDCGMClient struct {
	dcgmGroupManager   DcgmGroupManager
	fakeAll            bool
	fakeFuncs          map[string]bool
	realDCGMClient     DCGMClient
	shutdownRealClient bool
}

func NewFakeDCGMClient(config *common.Config, fakeAll bool) FakeDCGMClient {

	fakeClient := FakeDCGMClient{
		dcgmGroupManager: DcgmGroupManager{},
		fakeAll:          fakeAll,
	}

	funcs := reflect.TypeOf((*DCGMClient)(nil)).Elem()
	fakeClient.fakeFuncs = make(map[string]bool)
	for i := 0; i < funcs.NumMethod(); i++ {
		fakeClient.fakeFuncs[funcs.Method(i).Name] = false
	}

	if !fakeAll {
		if realDCGMClient := Client(); realDCGMClient != nil {
			fmt.Println("Using existing client")
			fakeClient.realDCGMClient = realDCGMClient
		} else {
			fmt.Println("Using new client")
			fakeClient.realDCGMClient = newDCGMClient(config)
			fakeClient.shutdownRealClient = true
		}
	}

	return fakeClient
}

func (f *FakeDCGMClient) FakeFunc(funcName string) {
	f.fakeFuncs[funcName] = true
}

func (f *FakeDCGMClient) FakeFuncs(funcNames []string) {
	for _, funcName := range funcNames {
		f.FakeFunc(funcName)
	}
}

func (f *FakeDCGMClient) AddEntityToGroup(
	groupId dcgm.GroupHandle, entityGroupId dcgm.Field_Entity_Group, entityId uint,
) error {
	if f.useFake(getFunctionName()) {
		if group := f.dcgmGroupManager.GetGroupById(uint(groupId.GetHandle())); group != nil {
			return group.AddEntityToGroup(entityGroupId, entityId)
		}
		return fmt.Errorf("group not found")
	} else {
		return f.realDCGMClient.AddEntityToGroup(groupId, entityGroupId, entityId)
	}
}

func (f *FakeDCGMClient) AddLinkEntityToGroup(groupId dcgm.GroupHandle, index uint, parentId uint) error {
	if f.useFake(getFunctionName()) {
		slice := []byte{uint8(dcgm.FE_SWITCH), uint8(index), uint8(parentId), 0}

		entityId := binary.LittleEndian.Uint32(slice)

		if group := f.dcgmGroupManager.GetGroupById(uint(groupId.GetHandle())); group != nil {
			return group.AddEntityToGroup(dcgm.FE_LINK, uint(entityId))
		}

		return fmt.Errorf("group not found")
	} else {
		return f.realDCGMClient.AddLinkEntityToGroup(groupId, index, parentId)
	}
}

func (f *FakeDCGMClient) CreateGroup(groupName string) (dcgm.GroupHandle, error) {
	if f.useFake(getFunctionName()) {
		var groupID *uint

		err := f.dcgmGroupManager.AddNewGroup(groupName, DCGM_GROUP_DEFAULT, groupID)
		if err != nil {
			return dcgm.GroupHandle{}, err
		}

		groupHandle := dcgm.GroupHandle{}
		groupHandle.Handle(uint64(*groupID))

		return groupHandle, nil
	} else {
		return f.realDCGMClient.CreateGroup(groupName)
	}
}

func (f *FakeDCGMClient) DestroyGroup(groupId dcgm.GroupHandle) error {
	if f.useFake(getFunctionName()) {
		return f.dcgmGroupManager.RemoveGroup(uint(groupId.GetHandle()))
	} else {
		return f.realDCGMClient.DestroyGroup(groupId)
	}
}

func (f *FakeDCGMClient) EntitiesGetLatestValues(
	entities []dcgm.GroupEntityPair, fields []dcgm.Short,
	flags uint,
) ([]dcgm.FieldValue_v2, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMClient.EntitiesGetLatestValues(entities, fields, flags)
	// }
}

func (f *FakeDCGMClient) EntityGetLatestValues(
	entityGroup dcgm.Field_Entity_Group, entityId uint,
	fields []dcgm.Short,
) ([]dcgm.FieldValue_v1, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.EntityGetLatestValues(entityGroup, entityId, fields)
	//  }
}

func (f *FakeDCGMClient) FieldGetById(fieldId dcgm.Short) dcgm.FieldMeta {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMClient.FieldGetById(fieldId)
	// }
}

func (f *FakeDCGMClient) FieldGroupCreate(fieldsGroupName string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMClient.FieldGroupCreate(fieldsGroupName, fields)
	// }
}

func (f *FakeDCGMClient) FieldGroupDestroy(fieldsGroup dcgm.FieldHandle) error {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMClient.FieldGroupDestroy(fieldsGroup)
	// }
}

func (f *FakeDCGMClient) GetAllDeviceCount() (uint, error) {
	if f.useFake(getFunctionName()) {
		// TODO (temp)
		return 1, nil
	} else {
		return f.realDCGMClient.GetAllDeviceCount()
	}
}

func (f *FakeDCGMClient) GetCpuHierarchy() (dcgm.CpuHierarchy_v1, error) {
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
		return f.realDCGMClient.GetCpuHierarchy()
	}
}

func (f *FakeDCGMClient) GetDeviceInfo(gpuId uint) (dcgm.Device, error) {
	if f.useFake(getFunctionName()) {
		// TODO (temp)
		dev := dcgm.Device{
			GPU:  0,
			UUID: fmt.Sprintf("fake%d", gpuId),
		}

		return dev, nil
	} else {
		return f.realDCGMClient.GetDeviceInfo(gpuId)
	}
}

func (f *FakeDCGMClient) GetEntityGroupEntities(entityGroup dcgm.Field_Entity_Group) ([]uint, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMClient.GetEntityGroupEntities(entityGroup)
	// }
}

func (f *FakeDCGMClient) GetGpuInstanceHierarchy() (dcgm.MigHierarchy_v2, error) {
	if f.useFake(getFunctionName()) {
		// TODO (temp)
		hierarchy := dcgm.MigHierarchy_v2{
			Count: 0,
		}
		return hierarchy, nil
	} else {
		return f.realDCGMClient.GetGpuInstanceHierarchy()
	}
}

func (f *FakeDCGMClient) GetNvLinkLinkStatus() ([]dcgm.NvLinkStatus, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMClient.GetNvLinkLinkStatus()
	// }
}

func (f *FakeDCGMClient) GetSupportedDevices() ([]uint, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.GetSupportedDevices()
	//  }
}

func (f *FakeDCGMClient) GetSupportedMetricGroups(gpuId uint) ([]dcgm.MetricGroup, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMClient.GetSupportedMetricGroups(gpuId)
	// }
}

func (f *FakeDCGMClient) LinkGetLatestValues(index uint, parentId uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
	error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.LinkGetLatestValues(index, parentId, fields)
	// }
}

func (f *FakeDCGMClient) GetValuesSince(
	gpuGroup dcgm.GroupHandle, fieldGroup dcgm.FieldHandle, sinceTime time.Time,
) ([]dcgm.FieldValue_v2, time.Time, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.GetValuesSince(gpuGroup, fieldGroup, sinceTime)
	// }
}

func (f *FakeDCGMClient) GroupAllGPUs() dcgm.GroupHandle {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.GroupAllGPUs()
	//  }
}

func (f *FakeDCGMClient) NewDefaultGroup(groupName string) (dcgm.GroupHandle, error) {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMClient.NewDefaultGroup(groupName)
	// }
}

func (f *FakeDCGMClient) UpdateAllFields() error {
	// if f.useFake(getFunctionName()) {

	// } else {
	return dcgm.UpdateAllFields()
	// }
}

func (f *FakeDCGMClient) WatchFieldsWithGroupEx(
	fieldsGroup dcgm.FieldHandle, group dcgm.GroupHandle, updateFreq int64, maxKeepAge float64,
	maxKeepSamples int32,
) error {
	// if f.useFake(getFunctionName()) {

	// } else {
	return f.realDCGMClient.WatchFieldsWithGroupEx(fieldsGroup, group, updateFreq, maxKeepAge, maxKeepSamples)
	// }
}

func (f *FakeDCGMClient) Cleanup() {
	if f.shutdownRealClient {
		f.realDCGMClient.Cleanup()
	}
}

func (f *FakeDCGMClient) useFake(funcName string) bool {
	return f.fakeAll || f.fakeFuncs[funcName]
}

func getFunctionName() string {
	pc, _, _, _ := runtime.Caller(1)
	funcNamePath := fmt.Sprintf("%s", runtime.FuncForPC(pc).Name())

	elements := strings.Split(funcNamePath, ".")
	return elements[len(elements)-1]
}
