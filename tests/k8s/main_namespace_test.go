//go:build k8s

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

package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestNamespaceOwnershipRequiresMatchingRunName rejects namespaces owned by another harness run.
func TestNamespaceOwnershipRequiresMatchingRunName(t *testing.T) {
	original := testContext.runName
	t.Cleanup(func() { testContext.runName = original })
	testContext.runName = "ci-17"

	labels := map[string]string{
		validationManagedByLabel: validationManagedByValue,
		e2eRunNameLabel:          "ci-17",
	}
	if !namespaceOwnedByRun(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Labels: labels}}) {
		t.Fatal("namespace with matching owner and run name was rejected")
	}
	labels[e2eRunNameLabel] = "ci-18"
	if namespaceOwnedByRun(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Labels: labels}}) {
		t.Fatal("namespace from another run was accepted")
	}
	if namespaceOwnedByRun(nil) {
		t.Fatal("nil namespace was accepted")
	}
}

// TestApplyNamespaceDefaultsUsesHelmNamespaceAsBase checks derived and explicit namespace values.
func TestApplyNamespaceDefaultsUsesHelmNamespaceAsBase(t *testing.T) {
	t.Setenv("E2E_HELM_NAMESPACE", "")
	t.Setenv("E2E_DCGM_NAMESPACE", "")
	original := testContext
	t.Cleanup(func() { testContext = original })

	testContext.runName = "ci-17"
	testContext.namespace = ""
	testContext.dcgmNS = ""
	applyNamespaceDefaults(nil)
	if testContext.namespace != "ci-17" || testContext.dcgmNS != "ci-17-dcgm" {
		t.Fatalf("default namespaces = (%q, %q), want (ci-17, ci-17-dcgm)", testContext.namespace, testContext.dcgmNS)
	}

	testContext.namespace = "exporter-test"
	testContext.dcgmNS = ""
	applyNamespaceDefaults(map[string]bool{"namespace": true})
	if testContext.namespace != "exporter-test" || testContext.dcgmNS != "exporter-test-dcgm" {
		t.Fatalf("overridden namespaces = (%q, %q), want (exporter-test, exporter-test-dcgm)", testContext.namespace, testContext.dcgmNS)
	}

	t.Setenv("E2E_HELM_NAMESPACE", "env-exporter")
	testContext.namespace = "env-exporter"
	testContext.dcgmNS = ""
	applyNamespaceDefaults(nil)
	if testContext.namespace != "env-exporter" || testContext.dcgmNS != "env-exporter-dcgm" {
		t.Fatalf("environment namespaces = (%q, %q), want (env-exporter, env-exporter-dcgm)", testContext.namespace, testContext.dcgmNS)
	}
}
