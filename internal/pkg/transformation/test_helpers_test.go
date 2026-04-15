/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

package transformation

import (
	"context"
	"time"

	"k8s.io/client-go/tools/cache"
)

// testInformerForDRA is a minimal SharedIndexInformer implementation for tests that
// want to inject a pre-populated cache.Indexer (matching the production indexers).
type testInformerForDRA struct {
	store cache.Store
}

func (t *testInformerForDRA) GetStore() cache.Store { return t.store }

func (t *testInformerForDRA) GetIndexer() cache.Indexer { return t.store.(cache.Indexer) }

func (t *testInformerForDRA) AddIndexers(indexers cache.Indexers) error { return nil }

func (t *testInformerForDRA) GetController() cache.Controller { return nil }

func (t *testInformerForDRA) LastSyncResourceVersion() string { return "" }

func (t *testInformerForDRA) AddEventHandler(handler cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	return nil, nil
}

func (t *testInformerForDRA) AddEventHandlerWithResyncPeriod(handler cache.ResourceEventHandler, resyncPeriod time.Duration) (cache.ResourceEventHandlerRegistration, error) {
	return nil, nil
}

func (t *testInformerForDRA) AddEventHandlerWithOptions(handler cache.ResourceEventHandler, options cache.HandlerOptions) (cache.ResourceEventHandlerRegistration, error) {
	return nil, nil
}

func (t *testInformerForDRA) RemoveEventHandler(handle cache.ResourceEventHandlerRegistration) error { return nil }

func (t *testInformerForDRA) IsStopped() bool { return false }

func (t *testInformerForDRA) SetWatchErrorHandler(handler cache.WatchErrorHandler) error { return nil }

func (t *testInformerForDRA) SetWatchErrorHandlerWithContext(handler cache.WatchErrorHandlerWithContext) error {
	return nil
}

func (t *testInformerForDRA) SetTransform(handler cache.TransformFunc) error { return nil }

func (t *testInformerForDRA) HasSynced() bool { return true }

func (t *testInformerForDRA) Run(stopCh <-chan struct{}) {}

func (t *testInformerForDRA) RunWithContext(ctx context.Context) {}

