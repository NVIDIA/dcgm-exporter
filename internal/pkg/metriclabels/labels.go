// Package metriclabels owns the built-in Prometheus label names for each DCGM entity group.
// Renderers and enrichers use it to avoid accepting a user label that changes a metric identity.
package metriclabels

import "github.com/NVIDIA/go-dcgm/pkg/dcgm"

// IsBuiltIn reports whether name is produced by the renderer for group. uuidLabel
// is included because the selected GPU UUID label name remains configurable.
func IsBuiltIn(group dcgm.Field_Entity_Group, name, uuidLabel string) bool {
	if name == "" {
		return false
	}
	if group == dcgm.FE_GPU && name == uuidLabel {
		return true
	}
	_, ok := names[group][name]
	return ok
}

// names contains static label names emitted by the renderer for each DCGM
// entity group. IsBuiltIn handles the configurable GPU UUID label separately.
var names = map[dcgm.Field_Entity_Group]map[string]struct{}{
	dcgm.FE_GPU: {
		"GPU_CI_ID":     {},
		"GPU_I_ID":      {},
		"GPU_I_PROFILE": {},
		"device":        {},
		"gpu":           {},
		"hostname":      {},
		"modelName":     {},
		"pci_bus_id":    {},
	},
	dcgm.FE_SWITCH: {
		"hostname": {},
		"nvswitch": {},
	},
	dcgm.FE_LINK: {
		"GPU_I_ID":      {},
		"GPU_I_PROFILE": {},
		"device":        {},
		"gpu":           {},
		"gpu_uuid":      {},
		"hostname":      {},
		"model_name":    {},
		"nvlink":        {},
		"nvswitch":      {},
		"pci_bus_id":    {},
	},
	dcgm.FE_CPU: {
		"cpu":        {},
		"cpu_serial": {},
		"hostname":   {},
	},
	dcgm.FE_CPU_CORE: {
		"cpu":      {},
		"cpucore":  {},
		"hostname": {},
	},
}
