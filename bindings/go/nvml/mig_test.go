package nvml

import (
	"testing"
)

func TestSetMigMode(t *testing.T) {
	// Initialize NVML
	err := Init()
	if err != nil {
		t.Errorf("%v", err)
	}
	defer Shutdown()

	// Grab a reference to our first device
	device, err := NewDevice(0)
	if err != nil {
		t.Errorf("%v", err)
	}

	// Disable MIG on the device
	_, err = device.SetMigMode(DEVICE_MIG_DISABLE)
	if err != nil {
		t.Errorf("error enabling MIG mode on Device: %v", err)
	}

	// Ensure MIG Mode is disabled on the device
	current, pending, err := device.GetMigMode()
	if err != nil {
		t.Errorf("error getting MIG mode on Device: %v", err)
	}
	if current != pending || current != DEVICE_MIG_DISABLE {
		t.Errorf("Expected MIG mode on Device to be DEVICE_MIG_DISABLE, got (current %v, pending %v)", current, pending)
	}

	// Enable MIG on the device
	_, err = device.SetMigMode(DEVICE_MIG_ENABLE)
	if err != nil {
		t.Errorf("error enabling MIG mode on Device: %v", err)
	}

	// Ensure MIG Mode is enabled on the device
	current, pending, err = device.GetMigMode()
	if err != nil {
		t.Errorf("error getting MIG mode on Device: %v", err)
	}
	if current != pending || current != DEVICE_MIG_ENABLE {
		t.Errorf("Expected MIG mode on Device to be DEVICE_MIG_ENABLE, got (current %v, pending %v)", current, pending)
	}

	// Disable MIG on the device
	_, err = device.SetMigMode(DEVICE_MIG_DISABLE)
	if err != nil {
		t.Errorf("error enabling MIG mode on Device: %v", err)
	}

	// Ensure MIG Mode is disabled on the device
	current, pending, err = device.GetMigMode()
	if err != nil {
		t.Errorf("error getting MIG mode on Device: %v", err)
	}
	if current != pending || current != DEVICE_MIG_DISABLE {
		t.Errorf("Expected MIG mode on Device to be DEVICE_MIG_DISABLE, got (current %v, pending %v)", current, pending)
	}
}
