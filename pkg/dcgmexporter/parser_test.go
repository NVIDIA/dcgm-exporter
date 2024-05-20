package dcgmexporter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEmptyConfigMap(t *testing.T) {
	// ConfigMap matches criteria but is empty
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap1",
			Namespace: "default",
		},
		Data: map[string]string{"metrics": ""},
	})

	c := Config{
		ConfigMapData: "default:configmap1",
	}
	records, err := readConfigMap(clientset, &c)
	if len(records) != 0 || err == nil {
		t.Fatalf("Should have returned an error and no records")
	}
}

func TestValidConfigMap(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap1",
			Namespace: "default",
		},
		Data: map[string]string{"metrics": "DCGM_FI_DEV_GPU_TEMP, gauge, temperature"},
	})

	c := Config{
		ConfigMapData: "default:configmap1",
	}
	records, err := readConfigMap(clientset, &c)
	if len(records) != 1 || err != nil {
		t.Fatalf("Should have succeeded")
	}
}

func TestInvalidConfigMapData(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap1",
			Namespace: "default",
		},
		Data: map[string]string{"bad": "DCGM_FI_DEV_GPU_TEMP, gauge, temperature"},
	})

	c := Config{
		ConfigMapData: "default:configmap1",
	}
	records, err := readConfigMap(clientset, &c)
	if len(records) != 0 || err == nil {
		t.Fatalf("Should have returned an error and no records")
	}
}

func TestInvalidConfigMapName(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap",
			Namespace: "default",
		},
	})

	c := Config{
		ConfigMapData: "default:configmap1",
	}
	records, err := readConfigMap(clientset, &c)
	if len(records) != 0 || err == nil {
		t.Fatalf("Should have returned an error and no records")
	}
}

func TestInvalidConfigMapNamespace(t *testing.T) {
	clientset := fake.NewSimpleClientset(&v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "configmap",
			Namespace: "c1",
		},
	})

	c := Config{
		ConfigMapData: "default:configmap1",
	}
	records, err := readConfigMap(clientset, &c)
	if len(records) != 0 || err == nil {
		t.Fatalf("Should have returned an error and no records")
	}
}

func TestExtractCounters(t *testing.T) {
	tests := []struct {
		name  string
		field string
		valid bool
	}{
		{
			name:  "Valid Input DCGM_FI_DEV_GPU_TEMP",
			field: "DCGM_FI_DEV_GPU_TEMP, gauge, temperature\n",
			valid: true,
		},
		{
			name:  "Invalid Input DCGM_EXP_XID_ERRORS_COUNTXXX",
			field: "DCGM_EXP_XID_ERRORS_COUNTXXX, gauge, temperature\n",
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractCountersHelper(t, tt.field, tt.valid)
		})
	}
}

func extractCountersHelper(t *testing.T, input string, valid bool) {
	tmpFile, err := os.CreateTemp(os.TempDir(), "prefix-")
	if err != nil {
		t.Fatalf("Cannot create temporary file: %v", err)
	}

	defer os.Remove(tmpFile.Name())

	text := []byte(input)
	if _, err = tmpFile.Write(text); err != nil {
		t.Fatalf("Failed to write to temporary file: %v", err)
	}

	t.Logf("Using file: %s", tmpFile.Name())

	if err := tmpFile.Close(); err != nil {
		t.Fatalf("Cannot close temp file: %v", err)
	}

	c := Config{
		ConfigMapData:  undefinedConfigMapData,
		CollectorsFile: tmpFile.Name(),
	}
	cc, err := GetCounterSet(&c)
	if valid {
		assert.NoError(t, err, "Expected no error.")
		assert.Equal(t, 1, len(cc.DCGMCounters), "Expected 1 record counters.")
	} else {
		assert.Error(t, err, "Expected error.")
		assert.Nil(t, cc, "Expected no counters.")
	}
}
