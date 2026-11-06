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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// migResourcePrefix starts the name of every MIG resource the device plugin
// advertises with the mixed MIG strategy, such as nvidia.com/mig-1g.23gb.
const migResourcePrefix = "nvidia.com/mig-"

// migResourceOnNode is a MIG resource and a node that can allocate it. node
// is the node's kubernetes.io/hostname label, which a node selector can pin a
// pod to.
type migResourceOnNode struct {
	node     string
	resource corev1.ResourceName
}

// migResourceName returns the first MIG resource, in name order, that any of
// nodes can allocate, and the first node, in name order, that can allocate
// it (by hostname). A workload requesting the resource must run on that node.
// A node without a kubernetes.io/hostname label is skipped, because the
// workload is pinned with that label.
func migResourceName(nodes []corev1.Node) (migResourceOnNode, error) {
	var chosen migResourceOnNode
	for _, node := range nodes {
		hostname := node.Labels[corev1.LabelHostname]
		if hostname == "" {
			continue
		}

		for name, quantity := range node.Status.Allocatable {
			if !strings.HasPrefix(string(name), migResourcePrefix) || quantity.Sign() <= 0 {
				continue
			}

			candidate := migResourceOnNode{node: hostname, resource: name}
			if chosen.resource == "" || candidate.before(chosen) {
				chosen = candidate
			}
		}
	}

	if chosen.resource == "" {
		return migResourceOnNode{}, fmt.Errorf("no node can allocate a %s* resource", migResourcePrefix)
	}
	return chosen, nil
}

// before orders choices by resource name, then by node hostname.
func (c migResourceOnNode) before(other migResourceOnNode) bool {
	if c.resource != other.resource {
		return c.resource < other.resource
	}
	return c.node < other.node
}
