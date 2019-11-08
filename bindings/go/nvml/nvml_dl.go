// Copyright (c) 2015-2018, NVIDIA CORPORATION. All rights reserved.

package nvml

import (
	"unsafe"
)

/*
#include <dlfcn.h>
#include "nvml.h"

// We wrap the call to nvmlInit() here to ensure that we pick up the correct
// version of this call. The macro magic in nvml.h that #defines the symbol
// 'nvmlInit' to 'nvmlInit_v2' is unfortunately lost on cgo.
static nvmlReturn_t nvmlInit_dl(void) {
	return nvmlInit();
}
*/
import "C"

type dlhandle struct{ handle unsafe.Pointer }

var dl dlhandle

// Initialize NVML, opening a dynamic reference to the NVML library in the process.
func (dl *dlhandle) nvmlInit() C.nvmlReturn_t {
	dl.handle = C.dlopen(C.CString("libnvidia-ml.so.1"), C.RTLD_LAZY|C.RTLD_GLOBAL)
	if dl.handle == C.NULL {
		return C.NVML_ERROR_LIBRARY_NOT_FOUND
	}
	return C.nvmlInit_dl()
}

// Shutdown NVML, closing our dynamic reference to the NVML library in the process.
func (dl *dlhandle) nvmlShutdown() C.nvmlReturn_t {
	ret := C.nvmlShutdown()
	if ret != C.NVML_SUCCESS {
		return ret
	}

	if dl.handle != C.NULL {
		err := C.dlclose(dl.handle)
		if err != 0 {
			return C.NVML_ERROR_UNKNOWN
		}
	}

	return C.NVML_SUCCESS
}

// Check to see if a specific symbol is present in the NVMl library.
func (dl *dlhandle) lookupSymbol(symbol string) C.nvmlReturn_t {
	C.dlerror()
	C.dlsym(dl.handle, C.CString(symbol))
	if unsafe.Pointer(C.dlerror()) != C.NULL {
		return C.NVML_ERROR_FUNCTION_NOT_FOUND
	}
	return C.NVML_SUCCESS
}
