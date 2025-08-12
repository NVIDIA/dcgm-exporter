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

package framework

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/pkg/errors"
	"k8s.io/client-go/transport/spdy"

	"k8s.io/client-go/tools/portforward"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	nvidiaResourceName = "nvidia.com/gpu"
	// maxLogBytes limits the amount of log data read into memory to prevent OOM
	maxLogBytes = 10 * 1024 * 1024 // 10MB limit
)

// KubeClient is a kubernetes client
type KubeClient struct {
	client     *kubernetes.Clientset
	restConfig *rest.Config
	OutWriter  io.Writer
	ErrWriter  io.Writer
}

// NewKubeClient creates a new KubeClient instance
func NewKubeClient(restConfig *rest.Config) (*KubeClient, error) {
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	return &KubeClient{
		client:     client,
		restConfig: restConfig,
		OutWriter:  io.Discard,
		ErrWriter:  io.Discard,
	}, nil
}

// CreateNamespace creates a new namespace
func (c *KubeClient) CreateNamespace(
	ctx context.Context,
	namespace string,
	labels map[string]string,
) (*corev1.Namespace, error) {
	namespaceObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   namespace,
			Labels: labels,
		},
		Status: corev1.NamespaceStatus{},
	}

	return c.client.CoreV1().Namespaces().Create(ctx, namespaceObj, metav1.CreateOptions{})
}

// GetNamespace checks if a namespace exists and returns its details
func (c *KubeClient) GetNamespace(ctx context.Context, namespace string) (*corev1.Namespace, error) {
	return c.client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
}

// DeleteNamespace deletes the namespace
func (c *KubeClient) DeleteNamespace(
	ctx context.Context,
	namespace string,
) error {
	err := c.client.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	if err != nil && k8serrors.IsNotFound(err) {
		// Namespace doesn't exist, which is fine for cleanup operations
		return nil
	}
	return err
}

// GetPodsByLabel returns a list of pods that matches with the label selector
func (c *KubeClient) GetPodsByLabel(ctx context.Context, namespace string, labelMap map[string]string) ([]corev1.Pod,
	error,
) {
	podList, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labelMap).String(),
	})
	if err != nil {
		return nil, err
	}
	return podList.Items, nil
}

// CheckPodStatus check pod status
func (c *KubeClient) CheckPodStatus(
	ctx context.Context,
	namespace, podName string,
	condition func(namespace, podName string, status corev1.PodStatus) (bool, error),
) (bool, error) {
	pod, err := c.client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("unexpected error getting pod %s; err: %w", podName, err)
	}

	if condition != nil {
		return condition(namespace, podName, pod.Status)
	}

	for _, c := range pod.Status.ContainerStatuses {
		if c.State.Waiting != nil && c.State.Waiting.Reason == "CrashLoopBackOff" {
			return false, fmt.Errorf("pod %s in namespace %s is in CrashLoopBackOff", pod.Name, pod.Namespace)
		}
	}

	return false, nil
}

// CreatePod creates a new pod in the defined namespace
func (c *KubeClient) CreatePod(
	ctx context.Context,
	namespace string,
	labels map[string]string,
	name string,
	containerName string,
	image string,
	runtimeClassName string,
) (*corev1.Pod, error) {
	// RuntimeClassName does not accept a reference to empty string, however nil is acceptable.
	var runtimeClassNameRef *string
	if runtimeClassName != "" {
		runtimeClassNameRef = &runtimeClassName
	}

	quantity, _ := resource.ParseQuantity("1")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			RuntimeClassName: runtimeClassNameRef,
			RestartPolicy:    corev1.RestartPolicyAlways,
			Containers: []corev1.Container{
				{
					Name:    containerName,
					Image:   image,
					Command: []string{"sleep", "infinity"},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							nvidiaResourceName: quantity,
						},
					},
				},
			},
		},
	}

	return c.client.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
}

// DeletePod deletes a pod in the defined namespace
func (c *KubeClient) DeletePod(
	ctx context.Context,
	namespace string,
	name string,
) error {
	err := c.client.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && k8serrors.IsNotFound(err) {
		// Pod doesn't exist, which is fine for cleanup operations
		return nil
	}
	return err
}

// DeleteConfigMap deletes a ConfigMap in the defined namespace
func (c *KubeClient) DeleteConfigMap(
	ctx context.Context,
	namespace string,
	name string,
) error {
	err := c.client.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && k8serrors.IsNotFound(err) {
		// ConfigMap doesn't exist, which is fine for cleanup operations
		return nil
	}
	return err
}

// GetPodLogs retrieves logs from a pod
func (c *KubeClient) GetPodLogs(
	ctx context.Context,
	namespace string,
	podName string,
	containerName string,
	tailLines *int64,
) (string, error) {
	req := c.client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		TailLines: tailLines,
	})

	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get log stream for pod %s/%s container %s: %w", namespace, podName, containerName, err)
	}
	defer stream.Close()

	// Limit memory usage by capping the amount of data read
	var reader io.Reader = stream

	// If tailLines is nil (reading full logs), apply a byte limit to prevent OOM
	if tailLines == nil {
		reader = io.LimitReader(stream, maxLogBytes)
	}

	logs, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read logs for pod %s/%s container %s: %w", namespace, podName, containerName, err)
	}

	// If we hit the limit, add a warning to the logs
	if tailLines == nil && len(logs) == maxLogBytes {
		logs = append(logs, []byte("\n\n[WARNING: Logs truncated due to size limit (10MB)]")...)
	}

	return string(logs), nil
}

// DoHTTPRequest makes http request to path on the pod
func (c *KubeClient) DoHTTPRequest(
	ctx context.Context,
	namespace string,
	name string,
	port uint,
	path string,
) ([]byte, error) {
	result := c.client.CoreV1().RESTClient().Get().
		Namespace(namespace).
		Resource("pods").
		Name(fmt.Sprintf("%s:%d", name, port)).
		SubResource("proxy").
		Suffix(path).
		Do(ctx)

	if result.Error() != nil {
		return nil, result.Error()
	}

	rawResponse, err := result.Raw()
	if err != nil {
		return nil, err
	}

	return rawResponse, nil
}

// PortForward turn on port forwarding for the pod
func (c *KubeClient) PortForward(
	ctx context.Context, namespace string,
	podName string,
	targetPort int,
) (int, error) {
	transport, upgrader, err := spdy.RoundTripperFor(c.restConfig)
	if err != nil {
		return -1, err
	}

	req := c.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("portforward")

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())

	// random select a unused port using port number 0
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return -1, err
	}

	localPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	fw, err := portforward.New(dialer, []string{fmt.Sprintf("%d:%d", localPort, targetPort)}, ctx.Done(),
		make(chan struct{}),
		c.OutWriter,
		c.ErrWriter)
	if err != nil {
		return -1, err
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- fw.ForwardPorts()
	}()

	select {
	case err = <-errCh:
		return -1, errors.Wrap(err, "port forwarding failed")
	case <-fw.Ready:
	}

	return localPort, nil
}

// RemoveFinalizersFromNamespace removes finalizers from a namespace using the Kubernetes API
func (c *KubeClient) RemoveFinalizersFromNamespace(ctx context.Context, namespace string) error {
	// Get the namespace
	ns, err := c.client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get namespace %s: %w", namespace, err)
	}

	// Remove finalizers
	ns.Spec.Finalizers = nil
	ns.ObjectMeta.Finalizers = nil

	// Use the finalize subresource to force remove finalizers
	_, err = c.client.CoreV1().RESTClient().Put().
		Resource("namespaces").
		Name(namespace).
		SubResource("finalize").
		Body(ns).
		Do(ctx).
		Get()
	if err != nil {
		return fmt.Errorf("failed to finalize namespace %s: %w", namespace, err)
	}

	return nil
}

// RemoveFinalizersFromPods removes finalizers from all pods in a namespace
func (c *KubeClient) RemoveFinalizersFromPods(ctx context.Context, namespace string) error {
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods in namespace %s: %w", namespace, err)
	}

	for _, pod := range pods.Items {
		if len(pod.ObjectMeta.Finalizers) > 0 {
			pod.ObjectMeta.Finalizers = nil
			_, err := c.client.CoreV1().Pods(namespace).Update(ctx, &pod, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from pod %s: %w", pod.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromServices removes finalizers from all services in a namespace
func (c *KubeClient) RemoveFinalizersFromServices(ctx context.Context, namespace string) error {
	services, err := c.client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list services in namespace %s: %w", namespace, err)
	}

	for _, service := range services.Items {
		if len(service.ObjectMeta.Finalizers) > 0 {
			service.ObjectMeta.Finalizers = nil
			_, err := c.client.CoreV1().Services(namespace).Update(ctx, &service, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from service %s: %w", service.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromDeployments removes finalizers from all deployments in a namespace
func (c *KubeClient) RemoveFinalizersFromDeployments(ctx context.Context, namespace string) error {
	deployments, err := c.client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list deployments in namespace %s: %w", namespace, err)
	}

	for _, deployment := range deployments.Items {
		if len(deployment.ObjectMeta.Finalizers) > 0 {
			deployment.ObjectMeta.Finalizers = nil
			_, err := c.client.AppsV1().Deployments(namespace).Update(ctx, &deployment, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from deployment %s: %w", deployment.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromReplicaSets removes finalizers from all replicasets in a namespace
func (c *KubeClient) RemoveFinalizersFromReplicaSets(ctx context.Context, namespace string) error {
	replicasets, err := c.client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list replicasets in namespace %s: %w", namespace, err)
	}

	for _, rs := range replicasets.Items {
		if len(rs.ObjectMeta.Finalizers) > 0 {
			rs.ObjectMeta.Finalizers = nil
			_, err := c.client.AppsV1().ReplicaSets(namespace).Update(ctx, &rs, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from replicaset %s: %w", rs.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromDaemonSets removes finalizers from all daemonsets in a namespace
func (c *KubeClient) RemoveFinalizersFromDaemonSets(ctx context.Context, namespace string) error {
	daemonsets, err := c.client.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list daemonsets in namespace %s: %w", namespace, err)
	}

	for _, ds := range daemonsets.Items {
		if len(ds.ObjectMeta.Finalizers) > 0 {
			ds.ObjectMeta.Finalizers = nil
			_, err := c.client.AppsV1().DaemonSets(namespace).Update(ctx, &ds, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from daemonset %s: %w", ds.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromStatefulSets removes finalizers from all statefulsets in a namespace
func (c *KubeClient) RemoveFinalizersFromStatefulSets(ctx context.Context, namespace string) error {
	statefulsets, err := c.client.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list statefulsets in namespace %s: %w", namespace, err)
	}

	for _, sts := range statefulsets.Items {
		if len(sts.ObjectMeta.Finalizers) > 0 {
			sts.ObjectMeta.Finalizers = nil
			_, err := c.client.AppsV1().StatefulSets(namespace).Update(ctx, &sts, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from statefulset %s: %w", sts.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromJobs removes finalizers from all jobs in a namespace
func (c *KubeClient) RemoveFinalizersFromJobs(ctx context.Context, namespace string) error {
	jobs, err := c.client.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list jobs in namespace %s: %w", namespace, err)
	}

	for _, job := range jobs.Items {
		if len(job.ObjectMeta.Finalizers) > 0 {
			job.ObjectMeta.Finalizers = nil
			_, err := c.client.BatchV1().Jobs(namespace).Update(ctx, &job, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from job %s: %w", job.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromPVCs removes finalizers from all persistentvolumeclaims in a namespace
func (c *KubeClient) RemoveFinalizersFromPVCs(ctx context.Context, namespace string) error {
	pvcs, err := c.client.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list persistentvolumeclaims in namespace %s: %w", namespace, err)
	}

	for _, pvc := range pvcs.Items {
		if len(pvc.ObjectMeta.Finalizers) > 0 {
			pvc.ObjectMeta.Finalizers = nil
			_, err := c.client.CoreV1().PersistentVolumeClaims(namespace).Update(ctx, &pvc, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from persistentvolumeclaim %s: %w", pvc.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromSecrets removes finalizers from all secrets in a namespace
func (c *KubeClient) RemoveFinalizersFromSecrets(ctx context.Context, namespace string) error {
	secrets, err := c.client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list secrets in namespace %s: %w", namespace, err)
	}

	for _, secret := range secrets.Items {
		if len(secret.ObjectMeta.Finalizers) > 0 {
			secret.ObjectMeta.Finalizers = nil
			_, err := c.client.CoreV1().Secrets(namespace).Update(ctx, &secret, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from secret %s: %w", secret.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromServiceAccounts removes finalizers from all serviceaccounts in a namespace
func (c *KubeClient) RemoveFinalizersFromServiceAccounts(ctx context.Context, namespace string) error {
	serviceaccounts, err := c.client.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list serviceaccounts in namespace %s: %w", namespace, err)
	}

	for _, sa := range serviceaccounts.Items {
		if len(sa.ObjectMeta.Finalizers) > 0 {
			sa.ObjectMeta.Finalizers = nil
			_, err := c.client.CoreV1().ServiceAccounts(namespace).Update(ctx, &sa, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from serviceaccount %s: %w", sa.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromRoles removes finalizers from all roles in a namespace
func (c *KubeClient) RemoveFinalizersFromRoles(ctx context.Context, namespace string) error {
	roles, err := c.client.RbacV1().Roles(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list roles in namespace %s: %w", namespace, err)
	}

	for _, role := range roles.Items {
		if len(role.ObjectMeta.Finalizers) > 0 {
			role.ObjectMeta.Finalizers = nil
			_, err := c.client.RbacV1().Roles(namespace).Update(ctx, &role, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from role %s: %w", role.Name, err)
			}
		}
	}

	return nil
}

// RemoveFinalizersFromRoleBindings removes finalizers from all rolebindings in a namespace
func (c *KubeClient) RemoveFinalizersFromRoleBindings(ctx context.Context, namespace string) error {
	rolebindings, err := c.client.RbacV1().RoleBindings(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list rolebindings in namespace %s: %w", namespace, err)
	}

	for _, rb := range rolebindings.Items {
		if len(rb.ObjectMeta.Finalizers) > 0 {
			rb.ObjectMeta.Finalizers = nil
			_, err := c.client.RbacV1().RoleBindings(namespace).Update(ctx, &rb, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to remove finalizers from rolebinding %s: %w", rb.Name, err)
			}
		}
	}

	return nil
}

// PatchDaemonSet patches a DaemonSet with the provided JSON patch
func (c *KubeClient) PatchDaemonSet(ctx context.Context, namespace, name, patch string) error {
	_, err := c.client.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch daemonset %s in namespace %s: %w", name, namespace, err)
	}
	return nil
}
