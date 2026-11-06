/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package static_test

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/action"
	helmchart "helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

const (
	envEnablePodLabels = "DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS"
	envEnablePodUID    = "DCGM_EXPORTER_KUBERNETES_ENABLE_POD_UID"
	envEnableDRA       = "KUBERNETES_ENABLE_DRA"
)

// rbacCase is one combination of chart values for the Kubernetes RBAC contract.
type rbacCase struct {
	labels, uid, dra, create, createAccount bool
	token                                   interface{}
}

// wantRBAC reports whether the chart should create the ClusterRole and binding.
func (c rbacCase) wantRBAC() bool { return (c.labels || c.uid || c.dra) && c.create }

// wantToken reports whether the service account token should be mounted.
func (c rbacCase) wantToken() bool {
	if c.token != nil {
		return c.token.(bool)
	}

	return c.wantRBAC()
}

// values returns the chart values for the case. The features are set in `config`.
func (c rbacCase) values() map[string]interface{} {
	sources := map[string]interface{}{
		"kubernetes": map[string]interface{}{
			"pod_uid":    c.uid,
			"pod_labels": map[string]interface{}{"enabled": c.labels, "allow": []string{".*"}},
		},
	}
	config := map[string]interface{}{"sources": sources}
	if c.dra {
		sources["dra"] = map[string]interface{}{"mode": "kubernetes"}
		config["metrics"] = draMetrics("true")
	}

	return map[string]interface{}{
		"config":         config,
		"kubernetes":     map[string]interface{}{"rbac": map[string]interface{}{"create": c.create}},
		"serviceAccount": map[string]interface{}{"create": c.createAccount, "name": "external-or-managed", "automountServiceAccountToken": c.token},
	}
}

// draMetrics returns the chart's default metrics list with
// enrichment.dra set to mode (true, false or auto).
func draMetrics(mode string) []interface{} {
	return []interface{}{map[string]interface{}{
		"name":       "default",
		"file":       chartMetricsFile,
		"every":      "30s",
		"enrichment": map[string]interface{}{"kubernetes": "auto", "dra": mode},
	}}
}

// rbacCases lists every combination of features, RBAC creation, token setting
// and service account creation.
func rbacCases() []rbacCase {
	var cases []rbacCase
	for features := 0; features < 8; features++ {
		for _, create := range []bool{false, true} {
			for _, token := range []interface{}{nil, false, true} {
				for _, createAccount := range []bool{false, true} {
					cases = append(cases, rbacCase{
						labels: features&1 != 0, uid: features&2 != 0, dra: features&4 != 0,
						create: create, token: token, createAccount: createAccount,
					})
				}
			}
		}
	}

	return cases
}

// requireTokenMount checks the token setting on the service account and pod.
func requireTokenMount(t *testing.T, resources []chartResource, tc rbacCase, daemonSet *chartResource) {
	t.Helper()
	if tc.createAccount {
		account := requireResourceKind(t, resources, "ServiceAccount")
		require.NotNil(t, account.AutomountServiceAccountToken)
		require.Equal(t, tc.wantToken(), *account.AutomountServiceAccountToken)
	} else {
		require.False(t, hasResourceKind(resources, "ServiceAccount"))
	}

	require.NotNil(t, daemonSet.Spec.Template.Spec.AutomountServiceAccountToken)
	require.Equal(t, tc.wantToken(), *daemonSet.Spec.Template.Spec.AutomountServiceAccountToken)
}

// requireClusterRBAC checks the ClusterRole, its binding and their rules.
func requireClusterRBAC(t *testing.T, resources []chartResource, tc rbacCase) {
	t.Helper()
	require.Equal(t, tc.wantRBAC(), hasResourceKind(resources, "ClusterRole"))
	require.Equal(t, tc.wantRBAC(), hasResourceKind(resources, "ClusterRoleBinding"))
	if !tc.wantRBAC() {
		return
	}

	verbs := []string{"get", "list", "watch"}
	expected := []rbacRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: verbs}}
	if tc.dra {
		expected = append(expected, rbacRule{APIGroups: []string{"resource.k8s.io"}, Resources: []string{"resourceslices"}, Verbs: verbs})
	}

	require.Equal(t, expected, requireResourceKind(t, resources, "ClusterRole").Rules)
}

// TestChartKubernetesRBACRenderContract verifies Kubernetes features opt into pod-reader access.
func TestChartKubernetesRBACRenderContract(t *testing.T) {
	for _, tc := range rbacCases() {
		name := fmt.Sprintf("labels=%t/uid=%t/dra=%t/rbac=%t/token=%v/account=%t",
			tc.labels, tc.uid, tc.dra, tc.create, tc.token, tc.createAccount)
		t.Run(name, func(t *testing.T) {
			resources := renderChart(t, tc.values())
			daemonSet := requireResourceKind(t, resources, "DaemonSet")
			requireTokenMount(t, resources, tc, &daemonSet)
			requireClusterRBAC(t, resources, tc)

			// The config file carries the features, never an environment variable.
			envNames := daemonSetContainerEnvNames(t, daemonSet, "exporter")
			for _, env := range []string{envEnablePodLabels, envEnablePodUID, envEnableDRA} {
				require.False(t, contains(envNames, env), env)
			}

			// The chart creates no namespaced Role or RoleBinding.
			require.False(t, hasResourceKind(resources, "Role"))
			require.False(t, hasResourceKind(resources, "RoleBinding"))
		})
	}
}

// TestChartDRAEnrichmentDefaultsToAuto checks that a metrics entry without enrichment.dra still gets the
// ResourceSlice permissions when sources.dra is set, because the exporter reads a missing switch as auto.
func TestChartDRAEnrichmentDefaultsToAuto(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"config": map[string]interface{}{
			"metrics": []interface{}{map[string]interface{}{"name": "default", "file": chartMetricsFile}},
			"sources": map[string]interface{}{"dra": map[string]interface{}{"mode": "kubernetes"}},
		},
	})

	var resourceSlices bool
	for _, rule := range requireResourceKind(t, resources, "ClusterRole").Rules {
		resourceSlices = resourceSlices || contains(rule.Resources, "resourceslices")
	}
	assert.True(t, resourceSlices, "ClusterRole must allow reading ResourceSlices")
}

// annotationKeys returns the annotation names in sorted order.
func annotationKeys(annotations map[string]string) []string {
	keys := make([]string, 0, len(annotations))
	for key := range annotations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestChartDaemonSetAnnotationsRenderContract keeps DaemonSet and pod-template annotations separate.
func TestChartDaemonSetAnnotationsRenderContract(t *testing.T) {
	t.Run("default deployment omits DaemonSet annotations", func(t *testing.T) {
		resources := renderChart(t, nil)
		daemonSet := requireResourceKind(t, resources, "DaemonSet")

		assert.Nil(t, daemonSet.Metadata.Annotations)
		// Only the chart's own config checksum; the user set no pod annotations.
		assert.Equal(t, []string{"checksum/config"}, annotationKeys(daemonSet.Spec.Template.Metadata.Annotations))
	})

	t.Run("DaemonSet annotations do not affect pod annotations", func(t *testing.T) {
		resources := renderChart(t, map[string]interface{}{
			"daemonSetAnnotations": map[string]interface{}{
				"example.com/release-owner": "platform-team",
			},
			"podAnnotations": map[string]interface{}{
				"example.com/scrape": "enabled",
			},
		})
		daemonSet := requireResourceKind(t, resources, "DaemonSet")

		assert.Equal(t, map[string]string{
			"example.com/release-owner": "platform-team",
		}, daemonSet.Metadata.Annotations)
		podAnnotations := daemonSet.Spec.Template.Metadata.Annotations
		assert.Equal(t, "enabled", podAnnotations["example.com/scrape"])
		assert.Equal(t, []string{"checksum/config", "example.com/scrape"}, annotationKeys(podAnnotations))
	})
}

// TestChartDefaultMetricsUseCanonicalCSV checks rendered defaults against the
// product-owned CSV. The Task static test uses the packaged chart archive.
func TestChartDefaultMetricsUseCanonicalCSV(t *testing.T) {
	want, err := os.ReadFile(repoPath(t, "etc", "default-counters.csv"))
	require.NoError(t, err)

	// Review enabled names and types when changing the shipped defaults. Row order,
	// comments, and help-text edits do not change the selected metric families.
	rows, err := metriccontract.ReadDefaultCounterRows(strings.NewReader(string(want)))
	require.NoError(t, err)
	for index := range rows {
		rows[index].Help = ""
	}
	assert.ElementsMatch(t, []metriccontract.DefaultCounterRow{
		{Name: "DCGM_FI_DEV_SM_CLOCK_HERTZ", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_MEMORY_CLOCK_HERTZ", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_MEMORY_TEMP_CELSIUS", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_BOARD_POWER_WATTS", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL", Type: dto.MetricType_COUNTER},
		{Name: "DCGM_FI_DEV_PCIE_REPLAY_TOTAL", Type: dto.MetricType_COUNTER},
		{Name: "DCGM_FI_DEV_GPU_UTIL_RATIO", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_MEMORY_UTIL_RATIO", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_ENC_UTIL_RATIO", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_DEC_UTIL_RATIO", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_XID_ERROR", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_FB_CAPACITY_BYTES", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_FB_FREE_BYTES", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_FB_USED_BYTES", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_FB_RESERVED_BYTES", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_VGPU_LICENSE_STATUS", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_ROW_REMAP_UNCORRECTABLE_TOTAL", Type: dto.MetricType_COUNTER},
		{Name: "DCGM_FI_DEV_ROW_REMAP_CORRECTABLE_TOTAL", Type: dto.MetricType_COUNTER},
		{Name: "DCGM_FI_DEV_ROW_REMAP_FAILED", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_SYSTEM_DRIVER_VERSION", Type: dto.MetricType_UNTYPED},
		{Name: "DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_PROF_TENSOR_UTIL_RATIO", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_PROF_DRAM_UTIL_RATIO", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_PROF_PCIE_TX_BYTES", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_PROF_PCIE_RX_BYTES", Type: dto.MetricType_GAUGE},
	}, rows, "enabled default counter names and types must be reviewed together")

	resources := renderChart(t, nil)
	metricsConfigMap := requireResourceName(t, resources, "ConfigMap", metricsConfigMapName)
	// YAML's literal-scalar decoding removes the source file's final newline.
	assert.Equal(t, strings.TrimSuffix(string(want), "\n"), metricsConfigMap.Data["metrics"])
}

// TestSourceChartDefaultMetrics compares the source chart's defaults with the canonical metrics CSV.
func TestSourceChartDefaultMetrics(t *testing.T) {
	linkInfo, err := os.Lstat(repoPath(t, "deployment", "files", "default-counters.csv"))
	require.NoError(t, err)
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Skip("source-chart defaults require Git to materialize the metrics symlink; the packaged chart does not")
	}

	chart, err := loader.Load(repoPath(t, "deployment"))
	require.NoError(t, err)
	want, err := os.ReadFile(repoPath(t, "etc", "default-counters.csv"))
	require.NoError(t, err)
	result, err := runHelmInstallChart(t, chart, nil)
	require.NoError(t, err)
	configMap := requireResourceName(t, parseManifest(t, result.Manifest), "ConfigMap", metricsConfigMapName)
	assert.Equal(t, strings.TrimSuffix(string(want), "\n"), configMap.Data["metrics"])
}

// TestChartMissingDefaultMetrics rejects missing chart metrics unless custom metrics supply the ConfigMap.
func TestChartMissingDefaultMetrics(t *testing.T) {
	chart := loadChart(t)
	var files []*helmchart.File
	for _, file := range chart.Files {
		if file.Name != "files/default-counters.csv" {
			files = append(files, file)
		}
	}
	chart.Files = files
	_, err := runHelmInstallChart(t, chart, nil)
	require.ErrorContains(t, err, "chart is missing files/default-counters.csv")
	custom := "DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature"
	result, err := runHelmInstallChart(t, chart, map[string]interface{}{"customMetrics": custom})
	require.NoError(t, err)
	configMap := requireResourceName(t, parseManifest(t, result.Manifest), "ConfigMap", metricsConfigMapName)
	assert.Equal(t, custom, configMap.Data["metrics"])
}

// TestChartRejectsSymlinkPlaceholder rejects a source-chart symlink placeholder used as packaged file content.
func TestChartRejectsSymlinkPlaceholder(t *testing.T) {
	chart := loadChart(t)
	found := false
	for _, file := range chart.Files {
		if file.Name == "files/default-counters.csv" {
			file.Data = []byte("../../etc/default-counters.csv")
			found = true
			break
		}
	}
	require.True(t, found, "chart must contain the default counters file")

	_, err := runHelmInstallChart(t, chart, nil)
	require.ErrorContains(t, err, "source-chart defaults need symlink support")
}

// TestChartFieldWatchRetentionArgumentsRenderContract verifies existing Helm argument passthrough carries both flags.
func TestChartFieldWatchRetentionArgumentsRenderContract(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"arguments": []interface{}{
			"--watch-max-keep-age=10m",
			"--watch-max-keep-samples=0",
		},
	})

	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	container := requireContainer(t, daemonSet, "exporter")
	assert.Contains(t, container.Args, "--watch-max-keep-age=10m")
	assert.Contains(t, container.Args, "--watch-max-keep-samples=0")
}

func TestChartImageRenderingContract(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"image": map[string]interface{}{
			"repository": "registry.example/dcgm-exporter",
			"tag":        "ignored-tag",
			"digest":     "sha256:abc123",
		},
		"imagePullSecrets": []interface{}{
			map[string]interface{}{"name": "registry-auth"},
		},
	})

	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	container := requireContainer(t, daemonSet, "exporter")
	assert.Equal(t, "registry.example/dcgm-exporter@sha256:abc123", container.Image)
	assert.NotContains(t, container.Image, "ignored-tag")
	require.Len(t, daemonSet.Spec.Template.Spec.ImagePullSecrets, 1)
	assert.Equal(t, "registry-auth", daemonSet.Spec.Template.Spec.ImagePullSecrets[0].Name)
}

// TestChartProbeSettingsRenderContract checks the liveness and readiness
// settings, including that they wait 45 seconds only when no startup probe
// covers a slow start and the user sets no delay.
func TestChartProbeSettingsRenderContract(t *testing.T) {
	testCases := []struct {
		name               string
		values             map[string]interface{}
		wantLivenessProbe  probe
		wantReadinessProbe probe
	}{
		{
			name: "defaults leave optional Kubernetes probe settings unset",
			wantLivenessProbe: probe{
				PeriodSeconds: intPointer(5),
			},
			wantReadinessProbe: probe{},
		},
		{
			name:   "without a startup probe both probes wait 45 seconds",
			values: map[string]interface{}{"startupProbe": map[string]interface{}{"enabled": false}},
			wantLivenessProbe: probe{
				InitialDelaySeconds: 45,
				PeriodSeconds:       intPointer(5),
			},
			wantReadinessProbe: probe{InitialDelaySeconds: 45},
		},
		{
			name: "a set initial delay renders without a startup probe",
			values: map[string]interface{}{
				"startupProbe":   map[string]interface{}{"enabled": false},
				"livenessProbe":  map[string]interface{}{"initialDelaySeconds": 30},
				"readinessProbe": map[string]interface{}{"initialDelaySeconds": 10},
			},
			wantLivenessProbe: probe{
				InitialDelaySeconds: 30,
				PeriodSeconds:       intPointer(5),
			},
			wantReadinessProbe: probe{InitialDelaySeconds: 10},
		},
		{
			name: "a set initial delay renders with the default startup probe",
			values: map[string]interface{}{
				"livenessProbe":  map[string]interface{}{"initialDelaySeconds": 30},
				"readinessProbe": map[string]interface{}{"initialDelaySeconds": 10},
			},
			wantLivenessProbe: probe{
				InitialDelaySeconds: 30,
				PeriodSeconds:       intPointer(5),
			},
			wantReadinessProbe: probe{InitialDelaySeconds: 10},
		},
		{
			name: "explicit probe settings render for liveness and readiness",
			values: map[string]interface{}{
				"livenessProbe": map[string]interface{}{
					"timeoutSeconds":   9,
					"failureThreshold": 12,
				},
				"readinessProbe": map[string]interface{}{
					"periodSeconds":    7,
					"timeoutSeconds":   8,
					"failureThreshold": 11,
				},
			},
			wantLivenessProbe: probe{
				PeriodSeconds:    intPointer(5),
				TimeoutSeconds:   intPointer(9),
				FailureThreshold: intPointer(12),
			},
			wantReadinessProbe: probe{
				PeriodSeconds:    intPointer(7),
				TimeoutSeconds:   intPointer(8),
				FailureThreshold: intPointer(11),
			},
		},
		{
			name: "explicit zero values are rendered for Kubernetes validation",
			values: map[string]interface{}{
				"livenessProbe": map[string]interface{}{
					"timeoutSeconds":   0,
					"failureThreshold": 0,
				},
				"readinessProbe": map[string]interface{}{
					"periodSeconds":    0,
					"timeoutSeconds":   0,
					"failureThreshold": 0,
				},
			},
			wantLivenessProbe: probe{
				PeriodSeconds:    intPointer(5),
				TimeoutSeconds:   intPointer(0),
				FailureThreshold: intPointer(0),
			},
			wantReadinessProbe: probe{
				PeriodSeconds:    intPointer(0),
				TimeoutSeconds:   intPointer(0),
				FailureThreshold: intPointer(0),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resources := renderChart(t, tc.values)
			container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")

			assert.Equal(t, tc.wantLivenessProbe, container.LivenessProbe)
			assert.Equal(t, tc.wantReadinessProbe, container.ReadinessProbe)
		})
	}
}

// TestChartStartupProbeRenderContract checks that the chart gives a slow start
// up to five minutes by default, also for values that leave the startupProbe key
// out, and that the probe can be tuned, turned off, or removed with an explicit
// `startupProbe: null`.
func TestChartStartupProbeRenderContract(t *testing.T) {
	testCases := []struct {
		name   string
		values map[string]interface{}
		want   *probe
	}{
		{
			name: "defaults allow five minutes to start",
			want: &probe{PeriodSeconds: intPointer(5), FailureThreshold: intPointer(60)},
		},
		{
			name: "explicit settings render",
			values: map[string]interface{}{
				"startupProbe": map[string]interface{}{"periodSeconds": 10, "failureThreshold": 30, "timeoutSeconds": 3},
			},
			want: &probe{PeriodSeconds: intPointer(10), FailureThreshold: intPointer(30), TimeoutSeconds: intPointer(3)},
		},
		{
			name:   "disabled renders no startup probe",
			values: map[string]interface{}{"startupProbe": map[string]interface{}{"enabled": false}},
		},
		{
			name:   "values without a startupProbe key get the default probe",
			values: map[string]interface{}{"service": map[string]interface{}{"port": 9400}},
			want:   &probe{PeriodSeconds: intPointer(5), FailureThreshold: intPointer(60)},
		},
		{
			name:   "an explicit null startupProbe renders no startup probe",
			values: map[string]interface{}{"startupProbe": nil},
		},
		{
			name:   "an enabled probe without timings still allows five minutes",
			values: map[string]interface{}{"startupProbe": map[string]interface{}{"periodSeconds": nil, "failureThreshold": nil}},
			want:   &probe{PeriodSeconds: intPointer(5), FailureThreshold: intPointer(60)},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resources := renderChart(t, tc.values)
			container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")

			assert.Equal(t, tc.want, container.StartupProbe)
		})
	}
}

// TestChartProbeHandlersRenderContract checks that the startup, liveness and
// readiness probes all reach the exporter the same way: HTTP on /health, HTTPS
// with TLS, and a TCP connection when basic authentication would refuse an
// unauthenticated request.
func TestChartProbeHandlersRenderContract(t *testing.T) {
	testCases := []struct {
		name       string
		values     map[string]interface{}
		wantScheme string // empty means a TCP socket check
	}{
		{name: "defaults use HTTP", wantScheme: "HTTP"},
		{name: "TLS uses HTTPS", values: map[string]interface{}{"tlsServerConfig": map[string]interface{}{"enabled": true}}, wantScheme: "HTTPS"},
		{name: "basic auth uses a TCP socket", values: map[string]interface{}{"basicAuth": map[string]interface{}{"users": map[string]interface{}{"admin": "secret"}}}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			release, err := runHelmInstall(t, tc.values)
			require.NoError(t, err)
			var spec struct {
				Kind string `yaml:"kind"`
				Spec struct {
					Template struct {
						Spec struct {
							Containers []struct {
								Name           string        `yaml:"name"`
								StartupProbe   *probeHandler `yaml:"startupProbe"`
								LivenessProbe  *probeHandler `yaml:"livenessProbe"`
								ReadinessProbe *probeHandler `yaml:"readinessProbe"`
							} `yaml:"containers"`
						} `yaml:"spec"`
					} `yaml:"template"`
				} `yaml:"spec"`
			}
			for _, document := range releaseutil.SplitManifests(release.Manifest) {
				if strings.Contains(document, "kind: DaemonSet") {
					require.NoError(t, yaml.Unmarshal([]byte(document), &spec))
				}
			}
			require.Equal(t, "DaemonSet", spec.Kind)
			require.NotEmpty(t, spec.Spec.Template.Spec.Containers)
			exporter := spec.Spec.Template.Spec.Containers[0]
			require.Equal(t, "exporter", exporter.Name)

			for name, handler := range map[string]*probeHandler{
				"startup": exporter.StartupProbe, "liveness": exporter.LivenessProbe, "readiness": exporter.ReadinessProbe,
			} {
				require.NotNil(t, handler, "%s probe", name)
				if tc.wantScheme == "" {
					assert.Nil(t, handler.HTTPGet, "%s probe", name)
					require.NotNil(t, handler.TCPSocket, "%s probe", name)
					assert.Equal(t, 9400, handler.TCPSocket.Port, "%s probe", name)
					continue
				}
				assert.Nil(t, handler.TCPSocket, "%s probe", name)
				require.NotNil(t, handler.HTTPGet, "%s probe", name)
				assert.Equal(t, "/health", handler.HTTPGet.Path, "%s probe", name)
				assert.Equal(t, 9400, handler.HTTPGet.Port, "%s probe", name)
				assert.Equal(t, tc.wantScheme, handler.HTTPGet.Scheme, "%s probe", name)
			}
		})
	}
}

// TestChartServiceMonitorRenderingContract renders endpoint settings, labels, and both relabeling lists.
func TestChartServiceMonitorRenderingContract(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"serviceMonitor": map[string]interface{}{
			"enabled":       true,
			"interval":      "45s",
			"scrapeTimeout": "40s",
			"honorLabels":   true,
			"additionalLabels": map[string]interface{}{
				"monitoring": "prometheus",
			},
			"relabelings": []interface{}{
				map[string]interface{}{
					"sourceLabels": []interface{}{"__meta_kubernetes_pod_node_name"},
					"targetLabel":  "nodename",
					"action":       "replace",
				},
			},
			"metricRelabelings": []interface{}{
				map[string]interface{}{
					"sourceLabels": []interface{}{"__name__"},
					"regex":        "DCGM_FI_DEV_GPU_TEMP",
					"action":       "keep",
				},
			},
		},
	})

	serviceMonitor := requireResourceKind(t, resources, "ServiceMonitor")
	assert.Equal(t, "prometheus", serviceMonitor.Metadata.Labels["monitoring"])
	require.Len(t, serviceMonitor.Spec.Endpoints, 1)
	endpoint := serviceMonitor.Spec.Endpoints[0]
	assert.Equal(t, "metrics", endpoint.Port)
	assert.Equal(t, "/metrics", endpoint.Path)
	assert.Equal(t, "45s", endpoint.Interval)
	assert.Equal(t, "40s", endpoint.ScrapeTimeout)
	assert.True(t, endpoint.HonorLabels)
	require.Len(t, endpoint.Relabelings, 1)
	assert.Equal(t, "nodename", endpoint.Relabelings[0].TargetLabel)
	assert.Equal(t, "replace", endpoint.Relabelings[0].Action)
	assert.Equal(t, []string{"__meta_kubernetes_pod_node_name"}, endpoint.Relabelings[0].SourceLabels)
	require.Len(t, endpoint.MetricRelabelings, 1)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", endpoint.MetricRelabelings[0].Regex)
	assert.Equal(t, "keep", endpoint.MetricRelabelings[0].Action)
}

// The ServiceMonitor is rendered whenever it is enabled, whatever the exporter config setup is.
func TestChartServiceMonitorIgnoresExporterConfig(t *testing.T) {
	tests := []struct {
		name        string
		values      map[string]interface{}
		wantMonitor bool
	}{
		{name: "default", wantMonitor: true},
		{name: "chart-created config", values: map[string]interface{}{"config": map[string]interface{}{"version": 2}}, wantMonitor: true},
		{name: "existing ConfigMap", values: map[string]interface{}{"configMap": map[string]interface{}{"existingName": "external"}}, wantMonitor: true},
		{name: "disabled monitor", values: map[string]interface{}{"serviceMonitor": map[string]interface{}{"enabled": false}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := false
			for _, resource := range renderChart(t, tt.values) {
				found = found || resource.Kind == "ServiceMonitor"
			}

			assert.Equal(t, tt.wantMonitor, found)
		})
	}
}

// daemonSetContainerEnvNames returns environment variable names for a rendered DaemonSet container.
func daemonSetContainerEnvNames(t *testing.T, daemonSet chartResource, containerName string) []string {
	t.Helper()

	container := requireContainer(t, daemonSet, containerName)

	envNames := make([]string, 0, len(container.Env))
	for _, env := range container.Env {
		envNames = append(envNames, env.Name)
	}
	return envNames
}

// requireContainer returns a named DaemonSet container or fails the test.
func requireContainer(t *testing.T, daemonSet chartResource, containerName string) container {
	t.Helper()

	for _, container := range daemonSet.Spec.Template.Spec.Containers {
		if container.Name == containerName {
			return container
		}
	}
	t.Fatalf("DaemonSet %q has no container named %q", daemonSet.Metadata.Name, containerName)
	return container{}
}

// renderChart renders the Helm chart with in-memory Helm clients and returns parsed resources.
func renderChart(t *testing.T, values map[string]interface{}) []chartResource {
	t.Helper()

	release, err := runHelmInstall(t, values)
	require.NoError(t, err)

	return parseManifest(t, release.Manifest)
}

// renderChartError returns the Helm error for invalid chart values.
func renderChartError(t *testing.T, values map[string]interface{}) error {
	t.Helper()

	_, err := runHelmInstall(t, values)
	return err
}

// runHelmInstall renders the chart with in-memory Helm clients and no cluster access.
func runHelmInstall(t *testing.T, values map[string]interface{}) (*release.Release, error) {
	t.Helper()

	return runHelmInstallChart(t, loadChart(t), values)
}

// runHelmInstallChart renders a chart through Helm's client-only dry-run install path.
func runHelmInstallChart(t *testing.T, chart *helmchart.Chart, values map[string]interface{}) (*release.Release, error) {
	t.Helper()

	registryClient, err := registry.NewClient()
	require.NoError(t, err)

	config := &action.Configuration{
		Releases:       storage.Init(driver.NewMemory()),
		KubeClient:     &kubefake.PrintingKubeClient{Out: io.Discard},
		Capabilities:   chartutil.DefaultCapabilities,
		RegistryClient: registryClient,
		Log:            func(string, ...interface{}) {},
	}

	install := action.NewInstall(config)
	install.ClientOnly = true
	install.DryRun = true
	install.Namespace = "dcgm-exporter"
	install.ReleaseName = "dcgm-exporter"

	return install.Run(chart, values)
}

// loadChart loads the configured chart archive or the source deployment chart.
func loadChart(t *testing.T) *helmchart.Chart {
	t.Helper()

	if archive := chartArchivePath(); archive != "" {
		archiveFile, err := os.Open(archive)
		require.NoError(t, err)
		defer archiveFile.Close()

		chart, err := loader.LoadArchive(archiveFile)
		require.NoError(t, err)
		return chart
	}

	chart, err := loader.Load(repoPath(t, "deployment"))
	require.NoError(t, err)
	for _, file := range chart.Files {
		if file.Name == "files/default-counters.csv" && strings.TrimSpace(string(file.Data)) == "../../etc/default-counters.csv" {
			t.Skip("source-chart defaults need symlink support; run task test:static to check the packaged chart")
		}
	}

	return chart
}

// chartArchivePath returns DCGM_EXPORTER_CHART_ARCHIVE for packaged-chart tests.
func chartArchivePath() string {
	return os.Getenv("DCGM_EXPORTER_CHART_ARCHIVE")
}

// parseManifest decodes multi-document YAML into the subset of resource fields under test.
func parseManifest(t *testing.T, manifest string) []chartResource {
	t.Helper()

	var resources []chartResource
	documents := releaseutil.SplitManifests(manifest)
	keys := make([]string, 0, len(documents))
	for key := range documents {
		keys = append(keys, key)
	}
	sort.Sort(releaseutil.BySplitManifestsOrder(keys))

	for _, key := range keys {
		document := strings.TrimSpace(documents[key])
		if document == "" {
			continue
		}

		var resource chartResource
		require.NoError(t, yaml.Unmarshal([]byte(document), &resource))
		if resource.Kind == "" {
			continue
		}
		resources = append(resources, resource)
	}

	return resources
}

// requireResourceKind returns the first rendered resource of the requested kind.
func requireResourceKind(t *testing.T, resources []chartResource, kind string) chartResource {
	t.Helper()

	for _, resource := range resources {
		if resource.Kind == kind {
			return resource
		}
	}

	t.Fatalf("rendered chart has no %s resource", kind)
	return chartResource{}
}

// requireResourceName returns a rendered resource matching kind and name or fails the test.
func requireResourceName(t *testing.T, resources []chartResource, kind string, name string) chartResource {
	t.Helper()

	for _, resource := range resources {
		if resource.Kind == kind && resource.Metadata.Name == name {
			return resource
		}
	}

	t.Fatalf("rendered chart has no %s named %s", kind, name)
	return chartResource{}
}

// hasResourceName reports whether the rendered resources contain the given kind and name.
func hasResourceName(resources []chartResource, kind string, name string) bool {
	for _, resource := range resources {
		if resource.Kind == kind && resource.Metadata.Name == name {
			return true
		}
	}
	return false
}

// hasResourceKind reports whether a rendered manifest contains a resource kind.
func hasResourceKind(resources []chartResource, kind string) bool {
	for _, resource := range resources {
		if resource.Kind == kind {
			return true
		}
	}
	return false
}

// contains reports whether a string slice contains a value.
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// daemonSetVolumeNames returns the volume names declared by a rendered DaemonSet.
func daemonSetVolumeNames(daemonSet chartResource) []string {
	names := make([]string, 0, len(daemonSet.Spec.Template.Spec.Volumes))
	for _, volume := range daemonSet.Spec.Template.Spec.Volumes {
		names = append(names, volume.Name)
	}
	return names
}

// requireVolume returns a named DaemonSet volume or fails the test.
func requireVolume(t *testing.T, daemonSet chartResource, name string) volume {
	t.Helper()

	for _, volume := range daemonSet.Spec.Template.Spec.Volumes {
		if volume.Name == name {
			return volume
		}
	}
	t.Fatalf("DaemonSet %q has no volume named %q", daemonSet.Metadata.Name, name)
	return volume{}
}

// containerVolumeMountNames returns the volume mount names declared by a container.
func containerVolumeMountNames(container container) []string {
	names := make([]string, 0, len(container.VolumeMounts))
	for _, mount := range container.VolumeMounts {
		names = append(names, mount.Name)
	}
	return names
}

// requireVolumeMount returns a named container volume mount or fails the test.
func requireVolumeMount(t *testing.T, container container, name string) volumeMount {
	t.Helper()

	mount, ok := findVolumeMount(container, name)
	if !ok {
		t.Fatalf("container %q has no volumeMount named %q", container.Name, name)
	}

	return mount
}

// findVolumeMount returns the container mount of volume name, if any.
func findVolumeMount(c container, name string) (volumeMount, bool) {
	for _, mount := range c.VolumeMounts {
		if mount.Name == name {
			return mount, true
		}
	}

	return volumeMount{}, false
}

// containerEnvValues maps each rendered environment variable name to its literal value.
func containerEnvValues(container container) map[string]string {
	values := make(map[string]string, len(container.Env))
	for _, env := range container.Env {
		values[env.Name] = env.Value
	}
	return values
}

// assertNoDefaultCollectorsArgs fails if the chart adds command-line counter overrides to the container.
func assertNoDefaultCollectorsArgs(t *testing.T, container container) {
	t.Helper()

	assert.NotContains(t, container.Args, "-f")
	assert.NotContains(t, container.Args, "--collectors")
	assert.NotContains(t, container.Args, imageMetricsFile)
}

// chartResource captures the rendered Kubernetes fields needed by render-contract tests.
type chartResource struct {
	APIVersion                   string            `yaml:"apiVersion"`
	Kind                         string            `yaml:"kind"`
	Type                         string            `yaml:"type"`
	Metadata                     metadata          `yaml:"metadata"`
	AutomountServiceAccountToken *bool             `yaml:"automountServiceAccountToken"`
	Data                         map[string]string `yaml:"data"`
	StringData                   map[string]string `yaml:"stringData"`
	Spec                         struct {
		Selector struct {
			MatchLabels map[string]string `yaml:"matchLabels"`
		} `yaml:"selector"`
		NamespaceSelector struct {
			MatchNames []string `yaml:"matchNames"`
		} `yaml:"namespaceSelector"`
		Endpoints      []serviceMonitorEndpoint `yaml:"endpoints"`
		UpdateStrategy struct {
			Type          string `yaml:"type"`
			RollingUpdate struct {
				MaxUnavailable int `yaml:"maxUnavailable"`
				MaxSurge       int `yaml:"maxSurge"`
			} `yaml:"rollingUpdate"`
		} `yaml:"updateStrategy"`
		Template struct {
			Metadata metadata `yaml:"metadata"`
			Spec     struct {
				AutomountServiceAccountToken *bool             `yaml:"automountServiceAccountToken"`
				Containers                   []container       `yaml:"containers"`
				InitContainers               []container       `yaml:"initContainers"`
				ImagePullSecrets             []imagePullSecret `yaml:"imagePullSecrets"`
				Tolerations                  []toleration      `yaml:"tolerations"`
				Volumes                      []volume          `yaml:"volumes"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
	Rules []rbacRule `yaml:"rules"`
}

// metadata captures object identity fields from rendered Kubernetes manifests.
type metadata struct {
	Name        string            `yaml:"name"`
	Namespace   string            `yaml:"namespace"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

// container captures the DaemonSet container fields needed by the render contract.
type container struct {
	Name      string   `yaml:"name"`
	Image     string   `yaml:"image"`
	Args      []string `yaml:"args"`
	Env       []env    `yaml:"env"`
	Resources struct {
		Limits   map[string]string `yaml:"limits"`
		Requests map[string]string `yaml:"requests"`
	} `yaml:"resources"`
	VolumeMounts   []volumeMount `yaml:"volumeMounts"`
	StartupProbe   *probe        `yaml:"startupProbe"`
	LivenessProbe  probe         `yaml:"livenessProbe"`
	ReadinessProbe probe         `yaml:"readinessProbe"`
}

// toleration captures the pod toleration fields needed by render-contract tests.
type toleration struct {
	Key      string `yaml:"key"`
	Operator string `yaml:"operator"`
	Effect   string `yaml:"effect"`
}

// probe captures configurable health probe timing fields from the rendered chart.
type probe struct {
	InitialDelaySeconds int  `yaml:"initialDelaySeconds"`
	PeriodSeconds       *int `yaml:"periodSeconds"`
	TimeoutSeconds      *int `yaml:"timeoutSeconds"`
	FailureThreshold    *int `yaml:"failureThreshold"`
}

// probeHandler captures how a rendered probe reaches the exporter.
type probeHandler struct {
	HTTPGet *struct {
		Path   string `yaml:"path"`
		Port   int    `yaml:"port"`
		Scheme string `yaml:"scheme"`
	} `yaml:"httpGet"`
	TCPSocket *struct {
		Port int `yaml:"port"`
	} `yaml:"tcpSocket"`
}

// intPointer makes explicit zero-valued Kubernetes probe settings distinguishable from omitted values.
func intPointer(value int) *int {
	return &value
}

// imagePullSecret captures a rendered image pull secret name.
type imagePullSecret struct {
	Name string `yaml:"name"`
}

// serviceMonitorEndpoint captures the scrape and relabel settings under test.
type serviceMonitorEndpoint struct {
	Port              string        `yaml:"port"`
	Path              string        `yaml:"path"`
	Interval          string        `yaml:"interval"`
	ScrapeTimeout     string        `yaml:"scrapeTimeout"`
	HonorLabels       bool          `yaml:"honorLabels"`
	Relabelings       []relabelRule `yaml:"relabelings"`
	MetricRelabelings []relabelRule `yaml:"metricRelabelings"`
}

// relabelRule captures selector and action fields for a rendered relabel rule.
type relabelRule struct {
	SourceLabels []string `yaml:"sourceLabels"`
	TargetLabel  string   `yaml:"targetLabel"`
	Regex        string   `yaml:"regex"`
	Action       string   `yaml:"action"`
}

// env captures a rendered container environment variable.
type env struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

// volume captures the DaemonSet volume name and ConfigMap source under test.
type volume struct {
	Name      string `yaml:"name"`
	ConfigMap struct {
		Name  string      `yaml:"name"`
		Items []keyToPath `yaml:"items"`
	} `yaml:"configMap"`
	Secret struct {
		SecretName  string `yaml:"secretName"`
		DefaultMode int    `yaml:"defaultMode"`
	} `yaml:"secret"`
	EmptyDir *struct {
		SizeLimit string `yaml:"sizeLimit"`
	} `yaml:"emptyDir"`
}

// volumeMount captures the rendered container volume path and key selection.
type volumeMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	SubPath   string `yaml:"subPath"`
	ReadOnly  bool   `yaml:"readOnly"`
}

// keyToPath is one ConfigMap key a volume mounts and the file name it gets.
type keyToPath struct {
	Key  string `yaml:"key"`
	Path string `yaml:"path"`
}

// rbacRule captures exact feature grants and named ConfigMap access.
type rbacRule struct {
	APIGroups     []string `yaml:"apiGroups"`
	ResourceNames []string `yaml:"resourceNames"`
	Resources     []string `yaml:"resources"`
	Verbs         []string `yaml:"verbs"`
}
