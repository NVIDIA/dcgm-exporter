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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const nvidiaResourceName = "nvidia.com/gpu"

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
	return c.client.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
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
			RestartPolicy:    corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:  containerName,
					Image: image,
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
	return c.client.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
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
