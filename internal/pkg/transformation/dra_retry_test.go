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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// shortDRARetry makes the background DRA retry run in milliseconds.
func shortDRARetry(t *testing.T) {
	t.Helper()
	previous := draRetryInterval
	draRetryInterval = 5 * time.Millisecond
	t.Cleanup(func() { draRetryInterval = previous })
}

// stubKubernetesClient lets NewPodMapper build without a cluster.
func stubKubernetesClient(t *testing.T) {
	t.Helper()
	restorePodMapperSeams(t)
	inClusterConfigFunc = func() (*rest.Config, error) { return &rest.Config{}, nil }
	newKubernetesForConfigFunc = func(*rest.Config) (kubernetes.Interface, error) {
		return fakeClientWithResources(), nil
	}
}

// DRA setup that fails at startup, such as a forbidden API, is retried in the
// background, logged at most once per interval, and DRA labels start when it
// works.
func TestPodMapperRetriesDRASetupUntilItWorks(t *testing.T) {
	stubKubernetesClient(t)
	shortDRARetry(t)
	logs := captureLogs(t)

	var attempts atomic.Int32
	manager := newTestDRAManager()
	newDRAResourceSliceManagerFunc = func(context.Context, func()) (*DRAResourceSliceManager, error) {
		if attempts.Add(1) < 4 {
			return nil, errors.New("resourceslices is forbidden")
		}
		return manager, nil
	}

	pm := NewPodMapper(&appconfig.Config{KubernetesEnableDRA: true})
	defer pm.Stop()
	assert.Nil(t, pm.draManager(), "the first attempt failed")

	require.Eventually(t, func() bool { return pm.draManager() != nil }, 5*time.Second, 5*time.Millisecond)
	assert.Same(t, manager, pm.draManager())
	assert.Equal(t, int32(4), attempts.Load(), "no more attempts once DRA works")
	assert.Equal(t, 1, logs.count("DRA pod labels are still not available"), "two failed retries share one warning")
	require.Eventually(t, func() bool { return logs.count("DRA pod labels are now available") == 1 },
		5*time.Second, 5*time.Millisecond)
}

// A mapper that stops while DRA setup is failing stops retrying, and a manager
// that starts after the stop is stopped, not leaked.
func TestPodMapperStopEndsTheDRARetry(t *testing.T) {
	stubKubernetesClient(t)
	shortDRARetry(t)

	var attempts atomic.Int32
	newDRAResourceSliceManagerFunc = func(context.Context, func()) (*DRAResourceSliceManager, error) {
		attempts.Add(1)
		return nil, errors.New("api unreachable")
	}
	pm := NewPodMapper(&appconfig.Config{KubernetesEnableDRA: true})

	require.Eventually(t, func() bool { return attempts.Load() >= 2 }, 5*time.Second, 5*time.Millisecond)
	pm.Stop()
	settled := attempts.Load()
	time.Sleep(50 * time.Millisecond)
	assert.LessOrEqual(t, attempts.Load(), settled+1, "an attempt already running may finish, but none start")

	late := newTestDRAManager()
	assert.False(t, pm.installDRAManager(late))
	assert.Nil(t, pm.draManager())
	assert.True(t, late.stopped, "a manager that arrives after Stop is stopped")
}

// Missing in-cluster credentials are a setup failure too: DRA labels start once
// the background retry succeeds.
func TestPodMapperRetriesDRAAfterMissingInClusterConfig(t *testing.T) {
	restorePodMapperSeams(t)
	shortDRARetry(t)
	inClusterConfigFunc = func() (*rest.Config, error) { return nil, errors.New("not in a cluster") }
	manager := newTestDRAManager()
	var attempts atomic.Int32
	newDRAResourceSliceManagerFunc = func(context.Context, func()) (*DRAResourceSliceManager, error) {
		if attempts.Add(1) < 2 {
			return nil, errors.New("not yet")
		}
		return manager, nil
	}

	pm := NewPodMapper(&appconfig.Config{KubernetesEnableDRA: true})
	defer pm.Stop()

	require.Eventually(t, func() bool { return pm.draManager() == manager }, 5*time.Second, 5*time.Millisecond)
}

// A DRA setup attempt that is still waiting when the mapper stops is cancelled
// through the mapper's context, so Stop returns promptly and the attempt ends.
func TestPodMapperStopCancelsDRASetupInProgress(t *testing.T) {
	stubKubernetesClient(t)
	shortDRARetry(t)

	started := make(chan struct{})
	finished := make(chan error, 1)
	var attempts atomic.Int32
	newDRAResourceSliceManagerFunc = func(ctx context.Context, _ func()) (*DRAResourceSliceManager, error) {
		// The attempt at construction fails; the first retry then blocks until cancelled.
		if attempts.Add(1) == 1 {
			return nil, errors.New("api unreachable")
		}

		close(started)
		<-ctx.Done()
		finished <- ctx.Err()

		return nil, ctx.Err()
	}

	pm := NewPodMapper(&appconfig.Config{KubernetesEnableDRA: true})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the DRA retry never started a setup attempt")
	}

	stopped := make(chan struct{})
	go func() {
		pm.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return while a DRA setup attempt was waiting")
	}

	select {
	case err := <-finished:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("the DRA setup attempt was not cancelled by Stop")
	}

	pm.Stop()
}
