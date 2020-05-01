package dcgm

/*
#include "./dcgm_agent.h"
#include "./dcgm_structs.h"
*/
import "C"
import (
	"fmt"
	"unsafe"
)

const (
	updateFreq     = 1000000 // usec
	maxKeepAge     = 300     // sec
	maxKeepSamples = 0       // nolimit
)

type FieldHandle struct{ handle C.dcgmFieldGrp_t }

func FieldGroupCreate(fieldsGroupName string, fields []Short) (fieldsId FieldHandle, err error) {
	var fieldsGroup C.dcgmFieldGrp_t
	cfields := *(*[]C.ushort)(unsafe.Pointer(&fields))

	groupName := C.CString(fieldsGroupName)
	defer freeCString(groupName)

	result := C.dcgmFieldGroupCreate(handle.handle, C.int(len(fields)), &cfields[0], groupName, &fieldsGroup)
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

func WatchFieldsWithGroup(fieldsGroup FieldHandle, group GroupHandle) error {
	result := C.dcgmWatchFields(handle.handle, group.handle, fieldsGroup.handle,
		C.longlong(updateFreq), C.double(maxKeepAge), C.int(maxKeepSamples))

	if err := errorString(result); err != nil {
		return fmt.Errorf("Error watching fields: %s", err)
	}

	if err := UpdateAllFields(); err != nil {
		return err
	}

	return nil
}

func GetLatestValuesForFields(gpu uint, fields []Short) ([]FieldValue_v1, error) {
	values := make([]C.dcgmFieldValue_v1, len(fields))
	cfields := *(*[]C.ushort)(unsafe.Pointer(&fields))

	result := C.dcgmGetLatestValuesForFields(handle.handle, C.int(gpu), &cfields[0], C.uint(len(fields)), &values[0])
	if err := errorString(result); err != nil {
		return nil, fmt.Errorf("Error watching fields: %s", err)
	}

	return toFieldValue(values), nil
}

func UpdateAllFields() error {
	waitForUpdate := C.int(1)
	result := C.dcgmUpdateAllFields(handle.handle, waitForUpdate)

	return errorString(result)
}

func toFieldValue(cfields []C.dcgmFieldValue_v1) []FieldValue_v1 {
	fields := make([]FieldValue_v1, len(cfields))
	for i, f := range cfields {
		fields[i] = FieldValue_v1{
			Version:   uint(f.version),
			FieldId:   uint(f.fieldId),
			FieldType: uint(f.fieldType),
			Status:    int(f.status),
			Ts:        int64(f.ts),
			Value:     f.value,
		}
	}

	return fields
}

func (fv FieldValue_v1) Int64() int64 {
	return *(*int64)(unsafe.Pointer(&fv.Value[0]))
}

func (fv FieldValue_v1) Float64() float64 {
	return *(*float64)(unsafe.Pointer(&fv.Value[0]))
}

func (fv FieldValue_v1) String() string {
	return *(*string)(unsafe.Pointer(&fv.Value[0]))
}

func (fv FieldValue_v1) Blob() [4096]byte {
	return fv.Value
}
