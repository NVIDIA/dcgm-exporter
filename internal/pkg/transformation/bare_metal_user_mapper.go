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
// Task coverage:
//   - T016: UID -> username resolver with TTL cache
//   - T017: env value sanitizer
//   - T018: mapper struct + constructor
//   - T019: Process() single-process branch (US1)
//   - T020: Process() idle-GPU branch (US1)
//   - T024: group key + weighted group type
//   - T025: per-cycle env cardinality cap helper (capEnvCardinality)
//   - T026: weighted split algorithm with closure compensation (splitUtil)
//   - T027: Process() multi-process branch = split + DeepCopy (US2)
//   - T028: "sum equals total" invariant guard

package transformation

import (
	"fmt"
	"log/slog"
	"math"
	"os/user"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
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

// debugInvariants toggles the US2 "sum equals total" invariant check. Left
// on for the first release: the cost is tiny and silent drift is far worse
// than a visible error log + self-monitoring counter increment.
const debugInvariants = true

// InvariantBreaches is incremented every time the weighted split failed to
// sum exactly to the original UTIL value. Exposed as a package-level atomic
// so the Prometheus self-monitoring counter registered in T037
// (`dcgm_exporter_bare_metal_mapper_invariant_breaches_total`) can read it.
//
// We intentionally avoid publishing `USER="invariant_error"` business labels
// (see specs/001-multi-user-gpu-util/contracts/metrics.contract.md §标签稳定性).
var InvariantBreaches atomic.Uint64

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
	b := make([]byte, 0, len(raw))
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
// T024: group key + weighted group type (shared by US1 and US2).
// --------------------------------------------------------------------------

// groupKey uniquely identifies an (USER, env-values) combination on a GPU.
// EnvVals is parallel-indexed with cfg.Labels.Env.
type groupKey struct {
	Username string
	EnvVals  []string
}

// canonical renders a stable string for map keying and sort order.
// 0x1f is ASCII Unit Separator, guaranteed not to appear in sanitized values.
func (k groupKey) canonical() string {
	b := make([]byte, 0, len(k.Username)+len(k.EnvVals)*8)
	b = append(b, k.Username...)
	for _, v := range k.EnvVals {
		b = append(b, 0x1f)
		b = append(b, v...)
	}
	return string(b)
}

type gpuProcessGroup struct {
	Key        groupKey
	ProcessCnt uint
	Weight     float64 // = ProcessCnt / total processes on the GPU
}

// --------------------------------------------------------------------------
// Process() — the only public entry point.
// --------------------------------------------------------------------------

func (m *bareMetalUserMapper) Process(metrics collector.MetricsByCounter, _ deviceinfo.Provider) error {
	if m == nil || m.cfg == nil {
		return nil
	}

	// Cache NVML process lists per GPU UUID across the counters loop.
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
			groups := m.buildAndCapGroups(pids)

			// Parse the original util value; if it's malformed we fall back to 0
			// and still emit one record per group so timeseries continuity is preserved.
			totalUtil, perr := strconv.Atoi(metric.Value)
			if perr != nil {
				slog.Warn("bareMetalUserMapper: non-integer DCGM_FI_DEV_GPU_UTIL value",
					slog.String("gpu_uuid", metric.GPUUUID),
					slog.String("value", metric.Value))
				totalUtil = 0
			}
			splits := splitUtil(totalUtil, groups)

			// T028: invariant guard. Rounding is closed by splitUtil already,
			// so sum must equal total exactly; log + bump the self-health
			// counter on any breach.
			if debugInvariants {
				sum := 0
				for _, v := range splits {
					sum += v
				}
				if sum != totalUtil {
					InvariantBreaches.Add(1)
					slog.Error("bareMetalUserMapper: invariant breach (sum != util_total)",
						slog.String("gpu_uuid", metric.GPUUUID),
						slog.Int("util_total", totalUtil),
						slog.Int("split_sum", sum),
						slog.Int("group_count", len(groups)))
				}
			}

			for i, g := range groups {
				cp, err := utils.DeepCopy(metric)
				if err != nil {
					slog.Warn("bareMetalUserMapper: DeepCopy failed, emitting in-place copy",
						slog.String("error", err.Error()))
					cp = metric
				}
				m.applyLabels(&cp, g.Key)
				cp.Value = strconv.Itoa(splits[i])
				newList = append(newList, cp)
			}
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

// buildAndCapGroups walks every PID, resolves (USER, env-values), groups by
// the canonical key, applies per-cycle env-cardinality capping (T025), and
// returns the resulting groups in stable (canonical) order with Weight set.
func (m *bareMetalUserMapper) buildAndCapGroups(pids []uint32) []gpuProcessGroup {
	groups := m.buildGroups(pids)
	groups = capEnvCardinality(groups, len(m.cfg.Labels.Env), appconfig.MaxEnvCardinalityPerCycle)

	// Set Weight + ensure deterministic order for split compensation (T026).
	var total uint
	for _, g := range groups {
		total += g.ProcessCnt
	}
	for i := range groups {
		if total == 0 {
			groups[i].Weight = 0
		} else {
			groups[i].Weight = float64(groups[i].ProcessCnt) / float64(total)
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Key.canonical() < groups[j].Key.canonical()
	})
	return groups
}

// buildGroups aggregates PIDs into (USER, env-values) groups. PID read
// failures are skipped (FR-016); successful PIDs are accumulated.
func (m *bareMetalUserMapper) buildGroups(pids []uint32) []gpuProcessGroup {
	grouped := map[string]*gpuProcessGroup{}
	order := []string{}

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

// --------------------------------------------------------------------------
// T025: per-cycle env cardinality cap (Clarification Q5).
//
// For each env label index i: collect the set of distinct values across all
// groups. If the set size > max, keep the lexicographically smallest `max`
// values and rewrite the i-th component of every remaining group's key to
// appconfig.FallbackOther. After rewriting, groups whose canonical keys
// collapse into the same string are merged (ProcessCnt summed). Insertion
// order of the survivors is preserved.
// --------------------------------------------------------------------------

func capEnvCardinality(groups []gpuProcessGroup, envLabelCount, max int) []gpuProcessGroup {
	if envLabelCount == 0 || max <= 0 || len(groups) <= 1 {
		return groups
	}
	// Compute, for each env label, the sorted unique value set.
	perLabel := make([]map[string]struct{}, envLabelCount)
	for i := 0; i < envLabelCount; i++ {
		perLabel[i] = map[string]struct{}{}
	}
	for _, g := range groups {
		for i := 0; i < envLabelCount && i < len(g.Key.EnvVals); i++ {
			perLabel[i][g.Key.EnvVals[i]] = struct{}{}
		}
	}
	// For each over-capacity label, compute the keep-set.
	keepSets := make([]map[string]struct{}, envLabelCount)
	needCap := false
	for i := 0; i < envLabelCount; i++ {
		if len(perLabel[i]) <= max {
			continue
		}
		needCap = true
		vals := make([]string, 0, len(perLabel[i]))
		for v := range perLabel[i] {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		keep := make(map[string]struct{}, max)
		for _, v := range vals[:max] {
			keep[v] = struct{}{}
		}
		keepSets[i] = keep
	}
	if !needCap {
		return groups
	}
	// Rewrite and merge.
	merged := map[string]*gpuProcessGroup{}
	order := []string{}
	for _, g := range groups {
		newVals := make([]string, len(g.Key.EnvVals))
		copy(newVals, g.Key.EnvVals)
		for i := 0; i < envLabelCount && i < len(newVals); i++ {
			if ks := keepSets[i]; ks != nil {
				if _, ok := ks[newVals[i]]; !ok {
					newVals[i] = appconfig.FallbackOther
				}
			}
		}
		newKey := groupKey{Username: g.Key.Username, EnvVals: newVals}
		skey := newKey.canonical()
		if existing, ok := merged[skey]; ok {
			existing.ProcessCnt += g.ProcessCnt
			continue
		}
		merged[skey] = &gpuProcessGroup{Key: newKey, ProcessCnt: g.ProcessCnt}
		order = append(order, skey)
	}
	out := make([]gpuProcessGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *merged[k])
	}
	return out
}

// --------------------------------------------------------------------------
// T026: weighted split with closure compensation.
//
// For N groups pre-sorted deterministically by canonical key:
//   - splits[0..N-2] = round(Weight_i * total)
//   - splits[N-1]    = total - sum(splits[0..N-2])   (closure compensation)
//
// The last group's value always absorbs any rounding residue, so
// sum(splits) == total exactly for any input (regardless of weight
// distribution). When N == 1, the only group receives total unchanged.
// When total == 0, every split is 0.
// --------------------------------------------------------------------------

func splitUtil(total int, groups []gpuProcessGroup) []int {
	n := len(groups)
	if n == 0 {
		return nil
	}
	splits := make([]int, n)
	if total == 0 {
		return splits // all zeros
	}
	if n == 1 {
		splits[0] = total
		return splits
	}
	acc := 0
	for i := 0; i < n-1; i++ {
		v := int(math.Round(groups[i].Weight * float64(total)))
		splits[i] = v
		acc += v
	}
	splits[n-1] = total - acc
	return splits
}

// --------------------------------------------------------------------------
// Metric decoration helpers.
// --------------------------------------------------------------------------

// decorateIdle rewrites the Labels on an idle-GPU metric per FR-005 / T020.
// Value is forced to "0" so we never leak a stale util from upstream.
func (m *bareMetalUserMapper) decorateIdle(in collector.Metric) collector.Metric {
	cp, err := utils.DeepCopy(in)
	if err != nil {
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

// applyLabels injects USER + static + env labels onto an in-hand Metric copy.
// Callers own `m` and must have DeepCopy'd from the source earlier.
func (m *bareMetalUserMapper) applyLabels(out *collector.Metric, key groupKey) {
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	out.Labels["USER"] = key.Username
	for _, s := range m.cfg.Labels.Static {
		out.Labels[s.Name] = s.ResolvedValue
	}
	for j, e := range m.cfg.Labels.Env {
		if j < len(key.EnvVals) {
			out.Labels[e.Name] = key.EnvVals[j]
		} else {
			out.Labels[e.Name] = appconfig.FallbackNone
		}
	}
}
