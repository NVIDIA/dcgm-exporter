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

func TestParseMigDeviceUUID(t *testing.T) {
	tests := []struct {
		name          string
		uuid          string
		expectedGPU   string
		expectedGi    uint
		expectedCi    uint
		expectedError bool
	}{
		{
			name:        "Successfull Parsing",
			uuid:        "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedGPU: "GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5",
			expectedGi:  1,
			expectedCi:  5,
		},
		{
			name:          "Fail, Missing MIG at the beginning of UUID",
			uuid:          "GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedError: true,
		},
		{
			name:          "Fail, Missing GPU at the beginning of GPU UUID",
			uuid:          "MIG-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/5",
			expectedError: true,
		},
		{
			name:          "Fail, GI not parsable",
			uuid:          "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/xx/5",
			expectedError: true,
		},
		{
			name:          "Fail, CI not a parsable",
			uuid:          "MIG-GPU-b8ea3855-276c-c9cb-b366-c6fa655957c5/1/xx",
			expectedError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gpu, gi, ci, err := ParseMigDeviceUUID(tc.uuid)
			if tc.expectedError && err != nil {
				return
			}
			if tc.expectedError && err == nil {
				t.Fatalf("Expected an error, but didn't get one: uuid: %v, (gpu: %v, gi: %v, ci: %v)", tc.uuid, gpu, gi, ci)
			}
			if !tc.expectedError && err != nil {
				t.Fatalf("Unexpected error: %v, uuid: %v, (gpu: %v, gi: %v, ci: %v)", err, tc.uuid, gpu, gi, ci)
			}
			if gpu != tc.expectedGPU || gi != tc.expectedGi || ci != tc.expectedCi {
				t.Fatalf("MIG UUID parsed incorrectly: uuid: %v, (gpu: %v, gi: %v, ci: %v)", tc.uuid, gpu, gi, ci)
			}
		})
	}
}
