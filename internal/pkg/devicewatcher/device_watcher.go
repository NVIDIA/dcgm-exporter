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

package devicewatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	. "github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

// DeviceWatcher resolves fields and creates watches with one captured DCGM
// client. A registry owns its resulting cleanup closures, so those closures
// always release handles through the same client that created them.
type DeviceWatcher struct {
	client dcgmprovider.DCGM
}

// WatchBinding records one successful DCGM watch. Consumers use these exact
// pairs for reads and cleanup, avoiding invalid group-by-field-group cross-products
// when a watch list has multiple scopes or split field groups.
type WatchBinding struct {
	Group      dcgm.GroupHandle
	FieldGroup dcgm.FieldHandle
}

// WatchResult owns the resources created by one or more successful watch
// requests. A caller receives it only after all requested watches succeed and
// closes it when the owning collector retires; Close is safe to call repeatedly.
type WatchResult struct {
	client      dcgmprovider.DCGM
	groups      []dcgm.GroupHandle
	fieldGroups []dcgm.FieldHandle
	bindings    []WatchBinding
	parts       []*WatchResult
	closeFunc   func()
	closeOnce   sync.Once
	// joinedClose counts a cleanup of this result that a joined result's
	// Close has claimed and not yet finished, so Close can wait for it.
	joinedClose sync.WaitGroup
}

// Close releases exact watches before their field groups and entity groups.
// It works for a joined result as well as one physical watch request. When a
// joined result is already releasing this result, Close waits until it is done.
func (r *WatchResult) Close() {
	if r == nil {
		return
	}

	r.closeOnce.Do(r.close)
	r.joinedClose.Wait()
}

// Bindings returns copies of the exact group and field-group pairs installed
// by this result. Event collectors use these pairs for DCGM reads.
func (r *WatchResult) Bindings() []WatchBinding {
	if r == nil {
		return nil
	}
	if len(r.parts) > 0 {
		bindings := make([]WatchBinding, 0)
		for _, part := range r.parts {
			bindings = append(bindings, part.Bindings()...)
		}
		return bindings
	}
	return append([]WatchBinding(nil), r.bindings...)
}

// JoinWatchResults combines independently installed scopes into one caller-
// owned result. WatchList joins only completed results; it closes earlier
// results itself if a later scope fails.
func JoinWatchResults(results ...*WatchResult) *WatchResult {
	parts := make([]*WatchResult, 0, len(results))
	for _, result := range results {
		if result != nil {
			parts = append(parts, result)
		}
	}
	return &WatchResult{parts: parts}
}

// ResultFromBindings wraps already-installed, explicitly paired watches from
// a non-DCGM Watcher implementation. The closer must release exactly those
// bindings and is called at most once by Close. DeviceWatcher builds results
// directly; this constructor keeps small test doubles usable at the boundary.
func ResultFromBindings(bindings []WatchBinding, closer func()) *WatchResult {
	return &WatchResult{
		bindings:  append([]WatchBinding(nil), bindings...),
		closeFunc: closer,
	}
}

// close releases the result's resources. For a joined result it gathers the
// physical results that are still open, unwatches all of their watches
// together (see unwatchLeaves), and then destroys each result's field groups and groups in reverse
// creation order. A result built with a closer releases itself through it.
func (r *WatchResult) close() {
	var claimed []*WatchResult
	defer func() {
		for _, part := range claimed {
			part.joinedClose.Done()
		}
	}()

	leaves := r.openLeaves(nil, &claimed)

	unwatchLeaves(leaves)

	for index := len(leaves) - 1; index >= 0; index-- {
		leaf := leaves[index]
		if leaf.closeFunc != nil {
			leaf.closeFunc()
			continue
		}

		if leaf.client != nil {
			leaf.destroyGroups()
		}
	}
}

// openLeaves appends r's physical results to leaves in creation order. The
// caller has already claimed r; each nested part is claimed here through its
// own closeOnce, so a part that was closed earlier is skipped. Each claimed
// part is added to claimed with its joinedClose raised, and a later Close of
// it waits until the caller lowers it after the cleanup.
func (r *WatchResult) openLeaves(leaves []*WatchResult, claimed *[]*WatchResult) []*WatchResult {
	if len(r.parts) == 0 {
		return append(leaves, r)
	}

	for _, part := range r.parts {
		won := false
		part.closeOnce.Do(func() {
			part.joinedClose.Add(1)
			won = true
		})
		if won {
			*claimed = append(*claimed, part)
			leaves = part.openLeaves(leaves, claimed)
		}
	}
	return leaves
}

// unwatchBatch collects the watches of the results that share one batching
// client.
type unwatchBatch struct {
	unwatcher dcgmprovider.FieldGroupUnwatcher
	watches   []dcgmprovider.FieldGroupWatch
}

// unwatchLeaves removes the exact installed watches of every DCGM-built leaf.
// Leaves whose client implements dcgmprovider.FieldGroupUnwatcher are
// unwatched with one UnwatchFieldGroups call per client, so the client
// restores surviving watches once; other clients unwatch each binding. A field
// group may belong to a different scope than a nearby entity group, so a
// Cartesian cleanup would be invalid.
func unwatchLeaves(leaves []*WatchResult) {
	var batches []*unwatchBatch
	for _, leaf := range leaves {
		if leaf.closeFunc != nil || leaf.client == nil || len(leaf.bindings) == 0 {
			continue
		}

		unwatcher, ok := leaf.client.(dcgmprovider.FieldGroupUnwatcher)
		if !ok {
			leaf.unwatchEachBinding()
			continue
		}

		batch := batchFor(&batches, unwatcher)
		for _, binding := range leaf.bindings {
			batch.watches = append(batch.watches, dcgmprovider.FieldGroupWatch{FieldGroup: binding.FieldGroup, Group: binding.Group})
		}
	}

	for _, batch := range batches {
		logUnwatchFailures(batch.unwatcher.UnwatchFieldGroups(batch.watches))
	}
}

// batchFor returns the batch for unwatcher, adding a new one when no batch
// uses it yet. Batching clients are pointers, so they compare by identity.
func batchFor(batches *[]*unwatchBatch, unwatcher dcgmprovider.FieldGroupUnwatcher) *unwatchBatch {
	for _, batch := range *batches {
		if batch.unwatcher == unwatcher {
			return batch
		}
	}

	batch := &unwatchBatch{unwatcher: unwatcher}
	*batches = append(*batches, batch)
	return batch
}

// unwatchEachBinding removes r's watches one binding at a time, for a client
// that cannot unwatch in a batch, and logs each failure.
func (r *WatchResult) unwatchEachBinding() {
	for _, binding := range r.bindings {
		logUnwatchFailures(r.client.UnwatchFields(binding.FieldGroup, binding.Group))
	}
}

// logUnwatchFailures logs each failure in err, which may be joined, except
// errors DCGM returns after it has shut down before our cleanup and a
// *dcgmprovider.RestoreError: the client logs restore failures when they
// happen.
func logUnwatchFailures(err error) {
	for _, unwatchErr := range splitJoinedErrors(err) {
		var restoreErr *dcgmprovider.RestoreError
		if errors.As(unwatchErr, &restoreErr) {
			continue
		}

		errMsg := unwatchErr.Error()
		if !strings.Contains(errMsg, DCGM_ST_NOT_CONFIGURED) &&
			!strings.Contains(errMsg, DCGM_ST_FIELD_NOT_WATCHED) {
			slog.Warn("Failed to unwatch fields", slog.String(ErrorKey, errMsg))
		}
	}
}

// splitJoinedErrors returns the errors that errors.Join combined into err, or
// err alone when it is not a joined error. A nil err gives nil.
func splitJoinedErrors(err error) []error {
	if err == nil {
		return nil
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}

// destroyGroups runs after unwatching, while the creating client remains alive.
func (r *WatchResult) destroyGroups() {
	client := r.client

	// Destroy field groups, then groups: the reverse of creation order.
	for _, fieldGroup := range r.fieldGroups {
		if err := client.FieldGroupDestroy(fieldGroup); err != nil {
			if !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
				slog.Warn("Cannot destroy field group", slog.String(ErrorKey, err.Error()))
			}
		}
	}

	for _, group := range r.groups {
		if destroyErr := client.DestroyGroup(group); destroyErr != nil {
			if !strings.Contains(destroyErr.Error(), DCGM_ST_NOT_CONFIGURED) {
				slog.LogAttrs(
					context.Background(), slog.LevelWarn, "cannot destroy group",
					slog.Any(GroupIDKey, group),
					slog.String(ErrorKey, destroyErr.Error()),
				)
			}
		}
	}
}

// NewDeviceWatcher creates a watcher using the process's current client for
// compatibility callers. Runtime construction uses NewDeviceWatcherWithClient
// to make the creating client explicit.
func NewDeviceWatcher() *DeviceWatcher {
	return NewDeviceWatcherWithClient(dcgmprovider.Client())
}

// NewDeviceWatcherWithClient creates a watcher bound to client. The caller
// keeps client alive until all returned watch cleanup closures have completed.
func NewDeviceWatcherWithClient(client dcgmprovider.DCGM) *DeviceWatcher {
	return &DeviceWatcher{client: client}
}

// dcgmClient returns the captured client for production watchers. The fallback
// keeps zero-value watchers used by older unit tests compatible while runtime
// construction always supplies an explicit client.
func (d *DeviceWatcher) dcgmClient() dcgmprovider.DCGM {
	if d.client != nil {
		return d.client
	}
	return dcgmprovider.Client()
}

// GetDeviceFields resolves configured counters supported by entityType and
// groups the accepted fields by the extra scopes callers must watch and scrape.
func (d *DeviceWatcher) GetDeviceFields(counters []counters.Counter, entityType dcgm.Field_Entity_Group) ResolvedFields {
	var fields ResolvedFields
	var failedCount int
	for _, counter := range counters {
		fieldMeta, err := d.dcgmClient().FieldGetByID(counter.FieldID)
		if err != nil {
			failedCount++
			slog.Debug(
				"FieldGetByID failed; skipping field",
				slog.Any("field_id", counter.FieldID),
				slog.String(ErrorKey, err.Error()),
			)
			continue
		}

		if dcgm.FieldSupportsEntityGroup(counter.FieldID, fieldMeta.EntityLevel, entityType) ||
			shouldIncludeField(entityType, fieldMeta.EntityLevel) {
			fields.Fields = append(fields.Fields, counter.FieldID)
			fields.recordEntityLevel(counter.FieldID, fieldMeta.EntityLevel)
		}
	}

	if failedCount > 0 {
		slog.Warn(
			"Some fields were skipped because FieldGetByID failed",
			slog.Int("failed_count", failedCount),
			slog.Int("total_count", len(counters)),
			slog.Any("entity_type", entityType),
		)
	}

	return fields
}

// recordEntityLevel adds an accepted field to the subsets its declared entity
// level calls for: compute-instance fields, fields DCGM also stores on parent
// GPUs, or GPU-level fields.
func (r *ResolvedFields) recordEntityLevel(fieldID dcgm.Short, level dcgm.Field_Entity_Group) {
	switch level {
	case dcgm.FE_GPU_CI:
		r.ComputeInstanceFields = append(r.ComputeInstanceFields, fieldID)
		if usesMultipleEntityScopes(fieldID) {
			r.FieldsAtMultipleScopes = append(r.FieldsAtMultipleScopes, fieldID)
		}
	case dcgm.FE_GPU:
		r.GPULevelFields = append(r.GPULevelFields, fieldID)
	}
}

// usesMultipleEntityScopes identifies fields whose values DCGM stores at more
// than the field metadata's entity level.
func usesMultipleEntityScopes(fieldID dcgm.Short) bool {
	return fieldID == dcgm.DCGM_FI_DEV_XID_ERRORS
}

// shouldIncludeField reports whether field metadata makes a field valid for entityType.
func shouldIncludeField(entityType, fieldLevel dcgm.Field_Entity_Group) bool {
	if fieldLevel == entityType || fieldLevel == dcgm.FE_NONE {
		return true
	}

	switch entityType {
	case dcgm.FE_GPU:
		return fieldLevel == dcgm.FE_GPU_CI || fieldLevel == dcgm.FE_GPU_I || fieldLevel == dcgm.FE_VGPU
	case dcgm.FE_CPU:
		return fieldLevel == dcgm.FE_CPU_CORE
	default:
		return false
	}
}

// Watch creates all groups, field groups, and exact watch pairs for one named
// scope. It rolls back its own partial work on failure, leaving callers with
// either one complete result or no resources to clean up.
func (d *DeviceWatcher) Watch(
	fieldWatchGroups []FieldWatchGroup,
	deviceInfo deviceinfo.Provider,
	scope WatchScope,
) (*WatchResult, error) {
	resources := &WatchResult{client: d.dcgmClient()}

	groups, err := d.createGroups(deviceInfo, scope)
	resources.groups = groups
	if err != nil {
		resources.Close()
		return nil, err
	}
	if len(resources.groups) == 0 {
		return resources, nil
	}

	for _, fieldWatchGroup := range fieldWatchGroups {
		if err := d.installFieldWatchGroup(resources, fieldWatchGroup, deviceInfo); err != nil {
			resources.Close()
			return nil, err
		}
	}

	return resources, nil
}

// ExplainWatchFailure adds a plain explanation to err when the hostengine
// refused some fields of groups. It uses this watcher's own DCGM client and
// returns err unchanged in every other case.
func (d *DeviceWatcher) ExplainWatchFailure(groups []FieldWatchGroup, err error) error {
	return ExplainRefusedFields(d.dcgmClient(), groups, err)
}

// installFieldWatchGroup records created field groups and installed watches.
// Watch cleans up all recorded resources if setup fails.
func (d *DeviceWatcher) installFieldWatchGroup(resources *WatchResult, fieldWatchGroup FieldWatchGroup, deviceInfo deviceinfo.Provider) error {
	fields := dedupeFields(fieldWatchGroup.Fields)
	if len(fields) == 0 {
		return nil
	}

	keepAge, keepSamples := retentionWithCompatibilityDefault(
		fieldWatchGroup.MaxKeepAge,
		fieldWatchGroup.MaxKeepSamples,
	)
	updateFreqInUsec := fieldWatchGroup.IntervalMSec * 1000
	fieldChunks := chunkFields(fields, maxFieldIDsPerFieldGroup)
	chunkCount := len(fieldChunks)
	fieldOffset := 0
	for chunkIndex, chunk := range fieldChunks {
		logSplitFieldWatchGroup(fieldWatchGroup.Name, len(fields), chunkIndex+1, chunkCount, len(chunk))

		fieldGroup, createErr := newFieldGroupWithClient(d.dcgmClient(), chunk)
		if createErr != nil {
			return newFieldGroupCreateError(
				fieldWatchGroup.Name,
				chunkIndex+1,
				chunkCount,
				fieldOffset+1,
				fieldOffset+len(chunk),
				createErr,
			)
		}
		resources.fieldGroups = append(resources.fieldGroups, fieldGroup)
		fieldOffset += len(chunk)

		for _, group := range resources.groups {
			logWatchFieldsCall(d.dcgmClient(), deviceInfo, group, fieldGroup, chunk)
			watchErr := watchFieldGroup(d.dcgmClient(), group, fieldGroup, updateFreqInUsec, keepAge, keepSamples)
			if watchErr != nil {
				logWatchFieldsFailure(d.dcgmClient(), deviceInfo, group, fieldGroup, chunk, watchErr)
				return fmt.Errorf(
					"watch fields for logical group %q chunk %d/%d (group_handle=%d, field_group_handle=%d): %w",
					fieldWatchGroup.Name,
					chunkIndex+1,
					chunkCount,
					group.GetHandle(),
					fieldGroup.GetHandle(),
					watchErr,
				)
			}
			resources.bindings = append(resources.bindings, WatchBinding{Group: group, FieldGroup: fieldGroup})
		}
	}
	return nil
}

// createGroups resolves the group construction strategy for a device type and
// scope. Link and CPU-core groups use their own topology rules; other entities
// use the named scope to decide which GPU entities belong in the group.
func (d *DeviceWatcher) createGroups(
	deviceInfo deviceinfo.Provider,
	scope WatchScope,
) ([]dcgm.GroupHandle, error) {
	switch deviceInfo.InfoType() {
	case dcgm.FE_LINK:
		return d.createNVLinkGroups(deviceInfo)
	case dcgm.FE_CPU_CORE:
		return d.createCPUCoreGroups(deviceInfo)
	default:
		group, err := d.createGenericGroup(deviceInfo, scope)
		if err != nil {
			return nil, err
		}
		if group == nil {
			return nil, nil
		}
		return []dcgm.GroupHandle{*group}, nil
	}
}

// createGenericGroup creates a group for the named scope's selected entities.
func (d *DeviceWatcher) createGenericGroup(
	deviceInfo deviceinfo.Provider,
	scope WatchScope,
) (*dcgm.GroupHandle, error) {
	monitoringInfo, err := EntitiesForScope(deviceInfo, scope)
	if err != nil {
		return nil, err
	}
	if len(monitoringInfo) == 0 {
		return nil, nil
	}

	groupID, err := createGroupWithClient(d.dcgmClient())
	if err != nil {
		return nil, err
	}

	for _, mi := range monitoringInfo {
		if err := d.dcgmClient().AddEntityToGroup(groupID, mi.Entity.EntityGroupId, mi.Entity.EntityId); err != nil {
			if destroyErr := d.dcgmClient().DestroyGroup(groupID); destroyErr != nil {
				slog.Warn("Cannot destroy group after failed entity setup", slog.String(ErrorKey, destroyErr.Error()))
			}
			return nil, err
		}
	}

	return &groupID, nil
}

// EntitiesForScope gives watches and reads the same device selection.
func EntitiesForScope(deviceInfo deviceinfo.Provider, scope WatchScope) ([]devicemonitoring.Info, error) {
	switch scope {
	case WatchConfiguredEntities:
		return devicemonitoring.GetMonitoredEntities(deviceInfo), nil
	case WatchComputeInstanceEntities:
		return devicemonitoring.GetMonitoredEntitiesForComputeInstanceFields(deviceInfo), nil
	case WatchUnselectedParentGPUs:
		return devicemonitoring.GetParentGPUsForMonitoredGPUInstances(deviceInfo), nil
	case WatchAllGPUEntities:
		return devicemonitoring.GetMonitoredEntitiesAtAllGPUScopes(deviceInfo), nil
	case WatchPhysicalGPUs:
		return devicemonitoring.GetPhysicalGPUs(deviceInfo), nil
	case WatchSelectedGPUs:
		return selectedEntitiesOfType(deviceInfo, dcgm.FE_GPU), nil
	case WatchGPUInstances:
		return selectedEntitiesOfType(deviceInfo, dcgm.FE_GPU_I), nil
	case WatchOnlyComputeInstances:
		return selectedEntitiesOfType(deviceInfo, dcgm.FE_GPU_CI), nil
	default:
		return nil, fmt.Errorf("unsupported watch scope %d", scope)
	}
}

// selectedEntitiesOfType narrows the monitored selection to one GPU, GI, or CI entity type.
func selectedEntitiesOfType(deviceInfo deviceinfo.Provider, entityType dcgm.Field_Entity_Group) []devicemonitoring.Info {
	var selected []devicemonitoring.Info
	for _, entity := range devicemonitoring.GetMonitoredEntitiesIncludingComputeInstances(deviceInfo) {
		if entity.Entity.EntityGroupId == entityType {
			selected = append(selected, entity)
		}
	}
	return selected
}

// entityGroupFiller hands out the group for each next entity of a scope and
// creates a new group only when the current one has been handed out
// dcgm.DCGM_GROUP_MAX_ENTITIES times, so a scope uses as few groups as the
// limit allows. It creates groups through result's client and records them in
// result, which owns their cleanup; callers add entities through the same
// client.
type entityGroupFiller struct {
	result  *WatchResult
	current dcgm.GroupHandle
	count   int
}

// nextGroup returns the group for the next entity, creating one when the
// current group is full or none exists yet. An entity DCGM later refuses still
// counts toward the limit.
func (f *entityGroupFiller) nextGroup() (dcgm.GroupHandle, error) {
	if f.count%dcgm.DCGM_GROUP_MAX_ENTITIES == 0 {
		group, err := createGroupWithClient(f.result.client)
		if err != nil {
			return dcgm.GroupHandle{}, err
		}

		f.current = group
		f.result.groups = append(f.result.groups, group)
	}

	f.count++
	return f.current, nil
}

// createCPUCoreGroups puts the watched cores of every watched CPU into shared
// groups, each filled up to the DCGM group limit.
func (d *DeviceWatcher) createCPUCoreGroups(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, error) {
	result := &WatchResult{client: d.dcgmClient()}
	filler := &entityGroupFiller{result: result}

	for _, cpu := range deviceInfo.CPUs() {
		if !deviceInfo.IsCPUWatched(cpu.EntityId) {
			continue
		}

		if err := d.addCPUCores(filler, cpu, deviceInfo); err != nil {
			result.Close()
			return nil, err
		}
	}
	return result.groups, nil
}

// addCPUCores adds one CPU's watched cores to the groups filler hands out;
// the caller owns failure cleanup.
func (d *DeviceWatcher) addCPUCores(
	filler *entityGroupFiller, cpu deviceinfo.CPUInfo, deviceInfo deviceinfo.Provider,
) error {
	client := filler.result.client

	for _, core := range cpu.Cores {
		if !deviceInfo.IsCoreWatched(core, cpu.EntityId) {
			continue
		}

		groupID, err := filler.nextGroup()
		if err != nil {
			return err
		}

		if err := client.AddEntityToGroup(groupID, dcgm.FE_CPU_CORE, core); err != nil {
			return err
		}
	}
	return nil
}

// createNVLinkGroups puts the links of all GPUs into shared groups and the
// links of all watched switches into separate shared groups, each filled up
// to the DCGM group limit.
func (d *DeviceWatcher) createNVLinkGroups(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, error) {
	result := &WatchResult{client: d.dcgmClient()}

	gpuLinks := &entityGroupFiller{result: result}
	for _, gpu := range deviceInfo.GPUs() {
		if gpu.Status == dcgm.EntityStatusDetached {
			continue
		}

		if err := d.addGPUNVLinks(gpuLinks, gpu); err != nil {
			result.Close()
			return nil, err
		}
	}

	switchLinks := &entityGroupFiller{result: result}
	for _, sw := range deviceInfo.Switches() {
		if !deviceInfo.IsSwitchWatched(sw.EntityId) {
			continue
		}

		if err := d.addSwitchNVLinks(switchLinks, deviceInfo, sw); err != nil {
			result.Close()
			return nil, err
		}
	}
	return result.groups, nil
}

// addGPUNVLinks adds the GPU's supported links to the groups filler hands
// out. Links DCGM refuses are logged and skipped; only group-creation errors
// are returned.
func (d *DeviceWatcher) addGPUNVLinks(filler *entityGroupFiller, gpu deviceinfo.GPUInfo) error {
	client := filler.result.client

	for _, link := range gpu.NvLinks {
		if link.State == dcgm.LS_NOT_SUPPORTED {
			continue
		}

		groupID, err := filler.nextGroup()
		if err != nil {
			return err
		}

		if err := client.AddLinkEntityToGroup(groupID, link.Index, dcgm.FE_GPU, gpu.DeviceInfo.GPU); err != nil {
			slog.Warn(fmt.Sprintf("could not add link %d on GPU %d to group %d: %s", link.Index, gpu.DeviceInfo.GPU, groupID, err))
		}
	}
	return nil
}

// addSwitchNVLinks adds the switch's watched, up links to the groups filler
// hands out. Links DCGM refuses are logged and skipped; only group-creation
// errors are returned.
func (d *DeviceWatcher) addSwitchNVLinks(
	filler *entityGroupFiller, deviceInfo deviceinfo.Provider, sw deviceinfo.SwitchInfo,
) error {
	client := filler.result.client

	for _, link := range sw.NvLinks {
		if link.State != dcgm.LS_UP || !deviceInfo.IsLinkWatched(link.Index, sw.EntityId) {
			continue
		}

		groupID, err := filler.nextGroup()
		if err != nil {
			return err
		}

		if err := client.AddLinkEntityToGroup(groupID, link.Index, dcgm.FE_SWITCH, link.ParentId); err != nil {
			slog.Warn(fmt.Sprintf("could not add link %d on NvSwitch %d to group %d: %s", link.Index, link.ParentId, groupID, err))
		}
	}
	return nil
}

// newFieldGroupWithClient creates one deduplicated field group through the
// client that will later destroy it as part of the containing WatchResult.
func newFieldGroupWithClient(client dcgmprovider.DCGM, deviceFields []dcgm.Short) (dcgm.FieldHandle, error) {
	newFieldGroupNumber, err := utils.RandUint64()
	if err != nil {
		return dcgm.FieldHandle{}, err
	}
	return client.FieldGroupCreate(
		fmt.Sprintf("gpu-collector-fieldgroup-%d", newFieldGroupNumber),
		dedupeFields(deviceFields),
	)
}

// dedupeFields removes repeated field IDs while preserving their first-seen order.
func dedupeFields(fields []dcgm.Short) []dcgm.Short {
	if len(fields) == 0 {
		return nil
	}

	seen := make(map[dcgm.Short]struct{}, len(fields))
	deduped := make([]dcgm.Short, 0, len(fields))
	for _, field := range fields {
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		deduped = append(deduped, field)
	}

	return deduped
}

// logWatchFieldsCall records the exact watch call and resolves field names
// through the same client that will own the created handles.
func logWatchFieldsCall(
	client dcgmprovider.DCGM,
	deviceInfo deviceinfo.Provider,
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
	fieldIDs []dcgm.Short,
) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}

	slog.LogAttrs(
		context.Background(),
		slog.LevelDebug,
		"Watching DCGM fields",
		watchLogAttrs(client, deviceInfo, group, fieldGroup, fieldIDs)...,
	)
}

// logWatchFieldsFailure records a failed watch call with the creating client's
// field metadata, keeping diagnostics in the same generation as the failure.
func logWatchFieldsFailure(
	client dcgmprovider.DCGM,
	deviceInfo deviceinfo.Provider,
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
	fieldIDs []dcgm.Short,
	err error,
) {
	attrs := append(
		watchLogAttrs(client, deviceInfo, group, fieldGroup, fieldIDs),
		slog.String(ErrorKey, err.Error()),
	)
	slog.LogAttrs(context.Background(), slog.LevelError, "Failed to watch DCGM fields", attrs...)
}

// watchLogAttrs builds structured watch diagnostics from one captured client.
func watchLogAttrs(
	client dcgmprovider.DCGM,
	deviceInfo deviceinfo.Provider,
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
	fieldIDs []dcgm.Short,
) []slog.Attr {
	options := deviceOptionsForInfoType(deviceInfo)
	return []slog.Attr{
		slog.String("entity_type", deviceInfo.InfoType().String()),
		slog.Any(GroupIDKey, group),
		slog.Any("field_group", fieldGroup),
		slog.Any("field_ids", fieldIDs),
		slog.Any("field_names", fieldNames(client, fieldIDs)),
		slog.String("device_option_mode", deviceOptionMode(options)),
		slog.Any("device_options", options),
	}
}

// fieldNames resolves field IDs through the client that owns the active watch
// so diagnostics do not accidentally consult a replacement DCGM session.
func fieldNames(client dcgmprovider.DCGM, fieldIDs []dcgm.Short) []string {
	names := make([]string, 0, len(fieldIDs))
	if client == nil {
		return names
	}

	for _, fieldID := range fieldIDs {
		meta, err := client.FieldGetByID(fieldID)
		if err == nil && meta.Tag != "" {
			names = append(names, meta.Tag)
		}
	}

	return names
}

// deviceOptionsForInfoType returns the device-selection options for the provider's entity type.
func deviceOptionsForInfoType(deviceInfo deviceinfo.Provider) appconfig.DeviceOptions {
	switch deviceInfo.InfoType() {
	case dcgm.FE_SWITCH, dcgm.FE_LINK:
		return deviceInfo.SOpts()
	case dcgm.FE_CPU, dcgm.FE_CPU_CORE:
		return deviceInfo.COpts()
	default:
		return deviceInfo.GOpts()
	}
}

// deviceOptionMode maps device options to the short mode recorded in watch diagnostics.
func deviceOptionMode(options appconfig.DeviceOptions) string {
	switch {
	case options.Flex:
		return "f"
	case len(options.MajorRange) > 0:
		return "g"
	case len(options.MinorRange) > 0:
		return "i"
	default:
		return ""
	}
}

// watchFieldGroup installs one resolved field-group policy with DCGM. Watch
// calls it for each exact group and field-group pair it installs. See
// dcgmprovider.DCGM.WatchFieldsWithGroupExNoRefresh for what DCGM samples
// when the watch starts.
func watchFieldGroup(
	client dcgmprovider.DCGM,
	group dcgm.GroupHandle,
	field dcgm.FieldHandle,
	updateFreq int64,
	keepAge float64,
	keepSamples int32,
) error {
	return client.WatchFieldsWithGroupExNoRefresh(field, group, updateFreq, keepAge, keepSamples)
}

// retentionWithCompatibilityDefault preserves the policy used before FieldWatchGroup carried retention fields.
// Runtime configuration rejects an explicit 0/0 policy before watch groups reach this package.
func retentionWithCompatibilityDefault(keepAge float64, keepSamples int32) (float64, int32) {
	if keepAge == 0 && keepSamples == 0 {
		return maxKeepAge, maxKeepSamples
	}
	return keepAge, keepSamples
}

// createGroupWithClient creates one group with the client that owns its
// containing WatchResult. The result later destroys the handle on that client.
func createGroupWithClient(client dcgmprovider.DCGM) (dcgm.GroupHandle, error) {
	newGroupNumber, err := utils.RandUint64()
	if err != nil {
		return dcgm.GroupHandle{}, err
	}

	groupID, err := client.CreateGroup(fmt.Sprintf("gpu-collector-group-%d", newGroupNumber))
	if err != nil {
		return dcgm.GroupHandle{}, err
	}
	return groupID, nil
}
