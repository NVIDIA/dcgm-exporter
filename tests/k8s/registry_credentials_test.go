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
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type pullSecretClient interface {
	GetSecret(context.Context, string, string) (*corev1.Secret, error)
	CreateSecret(context.Context, *corev1.Secret) (*corev1.Secret, error)
}

// snapshotPullSecret retains only the credential fields needed after namespace recreation.
func snapshotPullSecret(ctx context.Context, client pullSecretClient, namespace, name string) (*corev1.Secret, error) {
	secret, err := client.GetSecret(ctx, namespace, name)
	if k8serrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	secret = secret.DeepCopy()
	return &corev1.Secret{Type: secret.Type, Data: secret.Data}, nil
}

// restorePullSecret recreates the configured Secret without server-managed metadata.
func restorePullSecret(ctx context.Context, client pullSecretClient, namespace, name string, snapshot *corev1.Secret) error {
	if snapshot == nil {
		return nil
	}

	secret := snapshot.DeepCopy()
	secret.Name = name
	secret.Namespace = namespace
	_, err := client.CreateSecret(ctx, secret)
	if k8serrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// TestPullSecretRestoreKeepsOnlyTypeAndData checks the snapshot preserves Secret type and data.
func TestPullSecretRestoreKeepsOnlyTypeAndData(t *testing.T) {
	client := &pullSecretClientStub{secret: &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "registry-auth",
			Namespace:       "dcgm-exporter",
			Labels:          map[string]string{"managed-by": "e2e"},
			ResourceVersion: "server-version",
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte("credentials")},
	}}

	snapshot, err := snapshotPullSecret(context.Background(), client, "dcgm-exporter", "registry-auth")
	if err != nil {
		t.Fatal(err)
	}
	client.secret.Data[corev1.DockerConfigJsonKey][0] = 'X'

	if err := restorePullSecret(context.Background(), client, "dcgm-exporter", "registry-auth", snapshot); err != nil {
		t.Fatal(err)
	}
	if client.created == nil {
		t.Fatal("restorePullSecret() did not create a Secret")
	}
	if client.created.Name != "registry-auth" || client.created.Namespace != "dcgm-exporter" {
		t.Fatalf("restored Secret identity = %s/%s", client.created.Namespace, client.created.Name)
	}
	if client.created.Type != corev1.SecretTypeDockerConfigJson ||
		string(client.created.Data[corev1.DockerConfigJsonKey]) != "credentials" {
		t.Fatalf("restored Secret lost type or data: %#v", client.created)
	}
	if client.created.UID != "" || client.created.ResourceVersion != "" || client.created.Labels != nil {
		t.Fatalf("restored Secret retained metadata: %#v", client.created.ObjectMeta)
	}
}

// TestPullSecretMissingSecretIsNoOp checks that a missing Secret requires no restore.
func TestPullSecretMissingSecretIsNoOp(t *testing.T) {
	client := &pullSecretClientStub{
		getErr: k8serrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "registry-auth"),
	}

	snapshot, err := snapshotPullSecret(context.Background(), client, "dcgm-exporter", "registry-auth")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != nil {
		t.Fatalf("snapshotPullSecret() = %#v, want nil", snapshot)
	}
	if err := restorePullSecret(context.Background(), client, "dcgm-exporter", "registry-auth", snapshot); err != nil {
		t.Fatal(err)
	}
	if client.created != nil {
		t.Fatalf("restorePullSecret() created %#v for a missing Secret", client.created)
	}
}

// TestPullSecretRestoreKeepsExistingSecret checks an existing Secret is left unchanged.
func TestPullSecretRestoreKeepsExistingSecret(t *testing.T) {
	client := &pullSecretClientStub{
		createErr: k8serrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, "registry-auth"),
	}
	snapshot := &corev1.Secret{Type: corev1.SecretTypeDockerConfigJson}
	if err := restorePullSecret(context.Background(), client, "dcgm-exporter", "registry-auth", snapshot); err != nil {
		t.Fatalf("restorePullSecret() error = %v, want nil when the Secret already exists", err)
	}
}

// pullSecretClientStub supplies configurable Secret results for snapshot and restore tests.
type pullSecretClientStub struct {
	secret    *corev1.Secret
	created   *corev1.Secret
	getErr    error
	createErr error
}

// GetSecret returns the fixture Secret or its configured lookup error.
func (f *pullSecretClientStub) GetSecret(context.Context, string, string) (*corev1.Secret, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.secret, nil
}

// CreateSecret records the request and returns a copy or the configured error.
func (f *pullSecretClientStub) CreateSecret(_ context.Context, secret *corev1.Secret) (*corev1.Secret, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = secret.DeepCopy()
	return f.created.DeepCopy(), nil
}
