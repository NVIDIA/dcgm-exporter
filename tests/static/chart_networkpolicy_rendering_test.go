// Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package static_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

func TestChartNetworkPolicyRenderContract(t *testing.T) {
	const selected = `[{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: monitoring}}, podSelector: {matchLabels: {app: prometheus}}}]`
	for _, tc := range []struct {
		name, peers string
		port        int
		address     string
		service     bool
	}{
		{"empty denies all", `[]`, 9400, ":9400", true},
		{"selected cross namespace", selected, 9400, ":9400", true},
		{"same namespace", `[{podSelector: {matchLabels: {app: prometheus}}}]`, 9400, ":9400", true},
		{"selector expressions", `[{podSelector: {matchExpressions: [{key: app, operator: In, values: [prometheus]}]}}]`, 9400, ":9400", true},
		{"explicit all sources", `[{}]`, 9400, ":9400", true},
		{"custom port without Service", selected, 19500, "[::]:19500", false},
		{"ipBlock", `[{ipBlock: {cidr: 10.0.0.0/8, except: [10.1.0.0/16]}}]`, 9400, ":9400", true},
		{"IPv6 ipBlock", `[{ipBlock: {cidr: '2001:db8::/32'}}]`, 9400, ":9400", true},
		{"IPv4-mapped IPv6 ipBlock", `[{ipBlock: {cidr: '::ffff:192.0.2.0/120'}}]`, 9400, ":9400", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var peers []interface{}
			require.NoError(t, yaml.Unmarshal([]byte(tc.peers), &peers))
			release, err := runHelmInstall(t, map[string]interface{}{
				"networkPolicy": map[string]interface{}{"enabled": true, "allowedPeers": peers},
				"service":       map[string]interface{}{"enable": tc.service, "port": tc.port},
				"config": map[string]interface{}{"outputs": map[string]interface{}{
					"prometheus": map[string]interface{}{"listen": tc.address},
				}},
			})
			require.NoError(t, err)
			var policy networkingv1.NetworkPolicy
			require.NoError(t, yaml.UnmarshalStrict([]byte(requireManifestKind(t, release.Manifest, "NetworkPolicy")), &policy))
			require.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, policy.Spec.PolicyTypes)
			require.Empty(t, policy.Spec.Egress)
			require.Empty(t, socketEnvValues(t, release.Manifest))
			require.Equal(t, map[string]string{
				"app.kubernetes.io/name":      "dcgm-exporter",
				"app.kubernetes.io/instance":  "dcgm-exporter",
				"app.kubernetes.io/component": "dcgm-exporter",
			}, policy.Spec.PodSelector.MatchLabels)
			if len(peers) == 0 {
				require.Empty(t, policy.Spec.Ingress)
				return
			}
			require.Len(t, policy.Spec.Ingress, 1)
			rule := policy.Spec.Ingress[0]
			var expectedPeers []networkingv1.NetworkPolicyPeer
			require.NoError(t, yaml.Unmarshal([]byte(tc.peers), &expectedPeers))
			require.Equal(t, expectedPeers, rule.From)
			require.Len(t, rule.Ports, 1)
			require.Equal(t, "TCP", string(*rule.Ports[0].Protocol))
			require.Equal(t, tc.port, int(rule.Ports[0].Port.IntVal))
		})
	}
	// The chart cannot read an existing ConfigMap. A file that turns socket
	// activation on makes the exporter fail at startup, because a pod gets no
	// systemd sockets, so the policed TCP port is never bypassed.
	t.Run("existing ConfigMap gets no socket setting from the chart", func(t *testing.T) {
		release, err := runHelmInstall(t, map[string]interface{}{
			"networkPolicy": map[string]interface{}{"enabled": true, "allowedPeers": []interface{}{map[string]interface{}{}}},
			"configMap":     map[string]interface{}{"existingName": "operator-config"},
		})
		require.NoError(t, err)
		require.Empty(t, socketEnvValues(t, release.Manifest))
	})
	for _, spelling := range []string{"0", "f", "F", "false", "False", "FALSE"} {
		for _, source := range []string{"argument", "environment"} {
			t.Run("disabled socket "+source+" "+spelling, func(t *testing.T) {
				checkDisabledSocketKeepsPolicedPort(t, source, spelling)
			})
		}
	}
}

// checkDisabledSocketKeepsPolicedPort renders the chart with socket activation turned off through the given
// source ("argument" or "environment") and spelling, and checks that the NetworkPolicy still polices port 9400
// and the chart adds no socket setting of its own.
func checkDisabledSocketKeepsPolicedPort(t *testing.T, source, spelling string) {
	t.Helper()

	values := map[string]interface{}{
		"networkPolicy": map[string]interface{}{"enabled": true, "allowedPeers": []interface{}{map[string]interface{}{}}},
	}
	if source == "argument" {
		values["arguments"] = []string{"--web-systemd-socket=" + spelling}
	} else {
		values["extraEnv"] = []interface{}{map[string]interface{}{"name": "DCGM_EXPORTER_SYSTEMD_SOCKET", "value": spelling}}
	}

	release, err := runHelmInstall(t, values)
	require.NoError(t, err)
	var policy networkingv1.NetworkPolicy
	require.NoError(t, yaml.UnmarshalStrict([]byte(requireManifestKind(t, release.Manifest, "NetworkPolicy")), &policy))
	require.Len(t, policy.Spec.Ingress, 1)
	require.Len(t, policy.Spec.Ingress[0].Ports, 1)
	require.Equal(t, int32(9400), policy.Spec.Ingress[0].Ports[0].Port.IntVal)

	if source == "environment" {
		require.Equal(t, []string{spelling}, socketEnvValues(t, release.Manifest), "only the extraEnv entry")
	} else {
		require.Empty(t, socketEnvValues(t, release.Manifest))
	}
}

// socketEnvValues returns the values of every DCGM_EXPORTER_SYSTEMD_SOCKET entry
// in the exporter container's environment.
func socketEnvValues(t *testing.T, manifest string) []string {
	t.Helper()
	var daemonSet appsv1.DaemonSet
	require.NoError(t, yaml.Unmarshal([]byte(requireManifestKind(t, manifest, "DaemonSet")), &daemonSet))

	var values []string
	for _, env := range daemonSet.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "DCGM_EXPORTER_SYSTEMD_SOCKET" {
			values = append(values, env.Value)
		}
	}
	return values
}

func TestChartNetworkPolicyDisabledPreservesRendering(t *testing.T) {
	for _, values := range []map[string]interface{}{nil, {
		"networkPolicy": map[string]interface{}{"enabled": false, "allowedPeers": "invalid inactive value", "allowedPeer": "ignored inactive typo"},
		"config":        nil,
		"arguments":     []string{"--address=:19500"},
	}} {
		resources := renderChart(t, values)
		require.False(t, hasResourceKind(resources, "NetworkPolicy"))
	}
}

func TestChartNetworkPolicyRejectsAmbiguousListenerAndPeers(t *testing.T) {
	for _, tc := range []struct{ name, values string }{
		{"host network", `hostNetwork: true`},
		{"zero port", `{config: null, service: {port: 0}}`},
		{"port too large", `{config: null, service: {port: 65536}}`},
		{"address CLI", `{config: null, arguments: ['--address=:19500']}`},
		{"short address CLI", `{config: null, arguments: ['-a', ':19500']}`},
		{"socket CLI", `arguments: ['--web-systemd-socket']`},
		{"single-dash address CLI", `{config: null, arguments: ['-address', ':19500']}`},
		{"single-dash socket CLI", `arguments: ['-web-systemd-socket']`},
		{"socket in config", `config: {version: 2, server: {systemd_socket: true}}`},
		{"address env", `{config: null, extraEnv: [{name: DCGM_EXPORTER_LISTEN, valueFrom: {secretKeyRef: {name: listener, key: address}}}]}`},
		{"socket env", `extraEnv: [{name: DCGM_EXPORTER_SYSTEMD_SOCKET, value: 'true'}]`},
		{"socket true CLI", `arguments: ['--web-systemd-socket=true']`},
		{"socket unknown CLI", `arguments: ['--web-systemd-socket=off']`},
		{"socket invalid case CLI", `arguments: ['--web-systemd-socket=fAlse']`},
		{"socket unknown env", `extraEnv: [{name: DCGM_EXPORTER_SYSTEMD_SOCKET, value: 'off'}]`},
		{"socket invalid case env", `extraEnv: [{name: DCGM_EXPORTER_SYSTEMD_SOCKET, value: 'fAlse'}]`},
		{"socket indirect env", `extraEnv: [{name: DCGM_EXPORTER_SYSTEMD_SOCKET, valueFrom: {configMapKeyRef: {name: socket, key: enabled}}}]`},
		{"socket false with valueFrom", `extraEnv: [{name: DCGM_EXPORTER_SYSTEMD_SOCKET, value: 'false', valueFrom: {configMapKeyRef: {name: socket, key: enabled}}}]`},
		// The API server validates peer contents but drops unknown keys, which can
		// widen a selector to every source, so the chart rejects unknown keys.
		{"unknown active policy key", `networkPolicy: {enabled: true, allowedPeer: []}`},
		{"peers scalar", `networkPolicy: {enabled: true, allowedPeers: bad}`},
		{"peer scalar", `networkPolicy: {enabled: true, allowedPeers: [bad]}`},
		{"unknown peer field", `networkPolicy: {enabled: true, allowedPeers: [{ports: [9400]}]}`},
		{"misspelled peer field", `networkPolicy: {enabled: true, allowedPeers: [{podselector: {matchLabels: {app: prometheus}}}]}`},
		{"selector scalar", `networkPolicy: {enabled: true, allowedPeers: [{podSelector: 'app=prometheus'}]}`},
		{"misspelled selector field", `networkPolicy: {enabled: true, allowedPeers: [{namespaceSelector: {matchLabel: {team: monitoring}}}]}`},
		{"misspelled expression field", `networkPolicy: {enabled: true, allowedPeers: [{podSelector: {matchExpressions: [{key: team, operator: Exists, value: [monitoring]}]}}]}`},
		{"expressions scalar", `networkPolicy: {enabled: true, allowedPeers: [{podSelector: {matchExpressions: team}}]}`},
		{"unknown ipBlock field", `networkPolicy: {enabled: true, allowedPeers: [{ipBlock: {cidr: 10.0.0.0/8, exclude: [10.1.0.0/16]}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var values map[string]interface{}
			require.NoError(t, yaml.Unmarshal([]byte(tc.values), &values))
			if _, ok := values["networkPolicy"]; !ok {
				values["networkPolicy"] = map[string]interface{}{"enabled": true}
			}
			_, err := runHelmInstall(t, values)
			require.Error(t, err)
			require.Contains(t, err.Error(), "networkPolicy")
		})
	}
}

func TestRawNetworkPolicyExampleMatchesExporter(t *testing.T) {
	policyYAML, err := os.ReadFile(repoPath(t, "deployment/examples/networkpolicy.yaml"))
	require.NoError(t, err)
	var policy networkingv1.NetworkPolicy
	require.NoError(t, yaml.UnmarshalStrict(policyYAML, &policy))
	exporterYAML, err := os.ReadFile(repoPath(t, "deployment/examples/dcgm-exporter.yaml"))
	require.NoError(t, err)
	var exporter appsv1.DaemonSet
	require.NoError(t, yaml.Unmarshal([]byte(requireManifestKind(t, string(exporterYAML), "DaemonSet")), &exporter))
	require.Equal(t, exporter.Spec.Selector.MatchLabels, policy.Spec.PodSelector.MatchLabels)
	require.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, policy.Spec.PolicyTypes)
	require.Empty(t, policy.Spec.Egress)
	require.Len(t, policy.Spec.Ingress, 1)
	require.Len(t, policy.Spec.Ingress[0].Ports, 1)
	require.Equal(t, exporter.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort, policy.Spec.Ingress[0].Ports[0].Port.IntVal)
}
