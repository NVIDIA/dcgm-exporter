package dcgmexporter

import (
	"os"
	"testing"

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
	tmpFile, err := os.CreateTemp(os.TempDir(), "prefix-")
	if err != nil {
		t.Fatalf("Cannot create temporary file: %v", err)
	}

	defer os.Remove(tmpFile.Name())

	text := []byte("DCGM_FI_DEV_GPU_TEMP, gauge, temperature\n")
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
	records, _, err := ExtractCounters(&c)
	if len(records) != 1 || err != nil {
		t.Fatalf("Should have succeeded: records (%d != 1) err=%v", len(records), err)
	}
}
