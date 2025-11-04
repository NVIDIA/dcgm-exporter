/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package transformation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestNewLabelFilterCache tests the initialization of the label filter cache
func TestNewLabelFilterCache(t *testing.T) {
	tests := []struct {
		name                string
		patterns            []string
		expectedEnabled     bool
		expectedPatternsLen int
	}{
		{
			name:                "EmptyPatterns",
			patterns:            []string{},
			expectedEnabled:     false,
			expectedPatternsLen: 0,
		},
		{
			name:                "NilPatterns",
			patterns:            nil,
			expectedEnabled:     false,
			expectedPatternsLen: 0,
		},
		{
			name:                "ValidSinglePattern",
			patterns:            []string{"^app$"},
			expectedEnabled:     true,
			expectedPatternsLen: 1,
		},
		{
			name:                "ValidMultiplePatterns",
			patterns:            []string{"^app$", "^tier$", "^env-.*"},
			expectedEnabled:     true,
			expectedPatternsLen: 3,
		},
		{
			name:                "InvalidPattern",
			patterns:            []string{"[invalid"},
			expectedEnabled:     false, // Should disable when all patterns fail
			expectedPatternsLen: 0,
		},
		{
			name:                "MixedValidInvalid",
			patterns:            []string{"^app$", "[invalid", "^tier$"},
			expectedEnabled:     true, // Should keep valid patterns
			expectedPatternsLen: 2,    // Only 2 valid patterns
		},
		{
			name:                "ComplexRegexPatterns",
			patterns:            []string{"^app\\.kubernetes\\.io/.*", "^(tier|environment)$"},
			expectedEnabled:     true,
			expectedPatternsLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newLabelFilterCache(tt.patterns, 1000)

			assert.Equal(t, tt.expectedEnabled, cache.enabled,
				"Cache enabled state should match expected")
			assert.Equal(t, tt.expectedPatternsLen, len(cache.compiledPatterns),
				"Number of compiled patterns should match expected")

			// Verify patterns are actually compiled
			for _, pattern := range cache.compiledPatterns {
				assert.NotNil(t, pattern, "Compiled pattern should not be nil")
			}

			// Verify LRU structures are initialized when enabled
			if cache.enabled {
				assert.NotNil(t, cache.cache, "Cache map should be initialized")
				assert.NotNil(t, cache.lruList, "LRU list should be initialized")
				assert.Equal(t, 1000, cache.maxSize, "Max size should be set")
			}
		})
	}
}

// TestShouldIncludeLabel tests the label filtering logic
func TestShouldIncludeLabel(t *testing.T) {
	tests := []struct {
		name              string
		allowlistPatterns []string
		labelKey          string
		expectedIncluded  bool
	}{
		// No filtering (backward compatibility)
		{
			name:              "NoFiltering_AllLabelsIncluded",
			allowlistPatterns: []string{},
			labelKey:          "any-label",
			expectedIncluded:  true,
		},
		{
			name:              "NoFiltering_ComplexLabel",
			allowlistPatterns: []string{},
			labelKey:          "app.kubernetes.io/name",
			expectedIncluded:  true,
		},

		// Exact match patterns
		{
			name:              "ExactMatch_Included",
			allowlistPatterns: []string{"^app$"},
			labelKey:          "app",
			expectedIncluded:  true,
		},
		{
			name:              "ExactMatch_Excluded",
			allowlistPatterns: []string{"^app$"},
			labelKey:          "tier",
			expectedIncluded:  false,
		},

		// Prefix patterns
		{
			name:              "PrefixMatch_Included",
			allowlistPatterns: []string{"^app\\..*"},
			labelKey:          "app.kubernetes.io/name",
			expectedIncluded:  true,
		},
		{
			name:              "PrefixMatch_Excluded",
			allowlistPatterns: []string{"^app\\..*"},
			labelKey:          "tier",
			expectedIncluded:  false,
		},

		// Multiple patterns (OR logic)
		{
			name:              "MultiplePatterns_FirstMatches",
			allowlistPatterns: []string{"^app$", "^tier$"},
			labelKey:          "app",
			expectedIncluded:  true,
		},
		{
			name:              "MultiplePatterns_SecondMatches",
			allowlistPatterns: []string{"^app$", "^tier$"},
			labelKey:          "tier",
			expectedIncluded:  true,
		},
		{
			name:              "MultiplePatterns_NoneMatch",
			allowlistPatterns: []string{"^app$", "^tier$"},
			labelKey:          "environment",
			expectedIncluded:  false,
		},

		// Complex regex patterns
		{
			name:              "RegexAlternation_FirstOption",
			allowlistPatterns: []string{"^(app|tier|version)$"},
			labelKey:          "app",
			expectedIncluded:  true,
		},
		{
			name:              "RegexAlternation_SecondOption",
			allowlistPatterns: []string{"^(app|tier|version)$"},
			labelKey:          "tier",
			expectedIncluded:  true,
		},
		{
			name:              "RegexAlternation_NoMatch",
			allowlistPatterns: []string{"^(app|tier|version)$"},
			labelKey:          "environment",
			expectedIncluded:  false,
		},

		// Common Kubernetes label patterns
		{
			name:              "K8sStandardLabels_AppName",
			allowlistPatterns: []string{"^app\\.kubernetes\\.io/.*"},
			labelKey:          "app.kubernetes.io/name",
			expectedIncluded:  true,
		},
		{
			name:              "K8sStandardLabels_AppVersion",
			allowlistPatterns: []string{"^app\\.kubernetes\\.io/.*"},
			labelKey:          "app.kubernetes.io/version",
			expectedIncluded:  true,
		},
		{
			name:              "K8sStandardLabels_NonAppLabel",
			allowlistPatterns: []string{"^app\\.kubernetes\\.io/.*"},
			labelKey:          "custom-label",
			expectedIncluded:  false,
		},

		// Filter out high-cardinality labels
		{
			name:              "FilterHighCardinality_BuildID",
			allowlistPatterns: []string{"^app$", "^tier$"},
			labelKey:          "build-id",
			expectedIncluded:  false,
		},
		{
			name:              "FilterHighCardinality_PodTemplateHash",
			allowlistPatterns: []string{"^app$", "^tier$"},
			labelKey:          "pod-template-hash",
			expectedIncluded:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			podMapper := &PodMapper{
				Config: &appconfig.Config{
					KubernetesPodLabelAllowlistRegex: tt.allowlistPatterns,
				},
				labelFilterCache: newLabelFilterCache(tt.allowlistPatterns, 1000),
			}

			result := podMapper.shouldIncludeLabel(tt.labelKey)
			assert.Equal(t, tt.expectedIncluded, result,
				"Label '%s' inclusion should match expected", tt.labelKey)
		})
	}
}

// TestShouldIncludeLabel_Caching tests that caching works correctly
func TestShouldIncludeLabel_Caching(t *testing.T) {
	patterns := []string{"^app$", "^tier$"}
	podMapper := &PodMapper{
		Config: &appconfig.Config{
			KubernetesPodLabelAllowlistRegex: patterns,
		},
		labelFilterCache: newLabelFilterCache(patterns, 1000),
	}

	result1 := podMapper.shouldIncludeLabel("app")
	assert.True(t, result1, "First call should return true for 'app'")

	// Verify it was cached
	cache := podMapper.labelFilterCache
	cache.mu.Lock()
	elem, exists := cache.cache["app"]
	cache.mu.Unlock()
	assert.True(t, exists, "Result should be cached")
	entry := elem.Value.(*labelCacheEntry)
	assert.True(t, entry.value, "Cached value should be true")

	result2 := podMapper.shouldIncludeLabel("app")
	assert.True(t, result2, "Second call should return cached true for 'app'")

	result3 := podMapper.shouldIncludeLabel("excluded-label")
	assert.False(t, result3, "Should return false for non-matching label")

	// Verify exclusion was cached
	cache.mu.Lock()
	elem2, exists2 := cache.cache["excluded-label"]
	cache.mu.Unlock()
	assert.True(t, exists2, "Exclusion should be cached")
	entry2 := elem2.Value.(*labelCacheEntry)
	assert.False(t, entry2.value, "Cached exclusion value should be false")
}

// TestGetPodMetadata_WithLabelFiltering tests the integration of label filtering with getPodMetadata
func TestGetPodMetadata_WithLabelFiltering(t *testing.T) {
	tests := []struct {
		name              string
		allowlistPatterns []string
		podLabels         map[string]string
		expectedLabels    map[string]string // After filtering AND sanitization
	}{
		{
			name:              "NoFiltering_AllLabelsIncluded",
			allowlistPatterns: []string{},
			podLabels: map[string]string{
				"app":                       "nginx",
				"tier":                      "frontend",
				"app.kubernetes.io/name":    "my-app",
				"app.kubernetes.io/version": "1.0.0",
			},
			expectedLabels: map[string]string{
				"app":                       "nginx",
				"tier":                      "frontend",
				"app_kubernetes_io_name":    "my-app",
				"app_kubernetes_io_version": "1.0.0",
			},
		},
		{
			name:              "FilterByExactMatch",
			allowlistPatterns: []string{"^app$"},
			podLabels: map[string]string{
				"app":  "nginx",
				"tier": "frontend",
			},
			expectedLabels: map[string]string{
				"app": "nginx",
			},
		},
		{
			name:              "FilterByPrefix",
			allowlistPatterns: []string{"^app\\.kubernetes\\.io/.*"},
			podLabels: map[string]string{
				"app":                       "nginx",
				"tier":                      "frontend",
				"app.kubernetes.io/name":    "my-app",
				"app.kubernetes.io/version": "1.0.0",
				"build-id":                  "abc123",
			},
			expectedLabels: map[string]string{
				"app_kubernetes_io_name":    "my-app",
				"app_kubernetes_io_version": "1.0.0",
			},
		},
		{
			name:              "FilterMultiplePatterns",
			allowlistPatterns: []string{"^app$", "^tier$", "^environment$"},
			podLabels: map[string]string{
				"app":               "nginx",
				"tier":              "frontend",
				"environment":       "prod",
				"build-id":          "abc123",
				"pod-template-hash": "xyz789",
			},
			expectedLabels: map[string]string{
				"app":         "nginx",
				"tier":        "frontend",
				"environment": "prod",
			},
		},
		{
			name:              "FilterCommonK8sLabels",
			allowlistPatterns: []string{"^app\\.kubernetes\\.io/.*", "^version$"},
			podLabels: map[string]string{
				"app.kubernetes.io/name":      "my-app",
				"app.kubernetes.io/component": "backend",
				"app.kubernetes.io/version":   "2.0.0",
				"version":                     "v2",
				"build-id":                    "xyz",
				"controller-revision-hash":    "abc",
			},
			expectedLabels: map[string]string{
				"app_kubernetes_io_name":      "my-app",
				"app_kubernetes_io_component": "backend",
				"app_kubernetes_io_version":   "2.0.0",
				"version":                     "v2",
			},
		},
		{
			name:              "NoMatchingLabels",
			allowlistPatterns: []string{"^nonexistent$"},
			podLabels: map[string]string{
				"app":  "nginx",
				"tier": "frontend",
			},
			expectedLabels: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					UID:       types.UID("test-uid-123"),
					Labels:    tt.podLabels,
				},
			}

			fakeClient := fake.NewSimpleClientset(pod)

			config := &appconfig.Config{
				KubernetesEnablePodLabels:        true,
				KubernetesPodLabelAllowlistRegex: tt.allowlistPatterns,
			}

			podMapper := &PodMapper{
				Config:           config,
				Client:           fakeClient,
				labelFilterCache: newLabelFilterCache(config.KubernetesPodLabelAllowlistRegex, 1000),
			}

			metadata, err := podMapper.getPodMetadata("default", "test-pod")
			require.NoError(t, err, "getPodMetadata should not return error")
			require.NotNil(t, metadata, "metadata should not be nil")

			assert.Equal(t, "test-uid-123", metadata.UID, "UID should match")
			assert.Equal(t, tt.expectedLabels, metadata.Labels)
		})
	}
}
