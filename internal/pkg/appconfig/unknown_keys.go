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

package appconfig

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// rejectUnknownKeys reports the first key in the YAML tree that the schema of
// target does not define, naming its path and line. The decoder's own error
// names only a Go type, which does not help a person fix their file.
func rejectUnknownKeys(node *yaml.Node, target reflect.Type) error {
	w := keyWalker{seen: map[walkKey]bool{}}
	return w.walk(node, target, "")
}

// walkKey identifies one node checked against one type, so an anchor that is
// referenced many times is checked once.
type walkKey struct {
	node *yaml.Node
	typ  reflect.Type
}

type keyWalker struct {
	seen map[walkKey]bool
}

// walk checks only mapping keys. It follows aliases and merge keys, and leaves
// values of the wrong shape to the decoder.
func (w *keyWalker) walk(node *yaml.Node, target reflect.Type, path string) error {
	if node == nil {
		return nil
	}

	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}

	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}

	// Skip a node already checked against this type, which also stops alias loops.
	visit := walkKey{node: node, typ: target}
	if w.seen[visit] {
		return nil
	}
	w.seen[visit] = true

	// Pick the walk that matches the shape of the target type.
	switch {
	case node.Kind == yaml.DocumentNode:
		return w.walkEach(node.Content, target, func(int) string { return path })
	case target.Kind() == reflect.Struct && node.Kind == yaml.MappingNode:
		return w.walkStructKeys(node, target, path)
	case target.Kind() == reflect.Slice && node.Kind == yaml.SequenceNode:
		return w.walkEach(node.Content, target.Elem(), func(i int) string { return fmt.Sprintf("%s[%d]", path, i) })
	case target.Kind() == reflect.Map && node.Kind == yaml.MappingNode:
		return w.walkMapValues(node, target, path)
	}
	return nil
}

// walkEach walks each node against the same type, naming node i with pathOf(i).
func (w *keyWalker) walkEach(nodes []*yaml.Node, target reflect.Type, pathOf func(int) string) error {
	for i, child := range nodes {
		if err := w.walk(child, target, pathOf(i)); err != nil {
			return err
		}
	}
	return nil
}

// walkMapValues checks the values of a map-typed mapping. Merged entries are
// entries of this same map.
func (w *keyWalker) walkMapValues(node *yaml.Node, target reflect.Type, path string) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		var err error
		if key.Tag == "!!merge" {
			err = w.walkMerged(value, target, path)
		} else {
			err = w.walk(value, target.Elem(), joinKeyPath(path, key.Value))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// walkStructKeys rejects keys that are not yaml tags of target. A merge key
// (<<) contributes the keys of the mappings it merges, which are checked
// against the same struct.
func (w *keyWalker) walkStructKeys(node *yaml.Node, target reflect.Type, path string) error {
	allowed := yamlFieldTypes(target)

	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag == "!!merge" {
			if err := w.walkMerged(value, target, path); err != nil {
				return err
			}

			continue
		}

		childPath := joinKeyPath(path, key.Value)
		fieldType, ok := allowed[key.Value]
		if !ok {
			return &keyError{
				Path: childPath, Msg: fmt.Sprintf("unknown key at line %d", key.Line), AtKey: true,
				Line: key.Line, Column: key.Column,
			}
		}

		if err := w.walk(value, fieldType, childPath); err != nil {
			return err
		}
	}

	return nil
}

// walkMerged checks the value of a merge key: one mapping or alias, or a
// sequence of them.
func (w *keyWalker) walkMerged(value *yaml.Node, target reflect.Type, path string) error {
	if value.Kind == yaml.AliasNode {
		value = value.Alias
	}
	if value.Kind == yaml.SequenceNode {
		return w.walkEach(value.Content, target, func(int) string { return path })
	}
	return w.walk(value, target, path)
}

// yamlFieldTypes maps each yaml key of a struct to the type of its field.
func yamlFieldTypes(target reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, target.NumField())

	for i := 0; i < target.NumField(); i++ {
		field := target.Field(i)
		if !field.IsExported() {
			continue
		}

		name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}

		// yaml.v3 lower-cases the field name when there is no tag.
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		fields[name] = field.Type
	}

	return fields
}

// joinKeyPath appends key to a YAML path.
func joinKeyPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
