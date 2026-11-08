/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package main

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

func TestAcceptanceClientDelegatesNativeWatchReadAndRetirement(t *testing.T) {
	native := mockdcgm.NewMockDCGM(gomock.NewController(t))
	client := &acceptanceClient{DCGM: native}
	group, fields := dcgm.GroupHandle{}, dcgm.FieldHandle{}
	since, next := time.UnixMicro(123), time.UnixMicro(456)
	values := []dcgm.FieldValue_v2{{EntityGroupId: dcgm.FE_GPU_CI, EntityID: 64, FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS, TS: 234}}
	readErr := errors.New("native read failed")
	gomock.InOrder(
		native.EXPECT().WatchFieldsWithGroupExNoRefresh(fields, group, int64(1_000_000), float64(120), int32(100)).Return(nil),
		native.EXPECT().GetValuesSince(group, fields, since).Return(values, next, readErr),
		// Oracle retirement happens while exporter collectors may still read.
		native.EXPECT().UnwatchFields(fields, group).Return(nil),
		native.EXPECT().FieldGroupDestroy(fields).Return(nil),
		native.EXPECT().DestroyGroup(group).Return(nil),
		native.EXPECT().Cleanup(),
	)
	if err := client.WatchFieldsWithGroupExNoRefresh(fields, group, 1_000_000, 120, 100); err != nil {
		t.Fatal(err)
	}
	client.Cleanup()
	client.Cleanup()
	got, cursor, err := client.GetValuesSince(group, fields, since)
	if !reflect.DeepEqual(got, values) || cursor != next || !errors.Is(err, readErr) {
		t.Fatalf("delegated read = %v, %v, %v", got, cursor, err)
	}
	if err := errors.Join(client.UnwatchFields(fields, group), client.FieldGroupDestroy(fields), client.DestroyGroup(group)); err != nil {
		t.Fatal(err)
	}
	app := acceptanceTestExporter(client)
	close(app.entered)
	app.done <- nil
	if err := client.finish(app); err != nil {
		t.Fatal(err)
	}
	if err := client.finish(app); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptanceClientWithholdsCleanupUntilExporterReturns(t *testing.T) {
	native := mockdcgm.NewMockDCGM(gomock.NewController(t))
	client := &acceptanceClient{DCGM: native}
	native.EXPECT().Cleanup().Times(1)
	app := acceptanceTestExporter(client)
	close(app.entered)
	client.Cleanup()
	// Even a cleanup request cannot establish completion of the application.
	if err := client.release(false, true); err == nil {
		t.Fatal("live application allowed native cleanup")
	}
	signalReceived := make(chan os.Signal, 1)
	go func() {
		signalReceived <- <-app.signals.signals
		app.done <- nil
	}()
	if err := client.finish(app); err != nil {
		t.Fatal(err)
	}
	if signal := <-signalReceived; signal != syscall.SIGTERM {
		t.Fatalf("retirement signal = %v", signal)
	}
}

func TestAcceptanceClientRetirementFailureKeepsNativeStateAlive(t *testing.T) {
	native := mockdcgm.NewMockDCGM(gomock.NewController(t))
	client := &acceptanceClient{DCGM: native}
	app := acceptanceTestExporter(client)
	close(app.entered)
	retirementErr := errors.New("collector retirement failed")
	app.done <- retirementErr
	// No Cleanup expectation: even an exited app is unsafe without its request.
	app.stage = "first scrape"
	app.historyVerified = true
	app.lastResponse = dcgmprobe.ExporterHTTPResponse{Body: "rejected scrape", Status: 200}
	var failure dcgmprobe.ExporterFailure
	assertionErr := errors.New("counter mismatch")
	err := finishExporterAttempt(client, app, "", &failure, assertionErr)
	if !errors.Is(err, assertionErr) || !errors.Is(err, retirementErr) || !strings.Contains(err.Error(), "native cleanup withheld") {
		t.Fatalf("finish = %v", err)
	}
	if failure.Stage != "first scrape" || !failure.HistoryVerified || failure.Response.Body != "rejected scrape" || failure.Cleanup != "failed" || len(failure.CleanupErrors) == 0 {
		t.Fatalf("retirement failure lost diagnostic evidence: %+v", failure)
	}
	if client.released {
		t.Fatal("failed retirement released the native client")
	}
	if err := client.finish(app); !errors.Is(err, retirementErr) || !strings.Contains(err.Error(), "native cleanup withheld") {
		t.Fatalf("repeated finish = %v", err)
	}
}

func TestAcceptanceClientReleasesNativeStateOnEarlyErrors(t *testing.T) {
	for _, stage := range []string{"before application", "before action", "safe action error"} {
		t.Run(stage, func(t *testing.T) {
			native := mockdcgm.NewMockDCGM(gomock.NewController(t))
			client := &acceptanceClient{DCGM: native}
			native.EXPECT().Cleanup().Times(1)
			var app *acceptanceExporter
			var actionErr error
			switch stage {
			case "before action":
				app = acceptanceTestExporter(client)
				actionErr = errors.New("startup failed")
				app.done <- actionErr
			case "safe action error":
				app = acceptanceTestExporter(client)
				actionErr = errors.New("startup failed")
				close(app.entered)
				client.Cleanup()
				app.done <- actionErr
			}
			if err := client.finish(app); !errors.Is(err, actionErr) {
				t.Fatalf("finish = %v, want startup error %v", err, actionErr)
			}
			if !client.released {
				t.Fatal("safe early error did not release native client")
			}
			if err := client.release(true, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func acceptanceTestExporter(client *acceptanceClient) *acceptanceExporter {
	return &acceptanceExporter{signals: &acceptanceSignals{signals: make(chan os.Signal, 1)}, done: make(chan error, 1), entered: make(chan struct{}), client: client}
}
