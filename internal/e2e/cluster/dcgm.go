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

package cluster

import (
	"context"
	"fmt"
	"io"
	"strconv"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// DCGMConfig configures the standalone DCGM deployment.
type DCGMConfig struct {
	Image             string
	RuntimeClass      string
	NodeSelectorKey   string
	NodeSelectorValue string
	ImagePullSecret   string
	Name              string
	Port              string
	WaitTimeout       string
}

// DefaultDCGMConfig returns defaults aligned with the k8s suite flags.
func DefaultDCGMConfig() DCGMConfig {
	return DCGMConfig{
		RuntimeClass:      defaultRuntimeClass,
		NodeSelectorKey:   defaultExporterNodeLabelKey,
		NodeSelectorValue: defaultExporterNodeLabelVal,
		Name:              "dcgm",
		Port:              "5555",
		WaitTimeout:       defaultK3dWaitTimeout,
	}
}

// DeployDCGM applies a standalone nv-hostengine Deployment and Service.
func DeployDCGM(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, dcgm DCGMConfig) error {
	if dcgm.Image == "" {
		return fmt.Errorf("remote DCGM requires a DCGM image")
	}
	if err := EnsureManagedNamespace(ctx, runner, w, cfg, cfg.DCGMNamespace); err != nil {
		return err
	}
	if dcgm.Name == "" {
		dcgm.Name = "dcgm"
	}
	if dcgm.RuntimeClass == "" {
		dcgm.RuntimeClass = defaultRuntimeClass
	}
	if dcgm.NodeSelectorKey == "" {
		dcgm.NodeSelectorKey = defaultExporterNodeLabelKey
	}
	if dcgm.NodeSelectorValue == "" {
		dcgm.NodeSelectorValue = defaultExporterNodeLabelVal
	}
	if dcgm.Port == "" {
		dcgm.Port = "5555"
	}
	port, err := strconv.Atoi(dcgm.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("DCGM %s has invalid port %q", dcgm.Name, dcgm.Port)
	}
	if dcgm.WaitTimeout == "" {
		dcgm.WaitTimeout = defaultK3dWaitTimeout
	}
	if err := runRequired(ctx, runner, w, "kubectl_apply_dcgm", kubectlWithStdin(cfg, []byte(dcgmManifest(cfg, dcgm)), "apply", "-f", "-")); err != nil {
		return err
	}
	return runRequired(ctx, runner, w, "kubectl_rollout_dcgm", kubectl(cfg, "-n", cfg.DCGMNamespace, "rollout", "status", "deployment/"+dcgm.Name, "--timeout="+dcgm.WaitTimeout))
}

// DeleteDCGM removes the standalone DCGM namespace.
func DeleteDCGM(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) {
	_ = cleanupManagedNamespace(ctx, runner, w, cfg, cfg.DCGMNamespace, "kubectl_delete_dcgm_namespace")
}

// dcgmManifest renders the standalone nv-hostengine Deployment and Service YAML.
func dcgmManifest(cfg Config, dcgm DCGMConfig) string {
	imagePullSecret := ""
	if dcgm.ImagePullSecret != "" {
		imagePullSecret = fmt.Sprintf(`      imagePullSecrets:
      - name: %s
`, dcgm.ImagePullSecret)
	}
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[2]s
  namespace: %[1]s
  labels:
    app.kubernetes.io/name: dcgm
    app.kubernetes.io/instance: %[2]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: dcgm
      app.kubernetes.io/instance: %[2]s
  template:
    metadata:
      labels:
        app.kubernetes.io/name: dcgm
        app.kubernetes.io/instance: %[2]s
    spec:
      runtimeClassName: %[3]s
      nodeSelector:
        "%[4]s": "%[5]s"
%[8]s      containers:
      - name: dcgm
        image: "%[6]s"
        imagePullPolicy: IfNotPresent
        command: ["/usr/bin/nv-hostengine"]
        args:
        - "-n"
        - "-b"
        - "ALL"
        - "-p"
        - "%[7]s"
        - "-f"
        - "/tmp/nv-hostengine.log"
        - "--log-level"
        - "DEBUG"
        env:
        - name: NVIDIA_VISIBLE_DEVICES
          value: all
        - name: NVIDIA_DRIVER_CAPABILITIES
          value: compute,utility,compat32
        - name: NVIDIA_DISABLE_REQUIRE
          value: "true"
        ports:
        - name: dcgm
          containerPort: %[7]s
        securityContext:
          runAsUser: 0
          privileged: true
---
apiVersion: v1
kind: Service
metadata:
  name: %[2]s
  namespace: %[1]s
  labels:
    app.kubernetes.io/name: dcgm
    app.kubernetes.io/instance: %[2]s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: dcgm
    app.kubernetes.io/instance: %[2]s
  ports:
  - name: dcgm
    port: %[7]s
    targetPort: dcgm
`, cfg.DCGMNamespace, dcgm.Name, dcgm.RuntimeClass, dcgm.NodeSelectorKey, dcgm.NodeSelectorValue, dcgm.Image, dcgm.Port, imagePullSecret)
}
