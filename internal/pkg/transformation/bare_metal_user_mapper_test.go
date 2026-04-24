/*
 * Unit tests for feature 001-multi-user-gpu-util: bare-metal user mapper.
 *
 * Covers US1 (task T022): single-process attribution, UID-fallback, environ
 * unreadable, idle GPU, non-UTIL counter pass-through, static fallback chain.
 *
 * US2 (T029) tests will be appended here once the weighted splitter lands.
 */

package transformation

import (
	"errors"
	"fmt"
	"math/rand"
	sysOS "os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
)

// --------------------------------------------------------------------------
// Test helpers: a fake NVML implementation that only populates the subset of
// methods used by bareMetalUserMapper, and a helper that lays out a fake
// /proc tree compatible with realProcFS.
// --------------------------------------------------------------------------

type fakeNVML struct {
	// pidsByGPU maps gpuUUID -> list of PIDs to return from GetDeviceProcessMemory.
	pidsByGPU map[string][]uint32
	// err, if non-nil, is returned from GetDeviceProcessMemory for any GPU.
	err error
}

func (f *fakeNVML) GetMIGDeviceInfoByID(string) (*nvmlprovider.MIGDeviceInfo, error) {
	return nil, errors.New("unimplemented in fake")
}

func (f *fakeNVML) GetDeviceProcessMemory(gpuUUID string) (map[uint32]uint64, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uint32]uint64{}
	for _, pid := range f.pidsByGPU[gpuUUID] {
		out[pid] = 1 // value doesn't matter; the mapper only reads keys
	}
	return out, nil
}

func (f *fakeNVML) GetDeviceProcessUtilization(string) (map[uint32]uint32, error) {
	return map[uint32]uint32{}, nil
}

func (f *fakeNVML) GetAllMIGDevicesProcessMemory(string) (map[uint]map[uint32]uint64, error) {
	return nil, nil
}

func (f *fakeNVML) Cleanup() {}

// newProcRoot creates a temp root containing the requested /proc entries.
// pids is a map from PID to (uid, environ tokens as a "KEY=VALUE"-separated
// single string; NULs inserted automatically).
type fakeProc struct {
	uid     uint32
	environ string // caller-supplied KEY=VALUE... separated by '\n'; we translate to '\x00'
	// If omitOk is set, we omit this PID's files to simulate I/O errors.
	omitStatus  bool
	omitEnviron bool
}

func newProcRoot(t *testing.T, entries map[uint32]fakeProc) string {
	t.Helper()
	root := t.TempDir()
	for pid, e := range entries {
		dir := filepath.Join(root, fmt.Sprintf("%d", pid))
		require.NoError(t, sysOS.MkdirAll(dir, 0o755))
		if !e.omitStatus {
			content := fmt.Sprintf("Name:\tfake\nUid:\t%d\t%d\t%d\t%d\n", e.uid, e.uid, e.uid, e.uid)
			require.NoError(t, sysOS.WriteFile(filepath.Join(dir, "status"), []byte(content), 0o644))
		}
		if !e.omitEnviron {
			// Convert newline-separated user tokens into NUL-separated /proc/environ form.
			tokens := []byte(e.environ)
			for i, b := range tokens {
				if b == '\n' {
					tokens[i] = 0x00
				}
			}
			if len(tokens) > 0 && tokens[len(tokens)-1] != 0x00 {
				tokens = append(tokens, 0x00)
			}
			require.NoError(t, sysOS.WriteFile(filepath.Join(dir, "environ"), tokens, 0o644))
		}
	}
	return root
}

// stubLookupUsername swaps in a deterministic UID->username function for the
// duration of the test. Unknown UIDs fall back to "uid:<n>".
func stubLookupUsername(t *testing.T, known map[uint32]string) {
	t.Helper()
	orig := lookupUsername
	t.Cleanup(func() { lookupUsername = orig })
	lookupUsername = func(uid uint32) string {
		if n, ok := known[uid]; ok {
			return n
		}
		return fmt.Sprintf("uid:%d", uid)
	}
}

func utilMetric(gpuUUID, value string) collector.Metric {
	return collector.Metric{
		Counter: counters.Counter{FieldName: GpuUtilFieldName},
		Value:   value,
		GPU:     "0",
		GPUUUID: gpuUUID,
		Labels:  map[string]string{},
	}
}

func tempMetric(gpuUUID, value string) collector.Metric {
	return collector.Metric{
		Counter: counters.Counter{FieldName: "DCGM_FI_DEV_GPU_TEMP"},
		Value:   value,
		GPU:     "0",
		GPUUUID: gpuUUID,
		Labels:  map[string]string{},
	}
}

func baseConfigWithResolved(t *testing.T, studioEnv string) *appconfig.Config {
	t.Helper()
	if studioEnv != "" {
		t.Setenv("STUDIO", studioEnv)
	}
	cfg := &appconfig.Config{
		Labels: appconfig.LabelsConfig{
			Static: []appconfig.StaticLabel{{Name: "STUDIO", Value: ""}},
			Env:    []appconfig.EnvLabel{{Name: "PROJECT", EnvVar: "PROJECT"}},
		},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())
	return cfg
}

// --------------------------------------------------------------------------
// US1 tests
// --------------------------------------------------------------------------

func TestBareMetalUserMapper_SingleProcess_Happy(t *testing.T) {
	stubLookupUsername(t, map[uint32]string{1000: "alice"})
	procRoot := newProcRoot(t, map[uint32]fakeProc{
		7001: {uid: 1000, environ: "PATH=/usr/bin\nPROJECT=llm-training\nHOME=/home/alice"},
	})

	cfg := baseConfigWithResolved(t, "ai-lab")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-abc": {7001}}}

	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))
	metrics := collector.MetricsByCounter{
		counters.Counter{FieldName: GpuUtilFieldName}: {utilMetric("GPU-abc", "85")},
	}
	require.NoError(t, m.Process(metrics, nil))

	out := metrics[counters.Counter{FieldName: GpuUtilFieldName}]
	require.Len(t, out, 1)
	require.Equal(t, "85", out[0].Value)
	require.Equal(t, "alice", out[0].Labels["USER"])
	require.Equal(t, "ai-lab", out[0].Labels["STUDIO"])
	require.Equal(t, "llm-training", out[0].Labels["PROJECT"])
}

func TestBareMetalUserMapper_UIDHasNoUsername(t *testing.T) {
	// lookupUsername returns "uid:<n>" for unknown UIDs.
	stubLookupUsername(t, map[uint32]string{})
	procRoot := newProcRoot(t, map[uint32]fakeProc{
		7002: {uid: 70000, environ: "PROJECT=x"},
	})
	cfg := baseConfigWithResolved(t, "ai-lab")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-abc": {7002}}}

	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))
	metrics := collector.MetricsByCounter{
		counters.Counter{FieldName: GpuUtilFieldName}: {utilMetric("GPU-abc", "50")},
	}
	require.NoError(t, m.Process(metrics, nil))

	out := metrics[counters.Counter{FieldName: GpuUtilFieldName}]
	require.Len(t, out, 1)
	require.Equal(t, "uid:70000", out[0].Labels["USER"])
}

func TestBareMetalUserMapper_EnvironUnreadable_ProjectNone(t *testing.T) {
	stubLookupUsername(t, map[uint32]string{1000: "alice"})
	procRoot := newProcRoot(t, map[uint32]fakeProc{
		7003: {uid: 1000, omitEnviron: true},
	})
	cfg := baseConfigWithResolved(t, "ai-lab")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-abc": {7003}}}

	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))
	metrics := collector.MetricsByCounter{
		counters.Counter{FieldName: GpuUtilFieldName}: {utilMetric("GPU-abc", "70")},
	}
	require.NoError(t, m.Process(metrics, nil))

	out := metrics[counters.Counter{FieldName: GpuUtilFieldName}]
	require.Len(t, out, 1)
	require.Equal(t, "alice", out[0].Labels["USER"])
	require.Equal(t, "none", out[0].Labels["PROJECT"])
}

func TestBareMetalUserMapper_IdleGPU(t *testing.T) {
	stubLookupUsername(t, nil)
	cfg := baseConfigWithResolved(t, "ai-lab")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-abc": {}}}

	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(t.TempDir()))
	metrics := collector.MetricsByCounter{
		counters.Counter{FieldName: GpuUtilFieldName}: {utilMetric("GPU-abc", "0")},
	}
	require.NoError(t, m.Process(metrics, nil))

	out := metrics[counters.Counter{FieldName: GpuUtilFieldName}]
	require.Len(t, out, 1)
	require.Equal(t, "0", out[0].Value)
	require.Equal(t, "none", out[0].Labels["USER"])
	require.Equal(t, "ai-lab", out[0].Labels["STUDIO"])
	require.Equal(t, "none", out[0].Labels["PROJECT"])
}

func TestBareMetalUserMapper_NonUtilCounter_Untouched(t *testing.T) {
	stubLookupUsername(t, map[uint32]string{1000: "alice"})
	procRoot := newProcRoot(t, map[uint32]fakeProc{
		7004: {uid: 1000, environ: "PROJECT=x"},
	})
	cfg := baseConfigWithResolved(t, "ai-lab")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-abc": {7004}}}

	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))
	// Include both UTIL and TEMP to verify non-UTIL pass-through.
	origTemp := tempMetric("GPU-abc", "72")
	origTemp.Labels = map[string]string{"upstream": "kept"}
	metrics := collector.MetricsByCounter{
		counters.Counter{FieldName: GpuUtilFieldName}:     {utilMetric("GPU-abc", "50")},
		counters.Counter{FieldName: "DCGM_FI_DEV_GPU_TEMP"}: {origTemp},
	}
	require.NoError(t, m.Process(metrics, nil))

	tempOut := metrics[counters.Counter{FieldName: "DCGM_FI_DEV_GPU_TEMP"}]
	require.Len(t, tempOut, 1)
	// Non-UTIL counter must be byte-identical to input.
	require.Equal(t, origTemp, tempOut[0])
}

func TestBareMetalUserMapper_StaticLabelEnvFallback(t *testing.T) {
	// STUDIO env already fed into baseConfigWithResolved("dev-host") by Setenv.
	stubLookupUsername(t, map[uint32]string{1000: "alice"})
	procRoot := newProcRoot(t, map[uint32]fakeProc{
		7005: {uid: 1000, environ: "PROJECT=p1"},
	})
	cfg := baseConfigWithResolved(t, "dev-host")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-abc": {7005}}}

	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))
	metrics := collector.MetricsByCounter{
		counters.Counter{FieldName: GpuUtilFieldName}: {utilMetric("GPU-abc", "40")},
	}
	require.NoError(t, m.Process(metrics, nil))

	out := metrics[counters.Counter{FieldName: GpuUtilFieldName}]
	require.Equal(t, "dev-host", out[0].Labels["STUDIO"])
}

func TestSanitizeEnvValue(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		require.Equal(t, appconfig.FallbackNone, sanitizeEnvValue(""))
	})
	t.Run("illegal_chars_replaced", func(t *testing.T) {
		require.Equal(t, "a_b_c", sanitizeEnvValue("a b/c"))
	})
	t.Run("truncate_over_max_len", func(t *testing.T) {
		in := ""
		for i := 0; i < appconfig.EnvValueMaxLen+10; i++ {
			in += "x"
		}
		got := sanitizeEnvValue(in)
		require.LessOrEqual(t, len(got), appconfig.EnvValueMaxLen)
	})
	t.Run("allowed_chars_preserved", func(t *testing.T) {
		require.Equal(t, "proj-a.1_2", sanitizeEnvValue("proj-a.1_2"))
	})
}

// --------------------------------------------------------------------------
// US2 tests (task T029)
// --------------------------------------------------------------------------

// processAndCollect runs Process and extracts the resulting metrics map for
// the UTIL counter.
func processAndCollect(t *testing.T, m Transform, gpuUUID string, util string) []collector.Metric {
	t.Helper()
	metrics := collector.MetricsByCounter{
		counters.Counter{FieldName: GpuUtilFieldName}: {utilMetric(gpuUUID, util)},
	}
	require.NoError(t, m.Process(metrics, nil))
	return metrics[counters.Counter{FieldName: GpuUtilFieldName}]
}

// labelSet flattens the (USER, env) label pairs from a metric for comparison.
func labelSet(t *testing.T, metrics []collector.Metric, keys ...string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, m := range metrics {
		key := ""
		for _, k := range keys {
			if key != "" {
				key += "|"
			}
			key += k + "=" + m.Labels[k]
		}
		v, err := strconv.Atoi(m.Value)
		require.NoError(t, err, "metric value must be integer")
		out[key] += v
	}
	return out
}

func TestBareMetalUserMapper_US2_WeightedSplit_AliceBob(t *testing.T) {
	// alice x3 (proj-a) + bob x1 (proj-b) @ UTIL=80 -> {60, 20}.
	stubLookupUsername(t, map[uint32]string{1000: "alice", 1001: "bob"})
	procRoot := newProcRoot(t, map[uint32]fakeProc{
		701: {uid: 1000, environ: "PROJECT=proj-a"},
		702: {uid: 1000, environ: "PROJECT=proj-a"},
		703: {uid: 1000, environ: "PROJECT=proj-a"},
		704: {uid: 1001, environ: "PROJECT=proj-b"},
	})
	cfg := baseConfigWithResolved(t, "ai-lab")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-abc": {701, 702, 703, 704}}}
	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))

	out := processAndCollect(t, m, "GPU-abc", "80")
	require.Len(t, out, 2)
	got := labelSet(t, out, "USER", "PROJECT")
	require.Equal(t, 60, got["USER=alice|PROJECT=proj-a"])
	require.Equal(t, 20, got["USER=bob|PROJECT=proj-b"])
}

func TestBareMetalUserMapper_US2_SameUserMultipleProjects(t *testing.T) {
	// alice/proj-a x1 + alice/proj-b x1 @ UTIL=80 -> {40, 40}.
	stubLookupUsername(t, map[uint32]string{1000: "alice"})
	procRoot := newProcRoot(t, map[uint32]fakeProc{
		810: {uid: 1000, environ: "PROJECT=proj-a"},
		811: {uid: 1000, environ: "PROJECT=proj-b"},
	})
	cfg := baseConfigWithResolved(t, "ai-lab")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-xyz": {810, 811}}}
	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))

	out := processAndCollect(t, m, "GPU-xyz", "80")
	require.Len(t, out, 2)
	got := labelSet(t, out, "USER", "PROJECT")
	require.Equal(t, 40, got["USER=alice|PROJECT=proj-a"])
	require.Equal(t, 40, got["USER=alice|PROJECT=proj-b"])
}

func TestBareMetalUserMapper_US2_ThreeEqualGroupsClosure(t *testing.T) {
	// 3 users equal @ UTIL=100 -> {33, 33, 34}; the closure compensation
	// falls on the last group by canonical sort order.
	stubLookupUsername(t, map[uint32]string{1000: "alice", 1001: "bob", 1002: "carol"})
	procRoot := newProcRoot(t, map[uint32]fakeProc{
		901: {uid: 1000, environ: "PROJECT=p"},
		902: {uid: 1001, environ: "PROJECT=p"},
		903: {uid: 1002, environ: "PROJECT=p"},
	})
	cfg := baseConfigWithResolved(t, "ai-lab")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-c": {901, 902, 903}}}
	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))

	out := processAndCollect(t, m, "GPU-c", "100")
	require.Len(t, out, 3)
	sum := 0
	for _, mt := range out {
		v, err := strconv.Atoi(mt.Value)
		require.NoError(t, err)
		sum += v
	}
	require.Equal(t, 100, sum, "splits must sum to UTIL exactly")
	// Specifically: alphabetically sorted keys are alice, bob, carol; closure
	// falls on the last one = carol.
	byUser := map[string]int{}
	for _, mt := range out {
		v, _ := strconv.Atoi(mt.Value)
		byUser[mt.Labels["USER"]] = v
	}
	require.Equal(t, 33, byUser["alice"])
	require.Equal(t, 33, byUser["bob"])
	require.Equal(t, 34, byUser["carol"])
}

func TestBareMetalUserMapper_US2_ZeroUtilWithProcesses(t *testing.T) {
	// UTIL=0 + 2 processes -> all splits zero; one record per group.
	stubLookupUsername(t, map[uint32]string{1000: "alice", 1001: "bob"})
	procRoot := newProcRoot(t, map[uint32]fakeProc{
		1101: {uid: 1000, environ: "PROJECT=x"},
		1102: {uid: 1001, environ: "PROJECT=x"},
	})
	cfg := baseConfigWithResolved(t, "ai-lab")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-zero": {1101, 1102}}}
	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))

	out := processAndCollect(t, m, "GPU-zero", "0")
	require.Len(t, out, 2)
	for _, mt := range out {
		require.Equal(t, "0", mt.Value)
	}
}

func TestBareMetalUserMapper_US2_FuzzInvariantSumEqualsTotal(t *testing.T) {
	// 100 iterations of random group counts and util totals; splitUtil must
	// always produce slices that sum exactly to the total.
	r := rand.New(rand.NewSource(42))
	for iter := 0; iter < 100; iter++ {
		n := 1 + r.Intn(8)
		total := r.Intn(101) // [0, 100]
		// Fabricate groups with random process counts; weights follow.
		groups := make([]gpuProcessGroup, n)
		var tp uint
		for i := 0; i < n; i++ {
			groups[i].ProcessCnt = 1 + uint(r.Intn(10))
			tp += groups[i].ProcessCnt
		}
		for i := range groups {
			groups[i].Weight = float64(groups[i].ProcessCnt) / float64(tp)
			groups[i].Key = groupKey{Username: fmt.Sprintf("u%02d", i)}
		}
		splits := splitUtil(total, groups)
		require.Len(t, splits, n)
		sum := 0
		for _, v := range splits {
			sum += v
		}
		require.Equal(t, total, sum,
			"iter=%d n=%d total=%d splits=%v", iter, n, total, splits)
	}
}

func TestBareMetalUserMapper_US2_MultiEnvAggregateKey(t *testing.T) {
	// Config with two env labels: PROJECT + EXPERIMENT.
	// alice/proj-a/exp1 x2 + alice/proj-a/exp2 x2 @ UTIL=80 -> {40, 40}.
	stubLookupUsername(t, map[uint32]string{1000: "alice"})
	procRoot := newProcRoot(t, map[uint32]fakeProc{
		1301: {uid: 1000, environ: "PROJECT=proj-a\nEXPERIMENT=exp1"},
		1302: {uid: 1000, environ: "PROJECT=proj-a\nEXPERIMENT=exp1"},
		1303: {uid: 1000, environ: "PROJECT=proj-a\nEXPERIMENT=exp2"},
		1304: {uid: 1000, environ: "PROJECT=proj-a\nEXPERIMENT=exp2"},
	})
	t.Setenv("STUDIO", "ai-lab")
	cfg := &appconfig.Config{
		Labels: appconfig.LabelsConfig{
			Static: []appconfig.StaticLabel{{Name: "STUDIO", Value: ""}},
			Env: []appconfig.EnvLabel{
				{Name: "PROJECT", EnvVar: "PROJECT"},
				{Name: "EXPERIMENT", EnvVar: "EXPERIMENT"},
			},
		},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-m": {1301, 1302, 1303, 1304}}}
	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))

	out := processAndCollect(t, m, "GPU-m", "80")
	require.Len(t, out, 2)
	got := labelSet(t, out, "USER", "PROJECT", "EXPERIMENT")
	require.Equal(t, 40, got["USER=alice|PROJECT=proj-a|EXPERIMENT=exp1"])
	require.Equal(t, 40, got["USER=alice|PROJECT=proj-a|EXPERIMENT=exp2"])
}

func TestBareMetalUserMapper_US2_EnvCardinalityCap(t *testing.T) {
	// Feed 200 distinct PROJECT values, one process per value. The first 128
	// lexicographic survive; the remaining 72 collapse to "other" and merge.
	uidMap := map[uint32]string{1000: "alice"}
	stubLookupUsername(t, uidMap)
	entries := map[uint32]fakeProc{}
	pids := make([]uint32, 0, 200)
	for i := 0; i < 200; i++ {
		pid := uint32(4000 + i)
		entries[pid] = fakeProc{uid: 1000, environ: fmt.Sprintf("PROJECT=%03d", i)}
		pids = append(pids, pid)
	}
	procRoot := newProcRoot(t, entries)
	cfg := baseConfigWithResolved(t, "ai-lab")
	nvml := &fakeNVML{pidsByGPU: map[string][]uint32{"GPU-big": pids}}
	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(procRoot))

	out := processAndCollect(t, m, "GPU-big", "100")
	// Distinct PROJECT values in output must be exactly 128 surviving + 1 "other".
	projects := map[string]int{}
	for _, mt := range out {
		projects[mt.Labels["PROJECT"]]++
	}
	require.Equal(t, appconfig.MaxEnvCardinalityPerCycle+1, len(projects),
		"expected %d kept + 1 'other' bucket", appconfig.MaxEnvCardinalityPerCycle)
	require.Equal(t, 1, projects[appconfig.FallbackOther])

	// Survived keys should be the lex-smallest 128: "000" .. "127".
	// Values ">=128" should have collapsed into "other".
	require.NotContains(t, projects, "199", "199 must have been evicted")
	require.Contains(t, projects, "000", "000 must survive")
	require.Contains(t, projects, "127", "127 must survive")

	// Sum of all splits still equals total.
	sum := 0
	for _, mt := range out {
		v, _ := strconv.Atoi(mt.Value)
		sum += v
	}
	require.Equal(t, 100, sum)
}

// --------------------------------------------------------------------------
// splitUtil unit tests (task T026)
// --------------------------------------------------------------------------

func TestSplitUtil(t *testing.T) {
	mk := func(weights ...float64) []gpuProcessGroup {
		gs := make([]gpuProcessGroup, len(weights))
		for i, w := range weights {
			gs[i].Weight = w
			gs[i].Key = groupKey{Username: fmt.Sprintf("u%d", i)}
		}
		return gs
	}
	t.Run("nil", func(t *testing.T) {
		require.Nil(t, splitUtil(50, nil))
	})
	t.Run("single", func(t *testing.T) {
		require.Equal(t, []int{80}, splitUtil(80, mk(1.0)))
	})
	t.Run("two_even_80", func(t *testing.T) {
		require.Equal(t, []int{40, 40}, splitUtil(80, mk(0.5, 0.5)))
	})
	t.Run("alice75_bob25_at_80", func(t *testing.T) {
		require.Equal(t, []int{60, 20}, splitUtil(80, mk(0.75, 0.25)))
	})
	t.Run("three_thirds_at_100", func(t *testing.T) {
		require.Equal(t, []int{33, 33, 34}, splitUtil(100, mk(1.0/3, 1.0/3, 1.0/3)))
	})
	t.Run("zero_total", func(t *testing.T) {
		require.Equal(t, []int{0, 0, 0}, splitUtil(0, mk(0.5, 0.3, 0.2)))
	})
}

// --------------------------------------------------------------------------
// capEnvCardinality unit tests (task T025)
// --------------------------------------------------------------------------

func TestCapEnvCardinality_NoOp(t *testing.T) {
	groups := []gpuProcessGroup{
		{Key: groupKey{Username: "a", EnvVals: []string{"x"}}, ProcessCnt: 1},
		{Key: groupKey{Username: "b", EnvVals: []string{"y"}}, ProcessCnt: 2},
	}
	out := capEnvCardinality(groups, 1, 10)
	require.Equal(t, groups, out) // under cap -> unchanged
}

func TestCapEnvCardinality_TrimsAndMerges(t *testing.T) {
	// 4 values, cap=2: keep {"A", "B"} (lex smallest), rewrite {"C","D"}->"other",
	// then merge any groups whose canonical key collapses.
	groups := []gpuProcessGroup{
		{Key: groupKey{Username: "u", EnvVals: []string{"A"}}, ProcessCnt: 1},
		{Key: groupKey{Username: "u", EnvVals: []string{"B"}}, ProcessCnt: 2},
		{Key: groupKey{Username: "u", EnvVals: []string{"C"}}, ProcessCnt: 3},
		{Key: groupKey{Username: "u", EnvVals: []string{"D"}}, ProcessCnt: 4},
	}
	out := capEnvCardinality(groups, 1, 2)
	require.Len(t, out, 3, "A, B, other")
	// Collect counts by the env value for the single label.
	counts := map[string]uint{}
	for _, g := range out {
		counts[g.Key.EnvVals[0]] = g.ProcessCnt
	}
	require.Equal(t, uint(1), counts["A"])
	require.Equal(t, uint(2), counts["B"])
	require.Equal(t, uint(7), counts[appconfig.FallbackOther]) // 3 + 4 merged
}
