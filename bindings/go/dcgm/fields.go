package dcgm

/*
#include "dcgm_agent.h"
#include "dcgm_structs.h"
*/
import "C"
import (
	"fmt"
)

const (
	updateFreq     = 1000000 // usec
	maxKeepAge     = 300     // sec
	maxKeepSamples = 0       // nolimit
)

type FieldHandle struct{ handle C.dcgmFieldGrp_t }

func FieldGroupCreate(fieldsGroupName string, fields []C.ushort, count int) (fieldsId FieldHandle, err error) {
	var fieldsGroup C.dcgmFieldGrp_t

	groupName := C.CString(fieldsGroupName)
	defer freeCString(groupName)

	result := C.dcgmFieldGroupCreate(handle.handle, C.int(count), &fields[0], groupName, &fieldsGroup)
	if err = errorString(result); err != nil {
		return fieldsId, fmt.Errorf("Error creating DCGM fields group: %s", err)
	}

	fieldsId = FieldHandle{fieldsGroup}
	return
}

func FieldGroupDestroy(fieldsGroup FieldHandle) (err error) {
	result := C.dcgmFieldGroupDestroy(handle.handle, fieldsGroup.handle)
	if err = errorString(result); err != nil {
		fmt.Errorf("Error destroying DCGM fields group: %s", err)
	}

	return
}

func WatchFields(gpuId uint, fieldsGroup FieldHandle, groupName string) (groupId GroupHandle, err error) {
	group, err := CreateGroup(groupName)
	if err != nil {
		return
	}

	err = AddToGroup(group, gpuId)
	if err != nil {
		return
	}

	result := C.dcgmWatchFields(handle.handle, group.handle, fieldsGroup.handle, C.longlong(updateFreq), C.double(maxKeepAge), C.int(maxKeepSamples))
	if err = errorString(result); err != nil {
		return groupId, fmt.Errorf("Error watching fields: %s", err)
	}

	_ = UpdateAllFields()
	return group, nil
}

func UpdateAllFields() error {
	waitForUpdate := C.int(1)
	result := C.dcgmUpdateAllFields(handle.handle, waitForUpdate)

	return errorString(result)
}
