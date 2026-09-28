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

package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const coordinatorTestTimeout = 5 * time.Second

type scrapeResult struct {
	response []byte
	err      error
}

type waitObservedContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitObservedContext) Done() <-chan struct{} {
	c.once.Do(func() {
		close(c.waiting)
	})

	return c.Context.Done()
}

type controlledWaitContext struct {
	done    chan struct{}
	waiting chan struct{}

	waitingOnce sync.Once
	errMu       sync.Mutex
	err         error
}

func newControlledWaitContext() *controlledWaitContext {
	return &controlledWaitContext{
		done:    make(chan struct{}),
		waiting: make(chan struct{}),
	}
}

func (c *controlledWaitContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *controlledWaitContext) Done() <-chan struct{} {
	c.waitingOnce.Do(func() {
		close(c.waiting)
	})

	return c.done
}

func (c *controlledWaitContext) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()

	return c.err
}

func (c *controlledWaitContext) Value(any) any {
	return nil
}

func (c *controlledWaitContext) finish(err error) {
	c.errMu.Lock()
	c.err = err
	c.errMu.Unlock()
	close(c.done)
}

func TestScrapeCoordinatorAdmission(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
	}{
		{name: "capacity 1", capacity: 1},
		{name: "capacity 2", capacity: 2},
		{name: "capacity 16", capacity: 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coordinator := newScrapeCoordinator(tt.capacity)

			for range tt.capacity {
				require.True(t, coordinator.tryAcquire())
			}
			assert.False(t, coordinator.tryAcquire())

			coordinator.release()
			require.True(t, coordinator.tryAcquire())

			for range tt.capacity {
				coordinator.release()
			}
		})
	}
}

func TestScrapeCoordinatorProducerOutcomes(t *testing.T) {
	producerErr := errors.New("producer failed")
	response := []byte("metrics")

	tests := []struct {
		name          string
		produce       func() ([]byte, error)
		wantResponse  []byte
		wantErr       error
		wantErrString string
		wantLog       []string
	}{
		{
			name: "success",
			produce: func() ([]byte, error) {
				return response, nil
			},
			wantResponse: response,
		},
		{
			name: "error",
			produce: func() ([]byte, error) {
				return nil, producerErr
			},
			wantErr: producerErr,
		},
		{
			name: "panic",
			produce: func() ([]byte, error) {
				panic("producer panicked")
			},
			wantErrString: "producer panicked",
			wantLog:       []string{"Scrape producer panicked.", "panic=\"producer panicked\"", "stacktrace="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coordinator := newScrapeCoordinator(1)
			var logs bytes.Buffer
			if len(tt.wantLog) > 0 {
				previousLogger := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
				t.Cleanup(func() {
					slog.SetDefault(previousLogger)
				})
			}

			got, err := coordinator.do(context.Background(), tt.produce)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantErrString != "":
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrString)
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.wantResponse, got)
				require.NotEmpty(t, got)
				assert.Equal(t, &response[0], &got[0])
			}
			for _, want := range tt.wantLog {
				assert.Contains(t, logs.String(), want)
			}
		})
	}
}

func TestScrapeCoordinatorContextOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		wantErr error
	}{
		{
			name:    "canceled",
			wantErr: context.Canceled,
		},
		{
			name:    "deadline exceeded",
			wantErr: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coordinator := newScrapeCoordinator(2)
			producerStarted := make(chan struct{})
			releaseProducer := make(chan struct{})
			primaryResult := make(chan scrapeResult, 1)
			var producerCalls atomic.Int32

			go func() {
				response, err := coordinator.do(context.Background(), func() ([]byte, error) {
					producerCalls.Add(1)
					close(producerStarted)
					<-releaseProducer

					return []byte("metrics"), nil
				})
				primaryResult <- scrapeResult{response: response, err: err}
			}()

			waitForSignal(t, producerStarted)

			waiterContext := newControlledWaitContext()
			waiterResult := make(chan scrapeResult, 1)
			go func() {
				response, err := coordinator.do(waiterContext, func() ([]byte, error) {
					producerCalls.Add(1)

					return []byte("unexpected"), nil
				})
				waiterResult <- scrapeResult{response: response, err: err}
			}()

			waitForSignal(t, waiterContext.waiting)
			waiterContext.finish(tt.wantErr)

			result := waitForScrapeResult(t, waiterResult)
			assert.Nil(t, result.response)
			require.ErrorIs(t, result.err, tt.wantErr)
			assert.Equal(t, int32(1), producerCalls.Load())

			close(releaseProducer)
			result = waitForScrapeResult(t, primaryResult)
			require.NoError(t, result.err)
			assert.Equal(t, []byte("metrics"), result.response)
			assert.Equal(t, int32(1), producerCalls.Load())
		})
	}
}

func TestScrapeCoordinatorWaitContextOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		wantErr error
	}{
		{name: "canceled", wantErr: context.Canceled},
		{name: "deadline exceeded", wantErr: context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coordinator := newScrapeCoordinator(1)
			producerStarted := make(chan struct{})
			releaseProducer := make(chan struct{})
			producerResult := make(chan scrapeResult, 1)
			released := false
			t.Cleanup(func() {
				if !released {
					close(releaseProducer)
				}
			})

			go func() {
				response, err := coordinator.do(context.Background(), func() ([]byte, error) {
					close(producerStarted)
					<-releaseProducer

					return []byte("metrics"), nil
				})
				producerResult <- scrapeResult{response: response, err: err}
			}()

			waitForSignal(t, producerStarted)
			waitContext := newControlledWaitContext()
			waitResult := make(chan error, 1)
			go func() {
				waitResult <- coordinator.wait(waitContext)
			}()

			waitForSignal(t, waitContext.waiting)
			waitContext.finish(tt.wantErr)
			select {
			case err := <-waitResult:
				require.ErrorIs(t, err, tt.wantErr)
			case <-time.After(coordinatorTestTimeout):
				t.Fatal("coordinator wait did not honor its context")
			}

			close(releaseProducer)
			released = true
			result := waitForScrapeResult(t, producerResult)
			require.NoError(t, result.err)
			assert.Equal(t, []byte("metrics"), result.response)
		})
	}
}

func TestScrapeCoordinatorWaitPrefersCompletedFlight(t *testing.T) {
	tests := []struct {
		name       string
		contextErr error
	}{
		{name: "canceled", contextErr: context.Canceled},
		{name: "deadline exceeded", contextErr: context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flightDone := make(chan struct{})
			close(flightDone)
			coordinator := newScrapeCoordinator(1)
			coordinator.inFlight = &scrapeFlight{done: flightDone}
			waitContext := newControlledWaitContext()
			waitContext.finish(tt.contextErr)

			require.NoError(t, coordinator.wait(waitContext))
		})
	}
}

func TestScrapeCoordinatorAbandonedContextDoesNotStartFlight(t *testing.T) {
	tests := []struct {
		name    string
		context func(t *testing.T) context.Context
		wantErr error
	}{
		{
			name: "already canceled",
			context: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				return ctx
			},
			wantErr: context.Canceled,
		},
		{
			name: "already expired deadline",
			context: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)

				return ctx
			},
			wantErr: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coordinator := newScrapeCoordinator(1)
			var producerCalls atomic.Int32

			response, err := coordinator.do(tt.context(t), func() ([]byte, error) {
				producerCalls.Add(1)

				return []byte("unexpected"), nil
			})

			assert.Nil(t, response)
			require.Equal(t, tt.wantErr, err)
			assert.Equal(t, int32(0), producerCalls.Load())

			coordinator.mu.Lock()
			assert.Nil(t, coordinator.inFlight)
			coordinator.mu.Unlock()
		})
	}
}

func TestScrapeCoordinatorCoalescesOverlappingCalls(t *testing.T) {
	const callers = 4

	coordinator := newScrapeCoordinator(callers)
	releaseProducer := make(chan struct{})
	results := make(chan scrapeResult, callers)
	waiting := make([]chan struct{}, callers)
	var producerCalls atomic.Int32

	for i := range callers {
		waiting[i] = make(chan struct{})
		ctx := &waitObservedContext{
			Context: context.Background(),
			waiting: waiting[i],
		}

		go func() {
			response, err := coordinator.do(ctx, func() ([]byte, error) {
				producerCalls.Add(1)
				<-releaseProducer

				return []byte("shared metrics"), nil
			})
			results <- scrapeResult{response: response, err: err}
		}()
	}

	for _, waiter := range waiting {
		waitForSignal(t, waiter)
	}
	close(releaseProducer)

	responses := make([][]byte, 0, callers)
	for range callers {
		result := waitForScrapeResult(t, results)
		require.NoError(t, result.err)
		responses = append(responses, result.response)
	}

	require.Equal(t, int32(1), producerCalls.Load())
	for i := 1; i < len(responses); i++ {
		assert.Equal(t, responses[0], responses[i])
		require.NotEmpty(t, responses[i])
		assert.Equal(t, &responses[0][0], &responses[i][0])
	}
}

func TestScrapeCoordinatorAlreadyCanceledWaitersDoNotStartProducers(t *testing.T) {
	coordinator := newScrapeCoordinator(101)
	producerStarted := make(chan struct{})
	releaseProducer := make(chan struct{})
	primaryResult := make(chan scrapeResult, 1)
	var producerCalls atomic.Int32

	go func() {
		response, err := coordinator.do(context.Background(), func() ([]byte, error) {
			producerCalls.Add(1)
			close(producerStarted)
			<-releaseProducer

			return []byte("metrics"), nil
		})
		primaryResult <- scrapeResult{response: response, err: err}
	}()

	waitForSignal(t, producerStarted)

	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		response, err := coordinator.do(ctx, func() ([]byte, error) {
			producerCalls.Add(1)

			return []byte("unexpected"), nil
		})

		assert.Nil(t, response)
		require.ErrorIs(t, err, context.Canceled)
	}
	assert.Equal(t, int32(1), producerCalls.Load())

	close(releaseProducer)
	result := waitForScrapeResult(t, primaryResult)
	require.NoError(t, result.err)
	assert.Equal(t, []byte("metrics"), result.response)
	assert.Equal(t, int32(1), producerCalls.Load())
}

func TestScrapeCoordinatorDoesNotCacheCompletedFlight(t *testing.T) {
	coordinator := newScrapeCoordinator(1)
	var producerCalls atomic.Int32
	produce := func() ([]byte, error) {
		call := producerCalls.Add(1)

		return []byte(fmt.Sprintf("metrics-%d", call)), nil
	}

	first, err := coordinator.do(context.Background(), produce)
	require.NoError(t, err)
	second, err := coordinator.do(context.Background(), produce)
	require.NoError(t, err)

	assert.Equal(t, []byte("metrics-1"), first)
	assert.Equal(t, []byte("metrics-2"), second)
	assert.Equal(t, int32(2), producerCalls.Load())
}

func TestScrapeCoordinatorStartsFreshFlightAfterFailure(t *testing.T) {
	tests := []struct {
		name      string
		firstCall func() ([]byte, error)
		assertErr func(t *testing.T, err error)
	}{
		{
			name: "error",
			firstCall: func() ([]byte, error) {
				return nil, errors.New("producer failed")
			},
			assertErr: func(t *testing.T, err error) {
				require.EqualError(t, err, "producer failed")
			},
		},
		{
			name: "panic",
			firstCall: func() ([]byte, error) {
				panic("producer panicked")
			},
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.True(t, strings.Contains(err.Error(), "producer panicked"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coordinator := newScrapeCoordinator(1)
			var producerCalls atomic.Int32

			first, err := coordinator.do(context.Background(), func() ([]byte, error) {
				producerCalls.Add(1)

				return tt.firstCall()
			})
			assert.Nil(t, first)
			tt.assertErr(t, err)

			second, err := coordinator.do(context.Background(), func() ([]byte, error) {
				producerCalls.Add(1)

				return []byte("fresh metrics"), nil
			})
			require.NoError(t, err)
			assert.Equal(t, []byte("fresh metrics"), second)
			assert.Equal(t, int32(2), producerCalls.Load())
		})
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(coordinatorTestTimeout):
		t.Fatal("timed out waiting for coordinator signal")
	}
}

func waitForScrapeResult(t *testing.T, results <-chan scrapeResult) scrapeResult {
	t.Helper()

	select {
	case result := <-results:
		return result
	case <-time.After(coordinatorTestTimeout):
		t.Fatal("timed out waiting for scrape result")
		return scrapeResult{}
	}
}
