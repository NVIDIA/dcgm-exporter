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
	"errors"
	"fmt"
	"net"
	"net/http"
	sysOS "os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// serveSocket creates a Unix socket that answers the runtime API container
// list with an empty list until the test ends. A kubelet socket check only
// connects, so the same socket also serves as a kubelet socket.
func serveSocket(t *testing.T) string {
	t.Helper()
	return serveRuntimeAPI(t, runtimeAPI(http.StatusOK, "[]"))
}

// serveRuntimeAPI creates a Unix socket served by handler until the test ends.
func serveRuntimeAPI(t *testing.T, handler http.Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.sock")
	server, err := startRuntimeAPI(t, path, handler)
	require.NoError(t, err)
	t.Cleanup(func() { server.Close() })
	return path
}

// runtimeAPI answers GET /containers/json?limit=1, the bounded container list
// the startup check makes, with status and body, and anything else with 404, so
// a check that asks for the wrong thing, such as the unbounded list, fails.
func runtimeAPI(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != containerListPath || r.URL.RawQuery != "limit=1" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		// A check cut off at its limit hangs up mid-reply; that write error is expected.
		_, _ = w.Write([]byte(body))
	})
}

// startRuntimeAPI serves handler on a Unix socket at path. It reports a serving
// failure with t.Errorf, which a timer goroutine may call, so it can start the
// server after a delay.
func startRuntimeAPI(t testing.TB, path string, handler http.Handler) (*http.Server, error) {
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}

	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: handler}
	go func() {
		if err := server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve runtime API on %s: %v", path, err)
		}
	}()

	return server, nil
}

// silentSocket listens until the test ends but never accepts, so a client
// connects through the listen backlog and then waits for an answer forever.
func silentSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "silent.sock")
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	return path
}

// deadSocket leaves a socket file behind with no process listening on it.
func deadSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dead.sock")
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, l.Close())
	return path
}

// forbiddenResourceSliceList serves the ResourceSlice API but denies listing it.
func forbiddenResourceSliceList() kubernetes.Interface {
	client := fakeClientWithResources(&metav1.APIResourceList{
		GroupVersion: resourcev1.SchemeGroupVersion.String(),
		APIResources: []metav1.APIResource{resourceSlicesAPIResource},
	})
	client.PrependReactor("list", "resourceslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceslices"}, "", errors.New("rbac denied"))
	})
	return client
}

// shortWait keeps the tests for a required source that never appears fast.
var shortWait = EnrichmentWait{Grace: 50 * time.Millisecond, Poll: 5 * time.Millisecond, LogEvery: 10 * time.Millisecond}

// allModes gives every source the same mode.
func allModes(mode appconfig.EnrichmentMode) appconfig.EnrichmentModes {
	return appconfig.EnrichmentModes{Kubernetes: mode, Container: mode, DRA: mode, HPCJobs: mode}
}

func TestResolveEnrichment(t *testing.T) {
	const (
		auto     = appconfig.EnrichmentAuto
		required = appconfig.EnrichmentRequired
		unset    = appconfig.EnrichmentUnset
	)
	missing := filepath.Join(t.TempDir(), "missing")
	socket := serveSocket(t)
	dir := t.TempDir()
	regularFile := filepath.Join(t.TempDir(), "file")
	require.NoError(t, sysOS.WriteFile(regularFile, nil, 0o600))

	previous := getKubeClientFunc
	t.Cleanup(func() { getKubeClientFunc = previous })
	servesResourceSlices := fakeClientWithResources(&metav1.APIResourceList{
		GroupVersion: resourcev1.SchemeGroupVersion.String(),
		APIResources: []metav1.APIResource{resourceSlicesAPIResource},
	})

	unreadable := filepath.Join(t.TempDir(), "unreadable")
	require.NoError(t, sysOS.Mkdir(unreadable, 0o000))
	t.Cleanup(func() { _ = sysOS.Chmod(unreadable, 0o700) })
	canReadAnyway := sysOS.Geteuid() == 0 // permissions do not stop root
	staleSocket := deadSocket(t)
	deniedSocket := serveSocket(t)
	require.NoError(t, sysOS.Chmod(deniedSocket, 0o000))
	forbiddenRuntime := serveRuntimeAPI(t, runtimeAPI(http.StatusForbidden, `{"message":"denied"}`))
	notDockerRuntime := serveRuntimeAPI(t, runtimeAPI(http.StatusOK, "<html>not a runtime</html>"))
	silentRuntime := silentSocket(t)

	type want struct {
		kubernetes, container, dra bool
		hpcDir                     string
		warnings                   []string // each must appear in the warnings, which must be this many
		err                        string
		source                     appconfig.EnrichmentSource
	}
	tests := []struct {
		name      string
		cfg       appconfig.Config
		skip      bool                                 // the host cannot produce the case, such as root reading a 0000 directory
		draClient func() (kubernetes.Interface, error) // nil serves the ResourceSlice API
		want      want
	}{
		{
			name: "auto Kubernetes and DRA stay on with one warning when the kubelet socket is missing",
			cfg: appconfig.Config{
				Kubernetes: true, KubernetesEnableDRA: true, PodResourcesKubeletSocket: missing,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: auto, DRA: auto},
			},
			want: want{kubernetes: true, dra: true, warnings: []string{"Kubernetes is not usable at startup and stays on"}},
		},
		{
			name: "auto container and HPC stay on with a warning while their paths are missing",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: missing, HPCJobMappingDir: missing,
				EnrichmentModes: appconfig.EnrichmentModes{Container: auto, HPCJobs: auto},
			},
			want: want{
				container: true, hpcDir: missing,
				warnings: []string{"container runtime is not usable at startup and stays on", "HPC job mapping directory is not usable"},
			},
		},
		{
			name: "auto container stays on with a warning when the path is not a socket",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: regularFile,
				EnrichmentModes: appconfig.EnrichmentModes{Container: auto},
			},
			want: want{container: true, warnings: []string{"must be a Unix socket"}},
		},
		{
			name: "auto HPC stays on with a warning when the path is not a directory",
			cfg: appconfig.Config{
				HPCJobMappingDir: regularFile, EnrichmentModes: appconfig.EnrichmentModes{HPCJobs: auto},
			},
			want: want{hpcDir: regularFile, warnings: []string{"not a directory"}},
		},
		{
			name: "auto with everything available keeps Kubernetes over container",
			cfg: appconfig.Config{
				Kubernetes: true, ContainerLabels: true, KubernetesEnableDRA: true, HPCJobMappingDir: dir,
				PodResourcesKubeletSocket: socket, ContainerRuntimeSocket: socket,
				EnrichmentModes: allModes(auto),
			},
			want: want{kubernetes: true, container: true, dra: true, hpcDir: dir},
		},
		{
			name: "container is used when auto Kubernetes is unavailable",
			cfg: appconfig.Config{
				Kubernetes: true, ContainerLabels: true,
				PodResourcesKubeletSocket: missing, ContainerRuntimeSocket: socket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: auto, Container: auto},
			},
			want: want{container: true, warnings: []string{"Kubernetes labels are skipped"}},
		},
		{
			name: "required Kubernetes fails when the socket is missing",
			cfg: appconfig.Config{
				Kubernetes: true, PodResourcesKubeletSocket: missing,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: required},
			},
			want: want{
				err:    "enrichment.kubernetes is true but Kubernetes is unavailable",
				source: appconfig.EnrichmentKubernetes,
			},
		},
		{
			name: "required container fails when the runtime socket is missing",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: missing,
				EnrichmentModes: appconfig.EnrichmentModes{Container: required},
			},
			want: want{
				err:    "enrichment.container is true but the container runtime is unavailable",
				source: appconfig.EnrichmentContainer,
			},
		},
		{
			name: "required container fails when the path is not a socket",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: regularFile,
				EnrichmentModes: appconfig.EnrichmentModes{Container: required},
			},
			want: want{err: "must be a Unix socket", source: appconfig.EnrichmentContainer},
		},
		{
			name: "required container beats auto Kubernetes",
			cfg: appconfig.Config{
				Kubernetes: true, ContainerLabels: true,
				PodResourcesKubeletSocket: socket, ContainerRuntimeSocket: socket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: auto, Container: required},
			},
			want: want{container: true, warnings: []string{"Kubernetes was turned off because"}},
		},
		{
			name: "required container beats Kubernetes that a flag turned on",
			cfg: appconfig.Config{
				Kubernetes: true, ContainerLabels: true, ContainerRuntimeSocket: socket,
				PodResourcesKubeletSocket: socket,
				EnrichmentModes:           appconfig.EnrichmentModes{Container: required},
			},
			want: want{container: true, warnings: []string{"Kubernetes was turned off because"}},
		},
		{
			name: "required DRA waits for the kubelet socket that auto Kubernetes leaves unchecked",
			cfg: appconfig.Config{
				Kubernetes: true, KubernetesEnableDRA: true, PodResourcesKubeletSocket: missing,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: auto, DRA: required},
			},
			want: want{err: "DRA is unavailable: stat", source: appconfig.EnrichmentDRA},
		},
		{
			name: "required DRA says when required container displaced Kubernetes",
			cfg: appconfig.Config{
				Kubernetes: true, ContainerLabels: true, KubernetesEnableDRA: true, ContainerRuntimeSocket: socket,
				EnrichmentModes: appconfig.EnrichmentModes{
					Kubernetes: auto, Container: required, DRA: required,
				},
			},
			want: want{
				err:    "Kubernetes was turned off because metrics[0].enrichment.container is true",
				source: appconfig.EnrichmentDRA,
			},
		},
		{
			name: "required Kubernetes fails when the socket denies access",
			cfg: appconfig.Config{
				Kubernetes: true, PodResourcesKubeletSocket: deniedSocket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: required},
			},
			skip: canReadAnyway,
			want: want{err: "cannot be used", source: appconfig.EnrichmentKubernetes},
		},
		{
			name: "required container fails when the runtime socket denies access",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: deniedSocket,
				EnrichmentModes: appconfig.EnrichmentModes{Container: required},
			},
			skip: canReadAnyway,
			want: want{err: "cannot be used", source: appconfig.EnrichmentContainer},
		},
		{
			name: "auto container stays on with a warning when the runtime socket denies access",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: deniedSocket,
				EnrichmentModes: appconfig.EnrichmentModes{Container: auto},
			},
			skip: canReadAnyway,
			want: want{container: true, warnings: []string{"cannot be used"}},
		},
		{
			name: "required HPC fails when the directory cannot be read",
			cfg: appconfig.Config{
				HPCJobMappingDir: unreadable, EnrichmentModes: appconfig.EnrichmentModes{HPCJobs: required},
			},
			skip: canReadAnyway,
			want: want{
				err:    "enrichment.hpc_jobs is true but HPC job mapping is unavailable",
				source: appconfig.EnrichmentHPCJobs,
			},
		},
		{
			name: "auto HPC stays on with a warning when the directory cannot be read",
			cfg: appconfig.Config{
				HPCJobMappingDir: unreadable, EnrichmentModes: appconfig.EnrichmentModes{HPCJobs: auto},
			},
			skip: canReadAnyway,
			want: want{hpcDir: unreadable, warnings: []string{"HPC job mapping directory is not usable"}},
		},
		{
			name: "required HPC fails when the directory is missing",
			cfg: appconfig.Config{
				HPCJobMappingDir: missing, EnrichmentModes: appconfig.EnrichmentModes{HPCJobs: required},
			},
			want: want{
				err:    "enrichment.hpc_jobs is true but HPC job mapping is unavailable",
				source: appconfig.EnrichmentHPCJobs,
			},
		},
		{
			name: "required Kubernetes, DRA and HPC succeed when their sources exist",
			cfg: appconfig.Config{
				Kubernetes: true, KubernetesEnableDRA: true, HPCJobMappingDir: dir, PodResourcesKubeletSocket: socket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: required, DRA: required, HPCJobs: required},
			},
			want: want{kubernetes: true, dra: true, hpcDir: dir},
		},
		{
			name: "auto DRA stays on with a warning when the API is not served",
			cfg: appconfig.Config{
				Kubernetes: true, KubernetesEnableDRA: true, PodResourcesKubeletSocket: socket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: required, DRA: auto},
			},
			draClient: func() (kubernetes.Interface, error) { return fakeClientWithResources(), nil },
			want: want{
				kubernetes: true, dra: true, warnings: []string{"DRA is not usable at startup and stays on"},
			},
		},
		{
			name: "required DRA fails without a kube client",
			cfg: appconfig.Config{
				Kubernetes: true, KubernetesEnableDRA: true, PodResourcesKubeletSocket: socket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: required, DRA: required},
			},
			draClient: func() (kubernetes.Interface, error) { return nil, errors.New("no kube client") },
			want: want{
				err:    "DRA is unavailable: error getting kube client: no kube client",
				source: appconfig.EnrichmentDRA,
			},
		},
		{
			name: "required DRA fails when the cluster does not serve the ResourceSlice API",
			cfg: appconfig.Config{
				Kubernetes: true, KubernetesEnableDRA: true, PodResourcesKubeletSocket: socket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: required, DRA: required},
			},
			draClient: func() (kubernetes.Interface, error) { return fakeClientWithResources(), nil },
			want: want{
				err:    "DRA is unavailable: ResourceSlice API not served by cluster",
				source: appconfig.EnrichmentDRA,
			},
		},
		{
			name: "required DRA fails when listing ResourceSlices is forbidden",
			cfg: appconfig.Config{
				Kubernetes: true, KubernetesEnableDRA: true, PodResourcesKubeletSocket: socket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: required, DRA: required},
			},
			draClient: func() (kubernetes.Interface, error) { return forbiddenResourceSliceList(), nil },
			want: want{
				err:    "DRA is unavailable: cannot list resource.k8s.io/v1 resourceslices (check RBAC",
				source: appconfig.EnrichmentDRA,
			},
		},
		{
			name: "auto DRA stays on with a warning when listing ResourceSlices is forbidden",
			cfg: appconfig.Config{
				Kubernetes: true, KubernetesEnableDRA: true, PodResourcesKubeletSocket: socket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: required, DRA: auto},
			},
			draClient: func() (kubernetes.Interface, error) { return forbiddenResourceSliceList(), nil },
			want: want{
				kubernetes: true, dra: true, warnings: []string{"cannot list resource.k8s.io/v1 resourceslices"},
			},
		},
		{
			name: "required Kubernetes fails when nothing listens on the socket",
			cfg: appconfig.Config{
				Kubernetes: true, PodResourcesKubeletSocket: staleSocket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: required},
			},
			want: want{err: "cannot be used", source: appconfig.EnrichmentKubernetes},
		},
		{
			name: "auto Kubernetes stays on with a warning when nothing listens on the socket",
			cfg: appconfig.Config{
				Kubernetes: true, PodResourcesKubeletSocket: staleSocket,
				EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: auto},
			},
			want: want{kubernetes: true, warnings: []string{"cannot be used"}},
		},
		{
			name: "required container fails when nothing listens on the runtime socket",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: staleSocket,
				EnrichmentModes: appconfig.EnrichmentModes{Container: required},
			},
			want: want{err: "cannot be used", source: appconfig.EnrichmentContainer},
		},
		{
			name: "auto container stays on with a warning when nothing listens on the runtime socket",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: staleSocket,
				EnrichmentModes: appconfig.EnrichmentModes{Container: auto},
			},
			want: want{container: true, warnings: []string{"cannot be used"}},
		},
		{
			name: "required container fails when the runtime API refuses the container list",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: forbiddenRuntime,
				EnrichmentModes: appconfig.EnrichmentModes{Container: required},
			},
			want: want{err: "returned HTTP 403", source: appconfig.EnrichmentContainer},
		},
		{
			name: "auto container stays on with a warning when the runtime API refuses the container list",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: forbiddenRuntime,
				EnrichmentModes: appconfig.EnrichmentModes{Container: auto},
			},
			want: want{container: true, warnings: []string{"returned HTTP 403"}},
		},
		{
			name: "required container fails when the socket answers with something other than the runtime API",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: notDockerRuntime,
				EnrichmentModes: appconfig.EnrichmentModes{Container: required},
			},
			want: want{err: "decode runtime API", source: appconfig.EnrichmentContainer},
		},
		{
			name: "auto container stays on with a warning when the runtime never answers",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: silentRuntime,
				EnrichmentModes: appconfig.EnrichmentModes{Container: auto},
			},
			want: want{container: true, warnings: []string{"deadline exceeded"}},
		},
		{
			name: "a runtime API that refuses the container list does not displace unavailable Kubernetes",
			cfg: appconfig.Config{
				Kubernetes: true, ContainerLabels: true,
				PodResourcesKubeletSocket: missing, ContainerRuntimeSocket: forbiddenRuntime,
				EnrichmentModes: allModes(unset),
			},
			want: want{
				kubernetes: true, container: true,
				warnings: []string{"Kubernetes is not usable at startup and stays on"},
			},
		},
		{
			name: "required container succeeds when the runtime socket exists",
			cfg: appconfig.Config{
				ContainerLabels: true, ContainerRuntimeSocket: socket,
				EnrichmentModes: appconfig.EnrichmentModes{Container: required},
			},
			want: want{container: true},
		},
		{
			name: "flag-style settings are checked like auto and stay on with warnings",
			cfg: appconfig.Config{
				Kubernetes: true, KubernetesEnableDRA: true, HPCJobMappingDir: missing,
				PodResourcesKubeletSocket: missing,
				EnrichmentModes:           allModes(unset),
			},
			want: want{
				kubernetes: true, dra: true, hpcDir: missing,
				warnings: []string{"Kubernetes is not usable at startup", "HPC job mapping directory is not usable"},
			},
		},
		{
			name: "flag-style Kubernetes gives way to flag-style container labels when the kubelet is missing",
			cfg: appconfig.Config{
				Kubernetes: true, ContainerLabels: true, KubernetesEnableDRA: true,
				PodResourcesKubeletSocket: missing, ContainerRuntimeSocket: socket,
				EnrichmentModes: allModes(unset),
			},
			want: want{
				container: true,
				warnings: []string{
					"Kubernetes labels are skipped because Kubernetes is unavailable at startup",
					"DRA labels are skipped because Kubernetes labels are not active",
				},
			},
		},
		{
			name: "container labels with an unusable socket do not displace unavailable Kubernetes",
			cfg: appconfig.Config{
				Kubernetes: true, ContainerLabels: true, PodResourcesKubeletSocket: missing,
				ContainerRuntimeSocket: missing, EnrichmentModes: allModes(unset),
			},
			want: want{
				kubernetes: true, container: true,
				warnings: []string{"Kubernetes is not usable at startup and stays on"},
			},
		},
		{
			name: "container labels without a socket do not displace unavailable Kubernetes",
			cfg: appconfig.Config{
				Kubernetes: true, ContainerLabels: true, PodResourcesKubeletSocket: missing,
				EnrichmentModes: allModes(unset),
			},
			want: want{
				kubernetes: true, container: true,
				warnings: []string{"Kubernetes is not usable at startup and stays on"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skip {
				t.Skip("this host ignores file permissions")
			}
			getKubeClientFunc = func() (kubernetes.Interface, error) { return servesResourceSlices, nil }
			if tt.draClient != nil {
				getKubeClientFunc = tt.draClient
			}
			cfg := tt.cfg
			before := cfg
			res, err := ResolveEnrichment(context.Background(), &cfg, shortWait)
			assert.Equal(t, before, cfg, "resolution must not change the config")
			if tt.want.err != "" {
				require.ErrorContains(t, err, tt.want.err)
				var required *RequiredEnrichmentError
				require.ErrorAs(t, err, &required)
				assert.Equal(t, tt.want.source, required.Source)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want.kubernetes, res.Kubernetes)
			assert.Equal(t, tt.want.container, res.ContainerLabels)
			assert.Equal(t, tt.want.dra, res.KubernetesEnableDRA)
			assert.Equal(t, tt.want.hpcDir, res.HPCJobMappingDir)
			require.Len(t, res.Warnings, len(tt.want.warnings))
			for i, text := range tt.want.warnings {
				assert.Contains(t, res.Warnings[i].Message, text)
			}
		})
	}
}

// A required source that shows up while startup waits lets startup continue.
func TestResolveEnrichmentWaitsForARequiredSourceThatAppears(t *testing.T) {
	const delay = 60 * time.Millisecond
	wait := EnrichmentWait{Grace: 5 * time.Second, Poll: 5 * time.Millisecond, LogEvery: time.Second}

	previous := getKubeClientFunc
	t.Cleanup(func() { getKubeClientFunc = previous })

	tests := []struct {
		name  string
		setup func(t *testing.T) (cfg appconfig.Config, appear func())
		check func(t *testing.T, res EnrichmentResolution)
	}{
		{
			name: "Kubernetes kubelet socket",
			setup: func(t *testing.T) (appconfig.Config, func()) {
				path := filepath.Join(t.TempDir(), "kubelet.sock")
				return appconfig.Config{
					Kubernetes: true, PodResourcesKubeletSocket: path,
					EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: appconfig.EnrichmentRequired},
				}, func() {
					if l, err := net.Listen("unix", path); err == nil {
						t.Cleanup(func() { l.Close() })
					}
				}
			},
			check: func(t *testing.T, res EnrichmentResolution) { assert.True(t, res.Kubernetes) },
		},
		{
			name: "container runtime socket",
			setup: func(t *testing.T) (appconfig.Config, func()) {
				path := filepath.Join(t.TempDir(), "runtime.sock")
				return appconfig.Config{
					ContainerLabels: true, ContainerRuntimeSocket: path,
					EnrichmentModes: appconfig.EnrichmentModes{Container: appconfig.EnrichmentRequired},
				}, func() {
					server, err := startRuntimeAPI(t, path, runtimeAPI(http.StatusOK, "[]"))
					if err != nil {
						t.Errorf("start runtime API: %v", err)
						return
					}

					t.Cleanup(func() { server.Close() })
				}
			},
			check: func(t *testing.T, res EnrichmentResolution) { assert.True(t, res.ContainerLabels) },
		},
		{
			name: "HPC job mapping directory",
			setup: func(t *testing.T) (appconfig.Config, func()) {
				path := filepath.Join(t.TempDir(), "jobs")
				return appconfig.Config{
					HPCJobMappingDir: path,
					EnrichmentModes:  appconfig.EnrichmentModes{HPCJobs: appconfig.EnrichmentRequired},
				}, func() {
					_ = sysOS.Mkdir(path, 0o700)
				}
			},
			check: func(t *testing.T, res EnrichmentResolution) { assert.NotEmpty(t, res.HPCJobMappingDir) },
		},
		{
			name: "DRA API",
			setup: func(t *testing.T) (appconfig.Config, func()) {
				var ready atomic.Bool
				getKubeClientFunc = func() (kubernetes.Interface, error) {
					if !ready.Load() {
						return nil, errors.New("API server not reachable")
					}
					return fakeClientWithResources(&metav1.APIResourceList{
						GroupVersion: resourcev1.SchemeGroupVersion.String(),
						APIResources: []metav1.APIResource{resourceSlicesAPIResource},
					}), nil
				}
				socket := serveSocket(t)
				return appconfig.Config{
					Kubernetes: true, KubernetesEnableDRA: true, PodResourcesKubeletSocket: socket,
					EnrichmentModes: appconfig.EnrichmentModes{
						Kubernetes: appconfig.EnrichmentRequired, DRA: appconfig.EnrichmentRequired,
					},
				}, func() {
					ready.Store(true)
				}
			},
			check: func(t *testing.T, res EnrichmentResolution) { assert.True(t, res.KubernetesEnableDRA) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, appear := tt.setup(t)
			appeared := make(chan struct{})
			time.AfterFunc(delay, func() {
				defer close(appeared)
				appear()
			})

			start := time.Now()
			res, err := ResolveEnrichment(context.Background(), &cfg, wait)
			<-appeared

			require.NoError(t, err)
			assert.GreaterOrEqual(t, time.Since(start), delay, "startup must have waited for the source")
			assert.Empty(t, res.Warnings)
			tt.check(t, res)
		})
	}
}

// A required source that never appears fails after the grace period with the
// YAML key and the last error.
func TestResolveEnrichmentFailsAfterTheGracePeriod(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "kubelet.sock")
	wait := EnrichmentWait{Grace: 200 * time.Millisecond, Poll: 10 * time.Millisecond, LogEvery: time.Second}
	cfg := appconfig.Config{
		Kubernetes: true, PodResourcesKubeletSocket: missing,
		EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: appconfig.EnrichmentRequired},
	}

	start := time.Now()
	_, err := ResolveEnrichment(context.Background(), &cfg, wait)
	elapsed := time.Since(start)

	var required *RequiredEnrichmentError
	require.ErrorAs(t, err, &required)
	assert.Equal(t, appconfig.EnrichmentKubernetes, required.Source)
	assert.ErrorContains(t, err, "metrics[0].enrichment.kubernetes is true")
	assert.ErrorContains(t, err, missing)
	assert.ErrorContains(t, err, "still unavailable after waiting 200ms")
	assert.GreaterOrEqual(t, elapsed, wait.Grace)
}

// The grace period is one budget for every required source, not one each. The
// first source appears late in the budget, so a budget of its own for the
// second would restart the whole wait.
func TestResolveEnrichmentSharesOneGracePeriodAcrossSources(t *testing.T) {
	dir := t.TempDir()
	lateSocket := filepath.Join(dir, "runtime.sock")
	missing := filepath.Join(dir, "missing")
	wait := EnrichmentWait{Grace: time.Second, Poll: 10 * time.Millisecond, LogEvery: time.Second}
	cfg := appconfig.Config{
		ContainerLabels: true, ContainerRuntimeSocket: lateSocket, HPCJobMappingDir: missing,
		EnrichmentModes: appconfig.EnrichmentModes{
			Container: appconfig.EnrichmentRequired, HPCJobs: appconfig.EnrichmentRequired,
		},
	}
	listened := make(chan *http.Server, 1)
	time.AfterFunc(400*time.Millisecond, func() {
		server, err := startRuntimeAPI(t, lateSocket, runtimeAPI(http.StatusOK, "[]"))
		if err != nil {
			t.Errorf("start the late runtime API: %v", err)
		}

		listened <- server
	})

	start := time.Now()
	_, err := ResolveEnrichment(context.Background(), &cfg, wait)
	elapsed := time.Since(start)
	if server := <-listened; server != nil {
		t.Cleanup(func() { server.Close() })
	}

	var required *RequiredEnrichmentError
	require.ErrorAs(t, err, &required)
	assert.Equal(t, appconfig.EnrichmentHPCJobs, required.Source, "the container source was usable")
	assert.GreaterOrEqual(t, elapsed, wait.Grace)
	assert.Less(t, elapsed, wait.Grace+350*time.Millisecond, "a second budget would end near 1.4s")
}

// A check that hangs is cut off by the budget, and the exporter can be stopped
// while one runs.
func TestResolveEnrichmentBoundsAHangingCheck(t *testing.T) {
	previous := getKubeClientFunc
	t.Cleanup(func() { getKubeClientFunc = previous })
	release := make(chan struct{})
	var started, finished atomic.Int32
	t.Cleanup(func() {
		// Abandoned checks read getKubeClientFunc; let them finish before it is restored.
		close(release)
		assert.Eventually(t, func() bool { return started.Load() == finished.Load() }, 5*time.Second, 5*time.Millisecond)
	})
	getKubeClientFunc = func() (kubernetes.Interface, error) {
		started.Add(1)
		defer finished.Add(1)
		<-release
		return nil, errors.New("released")
	}
	socket := serveSocket(t)
	cfg := appconfig.Config{
		Kubernetes: true, KubernetesEnableDRA: true, PodResourcesKubeletSocket: socket,
		EnrichmentModes: appconfig.EnrichmentModes{
			Kubernetes: appconfig.EnrichmentRequired, DRA: appconfig.EnrichmentRequired,
		},
	}
	wait := EnrichmentWait{Grace: 200 * time.Millisecond, Poll: 10 * time.Millisecond, LogEvery: time.Second}

	start := time.Now()
	_, err := ResolveEnrichment(context.Background(), &cfg, wait)
	var required *RequiredEnrichmentError
	require.ErrorAs(t, err, &required)
	assert.ErrorContains(t, err, "did not finish")
	assert.Less(t, time.Since(start), 3*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start = time.Now()
	_, err = ResolveEnrichment(ctx, &cfg, EnrichmentWait{Grace: time.Minute, Poll: time.Second, LogEvery: time.Minute})
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 3*time.Second)
}

// A later check that the budget cuts short must not replace the error of the
// last check that finished, because it says nothing about the source.
func TestProbeKeepsTheLastFinishedErrorWhenALaterCheckRunsOutOfBudget(t *testing.T) {
	wait := EnrichmentWait{Grace: 100 * time.Millisecond, Poll: 10 * time.Millisecond, LogEvery: time.Second}
	r := &enrichmentResolver{wait: wait, deadline: time.Now().Add(wait.Grace)}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	var calls atomic.Int32
	err := r.probe(context.Background(), appconfig.EnrichmentKubernetes, appconfig.EnrichmentRequired,
		func(context.Context) error {
			if calls.Add(1) == 1 {
				return errors.New("socket is missing")
			}

			<-release // never ends within the limit, so the check is always abandoned
			return nil
		})

	require.Error(t, err)
	assert.ErrorContains(t, err, "socket is missing")
	assert.ErrorContains(t, err, "still unavailable after waiting 100ms")
	assert.NotContains(t, err.Error(), "did not finish")
	assert.Greater(t, calls.Load(), int32(1), "a later check ran")
}

// A check that honors its context and returns the deadline error, as an HTTP
// request to the runtime does, did not finish, so a later check cut off by the
// budget cannot replace the last real error. Other results stay as they are.
func TestUnfinishedAtLimit(t *testing.T) {
	ended, cancel := context.WithDeadline(context.Background(), time.Now())
	defer cancel()
	stopped, stop := context.WithCancel(context.Background())
	stop()
	cutOff := fmt.Errorf("call runtime API: %w", context.DeadlineExceeded)
	refused := errors.New("runtime API returned HTTP 403")

	assert.ErrorIs(t, unfinishedAtLimit(context.Background(), ended, cutOff, time.Second), errCheckUnfinished)
	assert.ErrorContains(t, unfinishedAtLimit(context.Background(), ended, cutOff, time.Second), "call runtime API",
		"the check's own error stays in the message")
	assert.Equal(t, refused, unfinishedAtLimit(context.Background(), ended, refused, time.Second), "a real error stays")
	assert.Equal(t, cutOff, unfinishedAtLimit(context.Background(), context.Background(), cutOff, time.Second),
		"the check's own shorter timeout is a real result while the limit still runs")
	assert.Equal(t, cutOff, unfinishedAtLimit(stopped, ended, cutOff, time.Second), "a stopped startup is not an unfinished check")
	assert.NoError(t, unfinishedAtLimit(context.Background(), ended, nil, time.Second))
}

// An auto source is checked once. It never waits, however long the grace is.
func TestResolveEnrichmentDoesNotWaitForAutoSources(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	wait := EnrichmentWait{Grace: time.Minute, Poll: time.Minute, LogEvery: time.Minute}
	cfg := appconfig.Config{
		Kubernetes: true, ContainerLabels: false, HPCJobMappingDir: missing, PodResourcesKubeletSocket: missing,
		EnrichmentModes: appconfig.EnrichmentModes{Kubernetes: appconfig.EnrichmentAuto, HPCJobs: appconfig.EnrichmentAuto},
	}

	start := time.Now()
	res, err := ResolveEnrichment(context.Background(), &cfg, wait)

	require.NoError(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.True(t, res.Kubernetes)
	assert.Equal(t, missing, res.HPCJobMappingDir)
	assert.Len(t, res.Warnings, 2)
}

// Stopping the exporter during the wait ends it at once, without a source error.
func TestResolveEnrichmentStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	wait := EnrichmentWait{Grace: time.Minute, Poll: 5 * time.Millisecond, LogEvery: time.Second}
	cfg := appconfig.Config{
		HPCJobMappingDir: filepath.Join(t.TempDir(), "missing"),
		EnrichmentModes:  appconfig.EnrichmentModes{HPCJobs: appconfig.EnrichmentRequired},
	}

	start := time.Now()
	_, err := ResolveEnrichment(ctx, &cfg, wait)

	require.ErrorIs(t, err, context.Canceled)
	var required *RequiredEnrichmentError
	assert.False(t, errors.As(err, &required))
	assert.Less(t, time.Since(start), 5*time.Second)
}

// Progress is logged when the wait starts and then at most once per LogEvery.
func TestResolveEnrichmentLogsWaitProgressAtMostEveryInterval(t *testing.T) {
	logs := captureLogs(t)
	wait := EnrichmentWait{Grace: 250 * time.Millisecond, Poll: 5 * time.Millisecond, LogEvery: 100 * time.Millisecond}
	cfg := appconfig.Config{
		HPCJobMappingDir: filepath.Join(t.TempDir(), "missing"),
		EnrichmentModes:  appconfig.EnrichmentModes{HPCJobs: appconfig.EnrichmentRequired},
	}

	_, err := ResolveEnrichment(context.Background(), &cfg, wait)
	require.Error(t, err)

	lines := logs.count("Waiting for a required enrichment source")
	assert.GreaterOrEqual(t, lines, 2)
	assert.LessOrEqual(t, lines, 4, "250ms of waiting with a 100ms log interval")
	assert.Contains(t, logs.String(), "key=metrics[0].enrichment.hpc_jobs")
}

// TestDefaultEnrichmentWaitUsesTheGraceTheReportsName checks that production
// startup waits as long as config explain says it does.
func TestDefaultEnrichmentWaitUsesTheGraceTheReportsName(t *testing.T) {
	assert.Equal(t, appconfig.RequiredEnrichmentWait, DefaultEnrichmentWait().Grace)
}
