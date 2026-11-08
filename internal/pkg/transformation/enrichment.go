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
	"log/slog"
	"net"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// RequiredEnrichmentError reports a source that the configuration requires
// but that cannot be used. Source says which switch asked for it.
type RequiredEnrichmentError struct {
	Source appconfig.EnrichmentSource
	Err    error
}

// Error returns the message of the underlying error.
func (e *RequiredEnrichmentError) Error() string { return e.Err.Error() }

// Unwrap returns the underlying error.
func (e *RequiredEnrichmentError) Unwrap() error { return e.Err }

// EnrichmentWarning is a source that is not usable yet and why. Source lets the
// caller add where the source's settings came from.
type EnrichmentWarning struct {
	Source  appconfig.EnrichmentSource
	Message string
}

// EnrichmentResolution is the outcome of the startup checks: the settings to
// use and the warnings to log. It never changes the Config it came from.
type EnrichmentResolution struct {
	Kubernetes          bool
	ContainerLabels     bool
	KubernetesEnableDRA bool
	HPCJobMappingDir    string
	Warnings            []EnrichmentWarning
}

// EnrichmentWait says how startup waits for a source that the configuration
// requires: the total time allowed for all required sources, how often to check
// again, and how often to log that it is still waiting.
type EnrichmentWait struct {
	Grace    time.Duration
	Poll     time.Duration
	LogEvery time.Duration
}

// DefaultEnrichmentWait is the wait that production startup uses. It is not a
// configuration setting. Tests pass shorter values.
func DefaultEnrichmentWait() EnrichmentWait {
	return EnrichmentWait{Grace: appconfig.RequiredEnrichmentWait, Poll: 2 * time.Second, LogEvery: 10 * time.Second}
}

// ResolveEnrichment checks each enabled workload-label source at startup, before
// the HTTP server starts. It is the only startup code that checks the host for
// enrichment sources, and it lives in this package to reuse the pod-resources
// socket check.
//
// Precedence between the sources is described on appconfig.EnrichmentModes.
// A source that a flag or an environment variable turned on
// is checked like an auto source.
//
//   - An auto source never fails startup. When it is not usable, one warning is
//     logged and the source stays on: the runtime keeps checking it, logs the
//     failure at most every few minutes, and adds the labels once the source
//     is usable.
//   - A required source is checked again every wait.Poll until wait.Grace has
//     passed, counted once for all required sources. If one is still not usable
//     the result is a *RequiredEnrichmentError that carries the last error.
//   - One exception to "auto stays on": Kubernetes wins over container labels
//     only if its socket is usable now. If it is not and container labels are
//     on, this process uses container labels and Kubernetes stays off.
//   - A source switched off is never checked.
//
// Losing a source after startup never stops the exporter, whatever its mode.
func ResolveEnrichment(ctx context.Context, c *appconfig.Config, wait EnrichmentWait) (EnrichmentResolution, error) {
	r := &enrichmentResolver{
		cfg:   c,
		modes: c.EnrichmentModes,
		wait:  wait,
		res: EnrichmentResolution{
			Kubernetes:          c.Kubernetes,
			ContainerLabels:     c.ContainerLabels,
			KubernetesEnableDRA: c.KubernetesEnableDRA,
			HPCJobMappingDir:    c.HPCJobMappingDir,
		},
		kubernetesOff: "Kubernetes is not enabled",
	}
	r.deadline = time.Now().Add(wait.Grace)

	// Run the checks in order: Kubernetes decides whether DRA and container
	// labels can run.
	steps := []func(context.Context) error{r.resolveKubernetes, r.resolveDRA, r.resolveContainer, r.resolveHPCJobs}
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return r.res, err
		}

		if err := step(ctx); err != nil {
			return r.res, err
		}
	}

	return r.res, nil
}

// enrichmentResolver holds what the startup checks share while they run in
// order: the config, its modes, the resolution so far, the one deadline for
// every required source, and why Kubernetes is not active when it is not.
type enrichmentResolver struct {
	cfg      *appconfig.Config
	modes    appconfig.EnrichmentModes
	wait     EnrichmentWait
	deadline time.Time
	res      EnrichmentResolution
	// kubernetesOff says why Kubernetes is not active, for the DRA messages.
	kubernetesOff string
}

// warn records a warning for a source that is not usable or was skipped.
func (r *enrichmentResolver) warn(source appconfig.EnrichmentSource, message string) {
	r.res.Warnings = append(r.res.Warnings, EnrichmentWarning{Source: source, Message: message})
}

// startupMode is the mode startup applies to an enabled source. Only the config
// file can require a source; every other way of turning one on means auto.
func startupMode(mode appconfig.EnrichmentMode) appconfig.EnrichmentMode {
	if mode == appconfig.EnrichmentRequired {
		return appconfig.EnrichmentRequired
	}

	return appconfig.EnrichmentAuto
}

// probe runs check once for an auto source. For a required source it runs it
// again every wait.Poll until the shared deadline, and logs progress every
// wait.LogEvery. It returns the last check error, or the context error when
// startup is stopped while waiting.
func (r *enrichmentResolver) probe(ctx context.Context, source appconfig.EnrichmentSource, mode appconfig.EnrichmentMode, check func(context.Context) error) error {
	err := r.runCheck(ctx, check, mode, true)
	if err == nil || mode != appconfig.EnrichmentRequired || ctx.Err() != nil {
		return err
	}

	// A required source that failed its first check: keep checking until the
	// shared deadline.
	lastLog := time.Time{}

	for err != nil {
		remaining := time.Until(r.deadline)
		if remaining <= 0 {
			return r.withGraceNote(err)
		}

		if time.Since(lastLog) >= r.wait.LogEvery {
			slog.Info("Waiting for a required enrichment source",
				slog.String("key", source.Key()),
				slog.Duration("remaining", remaining.Round(time.Second)),
				slog.String("error", err.Error()))

			lastLog = time.Now()
		}

		if waitErr := waitOrStop(ctx, min(r.wait.Poll, remaining)); waitErr != nil {
			return waitErr
		}

		if time.Until(r.deadline) <= 0 {
			continue // out of budget: report the last real error
		}

		err = r.recheck(ctx, check, mode, err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	return nil
}

// recheck runs one more check of a required source and returns the error to
// keep. A check that did not finish within its limit says less about the source
// than one that did, so the error of the last check that ran to its end stays.
func (r *enrichmentResolver) recheck(ctx context.Context, check func(context.Context) error, mode appconfig.EnrichmentMode, previous error) error {
	next := r.runCheck(ctx, check, mode, false)

	if errors.Is(next, errCheckUnfinished) && previous != nil && !errors.Is(previous, errCheckUnfinished) {
		return previous
	}

	return next
}

// withGraceNote adds the length of the wait to the error of a source that was
// still unavailable when the wait ended. Without a wait the error is unchanged.
func (r *enrichmentResolver) withGraceNote(err error) error {
	if r.wait.Grace > 0 {
		return fmt.Errorf("%w; still unavailable after waiting %s", err, r.wait.Grace)
	}

	return err
}

// waitOrStop sleeps for d. It returns the context error when startup is stopped
// first.
func waitOrStop(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// checkTimeout bounds one check, so a check that hangs is abandoned and tried
// again instead of using up the whole wait, and an auto source, which is never
// retried at startup, cannot hold startup back.
const checkTimeout = 10 * time.Second

// errCheckUnfinished is the error of a check that was abandoned when its limit ended.
var errCheckUnfinished = errors.New("check did not finish")

// runCheck runs one check under a limit: checkTimeout, or less when the shared
// budget of a required source has less time left. The first check of a source
// always gets at least a second, so a zero budget still tests the source. The
// check gets a context that ends with the limit or when startup is stopped. A
// check that does not honor its context is abandoned, not waited for.
func (r *enrichmentResolver) runCheck(parent context.Context, check func(context.Context) error, mode appconfig.EnrichmentMode, first bool) error {
	// Work out the limit: a required source cannot outlast the shared deadline.
	limit := checkTimeout
	if mode == appconfig.EnrichmentRequired {
		remaining := time.Until(r.deadline)
		if first {
			remaining = max(remaining, time.Second)
		}

		limit = min(limit, remaining)
	}

	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()

	// Run the check in its own goroutine so a check that hangs can be abandoned.
	done := make(chan error, 1)
	go func() { done <- check(ctx) }()

	select {
	case err := <-done:
		return unfinishedAtLimit(parent, ctx, err, limit)
	case <-ctx.Done():
		// A check that finished as the limit ran out still counts.
		select {
		case err := <-done:
			return unfinishedAtLimit(parent, ctx, err, limit)
		default:
		}

		if err := parent.Err(); err != nil {
			return err
		}

		return fmt.Errorf("%w within %s", errCheckUnfinished, limit)
	}
}

// unfinishedAtLimit marks a check that returned only because its limit ended,
// such as an HTTP request cut off by ctx, as errCheckUnfinished. The check's own
// error stays wrapped, so its detail (the API it called, for example) is kept.
// Any other result is returned unchanged.
func unfinishedAtLimit(parent, ctx context.Context, err error, limit time.Duration) error {
	if err != nil && parent.Err() == nil && ctx.Err() != nil && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w within %s: %w", errCheckUnfinished, limit, err)
	}

	return err
}

// required builds the startup error for a required source that is not usable.
func (r *enrichmentResolver) required(ctx context.Context, source appconfig.EnrichmentSource, what string, err error) error {
	if stop := ctx.Err(); stop != nil {
		return fmt.Errorf("stopped while waiting for %s: %w", source.Key(), stop)
	}

	return &RequiredEnrichmentError{
		Source: source,
		Err:    fmt.Errorf("%s is true but %s: %w", source.Key(), what, err),
	}
}

// keepOn records the warning for an auto source that is not usable yet.
func (r *enrichmentResolver) keepOn(source appconfig.EnrichmentSource, what string, err error) {
	r.warn(source, fmt.Sprintf("%s is not usable at startup and stays on; labels appear when it becomes usable: %v",
		what, err))
}

// resolveKubernetes checks the kubelet pod-resources socket when Kubernetes
// labels are on, and decides whether Kubernetes or container labels win.
func (r *enrichmentResolver) resolveKubernetes(ctx context.Context) error {
	if !r.res.Kubernetes {
		return nil
	}

	mode := startupMode(r.modes.Kubernetes)

	// A required container source displaces an auto Kubernetes source.
	if appconfig.ContainerDisplacesKubernetes(r.cfg) {
		r.res.Kubernetes = false
		r.kubernetesOff = "Kubernetes was turned off because " + appconfig.EnrichmentContainer.Key() + " is true"
		r.warn(appconfig.EnrichmentKubernetes, "Kubernetes labels are skipped: "+r.kubernetesOff)

		return nil
	}

	err := r.probe(ctx, appconfig.EnrichmentKubernetes, mode, func(context.Context) error { return r.checkKubelet() })
	if err == nil {
		return nil
	}

	if mode == appconfig.EnrichmentRequired {
		return r.required(ctx, appconfig.EnrichmentKubernetes, "Kubernetes is unavailable", err)
	}

	// Kubernetes wins over container labels only when its socket is usable at
	// startup. Otherwise container labels, if they are on and their socket is
	// usable, are used for this process and Kubernetes stays off.
	if r.containerLabelsCanRun(ctx) {
		r.res.Kubernetes = false
		r.kubernetesOff = "Kubernetes was skipped because it is unavailable"
		r.warn(appconfig.EnrichmentKubernetes, fmt.Sprintf(
			"Kubernetes labels are skipped because Kubernetes is unavailable at startup, "+
				"so container labels are used and Kubernetes stays off until the exporter restarts: %v", err))

		return nil
	}

	r.keepOn(appconfig.EnrichmentKubernetes, "Kubernetes", err)

	return nil
}

// containerSocketName names the container runtime socket in errors.
const containerSocketName = "container runtime socket"

// containerLabelsCanRun reports whether container labels are on and their
// runtime socket is usable now, so giving up on Kubernetes for them is safe.
func (r *enrichmentResolver) containerLabelsCanRun(ctx context.Context) bool {
	socket := r.cfg.ContainerRuntimeSocket
	return r.res.ContainerLabels && strings.TrimSpace(socket) != "" &&
		checkContainerRuntime(ctx, socket) == nil
}

// checkContainerRuntime requires a runtime socket that answers the container
// list and inspect requests container labels use. A socket that accepts the
// connection but refuses a request, such as a proxy that returns HTTP 403,
// would leave the labels missing at run time.
func checkContainerRuntime(ctx context.Context, socket string) error {
	if err := checkUnixSocket(socket, containerSocketName); err != nil {
		return err
	}

	if err := checkSocketAccess(socket, containerSocketName); err != nil {
		return err
	}

	if err := newDockerCompatibleRuntime(socket, 0).checkLabelRequests(ctx); err != nil {
		return fmt.Errorf("%s %q cannot be used: %w", containerSocketName, socket, err)
	}

	return nil
}

// checkKubelet requires a usable pod-resources socket.
func (r *enrichmentResolver) checkKubelet() error {
	if err := validatePodResourcesSocketPath(r.cfg.PodResourcesKubeletSocket); err != nil {
		return err
	}

	return checkSocketAccess(r.cfg.PodResourcesKubeletSocket, "pod-resources kubelet socket")
}

// resolveDRA checks DRA labels, which need active Kubernetes labels and a
// cluster that serves the ResourceSlice API.
func (r *enrichmentResolver) resolveDRA(ctx context.Context) error {
	if !r.res.KubernetesEnableDRA {
		return nil
	}

	mode := startupMode(r.modes.DRA)

	// Without Kubernetes labels DRA cannot run: fail if required, else skip it.
	if !r.res.Kubernetes {
		if mode == appconfig.EnrichmentRequired {
			return &RequiredEnrichmentError{
				Source: appconfig.EnrichmentDRA,
				Err: fmt.Errorf("%s is true but Kubernetes labels are not active: %s",
					appconfig.EnrichmentDRA.Key(), r.kubernetesOff),
			}
		}

		r.res.KubernetesEnableDRA = false
		r.warn(appconfig.EnrichmentDRA, "DRA labels are skipped because Kubernetes labels are not active: "+r.kubernetesOff)

		return nil
	}

	// The pod mapper starts without DRA labels when DRA setup fails and keeps
	// retrying, so startup checks with the same client, API lookup and first
	// list the mapper uses.
	// Required DRA labels also need the kubelet allocations, which are read from
	// the pod-resources socket, so a required source waits for that socket too.
	err := r.probe(ctx, appconfig.EnrichmentDRA, mode, func(ctx context.Context) error {
		if mode == appconfig.EnrichmentRequired {
			if err := r.checkKubelet(); err != nil {
				return err
			}
		}

		return checkDRAAvailable(ctx)
	})
	if err == nil {
		return nil
	}

	if mode == appconfig.EnrichmentRequired {
		return r.required(ctx, appconfig.EnrichmentDRA, "DRA is unavailable", err)
	}

	r.keepOn(appconfig.EnrichmentDRA, "DRA", err)

	return nil
}

// resolveContainer checks the container runtime socket. It is skipped when
// container labels are off or Kubernetes labels are active.
func (r *enrichmentResolver) resolveContainer(ctx context.Context) error {
	if !r.res.ContainerLabels || r.res.Kubernetes {
		return nil
	}

	mode := startupMode(r.modes.Container)
	socket := r.cfg.ContainerRuntimeSocket

	err := r.probe(ctx, appconfig.EnrichmentContainer, mode, func(ctx context.Context) error {
		return checkContainerRuntime(ctx, socket)
	})
	if err == nil {
		return nil
	}

	if mode == appconfig.EnrichmentRequired {
		return r.required(ctx, appconfig.EnrichmentContainer, "the container runtime is unavailable", err)
	}

	r.keepOn(appconfig.EnrichmentContainer, "the container runtime", err)

	return nil
}

// resolveHPCJobs checks the HPC job mapping directory when one is set.
func (r *enrichmentResolver) resolveHPCJobs(ctx context.Context) error {
	dir := r.res.HPCJobMappingDir
	if strings.TrimSpace(dir) == "" {
		return nil
	}

	mode := startupMode(r.modes.HPCJobs)

	err := r.probe(ctx, appconfig.EnrichmentHPCJobs, mode, func(context.Context) error { return checkDirectory(dir) })
	if err == nil {
		return nil
	}

	if mode == appconfig.EnrichmentRequired {
		return r.required(ctx, appconfig.EnrichmentHPCJobs, "HPC job mapping is unavailable", err)
	}

	r.keepOn(appconfig.EnrichmentHPCJobs, "the HPC job mapping directory", err)

	return nil
}

// draAvailabilityTimeout bounds the one ResourceSlice list that proves the
// exporter may read the API.
const draAvailabilityTimeout = 10 * time.Second

// checkDRAAvailable requires a Kubernetes client, a cluster that serves the
// ResourceSlice API, and permission to list ResourceSlices, which is the first
// call the DRA label source makes. A forbidden or unreachable API fails here
// instead of leaving the pod mapper to start without DRA labels.
func checkDRAAvailable(parent context.Context) error {
	client, err := getKubeClientFunc()
	if err != nil {
		return fmt.Errorf("error getting kube client: %w", err)
	}

	version := detectResourceSliceAPIVersion(client)
	if version == resourceSliceAPIUnknown {
		return errors.New("ResourceSlice API not served by cluster")
	}

	ctx, cancel := context.WithTimeout(parent, draAvailabilityTimeout)
	defer cancel()

	opts := metav1.ListOptions{
		Limit:         1,
		FieldSelector: fields.OneTermEqualSelector("spec.driver", DRAGPUDriverName).String(),
	}

	// List one slice through whichever API version the cluster serves.
	if version == resourceSliceAPIV1 {
		_, err = client.ResourceV1().ResourceSlices().List(ctx, opts)
	} else {
		_, err = client.ResourceV1beta1().ResourceSlices().List(ctx, opts)
	}

	if err != nil {
		return fmt.Errorf("cannot list %s resourceslices (check RBAC: list/watch on resource.k8s.io/resourceslices): %w",
			version, err)
	}

	return nil
}

// socketAccessTimeout bounds the connection attempt that tests socket access.
const socketAccessTimeout = time.Second

// checkSocketAccess reports a socket the exporter cannot use. A file-type check
// passes a socket the exporter is not allowed to connect to, or one with no
// listener, and the labels would then be missing at run time.
func checkSocketAccess(path, what string) error {
	conn, err := net.DialTimeout("unix", path, socketAccessTimeout)
	if err != nil {
		return fmt.Errorf("%s %q cannot be used: %w", what, path, err)
	}
	return conn.Close()
}

// checkDirectory requires a directory the HPC mapper can open: one that
// exists, can be read, and whose final component is not a symlink.
func checkDirectory(path string) error {
	dir, err := openMappingDirectory(path)
	if err != nil {
		return err
	}

	return dir.Close()
}
