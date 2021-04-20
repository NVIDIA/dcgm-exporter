package dcgm

/*
#include "dcgm_agent.h"
#include "dcgm_structs.h"
*/
import "C"
import (
	"fmt"
)

type GroupHandle struct{ handle C.dcgmGpuGrp_t }

func CreateGroup(groupName string) (goGroupId GroupHandle, err error) {
	var cGroupId C.dcgmGpuGrp_t
	cname := C.CString(groupName)
	defer freeCString(cname)

	result := C.dcgmGroupCreate(handle.handle, C.DCGM_GROUP_EMPTY, cname, &cGroupId)
	if err = errorString(result); err != nil {
		return goGroupId, fmt.Errorf("Error creating group: %s", err)
	}

	goGroupId = GroupHandle{cGroupId}
	return
}

func NewDefaultGroup(groupName string) (GroupHandle, error) {
	var cGroupId C.dcgmGpuGrp_t

	cname := C.CString(groupName)
	defer freeCString(cname)

	result := C.dcgmGroupCreate(handle.handle, C.DCGM_GROUP_DEFAULT, cname, &cGroupId)
	if err := errorString(result); err != nil {
		return GroupHandle{}, fmt.Errorf("Error creating group: %s", err)
	}

	return GroupHandle{cGroupId}, nil
}

func AddToGroup(groupId GroupHandle, gpuId uint) (err error) {
	result := C.dcgmGroupAddDevice(handle.handle, groupId.handle, C.uint(gpuId))
	if err = errorString(result); err != nil {
		return fmt.Errorf("Error adding GPU %v to group: %s", gpuId, err)
	}

	return
}

func AddEntityToGroup(groupId GroupHandle, entityGroupId Field_Entity_Group, entityId uint) (err error) {
	result := C.dcgmGroupAddEntity(handle.handle, groupId.handle, C.dcgm_field_entity_group_t(entityGroupId), C.uint(entityId))
	if err = errorString(result); err != nil {
		return fmt.Errorf("Error adding entity group type %v, entity %v to group: %s", entityGroupId, entityId, err)
	}

	return
}

func DestroyGroup(groupId GroupHandle) (err error) {
	result := C.dcgmGroupDestroy(handle.handle, groupId.handle)
	if err = errorString(result); err != nil {
		return fmt.Errorf("Error destroying group: %s", err)
	}

	return
}
