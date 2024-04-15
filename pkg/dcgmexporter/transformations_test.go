package dcgmexporter

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestGetTransformations(t *testing.T) {
	tests := []struct {
		name   string
		config *appconfig.Config
		assert func(*testing.T, []Transform)
	}{
		{
			name: "The environment is not kubernetes",
			config: &appconfig.Config{
				Kubernetes: false,
			},
			assert: func(t *testing.T, transforms []Transform) {
				assert.Len(t, transforms, 0)
			},
		},
		{
			name: "The environment is kubernetes",
			config: &appconfig.Config{
				Kubernetes: true,
			},
			assert: func(t *testing.T, transforms []Transform) {
				assert.Len(t, transforms, 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transformations := GetTransformations(tt.config)
			tt.assert(t, transformations)
		})
	}
}
