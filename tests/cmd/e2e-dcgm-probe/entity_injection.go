/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>

// Constants from the pinned DCGM test and field headers.
#define FEC_EMBEDDED_HANDLE 0x7fffffff
#define FEC_FT_INT64 'i'

// Test-only DCGM field-value v1 ABI from pinned go-dcgm's dcgm_structs_internal.h.
// The package's test header spells the function dcgmEntityInjectFieldValue;
// paired DCGM 4.7 exports dcgmInjectEntityFieldValue instead.
typedef struct {
    uint32_t version;
    uint16_t fieldId;
    uint16_t fieldType;
    int32_t status;
    int64_t ts;
    union { int64_t i64; double dbl; char str[4096]; } value;
} fec_field_value;
_Static_assert(sizeof(fec_field_value) == 4120, "DCGM field-value v1 size");
_Static_assert(offsetof(fec_field_value, ts) == 16, "DCGM timestamp offset");
_Static_assert(offsetof(fec_field_value, value) == 24, "DCGM value offset");

typedef struct {
    uint32_t version;
    uint32_t persistAfterDisconnect;
    uint32_t timeoutMs;
    uint32_t addressIsUnixSocket;
} fec_connect_params;

static int fec_inject(const char *socket, unsigned int group, unsigned int entity,
                      unsigned int field, int64_t timestamp, int64_t value, int *cleanup_status) {
    *cleanup_status = 0;
    void *library = dlopen("libdcgm.so.4", RTLD_NOW | RTLD_LOCAL);
    if (!library) return -1001;
    typedef int (*inject_fn)(uintptr_t, unsigned int, unsigned int, fec_field_value *);
    inject_fn inject = (inject_fn)dlsym(library, "dcgmInjectEntityFieldValue");
    if (!inject) { dlclose(library); return -1002; }
    uintptr_t handle = FEC_EMBEDDED_HANDLE;
    typedef int (*disconnect_fn)(uintptr_t);
    disconnect_fn disconnect = NULL;
    if (socket[0]) {
        typedef int (*connect_fn)(const char *, fec_connect_params *, uintptr_t *);
        connect_fn connect = (connect_fn)dlsym(library, "dcgmConnect_v2");
        disconnect = (disconnect_fn)dlsym(library, "dcgmDisconnect");
        if (!connect || !disconnect) { dlclose(library); return -1003; }
        fec_connect_params params = {sizeof(fec_connect_params) | (2u << 24), 0, 5000, 1};
        int result = connect(socket, &params, &handle);
        if (result) { dlclose(library); return result; }
    }
    fec_field_value sample = {0};
    sample.version = sizeof(sample) | (1u << 24);
    sample.fieldId = field;
    sample.fieldType = FEC_FT_INT64;
    sample.ts = timestamp;
    sample.value.i64 = value;
    int result = inject(handle, group, entity, &sample);
    if (disconnect) {
        *cleanup_status = disconnect(handle);
    }
    dlclose(library);
    return result;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

// Reject a field-type mismatch between the test shim and pinned Go binding at build time.
var _ [1]struct{} = [1 + int(C.FEC_FT_INT64) - int(dcgm.DCGM_FT_INT64)]struct{}{}

// injectEntitySample mutates only the fake hierarchy created by this probe.
// Empty socket selects the embedded session already owned by the probe.
func injectEntitySample(socket string, sample dcgmprobe.HistorySample) error {
	address := C.CString(socket)
	defer C.free(unsafe.Pointer(address))
	var cleanupStatus C.int
	status := C.fec_inject(address, C.uint(sample.Entity.Group), C.uint(sample.Entity.ID),
		C.uint(sample.FieldID), C.int64_t(sample.TimestampUsec), C.int64_t(sample.Value), &cleanupStatus)
	return entityInjectionError(int(status), int(cleanupStatus))
}

// entityInjectionError distinguishes shim prerequisites from real DCGM errors
// and retains an injection failure even when its connection also fails to close.
func entityInjectionError(status, cleanupStatus int) error {
	var operationErr, cleanupErr error
	switch status {
	case 0:
	case -1001:
		operationErr = errors.New("load native injection library libdcgm.so.4 failed")
	case -1002:
		operationErr = errors.New("native injection library lacks dcgmInjectEntityFieldValue")
	case -1003:
		operationErr = errors.New("native injection library lacks dcgmConnect_v2 or dcgmDisconnect")
	default:
		operationErr = fmt.Errorf("native entity injection or connection failed with DCGM status %d", status)
	}
	if cleanupStatus != 0 {
		cleanupErr = fmt.Errorf("disconnect native injection client failed with DCGM status %d", cleanupStatus)
	}
	return errors.Join(operationErr, cleanupErr)
}
