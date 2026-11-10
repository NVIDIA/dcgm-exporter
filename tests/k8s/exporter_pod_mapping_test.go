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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestReadyExporterPodOnNode checks selection of a ready exporter Pod on the workload node.
func TestReadyExporterPodOnNode(t *testing.T) {
	t.Parallel()

	readyExporter := func(name, nodeName string) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       corev1.PodSpec{NodeName: nodeName},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}}},
		}
	}
	terminatingExporter := readyExporter("exporter-terminating", "gpu-node-a")
	deletionTimestamp := metav1.NewTime(time.Unix(1, 0))
	terminatingExporter.DeletionTimestamp = &deletionTimestamp

	testCases := []struct {
		name     string
		pods     []corev1.Pod
		nodeName string
		wantName string
		wantErr  bool
	}{
		{
			name:     "no exporter on workload node",
			nodeName: "gpu-node-a",
			wantErr:  true,
		},
		{
			name:     "one ready exporter on workload node",
			pods:     []corev1.Pod{readyExporter("exporter-a", "gpu-node-a")},
			nodeName: "gpu-node-a",
			wantName: "exporter-a",
		},
		{
			name: "workload uses exporter on second node",
			pods: []corev1.Pod{
				readyExporter("exporter-a", "gpu-node-a"),
				readyExporter("exporter-b", "gpu-node-b"),
			},
			nodeName: "gpu-node-b",
			wantName: "exporter-b",
		},
		{
			name: "exporter on workload node is not ready",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "exporter-a"},
					Spec:       corev1.PodSpec{NodeName: "gpu-node-a"},
				},
				readyExporter("exporter-b", "gpu-node-b"),
			},
			nodeName: "gpu-node-a",
			wantErr:  true,
		},
		{
			name: "terminating exporter on workload node is ignored",
			pods: []corev1.Pod{
				terminatingExporter,
				readyExporter("exporter-b", "gpu-node-b"),
			},
			nodeName: "gpu-node-a",
			wantErr:  true,
		},
		{
			name: "active exporter is selected over terminating exporter",
			pods: []corev1.Pod{
				terminatingExporter,
				readyExporter("exporter-active", "gpu-node-a"),
			},
			nodeName: "gpu-node-a",
			wantName: "exporter-active",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := readyExporterPodOnNode(testCase.pods, testCase.nodeName)
			if testCase.wantErr {
				if err == nil {
					t.Fatal("readyExporterPodOnNode() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("readyExporterPodOnNode() error = %v", err)
			}
			if got.Name != testCase.wantName {
				t.Errorf("readyExporterPodOnNode() pod = %q, want %q", got.Name, testCase.wantName)
			}
		})
	}
}

// TestFirstReadyExporterPod checks that unready and terminating Pods are skipped.
func TestFirstReadyExporterPod(t *testing.T) {
	t.Parallel()

	readyExporter := func(name string) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}}},
		}
	}
	terminatingExporter := readyExporter("terminating")
	deletionTimestamp := metav1.NewTime(time.Unix(1, 0))
	terminatingExporter.DeletionTimestamp = &deletionTimestamp

	tests := []struct {
		name    string
		pods    []corev1.Pod
		want    string
		wantErr bool
	}{
		{
			name: "skips unready and terminating pods",
			pods: []corev1.Pod{
				{ObjectMeta: metav1.ObjectMeta{Name: "unready"}},
				terminatingExporter,
				readyExporter("ready"),
			},
			want: "ready",
		},
		{
			name: "no ready active pod",
			pods: []corev1.Pod{
				{ObjectMeta: metav1.ObjectMeta{Name: "unready"}},
				terminatingExporter,
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := firstReadyExporterPod(test.pods)
			if test.wantErr {
				if err == nil {
					t.Fatal("firstReadyExporterPod() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("firstReadyExporterPod() error = %v", err)
			}
			if got.Name != test.want {
				t.Errorf("firstReadyExporterPod() pod = %q, want %q", got.Name, test.want)
			}
		})
	}
}

// TestReplacementExporterPod checks for a newer Pod with another UID on the same node.
func TestReplacementExporterPod(t *testing.T) {
	t.Parallel()

	createdAt := func(seconds int64) metav1.Time {
		return metav1.NewTime(time.Unix(seconds, 0))
	}
	previousPod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "exporter-old",
			UID:               "old-uid",
			CreationTimestamp: createdAt(1),
		},
		Spec: corev1.PodSpec{NodeName: "gpu-node-a"},
	}
	replacement := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "exporter-new",
			UID:               "new-uid",
			CreationTimestamp: createdAt(2),
		},
		Spec: corev1.PodSpec{NodeName: "gpu-node-a"},
	}
	otherNode := replacement.DeepCopy()
	otherNode.Name = "exporter-other-node"
	otherNode.Spec.NodeName = "gpu-node-b"
	sameUID := replacement.DeepCopy()
	sameUID.Name = "exporter-same-uid"
	sameUID.UID = previousPod.UID
	notNewer := replacement.DeepCopy()
	notNewer.Name = "exporter-not-newer"
	notNewer.CreationTimestamp = createdAt(1)
	terminating := replacement.DeepCopy()
	terminating.Name = "exporter-terminating"
	deletionTimestamp := createdAt(3)
	terminating.DeletionTimestamp = &deletionTimestamp

	tests := []struct {
		name    string
		pods    []corev1.Pod
		want    string
		wantErr bool
	}{
		{
			name: "selects fresh replacement on the same node",
			pods: []corev1.Pod{
				*otherNode,
				*sameUID,
				*notNewer,
				*terminating,
				replacement,
			},
			want: replacement.Name,
		},
		{
			name:    "no replacement on previous node",
			pods:    []corev1.Pod{*otherNode},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := replacementExporterPod(test.pods, previousPod)
			if test.wantErr {
				if err == nil {
					t.Fatal("replacementExporterPod() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("replacementExporterPod() error = %v", err)
			}
			if got.Name != test.want {
				t.Errorf("replacementExporterPod() pod = %q, want %q", got.Name, test.want)
			}
		})
	}
}
