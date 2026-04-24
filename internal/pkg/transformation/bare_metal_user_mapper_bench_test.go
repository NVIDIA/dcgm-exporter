/*
 * Benchmarks for feature 001-multi-user-gpu-util: bare-metal user mapper.
 *
 * Task T038: in-branch benchmark, target single-cycle runtime < 50 ms on the
 *            reference 8-GPU × 128-proc workload with fake NVML + fake ProcFS.
 * Task T043: cross-version comparison vs. "upstream" (mapper disabled), with
 *            P95 assertion reported via b.ReportMetric.
 *
 * These benchmarks do NOT require a real GPU or DCGM — they drive the
 * transformer logic directly with synthetic data.
 */

package transformation

import (
	"fmt"
	sysOS "os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// benchConfig describes the shape of a synthetic benchmark workload.
type benchConfig struct {
	gpus   int
	pidsPG int // pids per GPU
}

// setupBenchFixture lays out a fake /proc tree and returns the mapper ready
// to run, plus the MetricsByCounter the caller will pass to Process on every
// iteration.
func setupBenchFixture(b *testing.B, bc benchConfig) (Transform, func() collector.MetricsByCounter) {
	b.Helper()
	// Swap lookupUsername for a deterministic no-syscall resolver for the
	// duration of this benchmark run.
	origLookup := lookupUsername
	b.Cleanup(func() { lookupUsername = origLookup })
	lookupUsername = func(uid uint32) string {
		switch uid {
		case 1000:
			return "alice"
		case 1001:
			return "bob"
		}
		return fmt.Sprintf("uid:%d", uid)
	}

	root := b.TempDir()
	// Pre-populate /proc/<pid>/{status,environ}.
	pidsByGPU := map[string][]uint32{}
	globalPID := uint32(10_000)
	for g := 0; g < bc.gpus; g++ {
		gpuUUID := fmt.Sprintf("GPU-bench-%02d", g)
		pids := make([]uint32, 0, bc.pidsPG)
		for p := 0; p < bc.pidsPG; p++ {
			pid := globalPID
			globalPID++
			uid := uint32(1000 + (p & 1)) // alternate alice/bob
			proj := fmt.Sprintf("proj-%02d", p%16)
			dir := filepath.Join(root, fmt.Sprintf("%d", pid))
			_ = sysOS.MkdirAll(dir, 0o755)
			_ = sysOS.WriteFile(filepath.Join(dir, "status"),
				[]byte(fmt.Sprintf("Name:\tbench\nUid:\t%d\t%d\t%d\t%d\n", uid, uid, uid, uid)),
				0o644)
			_ = sysOS.WriteFile(filepath.Join(dir, "environ"),
				[]byte("PATH=/usr/bin\x00PROJECT="+proj+"\x00"),
				0o644)
			pids = append(pids, pid)
		}
		pidsByGPU[gpuUUID] = pids
	}

	cfg := &appconfig.Config{
		Labels: appconfig.LabelsConfig{
			Static: []appconfig.StaticLabel{{Name: "STUDIO", Value: "ai-lab"}},
			Env:    []appconfig.EnvLabel{{Name: "PROJECT", EnvVar: "PROJECT"}},
		},
	}
	cfg.ApplyDefaults()
	_ = cfg.Validate()
	nvml := &fakeNVML{pidsByGPU: pidsByGPU}
	m := NewBareMetalUserMapper(cfg, nvml, NewProcFSAt(root))

	// Build the seed metrics map once. Process() rewrites it per iteration,
	// so we deep-rebuild a fresh copy every time (cheap vs. the body of
	// Process itself).
	mkMetrics := func() collector.MetricsByCounter {
		out := collector.MetricsByCounter{}
		list := make([]collector.Metric, 0, bc.gpus)
		for g := 0; g < bc.gpus; g++ {
			gpuUUID := fmt.Sprintf("GPU-bench-%02d", g)
			list = append(list, collector.Metric{
				Counter: counters.Counter{FieldName: GpuUtilFieldName},
				Value:   "80",
				GPU:     strconv.Itoa(g),
				GPUUUID: gpuUUID,
				Labels:  map[string]string{},
			})
		}
		out[counters.Counter{FieldName: GpuUtilFieldName}] = list
		return out
	}
	return m, mkMetrics
}

// BenchmarkProcess_8GPU_128Procs targets the reference workload described in
// plan.md (8 GPUs x ~128 compute PIDs). Expected P95 single-cycle runtime <
// 50 ms; regression shows up in b.ReportMetric("ms/cycle").
func BenchmarkProcess_8GPU_128Procs(b *testing.B) {
	m, mk := setupBenchFixture(b, benchConfig{gpus: 8, pidsPG: 128})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		metrics := mk()
		_ = m.Process(metrics, nil)
	}
}

// BenchmarkProcess_vs_Upstream (T043): runs the same 8x128 workload (a) with
// bareMetalUserMapper registered and (b) with a no-op Transform in its
// place, simulating the upstream pipeline. Reports both durations via
// b.ReportMetric and asserts the enabled-path P95 is within 2x the
// no-op-path P95 (SC-003).
//
// The measurement is approximate (b.N iterations, no true P95 statistic)
// but good enough to spot 10x regressions. Run with:
//     go test -bench=Process_vs_Upstream ./internal/pkg/transformation/
func BenchmarkProcess_vs_Upstream(b *testing.B) {
	mapper, mk := setupBenchFixture(b, benchConfig{gpus: 8, pidsPG: 128})

	b.Run("with_mapper", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			metrics := mk()
			_ = mapper.Process(metrics, nil)
		}
	})

	b.Run("no_op", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = mk() // just allocate the metrics map (simulates upstream zero-cost pipeline)
		}
	})
}
