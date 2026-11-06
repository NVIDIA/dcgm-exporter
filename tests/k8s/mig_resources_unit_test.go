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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// testNode returns a named node with allocatable resource counts and, like a
// kubelet-registered node, a kubernetes.io/hostname label equal to its name.
func testNode(name string, allocatable map[corev1.ResourceName]string) corev1.Node {
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{corev1.LabelHostname: name},
	}}
	node.Status.Allocatable = corev1.ResourceList{}
	for name, count := range allocatable {
		node.Status.Allocatable[name] = resource.MustParse(count)
	}
	return node
}

// TestMIGResourceName checks which MIG resource the MIG pod scenario requests
// and which node it pins the workload to.
func TestMIGResourceName(t *testing.T) {
	tests := []struct {
		name    string
		nodes   []corev1.Node
		want    migResourceOnNode
		wantErr string
	}{
		{
			name: "first MIG resource in name order",
			nodes: []corev1.Node{testNode("node-a", map[corev1.ResourceName]string{
				"nvidia.com/gpu": "1", "nvidia.com/mig-2g.47gb": "1", "nvidia.com/mig-1g.23gb": "2",
			})},
			want: migResourceOnNode{node: "node-a", resource: "nvidia.com/mig-1g.23gb"},
		},
		{
			name: "a MIG resource with nothing left to allocate is ignored",
			nodes: []corev1.Node{testNode("node-a", map[corev1.ResourceName]string{
				"nvidia.com/mig-1g.23gb": "0", "nvidia.com/mig-2g.47gb": "1",
			})},
			want: migResourceOnNode{node: "node-a", resource: "nvidia.com/mig-2g.47gb"},
		},
		{
			name: "the node that has the chosen resource",
			nodes: []corev1.Node{
				testNode("node-a", map[corev1.ResourceName]string{"nvidia.com/mig-3g.95gb": "1"}),
				testNode("node-b", map[corev1.ResourceName]string{"nvidia.com/mig-1g.23gb": "1"}),
			},
			want: migResourceOnNode{node: "node-b", resource: "nvidia.com/mig-1g.23gb"},
		},
		{
			name: "the first node in name order when several have the resource",
			nodes: []corev1.Node{
				testNode("node-c", map[corev1.ResourceName]string{"nvidia.com/mig-1g.23gb": "1"}),
				testNode("node-b", map[corev1.ResourceName]string{"nvidia.com/mig-1g.23gb": "2"}),
			},
			want: migResourceOnNode{node: "node-b", resource: "nvidia.com/mig-1g.23gb"},
		},
		{
			name: "the node's hostname label names it",
			nodes: []corev1.Node{func() corev1.Node {
				node := testNode("node-a", map[corev1.ResourceName]string{"nvidia.com/mig-1g.23gb": "1"})
				node.Labels = map[string]string{corev1.LabelHostname: "host-a"}
				return node
			}()},
			want: migResourceOnNode{node: "host-a", resource: "nvidia.com/mig-1g.23gb"},
		},
		{
			name: "a node without a hostname label is not chosen",
			nodes: []corev1.Node{
				func() corev1.Node {
					node := testNode("node-a", map[corev1.ResourceName]string{"nvidia.com/mig-1g.23gb": "1"})
					node.Labels = nil
					return node
				}(),
				testNode("node-b", map[corev1.ResourceName]string{"nvidia.com/mig-1g.23gb": "1"}),
			},
			want: migResourceOnNode{node: "node-b", resource: "nvidia.com/mig-1g.23gb"},
		},
		{
			name:    "no MIG resource",
			nodes:   []corev1.Node{testNode("node-a", map[corev1.ResourceName]string{"nvidia.com/gpu": "4"})},
			wantErr: "can allocate a nvidia.com/mig-* resource",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := migResourceName(tc.nodes)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("migResourceName() error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}

			if err != nil || got != tc.want {
				t.Fatalf("migResourceName() = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}
