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
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// watchClient keeps logical watch requests for one native DCGM session.
// DCGM owns watches by connection, entity, and field, so unwatching one group
// can remove another group's watch. While the session is live, restore surviving
// requests before readers can enter DCGM, including after failed watch setup.
type watchClient struct {
	DCGM
	mu           sync.RWMutex
	requests     []watchRequest
	nextSeq      uint64
	shuttingDown atomic.Bool
	topology     watchTopology
	// deferDepth counts open DeferRestores scopes. While it is above zero, an
	// unwatch leaves the restore to the end of the scope.
	deferDepth int
	// restored is open while an unwatch has removed watches and their restore
	// has not run yet. Reads wait until it is closed.
	restored chan struct{}
}

// watchRequestKey identifies one watch request: a group and field group, or a
// direct GPU field watch.
type watchRequestKey struct {
	group  dcgm.GroupHandle
	fields dcgm.FieldHandle
	gpu    uint
	field  dcgm.Short
	direct bool
}

// watchRequest pairs a request's identity with the call that installs it
// again. seq orders installs. pairs are the (entity, field) pairs a group
// watch covers, when pairsKnown; see watch_pairs.go.
type watchRequest struct {
	key        watchRequestKey
	install    func() error
	seq        uint64
	pairs      []watchPair
	pairsKnown bool
}

var _ FieldGroupUnwatcher = (*watchClient)(nil)

// newWatchClient wraps a DCGM session so it keeps and restores watch requests.
func newWatchClient(client DCGM) *watchClient {
	return &watchClient{DCGM: client}
}

// BeginShutdown disables watch restoration for final process cleanup without
// waiting for native calls; live registry retirement must not use it.
func BeginShutdown() {
	if client, ok := Client().(*watchClient); ok {
		client.shuttingDown.Store(true)
	}
}

// WatchFieldsWithGroupExNoRefresh installs a group watch without asking DCGM
// to update every watched field afterwards, and keeps it for restore. If
// installing fails, the watch is removed and the surviving watches are
// restored.
func (c *watchClient) WatchFieldsWithGroupExNoRefresh(
	fields dcgm.FieldHandle, group dcgm.GroupHandle, interval int64, age float64, samples int32,
) error {
	pairs, pairsKnown := c.topology.pairs(group, fields)

	return c.install(watchRequest{
		key: watchRequestKey{group: group, fields: fields},
		install: func() error {
			return c.DCGM.WatchFieldsWithGroupExNoRefresh(fields, group, interval, age, samples)
		},
		pairs:      pairs,
		pairsKnown: pairsKnown,
	}, func() error {
		return c.DCGM.UnwatchFields(fields, group)
	})
}

// WatchFieldValue retains direct watches until session cleanup. The GPU
// lifecycle watcher uses this path even when no GPU groups can be created.
func (c *watchClient) WatchFieldValue(gpu uint, field dcgm.Short, interval, age time.Duration, samples int) error {
	return c.install(watchRequest{
		key: watchRequestKey{gpu: gpu, field: field, direct: true},
		install: func() error {
			return c.DCGM.WatchFieldValue(gpu, field, interval, age, samples)
		},
	}, nil)
}

// install runs one request under the write lock and keeps it, replacing an
// earlier request with the same key. On failure it runs rollback, restores the
// surviving requests, and keeps nothing. It refuses new watches during shutdown.
func (c *watchClient) install(request watchRequest, rollback func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.shuttingDown.Load() {
		return errors.New("cannot install DCGM watch during shutdown")
	}

	if err := request.install(); err != nil {
		// A failed native watch can still leave some of the candidate's
		// watches in DCGM. Remove them before restoring the published
		// consumers; do not retain the candidate's soon-to-be-destroyed handles.
		var rollbackErr error
		if rollback != nil {
			if cleanupErr := rollback(); cleanupErr != nil {
				rollbackErr = fmt.Errorf("rollback failed DCGM watch: %w", cleanupErr)
			}
		}
		return errors.Join(err, rollbackErr, c.restore())
	}

	// Replay in installation order to preserve DCGM's policy for overlapping
	// requests. Repeated setup replaces the same request rather than retaining
	// obsolete policies or adding a second cleanup obligation.
	c.requests = slices.DeleteFunc(c.requests, func(existing watchRequest) bool {
		return existing.key == request.key
	})
	c.nextSeq++
	request.seq = c.nextSeq
	c.requests = append(c.requests, request)
	return nil
}

// UnwatchFields removes one group watch the same way UnwatchFieldGroups does.
func (c *watchClient) UnwatchFields(fields dcgm.FieldHandle, group dcgm.GroupHandle) error {
	return c.UnwatchFieldGroups([]FieldGroupWatch{{FieldGroup: fields, Group: group}})
}

// UnwatchFieldGroups forgets the requests for watches and removes the watches
// in DCGM, one at a time. A watch whose pairs all have a newer surviving
// request is not removed in DCGM (see watch_pairs.go). After any removal in
// DCGM, every surviving request is restored once, since DCGM may have removed
// survivors too; reads wait until the restore has finished. The error joins
// one error per failed unwatch, then a *RestoreError if the restore failed;
// restore has already logged that failure.
func (c *watchClient) UnwatchFieldGroups(watches []FieldGroupWatch) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	native := c.forgetRequests(watches)
	if len(native) == 0 {
		return nil
	}

	errs := unwatchEach(c.DCGM, native)
	if c.deferDepth > 0 {
		if c.restored == nil {
			c.restored = make(chan struct{})
		}
		return errors.Join(errs...)
	}

	restoreErr := c.restore()
	return errors.Join(append(errs, restoreErr)...)
}

// DeferRestores makes the client of the process restore surviving watches
// once, when the returned function runs, instead of after each unwatch in
// between. Reads wait for that restore, so they never see watches an unwatch
// removed. Registry retirement uses it to restore once for all of its
// collectors. Nothing a scope's caller waits for may read through the client
// before the scope ends. For a client without restores it does nothing.
func DeferRestores() (end func()) {
	client, ok := Client().(*watchClient)
	if !ok {
		return func() {
			// A client that does not restore watches has nothing to defer.
		}
	}

	client.mu.Lock()
	client.deferDepth++
	client.mu.Unlock()

	return client.endDeferredRestores
}

// endDeferredRestores closes one DeferRestores scope and, when it was the last
// one, restores the survivors if an unwatch inside the scopes removed watches.
// restore logs its own failures.
func (c *watchClient) endDeferredRestores() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.deferDepth--
	if c.deferDepth == 0 && c.restored != nil {
		// replaySurvivors logs each failure; the retiring caller has no use
		// for them.
		c.replaySurvivors()
	}
}

// rlockRestored takes the read lock once no restore is pending, so a read
// never sees watches an unwatch removed and a deferred restore has not put
// back yet. The caller releases c.mu with RUnlock.
func (c *watchClient) rlockRestored() {
	for {
		c.mu.RLock()
		pending := c.restored
		if pending == nil {
			return
		}

		c.mu.RUnlock()
		<-pending
	}
}

// forgetRequests removes the requests for watches and returns the watches to
// remove in DCGM: all of them during shutdown, otherwise every watch that has
// no request or is not covered by newer survivors. The caller holds c.mu.
func (c *watchClient) forgetRequests(watches []FieldGroupWatch) []FieldGroupWatch {
	if c.shuttingDown.Load() {
		c.requests = nil
		return watches
	}

	wanted := make(map[watchRequestKey]bool, len(watches))
	for _, watch := range watches {
		wanted[watchRequestKey{group: watch.Group, fields: watch.FieldGroup}] = true
	}

	removed := make(map[watchRequestKey]watchRequest, len(watches))
	c.requests = slices.DeleteFunc(c.requests, func(request watchRequest) bool {
		if wanted[request.key] {
			removed[request.key] = request
		}
		return wanted[request.key]
	})

	newest := coveringSeq(c.requests)
	var native []FieldGroupWatch
	for _, watch := range watches {
		request, ok := removed[watchRequestKey{group: watch.Group, fields: watch.FieldGroup}]
		if !ok || !coveredByNewer(request, newest) {
			native = append(native, watch)
		}
	}
	return native
}

// unwatchEach removes each watch through client and returns one error, with
// the watch's handles, for each failure. It keeps going after a failure.
func unwatchEach(client DCGM, watches []FieldGroupWatch) []error {
	var errs []error
	for _, watch := range watches {
		if err := client.UnwatchFields(watch.FieldGroup, watch.Group); err != nil {
			errs = append(errs, fmt.Errorf(
				"unwatch field group %d on group %d: %w",
				watch.FieldGroup.GetHandle(), watch.Group.GetHandle(), err,
			))
		}
	}
	return errs
}

// RestoreError reports that reinstalling surviving watches failed after an
// unwatch or a failed install. The client logs it when it happens, so callers
// that log unwatch failures can skip it.
type RestoreError struct {
	Err error
}

// Error returns the failed restores' messages.
func (e *RestoreError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the joined restore failures.
func (e *RestoreError) Unwrap() error {
	return e.Err
}

// restore installs every surviving request again with its original policy and
// handles, and forces no update. Replaying all requests also covers DCGM
// modules whose unwatch operation removes more than the supplied field group.
// It returns the failures, which replaySurvivors has logged, as a
// *RestoreError, or nil. The caller holds c.mu.
func (c *watchClient) restore() error {
	errs := c.replaySurvivors()
	if len(errs) == 0 {
		return nil
	}
	return &RestoreError{Err: errors.Join(errs...)}
}

// replaySurvivors installs every surviving request again in install order,
// logs and returns each failure, and lets reads waiting for a deferred restore
// go on. The caller holds c.mu.
func (c *watchClient) replaySurvivors() []error {
	var errs []error
	for _, request := range c.requests {
		if c.shuttingDown.Load() {
			break
		}

		if err := request.install(); err != nil {
			restoreErr := fmt.Errorf("restore DCGM watch %+v: %w", request.key, err)
			// Cleanup callers may suppress statuses that are benign for retired
			// watches. A surviving watch's failure always needs to be reported.
			slog.Error("Failed to restore DCGM watch", "error", restoreErr)
			errs = append(errs, restoreErr)
		}
	}

	if c.restored != nil {
		close(c.restored)
		c.restored = nil
	}
	return errs
}

// GetValuesSince reads under the read lock, so it never sees a restore in progress or pending.
func (c *watchClient) GetValuesSince(group dcgm.GroupHandle, fields dcgm.FieldHandle, since time.Time) ([]dcgm.FieldValue_v2, time.Time, error) {
	c.rlockRestored()
	defer c.mu.RUnlock()
	return c.DCGM.GetValuesSince(group, fields, since)
}

// EntitiesGetLatestValues reads under the read lock, so it never sees a restore in progress or pending.
func (c *watchClient) EntitiesGetLatestValues(entities []dcgm.GroupEntityPair, fields []dcgm.Short, flags uint) ([]dcgm.FieldValue_v2, error) {
	c.rlockRestored()
	defer c.mu.RUnlock()
	return c.DCGM.EntitiesGetLatestValues(entities, fields, flags)
}

// EntityGetLatestValues reads under the read lock, so it never sees a restore in progress or pending.
func (c *watchClient) EntityGetLatestValues(entityGroup dcgm.Field_Entity_Group, entity uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1, error) {
	c.rlockRestored()
	defer c.mu.RUnlock()
	return c.DCGM.EntityGetLatestValues(entityGroup, entity, fields)
}

// LinkGetLatestValues reads under the read lock, so it never sees a restore in progress or pending.
func (c *watchClient) LinkGetLatestValues(index uint, parentType dcgm.Field_Entity_Group, parent uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1, error) {
	c.rlockRestored()
	defer c.mu.RUnlock()
	return c.DCGM.LinkGetLatestValues(index, parentType, parent, fields)
}

// GetMultipleValuesForField reads under the read lock, so it never sees a restore in progress or pending.
func (c *watchClient) GetMultipleValuesForField(gpu uint, field dcgm.Short, samples int, start, end time.Time) ([]dcgm.FieldValue_v1, error) {
	c.rlockRestored()
	defer c.mu.RUnlock()
	return c.DCGM.GetMultipleValuesForField(gpu, field, samples, start, end)
}
