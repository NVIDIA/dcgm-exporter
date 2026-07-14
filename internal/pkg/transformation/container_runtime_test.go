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

package transformation

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

type runtimeTestDeviceInfo struct {
	gpus []deviceinfo.GPUInfo
}

func (r runtimeTestDeviceInfo) GPUCount() uint                    { return uint(len(r.gpus)) }
func (r runtimeTestDeviceInfo) GPUs() []deviceinfo.GPUInfo        { return r.gpus }
func (r runtimeTestDeviceInfo) GPU(i uint) deviceinfo.GPUInfo     { return r.gpus[i] }
func (r runtimeTestDeviceInfo) Switches() []deviceinfo.SwitchInfo { return nil }
func (r runtimeTestDeviceInfo) Switch(uint) deviceinfo.SwitchInfo { return deviceinfo.SwitchInfo{} }
func (r runtimeTestDeviceInfo) CPUs() []deviceinfo.CPUInfo        { return nil }
func (r runtimeTestDeviceInfo) CPU(uint) deviceinfo.CPUInfo       { return deviceinfo.CPUInfo{} }
func (r runtimeTestDeviceInfo) GOpts() appconfig.DeviceOptions    { return appconfig.DeviceOptions{} }
func (r runtimeTestDeviceInfo) SOpts() appconfig.DeviceOptions    { return appconfig.DeviceOptions{} }
func (r runtimeTestDeviceInfo) COpts() appconfig.DeviceOptions    { return appconfig.DeviceOptions{} }
func (r runtimeTestDeviceInfo) InfoType() dcgm.Field_Entity_Group { return dcgm.FE_GPU }
func (r runtimeTestDeviceInfo) IsCPUWatched(uint) bool            { return false }
func (r runtimeTestDeviceInfo) IsCoreWatched(uint, uint) bool     { return false }
func (r runtimeTestDeviceInfo) IsSwitchWatched(uint) bool         { return false }
func (r runtimeTestDeviceInfo) IsLinkWatched(uint, uint) bool     { return false }

func testDeviceInfo() runtimeTestDeviceInfo {
	return runtimeTestDeviceInfo{gpus: []deviceinfo.GPUInfo{
		{DeviceInfo: dcgm.Device{UUID: "GPU-0", GPU: 0}},
		{DeviceInfo: dcgm.Device{UUID: "GPU-1", GPU: 1}},
	}}
}

func testMIGDeviceInfo() runtimeTestDeviceInfo {
	return runtimeTestDeviceInfo{gpus: []deviceinfo.GPUInfo{
		{
			DeviceInfo: dcgm.Device{UUID: "GPU-0", GPU: 0},
			GPUInstances: []deviceinfo.GPUInstanceInfo{
				{
					Info:        dcgm.MigEntityInfo{NvmlInstanceId: 8},
					ProfileName: "1g.10gb",
				},
			},
		},
	}}
}

func TestDockerCompatibleRuntimeContainersByGPU(t *testing.T) {
	rt := newTestDockerRuntime(t, map[string]string{
		"c1": `{"Id":"c1","Name":"/trainer","HostConfig":{"DeviceRequests":[{"Driver":"nvidia","DeviceIDs":["GPU-0"],"Capabilities":[["gpu"]]}]}}`,
		"c2": `{"Id":"c2","Name":"/all-gpus","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=all"]}}`,
		"c3": `{"Id":"c3","Name":"/count-only","HostConfig":{"DeviceRequests":[{"Driver":"nvidia","Count":1,"Capabilities":[["gpu"]]}]}}`,
	})

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)

	assert.ElementsMatch(t, []containerInfo{{Name: "trainer"}, {Name: "all-gpus"}}, got["GPU-0"])
	assert.Equal(t, []containerInfo{{Name: "all-gpus"}}, got["GPU-1"])
	for _, infos := range got {
		for _, info := range infos {
			assert.NotEqual(t, "count-only", info.Name, "count-only container should not be labeled")
		}
	}
}

func TestDockerCompatibleRuntimeNormalizesAssignments(t *testing.T) {
	rt := newTestDockerRuntime(t, map[string]string{
		"c1": `{"Id":"c1","Name":"/numeric","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=0,9"]}}`,
		"c2": `{"Id":"c2","Name":"/prefer-uuid","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-1,1"]}}`,
		"c3": `{"Id":"c3","Name":"/none","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=none"]}}`,
	})

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)

	assert.Equal(t, []containerInfo{{Name: "numeric"}}, got["GPU-0"])
	assert.Equal(t, []containerInfo{{Name: "prefer-uuid"}}, got["GPU-1"])
	assert.Len(t, got, 2)
}

func TestDockerCompatibleRuntimeNormalizesMixedUUIDAndIndexAssignments(t *testing.T) {
	rt := newTestDockerRuntime(t, map[string]string{
		"c1": `{"Id":"c1","Name":"/mixed","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-1,0"]}}`,
	})

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)

	assert.Equal(t, []containerInfo{{Name: "mixed"}}, got["GPU-0"])
	assert.Equal(t, []containerInfo{{Name: "mixed"}}, got["GPU-1"])
}

func TestDockerCompatibleRuntimeNormalizesMIGAssignments(t *testing.T) {
	rt := newTestDockerRuntime(t, map[string]string{
		"c1": `{"Id":"c1","Name":"/mig-container","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=MIG-GPU-0/8/5"]}}`,
	})

	got, err := rt.ContainersByGPU(context.Background(), testMIGDeviceInfo())
	require.NoError(t, err)

	assert.Equal(t, []containerInfo{{Name: "mig-container"}}, got["0.8"])
	assert.NotContains(t, got, "MIG-GPU-0/8/5")
}

func TestDockerCompatibleRuntimeSanitizesContainerName(t *testing.T) {
	longName := "/" + strings.Repeat("a", 140) + "\ncontrol"
	rt := newTestDockerRuntime(t, map[string]string{
		"c1": fmt.Sprintf(`{"Id":"c1","Name":%q,"Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`, longName),
	})

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	require.Len(t, got["GPU-0"], 1)

	assert.Len(t, got["GPU-0"][0].Name, maxContainerLabelBytes)
	assert.NotContains(t, got["GPU-0"][0].Name, "\n")
}

func TestDockerCompatibleRuntimeTruncatesContainerNameAtUTF8Boundary(t *testing.T) {
	longName := "/" + strings.Repeat("é", 80)
	rt := newTestDockerRuntime(t, map[string]string{
		"c1": fmt.Sprintf(`{"Id":"c1","Name":%q,"Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`, longName),
	})

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	require.Len(t, got["GPU-0"], 1)

	assert.LessOrEqual(t, len(got["GPU-0"][0].Name), maxContainerLabelBytes)
	assert.True(t, utf8.ValidString(got["GPU-0"][0].Name))
}

func FuzzContainerGPUKeys(f *testing.F) {
	seeds := [][2]string{
		{"0,GPU-1,MIG-GPU-0/8/5", "trainer"},
		{"all", "all-gpus"},
		{"none,void,,GPU-0,GPU-0", "\ncontrol"},
		{"MIG-GPU-0/not-a-number/5,999", strings.Repeat("é", 80)},
	}
	for _, seed := range seeds {
		f.Add(seed[0], seed[1])
	}

	deviceInfo := testMIGDeviceInfo()
	deviceInfo.gpus = append(deviceInfo.gpus, deviceinfo.GPUInfo{
		DeviceInfo: dcgm.Device{UUID: "GPU-1", GPU: 1},
	})

	f.Fuzz(func(t *testing.T, visibleDevices, containerName string) {
		inspect := dockerContainerInspect{
			Config: &dockerContainerConfig{Env: []string{"IGNORED=value", "NVIDIA_VISIBLE_DEVICES=" + visibleDevices}},
		}
		first := containerGPUKeys(inspect, deviceInfo)
		second := containerGPUKeys(inspect, deviceInfo)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("GPU assignment is not deterministic: first=%q second=%q", first, second)
		}

		seen := make(map[string]struct{}, len(first))
		for _, key := range first {
			if key == "" {
				t.Fatal("GPU assignment returned an empty key")
			}
			if _, ok := seen[key]; ok {
				t.Fatalf("GPU assignment returned duplicate key %q", key)
			}
			seen[key] = struct{}{}
		}

		label := sanitizeContainerLabel(containerName)
		if len(label) > maxContainerLabelBytes {
			t.Fatalf("container label is %d bytes, limit is %d", len(label), maxContainerLabelBytes)
		}
		if !utf8.ValidString(label) {
			t.Fatalf("container label is not valid UTF-8: %q", label)
		}
		for _, r := range label {
			if unicode.IsControl(r) {
				t.Fatalf("container label contains control character %U", r)
			}
		}
	})
}

func TestDockerCompatibleRuntimeUsesStaleCacheOnRefreshFailure(t *testing.T) {
	now := time.Now()
	rt, fail := newTestDockerRuntimeWithFailure(t, map[string]string{
		"c1": `{"Id":"c1","Name":"/cached","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`,
	})
	rt.cacheTTL = time.Millisecond
	rt.now = func() time.Time { return now }

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	assert.Equal(t, []containerInfo{{Name: "cached"}}, got["GPU-0"])

	fail.Store(true)
	now = now.Add(time.Second)

	got, err = rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	assert.Equal(t, []containerInfo{{Name: "cached"}}, got["GPU-0"])
}

func TestDockerCompatibleRuntimeResetsStaleWarningAfterSuccessfulRefresh(t *testing.T) {
	var logBuf bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	now := time.Now()
	rt, fail := newTestDockerRuntimeWithFailure(t, map[string]string{
		"c1": `{"Id":"c1","Name":"/cached","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`,
	})
	rt.cacheTTL = time.Millisecond
	rt.now = func() time.Time { return now }

	_, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)

	fail.Store(true)
	now = now.Add(time.Second)
	_, err = rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	assert.Contains(t, logBuf.String(), "container runtime refresh failed")

	logBuf.Reset()
	fail.Store(false)
	now = now.Add(time.Second)
	_, err = rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)

	fail.Store(true)
	now = now.Add(time.Second)
	_, err = rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	assert.Contains(t, logBuf.String(), "container runtime refresh failed")
}

func TestDockerCompatibleRuntimeSkipsContainerMissingAtInspect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"c1","Names":["/trainer"]},{"Id":"gone","Names":["/gone"]}]`))
		case "/containers/c1/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"c1","Name":"/trainer","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`))
		case "/containers/gone/json":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	rt := &dockerCompatibleRuntime{
		baseURL:  server.URL,
		client:   server.Client(),
		timeout:  time.Second,
		cacheTTL: time.Minute,
		now:      time.Now,
	}

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)

	assert.Equal(t, []containerInfo{{Name: "trainer"}}, got["GPU-0"])
}

func TestDockerCompatibleRuntimeUsesListNameAndShortIDFallbacks(t *testing.T) {
	longID := "bbbbbbbbbbbb9999"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/containers/json":
			_, _ = fmt.Fprintf(w, `[{"Id":"aaaaaaaaaaaa9999","Names":["/from-list"]},{"Id":%q}]`, longID)
		case "/containers/aaaaaaaaaaaa9999/json":
			_, _ = w.Write([]byte(`{"Id":"aaaaaaaaaaaa9999","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`))
		case "/containers/bbbbbbbbbbbb9999/json":
			_, _ = fmt.Fprintf(w, `{"Id":%q,"Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`, longID)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	rt := &dockerCompatibleRuntime{
		baseURL:  server.URL,
		client:   server.Client(),
		timeout:  time.Second,
		cacheTTL: time.Minute,
		now:      time.Now,
	}

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)

	assert.Equal(t, []containerInfo{
		{Name: "from-list"},
		{Name: "bbbbbbbbbbbb"},
	}, got["GPU-0"])
}

func TestDockerCompatibleRuntimeHonorsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	rt := &dockerCompatibleRuntime{
		baseURL:  server.URL,
		client:   server.Client(),
		timeout:  10 * time.Millisecond,
		cacheTTL: time.Minute,
		now:      time.Now,
	}

	_, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestDockerCompatibleRuntimeMalformedListDoesNotPoisonCache(t *testing.T) {
	fail := &atomic.Bool{}
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/containers/json" && fail.Load() {
			_, _ = w.Write([]byte(`[`))
			return
		}
		switch r.URL.Path {
		case "/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"c1","Names":["/trainer"]}]`))
		case "/containers/c1/json":
			_, _ = w.Write([]byte(`{"Id":"c1","Name":"/trainer","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	rt := &dockerCompatibleRuntime{
		baseURL:  server.URL,
		client:   server.Client(),
		timeout:  time.Second,
		cacheTTL: time.Minute,
		now:      time.Now,
	}

	_, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.Error(t, err)

	fail.Store(false)
	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	assert.Equal(t, []containerInfo{{Name: "trainer"}}, got["GPU-0"])
}

func TestDockerCompatibleRuntimeReturnedMapDoesNotMutateCache(t *testing.T) {
	rt := newTestDockerRuntime(t, map[string]string{
		"c1": `{"Id":"c1","Name":"/cached","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`,
	})

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	got["GPU-0"][0].Name = "mutated"

	got, err = rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	assert.Equal(t, []containerInfo{{Name: "cached"}}, got["GPU-0"])
}

func newTestDockerRuntime(t *testing.T, inspectByID map[string]string) *dockerCompatibleRuntime {
	t.Helper()
	rt, _ := newTestDockerRuntimeWithFailure(t, inspectByID)
	return rt
}

func newTestDockerRuntimeWithFailure(t *testing.T, inspectByID map[string]string) (*dockerCompatibleRuntime, *atomic.Bool) {
	t.Helper()
	fail := &atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "runtime unavailable", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[`))
			first := true
			for id := range inspectByID {
				if !first {
					_, _ = w.Write([]byte(`,`))
				}
				first = false
				_, _ = fmt.Fprintf(w, `{"Id":%q,"Names":[%q]}`, id, "/"+id)
			}
			_, _ = w.Write([]byte(`]`))
		default:
			id := strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json"), "/")
			body, ok := inspectByID[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(server.Close)

	return &dockerCompatibleRuntime{
		baseURL:  server.URL,
		client:   server.Client(),
		timeout:  time.Second,
		cacheTTL: time.Minute,
		now:      time.Now,
	}, fail
}
