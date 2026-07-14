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
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	"sigs.k8s.io/yaml"
)

const (
	envEnablePodLabels = "DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS"
	envEnablePodUID    = "DCGM_EXPORTER_KUBERNETES_ENABLE_POD_UID"
	envEnableDRA       = "KUBERNETES_ENABLE_DRA"
	envConfigFile      = "DCGM_EXPORTER_CONFIG_FILE"
)

// TestChartKubernetesRBACRenderContract verifies Kubernetes features opt into pod-reader access.
func TestChartKubernetesRBACRenderContract(t *testing.T) {
	testCases := []struct {
		name              string
		values            map[string]interface{}
		wantToken         bool
		wantClusterRBAC   bool
		wantEnabledEnvVar string
	}{
		{
			name:            "default deployment does not mount token or create pod RBAC",
			values:          nil,
			wantToken:       false,
			wantClusterRBAC: false,
		},
		{
			name: "explicit token mount overrides the default",
			values: map[string]interface{}{
				"serviceAccount": map[string]interface{}{
					"automountServiceAccountToken": true,
				},
			},
			wantToken:       true,
			wantClusterRBAC: false,
		},
		{
			name: "pod labels require token and pod informer RBAC",
			values: map[string]interface{}{
				"kubernetes": map[string]interface{}{
					"enablePodLabels": true,
				},
			},
			wantToken:         true,
			wantClusterRBAC:   true,
			wantEnabledEnvVar: envEnablePodLabels,
		},
		{
			name: "explicit token opt-out overrides pod labels",
			values: map[string]interface{}{
				"serviceAccount": map[string]interface{}{
					"automountServiceAccountToken": false,
				},
				"kubernetes": map[string]interface{}{
					"enablePodLabels": true,
				},
			},
			wantToken:         false,
			wantClusterRBAC:   true,
			wantEnabledEnvVar: envEnablePodLabels,
		},
		{
			name: "pod UID requires token and pod informer RBAC",
			values: map[string]interface{}{
				"kubernetes": map[string]interface{}{
					"enablePodUID": true,
				},
			},
			wantToken:         true,
			wantClusterRBAC:   true,
			wantEnabledEnvVar: envEnablePodUID,
		},
		{
			name: "DRA requires token and pod informer RBAC",
			values: map[string]interface{}{
				"kubernetesDRA": map[string]interface{}{
					"enabled": true,
				},
			},
			wantToken:         true,
			wantClusterRBAC:   true,
			wantEnabledEnvVar: envEnableDRA,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resources := renderChart(t, tc.values)

			serviceAccount := requireResourceKind(t, resources, "ServiceAccount")
			require.NotNil(t, serviceAccount.AutomountServiceAccountToken)
			assert.Equal(t, tc.wantToken, *serviceAccount.AutomountServiceAccountToken)

			daemonSet := requireResourceKind(t, resources, "DaemonSet")
			require.NotNil(t, daemonSet.Spec.Template.Spec.AutomountServiceAccountToken)
			assert.Equal(t, tc.wantToken, *daemonSet.Spec.Template.Spec.AutomountServiceAccountToken)

			assert.Equal(t, tc.wantClusterRBAC, hasResourceKind(resources, "ClusterRole"))
			assert.Equal(t, tc.wantClusterRBAC, hasResourceKind(resources, "ClusterRoleBinding"))

			envNames := daemonSetContainerEnvNames(t, daemonSet, "exporter")
			if tc.wantEnabledEnvVar == "" {
				assert.NotContains(t, envNames, envEnablePodLabels)
				assert.NotContains(t, envNames, envEnablePodUID)
				assert.NotContains(t, envNames, envEnableDRA)
				return
			}

			assert.Contains(t, envNames, tc.wantEnabledEnvVar)
			for _, envName := range []string{envEnablePodLabels, envEnablePodUID, envEnableDRA} {
				if envName == tc.wantEnabledEnvVar {
					continue
				}
				assert.NotContains(t, envNames, envName)
			}
			assertPodInformerRule(t, requireResourceKind(t, resources, "ClusterRole"))
		})
	}
}

func TestChartYAMLConfigRenderContract(t *testing.T) {
	configData := strings.TrimSpace(`
version: 1
metrics:
  file: /etc/dcgm-exporter/default-counters.csv
collection:
  interval: 30s
`)

	testCases := []struct {
		name              string
		configName        string
		wantConfigMapName string
	}{
		{
			name:              "default generated ConfigMap name",
			wantConfigMapName: "dcgm-exporter-config",
		},
		{
			name:              "explicit ConfigMap name",
			configName:        "custom-config-name",
			wantConfigMapName: "custom-config-name",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			configValues := map[string]interface{}{
				"enabled":   true,
				"create":    true,
				"key":       "dcgm-exporter.yaml",
				"mountPath": "/etc/dcgm-exporter/config.yaml",
				"data":      configData,
			}
			if tc.configName != "" {
				configValues["name"] = tc.configName
			}
			resources := renderChart(t, map[string]interface{}{
				"config": configValues,
			})

			configMap := requireResourceName(t, resources, "ConfigMap", tc.wantConfigMapName)
			assert.Contains(t, configMap.Data["dcgm-exporter.yaml"], "version: 1")
			assert.Contains(t, configMap.Data["dcgm-exporter.yaml"], "file: /etc/dcgm-exporter/default-counters.csv")
			assert.Contains(t, configMap.Data["dcgm-exporter.yaml"], "interval: 30s")

			daemonSet := requireResourceKind(t, resources, "DaemonSet")
			container := requireContainer(t, daemonSet, "exporter")
			assert.Contains(t, daemonSetVolumeNames(daemonSet), "dcgm-exporter-config")
			assert.Contains(t, daemonSetVolumeNames(daemonSet), "exporter-metrics-volume")
			assert.Contains(t, containerVolumeMountNames(container), "dcgm-exporter-config")
			assert.Contains(t, containerVolumeMountNames(container), "exporter-metrics-volume")
			assertNoDefaultCollectorsArgs(t, container)
			configVolume := requireVolume(t, daemonSet, "dcgm-exporter-config")
			assert.Equal(t, tc.wantConfigMapName, configVolume.ConfigMap.Name)
			configMount := requireVolumeMount(t, container, "dcgm-exporter-config")
			assert.Equal(t, "/etc/dcgm-exporter/config.yaml", configMount.MountPath)
			assert.Equal(t, "dcgm-exporter.yaml", configMount.SubPath)
			metricsVolume := requireVolume(t, daemonSet, "exporter-metrics-volume")
			assert.Equal(t, "exporter-metrics-config-map", metricsVolume.ConfigMap.Name)
			metricsMount := requireVolumeMount(t, container, "exporter-metrics-volume")
			assert.Equal(t, "/etc/dcgm-exporter/default-counters.csv", metricsMount.MountPath)
			assert.Equal(t, "default-counters.csv", metricsMount.SubPath)

			envValues := containerEnvValues(container)
			assert.Equal(t, "/etc/dcgm-exporter/config.yaml", envValues[envConfigFile])
		})
	}
}

func TestChartYAMLConfigCanMountExistingConfigMap(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"config": map[string]interface{}{
			"enabled":   true,
			"create":    false,
			"name":      "existing-exporter-config",
			"key":       "custom-config.yaml",
			"mountPath": "/etc/dcgm-exporter/custom.yaml",
		},
	})

	assert.False(t, hasResourceName(resources, "ConfigMap", "existing-exporter-config"))

	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	container := requireContainer(t, daemonSet, "exporter")
	configVolume := requireVolume(t, daemonSet, "dcgm-exporter-config")
	assert.Equal(t, "existing-exporter-config", configVolume.ConfigMap.Name)
	configMount := requireVolumeMount(t, container, "dcgm-exporter-config")
	assert.Equal(t, "/etc/dcgm-exporter/custom.yaml", configMount.MountPath)
	assert.Equal(t, "custom-config.yaml", configMount.SubPath)

	envValues := containerEnvValues(container)
	assert.Equal(t, "/etc/dcgm-exporter/custom.yaml", envValues[envConfigFile])
}

func TestChartYAMLConfigExistingConfigMapRequiresName(t *testing.T) {
	err := renderChartError(t, map[string]interface{}{
		"config": map[string]interface{}{
			"enabled": true,
			"create":  false,
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "config.name must be set when config.create is false")
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

// assertPodInformerRule requires the rendered ClusterRole to allow pod informer reads.
func assertPodInformerRule(t *testing.T, clusterRole chartResource) {
	t.Helper()

	foundPodsRule := false
	verbs := map[string]bool{}

	for _, rule := range clusterRole.Rules {
		if !contains(rule.Resources, "pods") {
			continue
		}

		foundPodsRule = true
		for _, verb := range rule.Verbs {
			verbs[verb] = true
		}
	}

	if !foundPodsRule {
		t.Fatalf("ClusterRole %q has no pods rule", clusterRole.Metadata.Name)
	}

	assert.True(t, verbs["get"], "ClusterRole %q is missing pod get permission", clusterRole.Metadata.Name)
	assert.True(t, verbs["list"], "ClusterRole %q is missing pod list permission", clusterRole.Metadata.Name)
	assert.True(t, verbs["watch"], "ClusterRole %q is missing pod watch permission", clusterRole.Metadata.Name)
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

func renderChartError(t *testing.T, values map[string]interface{}) error {
	t.Helper()

	_, err := runHelmInstall(t, values)
	return err
}

func runHelmInstall(t *testing.T, values map[string]interface{}) (*release.Release, error) {
	t.Helper()

	chart, err := loader.Load(repoPath(t, "deployment"))
	require.NoError(t, err)

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

func daemonSetVolumeNames(daemonSet chartResource) []string {
	names := make([]string, 0, len(daemonSet.Spec.Template.Spec.Volumes))
	for _, volume := range daemonSet.Spec.Template.Spec.Volumes {
		names = append(names, volume.Name)
	}
	return names
}

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

func containerVolumeMountNames(container container) []string {
	names := make([]string, 0, len(container.VolumeMounts))
	for _, mount := range container.VolumeMounts {
		names = append(names, mount.Name)
	}
	return names
}

func requireVolumeMount(t *testing.T, container container, name string) volumeMount {
	t.Helper()

	for _, mount := range container.VolumeMounts {
		if mount.Name == name {
			return mount
		}
	}
	t.Fatalf("container %q has no volumeMount named %q", container.Name, name)
	return volumeMount{}
}

func containerEnvValues(container container) map[string]string {
	values := make(map[string]string, len(container.Env))
	for _, env := range container.Env {
		values[env.Name] = env.Value
	}
	return values
}

func assertNoDefaultCollectorsArgs(t *testing.T, container container) {
	t.Helper()

	assert.NotContains(t, container.Args, "-f")
	assert.NotContains(t, container.Args, "--collectors")
	assert.NotContains(t, container.Args, "/etc/dcgm-exporter/default-counters.csv")
}

// chartResource captures the rendered Kubernetes fields needed by the RBAC contract tests.
type chartResource struct {
	APIVersion                   string            `yaml:"apiVersion"`
	Kind                         string            `yaml:"kind"`
	Metadata                     metadata          `yaml:"metadata"`
	AutomountServiceAccountToken *bool             `yaml:"automountServiceAccountToken"`
	Data                         map[string]string `yaml:"data"`
	Spec                         struct {
		Selector struct {
			MatchLabels map[string]string `yaml:"matchLabels"`
		} `yaml:"selector"`
		NamespaceSelector struct {
			MatchNames []string `yaml:"matchNames"`
		} `yaml:"namespaceSelector"`
		Endpoints []serviceMonitorEndpoint `yaml:"endpoints"`
		Template  struct {
			Spec struct {
				AutomountServiceAccountToken *bool             `yaml:"automountServiceAccountToken"`
				Containers                   []container       `yaml:"containers"`
				ImagePullSecrets             []imagePullSecret `yaml:"imagePullSecrets"`
				Volumes                      []volume          `yaml:"volumes"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
	Rules []rbacRule `yaml:"rules"`
}

// metadata captures object identity fields from rendered Kubernetes manifests.
type metadata struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace"`
	Labels    map[string]string `yaml:"labels"`
}

// container captures the DaemonSet container fields needed by the render contract.
type container struct {
	Name         string        `yaml:"name"`
	Image        string        `yaml:"image"`
	Args         []string      `yaml:"args"`
	Env          []env         `yaml:"env"`
	VolumeMounts []volumeMount `yaml:"volumeMounts"`
}

type imagePullSecret struct {
	Name string `yaml:"name"`
}

type serviceMonitorEndpoint struct {
	Port              string        `yaml:"port"`
	Path              string        `yaml:"path"`
	Interval          string        `yaml:"interval"`
	ScrapeTimeout     string        `yaml:"scrapeTimeout"`
	HonorLabels       bool          `yaml:"honorLabels"`
	Relabelings       []relabelRule `yaml:"relabelings"`
	MetricRelabelings []relabelRule `yaml:"metricRelabelings"`
}

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

type volume struct {
	Name      string `yaml:"name"`
	ConfigMap struct {
		Name string `yaml:"name"`
	} `yaml:"configMap"`
}

type volumeMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	SubPath   string `yaml:"subPath"`
}

// rbacRule captures the policy rule fields needed by the pod informer assertion.
type rbacRule struct {
	APIGroups []string `yaml:"apiGroups"`
	Resources []string `yaml:"resources"`
	Verbs     []string `yaml:"verbs"`
}
