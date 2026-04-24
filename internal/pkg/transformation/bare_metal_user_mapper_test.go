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
	sysOS "os"
	"path/filepath"
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
