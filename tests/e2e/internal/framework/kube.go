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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
)

const nvidiaResourceName = "nvidia.com/gpu"

// KubeClient is a kubernetes client
type KubeClient struct {
	client *clientset.Clientset
}

// NewKubeClient creates a new KubeClient instance
func NewKubeClient(client *clientset.Clientset) *KubeClient {
	return &KubeClient{client: client}
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

// DeleteNamespace deletes the namespace
func (c *KubeClient) DeleteNamespace(
	ctx context.Context,
	namespace string,
) error {
	return c.client.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
}

// GetPodsByLabel returns a list of pods that matches with the label selector
func (c *KubeClient) GetPodsByLabel(ctx context.Context, namespace string, labelMap map[string]string) ([]corev1.Pod, error) {
	podList, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labelMap).String(),
	})
	if err != nil {
		return nil, err
	}
	return podList.Items, nil
}

func (c *KubeClient) CheckPodCondition(ctx context.Context,
	namespace, podName string,
	podConditionType corev1.PodConditionType) (bool, error) {
	pod, err := c.client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("unexpected error getting pod %s; err: %w", podName, err)
	}

	for _, c := range pod.Status.Conditions {
		if c.Type != podConditionType {
			continue
		}
		if c.Status == corev1.ConditionTrue {
			return true, nil
		}
	}

	return false, nil
}

// CreatePod creates a new pod in the defined namespace
func (c *KubeClient) CreatePod(ctx context.Context,
	namespace string,
	labels map[string]string,
	name string,
	containerName string,
	image string,
) (*corev1.Pod, error) {

	quantity, _ := resource.ParseQuantity("1")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
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
func (c *KubeClient) DeletePod(ctx context.Context,
	namespace string,
	name string,
) error {
	return c.client.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// DoHttpRequest makes http request to path on the pod
func (c *KubeClient) DoHttpRequest(ctx context.Context,
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
