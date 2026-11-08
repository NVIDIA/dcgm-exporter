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
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

// containerListPath is the runtime API request that lists running containers.
const containerListPath = "/containers/json"

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
	Name       string
	Identities []collector.ContainerIdentity
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
	preserveIdentity bool
	baseURL          string
	client           httpDoer
	timeout          time.Duration
	cacheTTL         time.Duration
	now              func() time.Time

	mu            sync.Mutex
	cached        map[string][]containerInfo
	cachedAt      time.Time
	staleWarnOnce sync.Once

	inspectWarnings warningLimiter
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

// checkLabelRequests makes the requests that container labels depend on and
// reports a runtime that refuses or cannot answer them: the container list for
// one container, then the inspect request for that container. A proxy may allow
// the list but refuse inspect. When the list is empty there is nothing to
// inspect, so the list alone is the proof. It closes its connection afterwards.
func (r *dockerCompatibleRuntime) checkLabelRequests(ctx context.Context) error {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	if client, ok := r.client.(interface{ CloseIdleConnections() }); ok {
		defer client.CloseIdleConnections()
	}

	var containers []dockerContainerListItem
	if err := r.getJSON(ctx, containerListPath+"?limit=1", &containers); err != nil {
		return err
	}

	if len(containers) == 0 || containers[0].ID == "" {
		return nil
	}

	id := containers[0].ID
	var inspect dockerContainerInspect
	err := r.getJSON(ctx, containerInspectPath(id), &inspect)

	var statusErr containerRuntimeStatusError
	if errors.As(err, &statusErr) && statusErr.statusCode == http.StatusNotFound {
		// A 404 means the container stopped between the two requests, unless the
		// list still shows it: then a proxy refused the inspect request with 404.
		return r.confirmContainerGone(ctx, id, err)
	}

	if err != nil {
		return fmt.Errorf("container inspect request failed: %w", err)
	}

	return nil
}

// confirmContainerGone asks the container list, including stopped containers,
// for the container whose inspect request returned 404. It returns nil when the
// container is gone and an error when the list still shows it, because then the
// runtime refused the inspect request instead of the container going away.
func (r *dockerCompatibleRuntime) confirmContainerGone(ctx context.Context, id string, notFound error) error {
	filter, err := json.Marshal(map[string][]string{"id": {id}})
	if err != nil {
		return fmt.Errorf("container inspect request failed: %w", notFound)
	}

	query := url.Values{"all": {"1"}, "filters": {string(filter)}}

	var containers []dockerContainerListItem
	if err := r.getJSON(ctx, containerListPath+"?"+query.Encode(), &containers); err != nil {
		return fmt.Errorf("container inspect request returned HTTP 404 and the follow-up container list failed: %w",
			errors.Join(notFound, err))
	}

	if len(containers) > 0 {
		return fmt.Errorf("container inspect request was refused for a listed container: %w", notFound)
	}

	return nil
}

// containerInspectPath is the runtime API request that describes one container.
func containerInspectPath(id string) string {
	return fmt.Sprintf("/containers/%s/json", url.PathEscape(id))
}

// nowFunc returns the injectable clock used by cache TTL tests.
func (r *dockerCompatibleRuntime) nowFunc() func() time.Time {
	if r.now != nil {
		return r.now
	}
	return time.Now
}

// fetchContainersByGPU builds a fresh GPU-to-container map from runtime state.
// An inspect 404 for one container is treated as a container that went away.
// Startup cannot rule out a runtime that refuses inspect with 404 when no
// container was running at startup, so a refresh where every listed container
// returns 404 logs a rate-limited warning that labels are missing.
func (r *dockerCompatibleRuntime) fetchContainersByGPU(ctx context.Context, deviceInfo deviceinfo.Provider) (map[string][]containerInfo, error) {
	var containers []dockerContainerListItem
	if err := r.getJSON(ctx, containerListPath, &containers); err != nil {
		return nil, err
	}

	result := make(map[string][]containerInfo)
	listed := 0
	var notFoundIDs []string
	for _, item := range containers {
		if item.ID == "" {
			continue
		}

		listed++

		inspect, found, err := r.inspectContainer(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		if !found {
			notFoundIDs = append(notFoundIDs, item.ID)
			continue
		}

		info := r.containerInfo(item, inspect)
		if info.Name == "" {
			continue
		}

		for _, key := range containerGPUKeys(inspect, deviceInfo) {
			result[key] = appendUniqueContainer(result[key], info)
		}
	}

	if listed > 0 && len(notFoundIDs) == listed {
		r.warnIfEveryInspectNotFound(ctx, notFoundIDs)
	}

	return result, nil
}

// inspectContainer describes one listed container. found is false when the
// inspect request returns 404, which usually means the container went away.
func (r *dockerCompatibleRuntime) inspectContainer(ctx context.Context, id string) (dockerContainerInspect, bool, error) {
	var inspect dockerContainerInspect
	err := r.getJSON(ctx, containerInspectPath(id), &inspect)

	var statusErr containerRuntimeStatusError
	if errors.As(err, &statusErr) && statusErr.statusCode == http.StatusNotFound {
		slog.Debug("container disappeared before runtime inspect", slog.String("container_id", id))
		return inspect, false, nil
	}

	return inspect, err == nil, err
}

// errInspectNotFound stands for the 404 answer when confirming a refused container.
var errInspectNotFound = errors.New("container inspect request returned HTTP 404")

// warnIfEveryInspectNotFound logs, at most once per interval, that the runtime
// listed containers but answered every inspect request with HTTP 404. It first
// asks the list about the refused containers: when all of them are gone, they
// exited during the refresh and nothing is reported.
func (r *dockerCompatibleRuntime) warnIfEveryInspectNotFound(ctx context.Context, ids []string) {
	refused := slices.ContainsFunc(ids, func(id string) bool {
		return r.confirmContainerGone(ctx, id, errInspectNotFound) != nil
	})
	if !refused || !r.inspectWarnings.allow() {
		return
	}

	slog.Warn("container runtime listed containers but returned HTTP 404 for every container inspect request; "+
		"container labels are missing until inspect requests work. The exporter keeps checking.",
		slog.Int("containers", len(ids)))
}

// containerInfo resolves runtime name and ID fallbacks, retaining raw identity only for OTLP.
func (r *dockerCompatibleRuntime) containerInfo(item dockerContainerListItem, inspect dockerContainerInspect) containerInfo {
	if inspect.ID == "" {
		inspect.ID = item.ID
	}
	if inspect.Name == "" && len(item.Names) > 0 {
		inspect.Name = item.Names[0]
	}
	info := containerInfo{
		Name: sanitizeContainerLabel(containerLabelName(inspect.Name, inspect.ID)),
	}
	if r.preserveIdentity {
		info.Identities = []collector.ContainerIdentity{{ID: inspect.ID, Name: strings.TrimPrefix(inspect.Name, "/")}}
	}
	return info
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
	for i, existing := range containers {
		if existing.Name != info.Name {
			continue
		}
		containers[i].Identities = mergeContainerIdentities(existing.Identities, info.Identities)
		return containers
	}
	info.Identities = slices.Clone(info.Identities)
	return append(containers, info)
}

// mergeContainerIdentities combines raw identities without mutating either input slice.
func mergeContainerIdentities(existing, incoming []collector.ContainerIdentity) []collector.ContainerIdentity {
	identities := slices.Clone(existing)
	for _, identity := range incoming {
		if !slices.Contains(identities, identity) {
			identities = append(identities, identity)
		}
	}
	return identities
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
		for i := range out[key] {
			out[key][i].Identities = slices.Clone(out[key][i].Identities)
		}
	}
	return out
}
