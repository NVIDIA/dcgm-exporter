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

// Package transformation: bare_metal_user_mapper.go implements the
// DCGM_FI_DEV_GPU_UTIL multi-user attribution transformer for feature
// 001-multi-user-gpu-util.
//
// Feature scope (all guarded by the transformer being non-nil in the pipeline;
// the pipeline is wired only when a config.yaml loaded successfully):
//
//   - T016: UID -> username resolver with TTL cache
//   - T017: env value sanitizer
//   - T018: mapper struct + constructor
//   - T019: Process() single-process branch (US1)
//   - T020: Process() idle-GPU branch (US1)
//   - US2 split logic is added in tasks T024-T028 below.

package transformation

import (
	"fmt"
	"log/slog"
	"os/user"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

// GpuUtilFieldName is the Prometheus-facing name of the single counter this
// transformer rewrites. Copied as a constant to avoid depending on the
// counters package layout for such a tiny lookup.
const GpuUtilFieldName = "DCGM_FI_DEV_GPU_UTIL"

// --------------------------------------------------------------------------
// T016: UID -> username resolver with a TTL cache.
// --------------------------------------------------------------------------

type userCacheEntry struct {
	name      string
	expiresAt time.Time
}

// userCache memoises os/user.LookupId lookups. Zero value is ready-to-use;
// TTL is read from appconfig.UserNameCacheTTL.
type userCache struct {
	m sync.Map // key: uint32 uid -> *userCacheEntry
}

func (c *userCache) resolveUsername(uid uint32) string {
	if v, ok := c.m.Load(uid); ok {
		ent := v.(*userCacheEntry)
		if time.Now().Before(ent.expiresAt) {
			return ent.name
		}
	}
	name := lookupUsername(uid)
	c.m.Store(uid, &userCacheEntry{name: name, expiresAt: time.Now().Add(appconfig.UserNameCacheTTL)})
	return name
}

// lookupUsername is a package-level var so tests can swap in a fake resolver.
// By default it calls os/user.LookupId and falls back to "uid:<n>" on error.
var lookupUsername = func(uid uint32) string {
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil || u == nil || u.Username == "" {
		return fmt.Sprintf("uid:%d", uid)
	}
	return u.Username
}

// --------------------------------------------------------------------------
// T017: env value sanitizer.
// --------------------------------------------------------------------------

// sanitizeEnvValue applies the label-value sanitation contract from FR-004:
//
//   - empty input  -> appconfig.FallbackNone ("none")
//   - invalid runes (outside [A-Za-z0-9_.-]) -> replaced with '_'
//   - over-length values truncated to appconfig.EnvValueMaxLen bytes, then
//     trimmed to the last UTF-8 boundary so we never emit a half-rune.
//
// Note: the per-cycle cardinality cap (128 distinct values per env label) is
// NOT applied here — that rewrite happens after all PIDs are collected, in
// capEnvCardinality (US2 / T025).
func sanitizeEnvValue(raw string) string {
	if raw == "" {
		return appconfig.FallbackNone
	}
	// Replace disallowed runes.
	var b []byte
	b = make([]byte, 0, len(raw))
	for _, r := range raw {
		if isAllowedLabelChar(r) {
			b = utf8.AppendRune(b, r)
		} else {
			b = append(b, '_')
		}
	}
	// Truncate to the max byte length, trimming any dangling partial rune.
	if len(b) > appconfig.EnvValueMaxLen {
		b = b[:appconfig.EnvValueMaxLen]
		for len(b) > 0 {
			if r, _ := utf8.DecodeLastRune(b); r != utf8.RuneError {
				break
			}
			b = b[:len(b)-1]
		}
	}
	if len(b) == 0 {
		return appconfig.FallbackNone
	}
	return string(b)
}

func isAllowedLabelChar(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z',
		r >= 'a' && r <= 'z',
		r >= '0' && r <= '9':
		return true
	case r == '_' || r == '.' || r == '-':
		return true
	}
	return false
}

// --------------------------------------------------------------------------
// T018: the mapper struct + constructor.
// --------------------------------------------------------------------------

// bareMetalUserMapper is a Transform that decorates DCGM_FI_DEV_GPU_UTIL
// samples with a USER label + all static/env labels declared in config.yaml.
// All other counters are passed through untouched.
//
// The constructor is exported (NewBareMetalUserMapper) so pkg/cmd can wire it
// up, but the struct itself stays unexported since consumers interact only
// through the Transform interface.
type bareMetalUserMapper struct {
	cfg       *appconfig.Config
	nvml      nvmlprovider.NVML
	procFS    ProcFS
	userCache *userCache
}

// NewBareMetalUserMapper constructs the transformer. cfg.Labels must be fully
// populated (ApplyDefaults + Validate have run). When nvml is nil, the
// provider defaults to the singleton from nvmlprovider.Client(). When procFS
// is nil, it defaults to NewProcFS() rooted at /proc.
func NewBareMetalUserMapper(cfg *appconfig.Config, nvml nvmlprovider.NVML, procFS ProcFS) Transform {
	if procFS == nil {
		procFS = NewProcFS()
	}
	return &bareMetalUserMapper{
		cfg:       cfg,
		nvml:      nvml,
		procFS:    procFS,
		userCache: &userCache{},
	}
}

func (m *bareMetalUserMapper) Name() string { return "bareMetalUserMapper" }

// getNVML returns the NVML handle, lazily initialising from the singleton
// provider so tests can inject their own.
func (m *bareMetalUserMapper) getNVML() nvmlprovider.NVML {
	if m.nvml != nil {
		return m.nvml
	}
	return nvmlprovider.Client()
}

// --------------------------------------------------------------------------
// T019 + T020: Process() single-process + idle-GPU branches (US1).
//
// US2 (T024-T028) extends this with weighted splitting; for now we handle
// only two shapes per GPU:
//
//    0 processes -> emit one placeholder (USER=none, env labels=none)
//    N processes -> we group by (username, env-values tuple) but do not yet
//                   split: the first group wins and we attribute the entire
//                   util value to it. That is correct for the P1 MVP
//                   (single-process GPUs) and deterministic for multi-process
//                   GPUs pending T027.
//
// T027 replaces the "first group wins" behaviour with weighted splitting.
// --------------------------------------------------------------------------

func (m *bareMetalUserMapper) Process(metrics collector.MetricsByCounter, _ deviceinfo.Provider) error {
	if m == nil || m.cfg == nil {
		return nil // defensive: uninitialised mapper is a no-op
	}

	// Cache NVML process lists per GPU UUID across the counters loop.
	// All counters share the same GPUs; querying NVML once per GPU is enough.
	pidsByGPU := map[string][]uint32{}
	getPIDs := func(gpuUUID string) []uint32 {
		if pids, ok := pidsByGPU[gpuUUID]; ok {
			return pids
		}
		pids := m.listGPUProcesses(gpuUUID)
		pidsByGPU[gpuUUID] = pids
		return pids
	}

	for counter := range metrics {
		if counter.FieldName != GpuUtilFieldName {
			// FR-001: all non-UTIL counters pass through byte-identical.
			continue
		}
		newList := make([]collector.Metric, 0, len(metrics[counter]))
		for _, metric := range metrics[counter] {
			pids := getPIDs(metric.GPUUUID)
			if len(pids) == 0 {
				// T020 idle-GPU branch.
				newList = append(newList, m.decorateIdle(metric))
				continue
			}
			groups := m.buildGroups(pids)
			// US1 interim: attribute the full util to the first group. US2
			// (T027) replaces this with weighted splitting.
			newList = append(newList, m.decorateSingleGroup(metric, groups[0]))
		}
		metrics[counter] = newList
	}
	return nil
}

// listGPUProcesses returns the list of compute PIDs on gpuUUID. Failures are
// logged but not propagated: per FR-016, a single GPU's NVML hiccup must not
// abort the whole cycle.
func (m *bareMetalUserMapper) listGPUProcesses(gpuUUID string) []uint32 {
	nvml := m.getNVML()
	if nvml == nil {
		return nil
	}
	mem, err := nvml.GetDeviceProcessMemory(gpuUUID)
	if err != nil {
		slog.Warn("bareMetalUserMapper: list processes failed",
			slog.String("gpu_uuid", gpuUUID),
			slog.String("error", err.Error()))
		return nil
	}
	pids := make([]uint32, 0, len(mem))
	for pid := range mem {
		pids = append(pids, pid)
	}
	return pids
}

// groupKey uniquely identifies an (USER, env-values) combination on a GPU.
// EnvVals is parallel-indexed with cfg.Labels.Env.
type groupKey struct {
	Username string
	EnvVals  []string
}

type gpuProcessGroup struct {
	Key        groupKey
	ProcessCnt uint
	Weight     float64 // filled by US2/T027 when splitting.
}

// buildGroups walks every PID, resolves (USER, env-values) and aggregates
// into stable-sorted groups. For US1 we only need the deterministic "first"
// group (groups[0]) which the caller uses. US2 rewrites the caller.
func (m *bareMetalUserMapper) buildGroups(pids []uint32) []gpuProcessGroup {
	grouped := map[string]*gpuProcessGroup{} // keyed by groupKey.String()
	order := []string{}                      // insertion order for stability

	for _, pid := range pids {
		uid, err := m.procFS.ReadStatus(pid)
		if err != nil {
			slog.Debug("bareMetalUserMapper: read status failed, skipping pid",
				slog.Uint64("pid", uint64(pid)), slog.String("error", err.Error()))
			continue
		}
		username := m.userCache.resolveUsername(uid)
		envVals := make([]string, len(m.cfg.Labels.Env))
		for j, el := range m.cfg.Labels.Env {
			raw, err := m.procFS.ReadEnviron(pid, el.EnvVar)
			if err != nil {
				// Read failure is not fatal for this PID; fall back to none.
				envVals[j] = appconfig.FallbackNone
				continue
			}
			envVals[j] = sanitizeEnvValue(raw)
		}
		key := groupKey{Username: username, EnvVals: envVals}
		skey := key.canonical()
		g, ok := grouped[skey]
		if !ok {
			g = &gpuProcessGroup{Key: key}
			grouped[skey] = g
			order = append(order, skey)
		}
		g.ProcessCnt++
	}
	result := make([]gpuProcessGroup, 0, len(order))
	for _, skey := range order {
		result = append(result, *grouped[skey])
	}
	return result
}

// canonical renders a stable string for map keying and sort order.
func (k groupKey) canonical() string {
	// 0x1f is ASCII Unit Separator, guaranteed not to appear in sanitized values.
	b := make([]byte, 0, len(k.Username)+len(k.EnvVals)*8)
	b = append(b, k.Username...)
	for _, v := range k.EnvVals {
		b = append(b, 0x1f)
		b = append(b, v...)
	}
	return string(b)
}

// --------------------------------------------------------------------------
// Metric decoration helpers.
// --------------------------------------------------------------------------

// decorateIdle rewrites the Labels on an idle-GPU metric per FR-005 / T020.
// Value is forced to "0" so we never leak a stale util from upstream.
func (m *bareMetalUserMapper) decorateIdle(in collector.Metric) collector.Metric {
	cp, err := utils.DeepCopy(in)
	if err != nil {
		// Fallback: mutate in place rather than fail the whole cycle.
		cp = in
	}
	if cp.Labels == nil {
		cp.Labels = map[string]string{}
	}
	cp.Labels["USER"] = appconfig.FallbackNone
	for _, s := range m.cfg.Labels.Static {
		cp.Labels[s.Name] = s.ResolvedValue
	}
	for _, e := range m.cfg.Labels.Env {
		cp.Labels[e.Name] = appconfig.FallbackNone
	}
	cp.Value = "0"
	return cp
}

// decorateSingleGroup attributes an entire metric value to one group (the
// US1 interim behaviour). US2 / T027 replaces callers of this with the
// weighted splitter.
func (m *bareMetalUserMapper) decorateSingleGroup(in collector.Metric, g gpuProcessGroup) collector.Metric {
	cp, err := utils.DeepCopy(in)
	if err != nil {
		cp = in
	}
	if cp.Labels == nil {
		cp.Labels = map[string]string{}
	}
	cp.Labels["USER"] = g.Key.Username
	for _, s := range m.cfg.Labels.Static {
		cp.Labels[s.Name] = s.ResolvedValue
	}
	for j, e := range m.cfg.Labels.Env {
		cp.Labels[e.Name] = g.Key.EnvVals[j]
	}
	return cp
}
