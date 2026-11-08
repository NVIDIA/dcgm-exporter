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
	"slices"
	"strconv"
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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

// runtimeTestDeviceInfo supplies a selected GPU inventory and empty non-GPU topology.
type runtimeTestDeviceInfo struct {
	gpus []deviceinfo.GPUInfo
}

// GPUCount returns the configured fixture GPU count.
func (r runtimeTestDeviceInfo) GPUCount() uint { return uint(len(r.gpus)) }

// GPUs returns the fixture GPU inventory.
func (r runtimeTestDeviceInfo) GPUs() []deviceinfo.GPUInfo { return r.gpus }

// GPU returns the requested fixture GPU.
func (r runtimeTestDeviceInfo) GPU(i uint) deviceinfo.GPUInfo { return r.gpus[i] }

// Switches returns no switches in this GPU-only fixture.
func (r runtimeTestDeviceInfo) Switches() []deviceinfo.SwitchInfo { return nil }

// Switch returns an empty switch in this GPU-only fixture.
func (r runtimeTestDeviceInfo) Switch(uint) deviceinfo.SwitchInfo { return deviceinfo.SwitchInfo{} }

// CPUs returns no CPUs in this GPU-only fixture.
func (r runtimeTestDeviceInfo) CPUs() []deviceinfo.CPUInfo { return nil }

// CPU returns an empty CPU in this GPU-only fixture.
func (r runtimeTestDeviceInfo) CPU(uint) deviceinfo.CPUInfo { return deviceinfo.CPUInfo{} }

// GOpts returns empty selectors because fixture GPUs are already selected.
func (r runtimeTestDeviceInfo) GOpts() appconfig.DeviceOptions { return appconfig.DeviceOptions{} }

// SOpts returns empty selectors for the absent switch topology.
func (r runtimeTestDeviceInfo) SOpts() appconfig.DeviceOptions { return appconfig.DeviceOptions{} }

// COpts returns empty selectors for the absent CPU topology.
func (r runtimeTestDeviceInfo) COpts() appconfig.DeviceOptions { return appconfig.DeviceOptions{} }

// InfoType identifies this fixture as GPU topology.
func (r runtimeTestDeviceInfo) InfoType() dcgm.Field_Entity_Group { return dcgm.FE_GPU }

// IsCPUWatched rejects CPU monitoring in this GPU-only fixture.
func (r runtimeTestDeviceInfo) IsCPUWatched(uint) bool { return false }

// IsCoreWatched rejects core monitoring in this GPU-only fixture.
func (r runtimeTestDeviceInfo) IsCoreWatched(uint, uint) bool { return false }

// IsSwitchWatched rejects switch monitoring in this GPU-only fixture.
func (r runtimeTestDeviceInfo) IsSwitchWatched(uint) bool { return false }

// IsLinkWatched rejects link monitoring in this GPU-only fixture.
func (r runtimeTestDeviceInfo) IsLinkWatched(uint, uint) bool { return false }

// testDeviceInfo supplies two whole GPUs for assignment normalization.
func testDeviceInfo() runtimeTestDeviceInfo {
	return runtimeTestDeviceInfo{gpus: []deviceinfo.GPUInfo{
		{DeviceInfo: dcgm.Device{UUID: "GPU-0", GPU: 0}},
		{DeviceInfo: dcgm.Device{UUID: "GPU-1", GPU: 1}},
	}}
}

// testMIGDeviceInfo supplies GPU instance 8 under GPU-0 for MIG assignment resolution.
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

// TestDockerCompatibleRuntimeContainersByGPU checks explicit/all GPU requests and skips unresolved count-only requests.
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

// TestDockerCompatibleRuntimeNormalizesAssignments deduplicates aliases and skips unknown or disabled assignments.
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

// TestDockerCompatibleRuntimeNormalizesMixedUUIDAndIndexAssignments checks mixed identifiers assign both discovered GPUs.
func TestDockerCompatibleRuntimeNormalizesMixedUUIDAndIndexAssignments(t *testing.T) {
	rt := newTestDockerRuntime(t, map[string]string{
		"c1": `{"Id":"c1","Name":"/mixed","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-1,0"]}}`,
	})

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)

	assert.Equal(t, []containerInfo{{Name: "mixed"}}, got["GPU-0"])
	assert.Equal(t, []containerInfo{{Name: "mixed"}}, got["GPU-1"])
}

// TestDockerCompatibleRuntimeNormalizesMIGAssignments resolves the legacy MIG path to its GPU.instance key.
func TestDockerCompatibleRuntimeNormalizesMIGAssignments(t *testing.T) {
	rt := newTestDockerRuntime(t, map[string]string{
		"c1": `{"Id":"c1","Name":"/mig-container","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=MIG-GPU-0/8/5"]}}`,
	})

	got, err := rt.ContainersByGPU(context.Background(), testMIGDeviceInfo())
	require.NoError(t, err)

	assert.Equal(t, []containerInfo{{Name: "mig-container"}}, got["0.8"])
	assert.NotContains(t, got, "MIG-GPU-0/8/5")
}

// TestDockerCompatibleRuntimeSanitizesContainerName checks the byte limit and absence of newline control characters.
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

// TestDockerCompatibleRuntimeTruncatesContainerNameAtUTF8Boundary keeps a long Unicode label valid after truncation.
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

// FuzzContainerGPUKeys checks deterministic, unique device keys and bounded control-free UTF-8 labels.
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

	// Each generated input must keep device assignment and label invariants.
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

// TestDockerCompatibleRuntimeUsesStaleCacheOnRefreshFailure keeps previous labels when a later runtime refresh fails.
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

// TestDockerCompatibleRuntimeResetsStaleWarningAfterSuccessfulRefresh checks a recovered refresh permits the next outage warning.
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

// TestDockerCompatibleRuntimeSkipsContainerMissingAtInspect keeps surviving container labels after one inspect returns 404.
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

// TestDockerCompatibleRuntimeUsesListNameAndShortIDFallbacks checks missing inspect names fall back to list names or short IDs.
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

// TestDockerCompatibleRuntimeHonorsTimeout checks a blocked HTTP response is bounded by the request deadline.
func TestDockerCompatibleRuntimeHonorsTimeout(t *testing.T) {
	// Keep the handler blocked until the client's request deadline cancels it.
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

// TestDockerCompatibleRuntimeMalformedListDoesNotPoisonCache checks an initial decoding error does not prevent a later refresh.
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

// TestDockerCompatibleRuntimeReturnedMapDoesNotMutateCache checks caller mutation cannot change cached labels.
func TestDockerCompatibleRuntimeReturnedMapDoesNotMutateCache(t *testing.T) {
	rt := newTestDockerRuntime(t, map[string]string{
		"c1": `{"Id":"c1","Name":"/cached","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`,
	})
	rt.preserveIdentity = true

	got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	got["GPU-0"][0].Name = "mutated"
	got["GPU-0"][0].Identities[0].ID = "mutated"

	got, err = rt.ContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)
	assert.Equal(t, []containerInfo{{Name: "cached", Identities: []collector.ContainerIdentity{{ID: "c1", Name: "cached"}}}}, got["GPU-0"])
}

// newTestDockerRuntime serves supplied list/inspect fixtures through the runtime HTTP client.
func newTestDockerRuntime(t *testing.T, inspectByID map[string]string) *dockerCompatibleRuntime {
	t.Helper()
	rt, _ := newTestDockerRuntimeWithFailure(t, inspectByID)
	return rt
}

// newTestDockerRuntimeWithFailure serves list/inspect fixtures with a switch for simulated refresh failures.
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
				_, _ = fmt.Fprintf(w, `{"Id":%q}`, id)
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

// TestDockerCompatibleRuntimeRawIdentity verifies full runtime identity survives label sanitization and fallbacks.
func TestDockerCompatibleRuntimeRawIdentity(t *testing.T) {
	tests := []struct{ name, raw, id, legacy string }{
		{"reported name", "/trainer", "full-container-id", "trainer"},
		{"no name", "", "0123456789abcdef", "0123456789ab"},
		{"control character", "/train\ner", "full-id", "train_er"},
		{"long name", "/" + strings.Repeat("a", 140), "full-id", strings.Repeat("a", 128)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := newTestDockerRuntime(t, map[string]string{tt.id: fmt.Sprintf(`{"Id":%q,"Name":%q,"Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`, tt.id, tt.raw)})
			rt.preserveIdentity = true
			got, err := rt.ContainersByGPU(context.Background(), testDeviceInfo())
			require.NoError(t, err)
			require.Equal(t, []containerInfo{{Name: tt.legacy, Identities: []collector.ContainerIdentity{{ID: tt.id, Name: strings.TrimPrefix(tt.raw, "/")}}}}, got["GPU-0"])
		})
	}
}

// labelRequestsHandler fakes the runtime API for the startup check. It answers
// the limited list with listBody and the inspect request with inspectStatus. It
// answers the follow-up filtered list only when the request asks for all
// containers and filters on c1, using followUpStatus when set or stillListed.
func labelRequestsHandler(inspected *atomic.Bool, listBody string, inspectStatus, followUpStatus int, stillListed string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/json" && r.URL.RawQuery == "limit=1":
			_, _ = w.Write([]byte(listBody))
		case r.Method == http.MethodGet && r.URL.Path == "/containers/json" &&
			query.Get("all") == "1" && query.Get("filters") == `{"id":["c1"]}`:
			if followUpStatus != 0 {
				w.WriteHeader(followUpStatus)
				return
			}

			_, _ = w.Write([]byte(stillListed))
		case r.Method == http.MethodGet && r.URL.Path == "/containers/c1/json":
			inspected.Store(true)
			w.WriteHeader(inspectStatus)
			_, _ = w.Write([]byte(`{"Id":"c1"}`))
		default:
			http.NotFound(w, r)
		}
	})
}

// TestDockerCompatibleRuntimeCheckLabelRequests checks that the startup check
// reports a runtime that refuses the list or inspect requests container labels
// use, and accepts a container that went away between the two requests.
func TestDockerCompatibleRuntimeCheckLabelRequests(t *testing.T) {
	tests := []struct {
		name          string
		listBody      string
		inspectStatus int
		// followUpStatus is the HTTP status of the filtered list sent after a 404.
		followUpStatus int
		// stillListed is the answer to the filtered list for c1 after a 404.
		stillListed string
		wantInspect bool
		wantErr     string
	}{
		{
			name:          "list and inspect succeed",
			listBody:      `[{"Id":"c1"}]`,
			inspectStatus: http.StatusOK,
			wantInspect:   true,
		},
		{
			name:          "inspect refused",
			listBody:      `[{"Id":"c1"}]`,
			inspectStatus: http.StatusForbidden,
			wantInspect:   true,
			wantErr:       "container inspect request failed: runtime API /containers/c1/json returned HTTP 403",
		},
		{
			name:          "container went away",
			listBody:      `[{"Id":"c1"}]`,
			inspectStatus: http.StatusNotFound,
			stillListed:   `[]`,
			wantInspect:   true,
		},
		{
			name:          "inspect refused with 404 for a listed container",
			listBody:      `[{"Id":"c1"}]`,
			inspectStatus: http.StatusNotFound,
			stillListed:   `[{"Id":"c1"}]`,
			wantInspect:   true,
			wantErr: "container inspect request was refused for a listed container: " +
				"runtime API /containers/c1/json returned HTTP 404",
		},
		{
			name:           "inspect 404 and the follow-up list refused",
			listBody:       `[{"Id":"c1"}]`,
			inspectStatus:  http.StatusNotFound,
			followUpStatus: http.StatusForbidden,
			wantInspect:    true,
			wantErr: "container inspect request returned HTTP 404 and the follow-up container list failed: " +
				"runtime API /containers/c1/json returned HTTP 404\n" +
				"runtime API /containers/json?all=1&filters=%7B%22id%22%3A%5B%22c1%22%5D%7D returned HTTP 403",
		},
		{name: "list entry without an ID", listBody: `[{"Id":""}]`},
		{name: "no containers", listBody: `[]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var inspected atomic.Bool

			server := httptest.NewServer(labelRequestsHandler(&inspected, tt.listBody, tt.inspectStatus, tt.followUpStatus, tt.stillListed))
			t.Cleanup(server.Close)

			rt := &dockerCompatibleRuntime{baseURL: server.URL, client: server.Client(), timeout: time.Second}

			err := rt.checkLabelRequests(context.Background())

			assert.Equal(t, tt.wantInspect, inspected.Load())
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}

			assert.EqualError(t, err, tt.wantErr)
		})
	}
}

// inspectRefusingRuntime returns a runtime whose list names the given containers and whose inspect
// requests answer 404 except for the container IDs in inspectable. The follow-up list for a refused
// container is empty when its ID is in gone and shows the same containers otherwise.
func inspectRefusingRuntime(t *testing.T, list string, gone []string, inspectable ...string) *dockerCompatibleRuntime {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/json" {
			if filters := r.URL.Query().Get("filters"); filters != "" && slices.ContainsFunc(gone, func(id string) bool {
				return strings.Contains(filters, strconv.Quote(id))
			}) {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_, _ = w.Write([]byte(list))
			return
		}
		for _, id := range inspectable {
			if r.URL.Path == "/containers/"+id+"/json" {
				_, _ = fmt.Fprintf(w, `{"Id":%q,"Name":"/%s","Config":{"Env":["NVIDIA_VISIBLE_DEVICES=GPU-0"]}}`, id, id)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	return &dockerCompatibleRuntime{baseURL: server.URL, client: server.Client(), timeout: time.Second}
}

const everyInspectNotFoundWarning = "returned HTTP 404 for every container inspect request"

// TestDockerCompatibleRuntimeWarnsWhenEveryInspectIsRefused checks the warning appears once per interval.
func TestDockerCompatibleRuntimeWarnsWhenEveryInspectIsRefused(t *testing.T) {
	logs := captureLogs(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	rt := inspectRefusingRuntime(t, `[{"Id":"c1"},{"Id":"c2"}]`, nil)
	rt.inspectWarnings = warningLimiter{interval: 5 * time.Minute, now: clock.Now}

	for range 3 {
		_, err := rt.fetchContainersByGPU(context.Background(), testDeviceInfo())
		require.NoError(t, err)
	}

	assert.Equal(t, 1, logs.count(everyInspectNotFoundWarning), "one warning for three refreshes")

	clock.now = clock.now.Add(5 * time.Minute)
	_, err := rt.fetchContainersByGPU(context.Background(), testDeviceInfo())
	require.NoError(t, err)

	assert.Equal(t, 2, logs.count(everyInspectNotFoundWarning), "the warning repeats after its interval")
}

// TestDockerCompatibleRuntimeEveryInspectNotFoundWarning checks which refreshes warn that every
// inspect request returned 404.
func TestDockerCompatibleRuntimeEveryInspectNotFoundWarning(t *testing.T) {
	tests := []struct {
		name        string
		list        string
		gone        []string
		inspectable []string
		wantWarning int
	}{
		{name: "no containers listed", list: `[]`},
		{name: "only containers without an ID", list: `[{"Id":""}]`},
		{name: "one exited container beside a working one", list: `[{"Id":"c1"},{"Id":"gone"}]`, inspectable: []string{"c1"}},
		{name: "the only container exited during the refresh", list: `[{"Id":"c1"}]`, gone: []string{"c1"}},
		{name: "a refused container is still listed beside one that exited", list: `[{"Id":"c1"},{"Id":"c2"}]`, gone: []string{"c2"}, wantWarning: 1},
		{name: "a container without an ID is not counted", list: `[{"Id":""},{"Id":"c1"}]`, wantWarning: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			rt := inspectRefusingRuntime(t, tt.list, tt.gone, tt.inspectable...)

			_, err := rt.fetchContainersByGPU(context.Background(), testDeviceInfo())
			require.NoError(t, err)

			assert.Equal(t, tt.wantWarning, logs.count(everyInspectNotFoundWarning))
		})
	}
}
