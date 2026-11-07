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
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
)

const (
	// NVIDIAGPUResourceName is the extended resource the NVIDIA device
	// plugin advertises for whole GPUs by default.
	NVIDIAGPUResourceName corev1.ResourceName = "nvidia.com/gpu"
	// NVIDIASharedGPUResourceName is the resource the NVIDIA device plugin
	// advertises for time-sliced GPUs when its config sets renameByDefault.
	NVIDIASharedGPUResourceName corev1.ResourceName = "nvidia.com/gpu.shared"
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

// DeleteNamespace deletes the configured test namespace only if its UID still
// matches the object whose ownership was checked by the caller.
func (c *KubeClient) DeleteNamespace(ctx context.Context, namespace string, uid types.UID) error {
	err := c.client.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if err != nil && k8serrors.IsNotFound(err) {
		// Namespace doesn't exist, which is fine for cleanup operations
		return nil
	}
	return err
}

// GetSecret returns one Secret without logging its data.
func (c *KubeClient) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	return c.client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
}

// CreateSecret creates a Secret prepared by the caller.
func (c *KubeClient) CreateSecret(ctx context.Context, secret *corev1.Secret) (*corev1.Secret, error) {
	return c.client.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
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

// GetPod returns the current Pod object. E2E helpers use it after scheduling to
// learn the node that Kubernetes assigned to a workload.
func (c *KubeClient) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	return c.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
}

// ServiceAccountCan asks the API server whether a ServiceAccount may perform an
// action. The review uses the groups Kubernetes puts in every ServiceAccount
// token, so bindings to those groups count as they would for the running pod.
func (c *KubeClient) ServiceAccountCan(
	ctx context.Context, namespace, name string, attributes authorizationv1.ResourceAttributes,
) (bool, error) {
	review := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: fmt.Sprintf("system:serviceaccount:%s:%s", namespace, name),
			Groups: []string{
				"system:serviceaccounts",
				"system:serviceaccounts:" + namespace,
				"system:authenticated",
			},
			ResourceAttributes: &attributes,
		},
	}

	result, err := c.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("review access for service account %s/%s: %w", namespace, name, err)
	}
	if result.Status.EvaluationError != "" {
		return false, fmt.Errorf("review access for service account %s/%s: incomplete review: %s",
			namespace, name, result.Status.EvaluationError)
	}
	return result.Status.Allowed, nil
}

// GetNodesByLabel returns nodes that match a label selector.
func (c *KubeClient) GetNodesByLabel(ctx context.Context, labelMap map[string]string) ([]corev1.Node, error) {
	nodeList, err := c.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labelMap).String(),
	})
	if err != nil {
		return nil, err
	}
	return nodeList.Items, nil
}

// GetEvents returns events from one namespace.
func (c *KubeClient) GetEvents(ctx context.Context, namespace string) ([]corev1.Event, error) {
	eventList, err := c.client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list events in namespace %q: %w", namespace, err)
	}
	return eventList.Items, nil
}

// GetServicesByLabel returns services that match the label selector.
func (c *KubeClient) GetServicesByLabel(ctx context.Context, namespace string, labelMap map[string]string) ([]corev1.Service,
	error,
) {
	serviceList, err := c.client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labelMap).String(),
	})
	if err != nil {
		return nil, err
	}
	return serviceList.Items, nil
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

// CreatePod creates a long-running pod in the namespace that requests one
// unit of gpuResourceName, such as NVIDIAGPUResourceName. The pod only sleeps,
// so it turns off the image's CUDA version requirement, which the runtime
// otherwise enforces against an older driver on some GPUs.
func (c *KubeClient) CreatePod(
	ctx context.Context,
	namespace string,
	labels map[string]string,
	name string,
	containerName string,
	image string,
	runtimeClassName string,
	nodeSelector map[string]string,
	gpuResourceName corev1.ResourceName,
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
			NodeSelector:     nodeSelector,
			RestartPolicy:    corev1.RestartPolicyAlways,
			Containers: []corev1.Container{
				{
					Name:    containerName,
					Image:   image,
					Command: []string{"sleep", "infinity"},
					Env:     []corev1.EnvVar{{Name: "NVIDIA_DISABLE_REQUIRE", Value: "true"}},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							gpuResourceName: quantity,
						},
					},
				},
			},
		},
	}

	return c.client.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
}

// CreatePodFromSpec creates a pod from a fully specified Pod object.
func (c *KubeClient) CreatePodFromSpec(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	return c.client.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
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

// ConfigMapExists reports whether the ConfigMap exists in the namespace.
func (c *KubeClient) ConfigMapExists(ctx context.Context, namespace, name string) (bool, error) {
	_, err := c.client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})

	return existsResult(err, "ConfigMap", namespace, name)
}

// CreateConfigMap creates a ConfigMap in the defined namespace.
func (c *KubeClient) CreateConfigMap(
	ctx context.Context,
	namespace string,
	name string,
	data map[string]string,
) (*corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: data,
	}

	return c.client.CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
}

// RoleExists reports whether the Role exists in the namespace.
func (c *KubeClient) RoleExists(ctx context.Context, namespace, name string) (bool, error) {
	_, err := c.client.RbacV1().Roles(namespace).Get(ctx, name, metav1.GetOptions{})

	return existsResult(err, "Role", namespace, name)
}

// GrantConfigMapRead lets a ServiceAccount read one ConfigMap by creating a Role
// and a RoleBinding named name in the namespace. A failed call can leave the Role
// behind, so callers register DeleteConfigMapReadGrant before calling it.
func (c *KubeClient) GrantConfigMapRead(ctx context.Context, namespace, name, configMapName, serviceAccount string) error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{""},
			Resources:     []string{"configmaps"},
			ResourceNames: []string{configMapName},
			Verbs:         []string{"get"},
		}},
	}
	if _, err := c.client.RbacV1().Roles(namespace).Create(ctx, role, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create Role %s/%s: %w", namespace, name, err)
	}

	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: serviceAccount, Namespace: namespace}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
	}
	if _, err := c.client.RbacV1().RoleBindings(namespace).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create RoleBinding %s/%s: %w", namespace, name, err)
	}

	return nil
}

// DeleteConfigMapReadGrant deletes the Role and RoleBinding that GrantConfigMapRead
// created, and tolerates ones that are already gone.
func (c *KubeClient) DeleteConfigMapReadGrant(ctx context.Context, namespace, name string) error {
	err := c.client.RbacV1().RoleBindings(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete RoleBinding %s/%s: %w", namespace, name, err)
	}

	err = c.client.RbacV1().Roles(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete Role %s/%s: %w", namespace, name, err)
	}

	return nil
}

// existsResult turns the error of a Get call into whether the object exists:
// not found means no, and any other error is returned with the object's kind,
// namespace and name.
func existsResult(err error, kind, namespace, name string) (bool, error) {
	if err == nil {
		return true, nil
	}

	if k8serrors.IsNotFound(err) {
		return false, nil
	}

	return false, fmt.Errorf("get %s %s/%s: %w", kind, namespace, name, err)
}

// GetPodLogs retrieves up to 10 MiB of logs from a pod.
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

	// Cap both full-log and tailed responses before reading them into memory.
	logs, err := io.ReadAll(io.LimitReader(stream, maxLogBytes+1))
	if err != nil {
		return "", fmt.Errorf("failed to read logs for pod %s/%s container %s: %w", namespace, podName, containerName, err)
	}

	if len(logs) > maxLogBytes {
		logs = logs[:maxLogBytes]
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

// DoServiceHTTPRequest makes an HTTP request to a Service proxy path.
func (c *KubeClient) DoServiceHTTPRequest(
	ctx context.Context,
	namespace string,
	name string,
	port uint,
	path string,
) ([]byte, error) {
	result := c.client.CoreV1().RESTClient().Get().
		Namespace(namespace).
		Resource("services").
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

// ExecutePodCommand runs a command in a pod container and returns stdout and stderr.
func (c *KubeClient) ExecutePodCommand(
	ctx context.Context,
	namespace string,
	podName string,
	containerName string,
	command []string,
) (string, string, error) {
	req := c.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, http.MethodPost, req.URL())
	if err != nil {
		return "", "", err
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return stdout.String(), stderr.String(), err
	}

	return stdout.String(), stderr.String(), nil
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
		if err == nil {
			return -1, nil
		}
		return -1, fmt.Errorf("port forwarding failed: %w", err)
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
