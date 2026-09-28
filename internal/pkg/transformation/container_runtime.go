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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

const (
	defaultContainerRuntimeTimeout   = 2 * time.Second
	maxContainerRuntimeResponseBytes = 10 << 20
	maxContainerLabelBytes           = 128
)

// containerRuntime returns runtime container labels keyed by GPU UUID, GPU index,
// or MIG GPU-instance key.
type containerRuntime interface {
	ContainersByGPU(ctx context.Context, deviceInfo deviceinfo.Provider) (map[string][]containerInfo, error)
}

// containerInfo carries the final sanitized value for the Prometheus container label.
type containerInfo struct {
	Name string
}

// httpDoer is the subset of http.Client used by the runtime client.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// containerRuntimeStatusError records non-2xx runtime API responses.
type containerRuntimeStatusError struct {
	path       string
	statusCode int
}

// Error formats the runtime status error for logs and callers.
func (e containerRuntimeStatusError) Error() string {
	return fmt.Sprintf("runtime API %s returned HTTP %d", e.path, e.statusCode)
}

// dockerCompatibleRuntime queries a Docker-compatible runtime API over HTTP.
type dockerCompatibleRuntime struct {
	baseURL  string
	client   httpDoer
	timeout  time.Duration
	cacheTTL time.Duration
	now      func() time.Time

	mu            sync.Mutex
	cached        map[string][]containerInfo
	cachedAt      time.Time
	staleWarnOnce sync.Once
}

// newDockerCompatibleRuntime creates a runtime client backed by a Unix socket.
func newDockerCompatibleRuntime(socketPath string, collectInterval time.Duration) *dockerCompatibleRuntime {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := &net.Dialer{}
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	if collectInterval <= 0 {
		collectInterval = time.Second
	}
	return &dockerCompatibleRuntime{
		baseURL:  "http://docker",
		client:   &http.Client{Transport: transport},
		timeout:  defaultContainerRuntimeTimeout,
		cacheTTL: collectInterval,
		now:      time.Now,
	}
}

// ContainersByGPU returns a cached or freshly fetched GPU-to-container map.
func (r *dockerCompatibleRuntime) ContainersByGPU(ctx context.Context, deviceInfo deviceinfo.Provider) (map[string][]containerInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFunc()()
	if r.cached != nil && r.cacheTTL > 0 && now.Sub(r.cachedAt) < r.cacheTTL {
		return cloneContainersByGPU(r.cached), nil
	}

	requestCtx := ctx
	cancel := func() {}
	if r.timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, r.timeout)
	}
	defer cancel()

	fresh, err := r.fetchContainersByGPU(requestCtx, deviceInfo)
	if err != nil {
		if r.cached != nil {
			r.staleWarnOnce.Do(func() {
				slog.Warn("container runtime refresh failed; using stale container label cache", slog.String(logging.ErrorKey, err.Error()))
			})
			slog.Debug("container runtime refresh failed; using stale container label cache", slog.String(logging.ErrorKey, err.Error()))
			return cloneContainersByGPU(r.cached), nil
		}
		return nil, err
	}

	r.staleWarnOnce = sync.Once{}
	r.cached = cloneContainersByGPU(fresh)
	r.cachedAt = now
	return cloneContainersByGPU(fresh), nil
}

// nowFunc returns the injectable clock used by cache TTL tests.
func (r *dockerCompatibleRuntime) nowFunc() func() time.Time {
	if r.now != nil {
		return r.now
	}
	return time.Now
}

// fetchContainersByGPU builds a fresh GPU-to-container map from runtime state.
func (r *dockerCompatibleRuntime) fetchContainersByGPU(ctx context.Context, deviceInfo deviceinfo.Provider) (map[string][]containerInfo, error) {
	var containers []dockerContainerListItem
	if err := r.getJSON(ctx, "/containers/json", &containers); err != nil {
		return nil, err
	}

	result := make(map[string][]containerInfo)
	for _, item := range containers {
		if item.ID == "" {
			continue
		}

		var inspect dockerContainerInspect
		inspectPath := fmt.Sprintf("/containers/%s/json", item.ID)
		if err := r.getJSON(ctx, inspectPath, &inspect); err != nil {
			var statusErr containerRuntimeStatusError
			if errors.As(err, &statusErr) && statusErr.statusCode == http.StatusNotFound {
				slog.Debug("container disappeared before runtime inspect", slog.String("container_id", item.ID))
				continue
			}
			return nil, err
		}
		if inspect.ID == "" {
			inspect.ID = item.ID
		}
		if inspect.Name == "" && len(item.Names) > 0 {
			inspect.Name = item.Names[0]
		}

		info := containerInfo{
			Name: sanitizeContainerLabel(containerLabelName(inspect.Name, inspect.ID)),
		}
		if info.Name == "" {
			continue
		}

		for _, key := range containerGPUKeys(inspect, deviceInfo) {
			result[key] = appendUniqueContainer(result[key], info)
		}
	}

	return result, nil
}

// getJSON performs a bounded runtime API GET and decodes a JSON response.
func (r *dockerCompatibleRuntime) getJSON(ctx context.Context, path string, dst any) error {
	baseURL := r.baseURL
	if baseURL == "" {
		baseURL = "http://docker"
	}
	client := r.client
	if client == nil {
		client = http.DefaultClient
	}

	fullURL := strings.TrimRight(baseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return fmt.Errorf("create runtime API request %s: %w", fullURL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("call runtime API %s: %w", fullURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxContainerRuntimeResponseBytes))
		return containerRuntimeStatusError{path: path, statusCode: resp.StatusCode}
	}

	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxContainerRuntimeResponseBytes))
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode runtime API %s response: %w", path, err)
	}
	return nil
}

// dockerContainerListItem mirrors the fields needed from GET /containers/json.
type dockerContainerListItem struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
}

// dockerContainerInspect mirrors the fields needed from GET /containers/<id>/json.
type dockerContainerInspect struct {
	ID         string                 `json:"Id"`
	Name       string                 `json:"Name"`
	Config     *dockerContainerConfig `json:"Config"`
	HostConfig *dockerHostConfig      `json:"HostConfig"`
}

// dockerContainerConfig contains environment variables from runtime inspect output.
type dockerContainerConfig struct {
	Env []string `json:"Env"`
}

// dockerHostConfig contains GPU device requests from runtime inspect output.
type dockerHostConfig struct {
	DeviceRequests []dockerDeviceRequest `json:"DeviceRequests"`
}

// dockerDeviceRequest mirrors runtime GPU device request fields.
type dockerDeviceRequest struct {
	Driver       string     `json:"Driver"`
	Count        int        `json:"Count"`
	DeviceIDs    []string   `json:"DeviceIDs"`
	Capabilities [][]string `json:"Capabilities"`
}

// containerGPUKeys returns GPU keys a container should label.
func containerGPUKeys(inspect dockerContainerInspect, deviceInfo deviceinfo.Provider) []string {
	var explicitDeviceIDs []string
	allDevices := false
	countOnly := false
	if inspect.HostConfig != nil {
		for _, request := range inspect.HostConfig.DeviceRequests {
			if !isNVIDIAGPUDeviceRequest(request) {
				continue
			}
			if len(request.DeviceIDs) > 0 {
				explicitDeviceIDs = append(explicitDeviceIDs, request.DeviceIDs...)
				continue
			}
			if request.Count < 0 {
				allDevices = true
				continue
			}
			if request.Count > 0 {
				countOnly = true
			}
		}
	}

	if len(explicitDeviceIDs) > 0 {
		return normalizeGPUAssignments(explicitDeviceIDs, deviceInfo)
	}
	if allDevices {
		return allGPUKeys(deviceInfo)
	}
	if countOnly {
		return nil
	}

	var env []string
	if inspect.Config != nil {
		env = inspect.Config.Env
	}
	tokens, envAll := parseVisibleDevices(env)
	if envAll {
		return allGPUKeys(deviceInfo)
	}
	return normalizeGPUAssignments(tokens, deviceInfo)
}

// isNVIDIAGPUDeviceRequest reports whether a device request targets NVIDIA GPUs.
func isNVIDIAGPUDeviceRequest(request dockerDeviceRequest) bool {
	if strings.EqualFold(request.Driver, "nvidia") {
		return true
	}
	for _, group := range request.Capabilities {
		for _, capability := range group {
			if strings.EqualFold(capability, "gpu") {
				return true
			}
		}
	}
	return false
}

// parseVisibleDevices reads NVIDIA_VISIBLE_DEVICES tokens from container env vars.
func parseVisibleDevices(env []string) ([]string, bool) {
	for _, entry := range env {
		value, ok := strings.CutPrefix(entry, "NVIDIA_VISIBLE_DEVICES=")
		if !ok {
			continue
		}
		var tokens []string
		all := false
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			switch strings.ToLower(token) {
			case "", "none", "void":
				continue
			case "all":
				all = true
			default:
				tokens = append(tokens, token)
			}
		}
		return tokens, all
	}
	return nil, false
}

// normalizeGPUAssignments converts runtime GPU tokens to exporter metric keys.
func normalizeGPUAssignments(tokens []string, deviceInfo deviceinfo.Provider) []string {
	seen := make(map[string]struct{})
	var keys []string
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		key := token
		if strings.HasPrefix(token, "MIG-") {
			resolved, ok := resolveMIGGPUAssignment(token, deviceInfo)
			if !ok {
				continue
			}
			key = resolved
		} else if !isGPUUUIDToken(token) {
			resolved, ok := resolveNumericGPUAssignment(token, deviceInfo)
			if !ok {
				continue
			}
			key = resolved
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

// isGPUUUIDToken reports whether a token is already a parent GPU UUID.
func isGPUUUIDToken(token string) bool {
	return strings.HasPrefix(token, "GPU-")
}

// resolveMIGGPUAssignment maps old-style MIG UUIDs to GPU.instance metric keys.
func resolveMIGGPUAssignment(token string, deviceInfo deviceinfo.Provider) (string, bool) {
	if deviceInfo == nil {
		return "", false
	}
	token, _, _ = strings.Cut(token, "::")
	payload, ok := strings.CutPrefix(token, "MIG-")
	if !ok {
		return "", false
	}
	parts := strings.Split(payload, "/")
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "GPU-") {
		return "", false
	}
	gpuInstanceID, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return "", false
	}
	for _, gpu := range deviceInfo.GPUs() {
		if gpu.DeviceInfo.UUID != parts[0] {
			continue
		}
		for _, instance := range gpu.GPUInstances {
			if uint64(instance.Info.NvmlInstanceId) == gpuInstanceID {
				return fmt.Sprintf("%d.%d", gpu.DeviceInfo.GPU, gpuInstanceID), true
			}
		}
		return "", false
	}
	return "", false
}

// resolveNumericGPUAssignment maps numeric runtime GPU IDs to GPU UUIDs when possible.
func resolveNumericGPUAssignment(token string, deviceInfo deviceinfo.Provider) (string, bool) {
	if deviceInfo == nil {
		return "", false
	}
	token = strings.TrimPrefix(token, "nvidia")
	idx, err := strconv.ParseUint(token, 10, 64)
	if err != nil {
		return "", false
	}
	for _, gpu := range deviceInfo.GPUs() {
		if uint64(gpu.DeviceInfo.GPU) != idx {
			continue
		}
		if gpu.DeviceInfo.UUID != "" {
			return gpu.DeviceInfo.UUID, true
		}
		return strconv.FormatUint(idx, 10), true
	}
	return "", false
}

// allGPUKeys returns every GPU key from the current device inventory.
func allGPUKeys(deviceInfo deviceinfo.Provider) []string {
	if deviceInfo == nil {
		return nil
	}
	keys := make([]string, 0, deviceInfo.GPUCount())
	for _, gpu := range deviceInfo.GPUs() {
		if gpu.DeviceInfo.UUID != "" {
			keys = append(keys, gpu.DeviceInfo.UUID)
			continue
		}
		keys = append(keys, strconv.FormatUint(uint64(gpu.DeviceInfo.GPU), 10))
	}
	return keys
}

// appendUniqueContainer appends a container label once per GPU key.
func appendUniqueContainer(containers []containerInfo, info containerInfo) []containerInfo {
	for _, existing := range containers {
		if existing.Name == info.Name {
			return containers
		}
	}
	return append(containers, info)
}

// containerLabelName chooses the runtime name or short ID fallback.
func containerLabelName(name string, id string) string {
	name = strings.TrimSpace(strings.TrimPrefix(name, "/"))
	if name != "" {
		return name
	}
	return shortContainerID(id)
}

// shortContainerID returns the conventional 12-character container ID prefix.
func shortContainerID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// sanitizeContainerLabel removes control characters and bounds label length.
func sanitizeContainerLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			builder.WriteByte('_')
			continue
		}
		builder.WriteRune(r)
	}
	return truncateStringBytes(builder.String(), maxContainerLabelBytes)
}

// truncateStringBytes trims a string to a byte limit without splitting UTF-8.
func truncateStringBytes(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}

// cloneContainersByGPU returns a detached copy of a GPU-to-container map.
func cloneContainersByGPU(in map[string][]containerInfo) map[string][]containerInfo {
	if in == nil {
		return nil
	}
	out := make(map[string][]containerInfo, len(in))
	for key, containers := range in {
		out[key] = append([]containerInfo(nil), containers...)
	}
	return out
}
