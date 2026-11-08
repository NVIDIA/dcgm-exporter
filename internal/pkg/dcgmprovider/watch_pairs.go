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

package dcgmprovider

import (
	"slices"
	"sync"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// The watch client skips the native unwatch of a group watch whose (entity,
// field) pairs a newer surviving request also watches. That relies on how
// DCGM keeps watches:
//
//   - DCGM keeps one watch per connection, entity and field. Watching a pair
//     again from another group replaces its policy; it does not add a second
//     watch.
//   - Destroying a group or a field group does not remove the connection's
//     watch on a pair that another group still watches.
//   - The core cache manager handles each pair on its own. Modules can clear
//     more than the pairs they are asked to unwatch, so pairs that a module
//     samples (profiling and NVSwitch fields, switch and CPU entities) are
//     always unwatched and the survivors restored, as before.
//
// Whenever the client cannot tell which pairs a watch covers, it unwatches it.

// groupEntity is one entity the exporter added to a DCGM group, as it named
// it: entity id of entityGroup, or link id of the parent entity.
type groupEntity struct {
	entityGroup dcgm.Field_Entity_Group
	id          uint
	link        bool
	parentGroup dcgm.Field_Entity_Group
	parent      uint
}

// watchPair is one (entity, field) pair a group watch covers.
type watchPair struct {
	entity groupEntity
	field  dcgm.Short
}

// watchTopology remembers the members of the groups and the fields of the
// field groups created through the client, so a watch's pairs are known when
// it is installed. A handle missing from a map is one the client did not
// create, whose contents are unknown.
type watchTopology struct {
	mu          sync.Mutex
	groups      map[uintptr][]groupEntity
	fieldGroups map[uintptr][]dcgm.Short
}

// addGroup records a new, empty group.
func (t *watchTopology) addGroup(group dcgm.GroupHandle) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.groups == nil {
		t.groups = make(map[uintptr][]groupEntity)
	}
	t.groups[group.GetHandle()] = []groupEntity{}
}

// addMember records an entity added to a group the client created.
func (t *watchTopology) addMember(group dcgm.GroupHandle, entity groupEntity) {
	t.mu.Lock()
	defer t.mu.Unlock()

	members, ok := t.groups[group.GetHandle()]
	if ok {
		t.groups[group.GetHandle()] = append(members, entity)
	}
}

// addFieldGroup records the fields of a new field group.
func (t *watchTopology) addFieldGroup(fieldGroup dcgm.FieldHandle, fields []dcgm.Short) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.fieldGroups == nil {
		t.fieldGroups = make(map[uintptr][]dcgm.Short)
	}
	t.fieldGroups[fieldGroup.GetHandle()] = slices.Clone(fields)
}

// removeGroup forgets a destroyed group, so a reused handle starts unknown.
func (t *watchTopology) removeGroup(group dcgm.GroupHandle) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.groups, group.GetHandle())
}

// removeFieldGroup forgets a destroyed field group.
func (t *watchTopology) removeFieldGroup(fieldGroup dcgm.FieldHandle) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.fieldGroups, fieldGroup.GetHandle())
}

// pairs returns every (entity, field) pair of the group and field group. ok
// is false when either was not created through the client, or a pair is one
// a DCGM module samples.
func (t *watchTopology) pairs(group dcgm.GroupHandle, fieldGroup dcgm.FieldHandle) (pairs []watchPair, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	members, knownGroup := t.groups[group.GetHandle()]
	fields, knownFields := t.fieldGroups[fieldGroup.GetHandle()]
	if !knownGroup || !knownFields {
		return nil, false
	}

	pairs = make([]watchPair, 0, len(members)*len(fields))
	for _, entity := range members {
		for _, field := range fields {
			pair := watchPair{entity: entity, field: field}
			if pair.moduleSampled() {
				return nil, false
			}

			pairs = append(pairs, pair)
		}
	}
	return pairs, true
}

// moduleSampled reports whether a DCGM module, not the core cache manager,
// samples the pair: profiling or NVSwitch fields, or switch, switch link or
// CPU entities.
func (p watchPair) moduleSampled() bool {
	switch {
	case p.field >= dcgm.DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO && p.field < dcgm.DCGM_FI_DEV_CPU_UTIL_RATIO,
		p.field == dcgm.DCGM_FI_PROF_NVLINK_TX_BYTES_PER_LINK,
		p.field == dcgm.DCGM_FI_PROF_NVLINK_RX_BYTES_PER_LINK,
		p.field >= dcgm.DCGM_FI_FIRST_NVSWITCH_FIELD_ID && p.field <= dcgm.DCGM_FI_LAST_NVSWITCH_FIELD_ID:
		return true
	case p.entity.link:
		return p.entity.parentGroup == dcgm.FE_SWITCH
	default:
		return p.entity.entityGroup == dcgm.FE_SWITCH ||
			p.entity.entityGroup == dcgm.FE_CPU ||
			p.entity.entityGroup == dcgm.FE_CPU_CORE
	}
}

// CreateGroup creates a group and records it as empty, so the pairs of its
// watches are known.
func (c *watchClient) CreateGroup(name string) (dcgm.GroupHandle, error) {
	group, err := c.DCGM.CreateGroup(name)
	if err != nil {
		return group, err
	}

	c.topology.addGroup(group)
	return group, nil
}

// AddEntityToGroup adds an entity to a group and records it.
func (c *watchClient) AddEntityToGroup(group dcgm.GroupHandle, entityGroup dcgm.Field_Entity_Group, entity uint) error {
	if err := c.DCGM.AddEntityToGroup(group, entityGroup, entity); err != nil {
		return err
	}

	c.topology.addMember(group, groupEntity{entityGroup: entityGroup, id: entity})
	return nil
}

// AddLinkEntityToGroup adds a link to a group and records it by its index and
// parent.
func (c *watchClient) AddLinkEntityToGroup(group dcgm.GroupHandle, index uint, parentGroup dcgm.Field_Entity_Group, parent uint) error {
	if err := c.DCGM.AddLinkEntityToGroup(group, index, parentGroup, parent); err != nil {
		return err
	}

	link := groupEntity{entityGroup: dcgm.FE_LINK, id: index, link: true, parentGroup: parentGroup, parent: parent}
	c.topology.addMember(group, link)
	return nil
}

// DestroyGroup destroys a group and forgets its members.
func (c *watchClient) DestroyGroup(group dcgm.GroupHandle) error {
	c.topology.removeGroup(group)
	return c.DCGM.DestroyGroup(group)
}

// FieldGroupCreate creates a field group and records its fields.
func (c *watchClient) FieldGroupCreate(name string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
	fieldGroup, err := c.DCGM.FieldGroupCreate(name, fields)
	if err != nil {
		return fieldGroup, err
	}

	c.topology.addFieldGroup(fieldGroup, fields)
	return fieldGroup, nil
}

// FieldGroupDestroy destroys a field group and forgets its fields.
func (c *watchClient) FieldGroupDestroy(fieldGroup dcgm.FieldHandle) error {
	c.topology.removeFieldGroup(fieldGroup)
	return c.DCGM.FieldGroupDestroy(fieldGroup)
}

// coveringSeq maps each pair of the surviving requests whose pairs are known
// to the newest install sequence that watches it.
func coveringSeq(survivors []watchRequest) map[watchPair]uint64 {
	newest := make(map[watchPair]uint64)
	for _, request := range survivors {
		if !request.pairsKnown {
			continue
		}

		for _, pair := range request.pairs {
			newest[pair] = max(newest[pair], request.seq)
		}
	}
	return newest
}

// coveredByNewer reports whether every pair of removed is watched by a
// surviving request installed after it, so DCGM's watch on each pair already
// carries a survivor's policy and needs no unwatch.
func coveredByNewer(removed watchRequest, newest map[watchPair]uint64) bool {
	if !removed.pairsKnown {
		return false
	}

	for _, pair := range removed.pairs {
		if newest[pair] <= removed.seq {
			return false
		}
	}
	return true
}
