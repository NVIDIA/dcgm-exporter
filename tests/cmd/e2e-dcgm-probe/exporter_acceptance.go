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
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	exportercmd "github.com/NVIDIA/dcgm-exporter/pkg/cmd"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

const (
	exporterPollInterval      = 10 * time.Second
	exporterAcceptanceTimeout = 35 * time.Second
)

type exporterAcceptanceOptions struct {
	entityHistoryOptions
	scenario string
}

func newExporterAcceptanceCommand() *cli.Command {
	options := exporterAcceptanceOptions{}
	return &cli.Command{
		Name: "field-entity-exporter", Usage: "Verify native fake-entity history against actual exporter HTTP COUNT/TOTAL",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "mode", Value: "embedded", Destination: &options.mode, Usage: "embedded or standalone"},
			&cli.StringFlag{Name: "owned-hostengine", Destination: &options.ownedHostengine, Usage: "standalone caller-owned isolated unix:// socket; caller stops backend"},
			&cli.StringFlag{Name: "scenario", Value: "ci-derived", Destination: &options.scenario, Usage: "ci-derived, ci-raw, ci-only, plain or mixed"},
			&cli.StringFlag{Name: "output", Destination: &options.outputPath, Usage: "private acceptance report path"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 0 {
				return errors.New("unexpected positional arguments")
			}
			socket, err := validateEntityHistoryOptions(options.entityHistoryOptions)
			if err != nil {
				return err
			}
			if !validExporterScenario(options.scenario) {
				return errors.New("--scenario must be ci-derived, ci-raw, ci-only, plain or mixed")
			}
			var failure dcgmprobe.ExporterFailure
			report, err := runExporterAcceptance(options, socket, &failure)
			return writeExporterAcceptanceResult(options, report, failure, err)
		},
	}
}

// runExporterAcceptance retains native ownership while the actual application
// starts, serves metrics and retires its collectors. The oracle closes first.
func runExporterAcceptance(options exporterAcceptanceOptions, socket string, failure *dcgmprobe.ExporterFailure) (report dcgmprobe.ExporterAcceptance, resultErr error) {
	failure.Stage, failure.Cleanup = "DCGM initialization", "not started"
	report = dcgmprobe.ExporterAcceptance{Version: 2, Mode: options.mode, Scenario: options.scenario, Topology: "fake", WindowSizeMS: 60000, CSV: exporterCSV(options.scenario)}
	config := &appconfig.Config{UseRemoteHE: options.mode == "standalone", RemoteHEInfo: options.ownedHostengine}
	if err := dcgmprovider.Initialize(config); err != nil {
		return report, probeStage("DCGM provider initialization", err)
	}
	owned := &acceptanceClient{DCGM: dcgmprovider.Client()}
	dcgmprovider.SetClient(owned)
	var app *acceptanceExporter
	var scratch string
	defer func() { resultErr = finishExporterAttempt(owned, app, scratch, failure, resultErr) }()
	var err error
	failure.Stage = "topology"
	report.Targets, report.Selector, err = createExporterTargets(options.scenario)
	if err != nil {
		return report, err
	}
	failure.Stage = "startup"
	scratch, err = os.MkdirTemp("", "fec-exporter-")
	if err != nil {
		return report, err
	}
	csvPath := filepath.Join(scratch, "counters.csv")
	if err := os.WriteFile(csvPath, []byte(report.CSV), 0o600); err != nil {
		return report, err
	}
	address, err := reserveExporterAddress()
	if err != nil {
		return report, err
	}
	report.TotalSinceUsec = time.Now().UnixMicro()
	app = startAcceptanceExporter(options, report.Selector, csvPath, address, owned)
	resultErr = observeExporterAcceptance(&report, app, address, socket)
	return report, resultErr
}

func observeExporterAcceptance(report *dcgmprobe.ExporterAcceptance, app *acceptanceExporter, address, socket string) (resultErr error) {
	app.stage = "readiness"
	body, _, err := app.waitScrape(address, func(string, int64) error { return nil }, time.Now().Add(exporterPollInterval-time.Second))
	if err != nil {
		return fmt.Errorf("exporter readiness: %w", err)
	}
	if body == "" {
		return errors.New("exporter readiness returned empty metrics")
	}
	app.stage = "oracle setup"
	oracle, err := newExporterOracle(report.Targets)
	if err != nil {
		return err
	}
	// The wrapper keeps the native backend alive even if the application exits.
	defer func() {
		cleanupErr := oracle.close()
		if cleanupErr != nil {
			app.cleanupErrors = append(app.cleanupErrors, cleanupErr.Error())
			if resultErr == nil {
				app.stage = "oracle cleanup"
			}
		}
		resultErr = errors.Join(resultErr, cleanupErr)
	}()
	app.stage = "initial injection"
	if err := injectExporterFixture(report, socket); err != nil {
		return err
	}
	if time.Now().UnixMicro() >= report.TotalSinceUsec+(exporterPollInterval-time.Second).Microseconds() {
		return errors.New("fresh injection missed the first TOTAL poll deadline")
	}
	app.stage = "initial history"
	report.History, err = readEntityHistory(oracle.group, oracle.fields, report.HistorySinceUsec)
	if err != nil {
		return err
	}
	if !sameExporterHistory(*report, report.History) {
		return errors.New("independent native history differs from injected exporter fixture")
	}
	app.historyVerified = true
	advance := func() error {
		return advanceExporterFixture(report, oracle, socket)
	}
	if err := observeExporterScrapes(report, app, address, advance); err != nil {
		return err
	}
	app.stage = "final history"
	finalHistory, err := readEntityHistory(oracle.group, oracle.fields, report.HistorySinceUsec)
	if err != nil {
		return err
	}
	if !sameHistory(report.SecondHistory, finalHistory) {
		return errors.New("native fixture history changed during exporter polling")
	}
	return nil
}

func advanceExporterFixture(report *dcgmprobe.ExporterAcceptance, oracle *exporterOracle, socket string) error {
	// A new code on every selected entity proves each TOTAL binding read
	// successfully after the first scrape; old codes must not be replayed.
	for _, sample := range exporterProgressFixture(*report, time.Now().UnixMicro()) {
		if err := injectEntitySample(socket, sample); err != nil {
			return fmt.Errorf("inject exporter progress fixture: %w", err)
		}
		report.Injected = append(report.Injected, sample)
	}
	var err error
	report.SecondHistory, err = readEntityHistory(oracle.group, oracle.fields, report.HistorySinceUsec)
	if err != nil {
		return err
	}
	if !sameExporterHistory(*report, report.SecondHistory) {
		return errors.New("independent native history differs from exporter progress fixture")
	}
	return nil
}

func observeExporterScrapes(report *dcgmprobe.ExporterAcceptance, app *acceptanceExporter, address string, advance func() error) error {
	check := func(body string, ts int64) error { return verifyExporterScrape(*report, body, ts) }
	var err error
	app.stage = "first scrape"
	report.FirstScrape, report.FirstScrapeUsec, err = app.waitScrape(address, check, time.Now().Add(exporterAcceptanceTimeout))
	if err != nil {
		return err
	}
	app.stage = "progress injection/history"
	if err := advance(); err != nil {
		return err
	}
	app.secondHistoryVerified = true
	second := *report
	second.History = report.SecondHistory
	check = func(body string, ts int64) error { return verifyExporterScrape(second, body, ts) }
	app.stage = "second scrape"
	report.SecondScrape, report.SecondScrapeUsec, err = app.waitScrape(address, check, time.Now().Add(exporterAcceptanceTimeout))
	return err
}

func reserveExporterAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address := listener.Addr().String()
	return address, listener.Close()
}

// createExporterTargets obtains identities through native discovery, never
// from emitted metrics. One CI avoids fake NVML compute-ID collisions.
func createExporterTargets(scenario string) ([]dcgmprobe.ExporterTarget, string, error) {
	hierarchy, giID, ciID, err := createHistoryHierarchy()
	if err != nil {
		return nil, "", err
	}
	plainRequests := []dcgm.MigHierarchyInfo{{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}}, {Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU}}}
	if scenario == "ci-only" {
		plainRequests = plainRequests[:1]
	}
	plain, err := dcgm.CreateFakeEntities(plainRequests)
	if err != nil {
		return nil, "", probeStage("fake plain GPU creation", err)
	}
	if len(plain) != len(plainRequests) {
		return nil, "", errors.New("fake plain GPU creation returned unexpected count")
	}
	info, err := deviceinfo.Initialize(appconfig.DeviceOptions{Flex: true}, appconfig.DeviceOptions{}, appconfig.DeviceOptions{}, true, dcgm.FE_GPU)
	if err != nil {
		return nil, "", probeStage("native exporter topology discovery", err)
	}
	parent := info.GPU(hierarchy[0].ID)
	if len(parent.GPUInstances) != 1 || len(parent.GPUInstances[0].ComputeInstances) != 1 {
		return nil, "", errors.New("fake hierarchy discovery differs from created topology")
	}
	if parent.GPUInstances[0].ProfileName == "" {
		return nil, "", errors.New("fake GI profile unavailable for identity assertion")
	}
	entities := append([]dcgmprobe.Entity(nil), hierarchy...)
	for _, id := range plain {
		entities = append(entities, dcgmprobe.Entity{Group: uint(dcgm.FE_GPU), ID: id})
	}
	var targets []dcgmprobe.ExporterTarget

	for i, entity := range entities {
		gpu := parent
		if i >= 3 {
			gpu = info.GPU(entity.ID)
		}
		labels := nativeExporterLabels(gpu.DeviceInfo)
		switch i {
		case 1, 2:
			labels = withMIGLabels(labels, parent.GPUInstances[0].ProfileName, giID, ciID, i == 2)
		}
		targets = append(targets, dcgmprobe.ExporterTarget{Entity: entity, Selected: selectedExporterTarget(scenario, i), Labels: labels})
	}

	selector := fmt.Sprintf("i:%d", hierarchy[1].ID)
	switch scenario {
	case "plain":
		selector = fmt.Sprintf("g:%d", plain[0])
	case "mixed":
		selector = fmt.Sprintf("g:%d,%d+i:%d", hierarchy[0].ID, plain[0], hierarchy[1].ID)
	}
	return targets, selector, nil
}

func nativeExporterLabels(device dcgm.Device) map[string]string {
	return map[string]string{"gpu": strconv.FormatUint(uint64(device.GPU), 10), "UUID": device.UUID, "device": fmt.Sprintf("nvidia%d", device.GPU), "modelName": device.Identifiers.Model, "pci_bus_id": device.PCI.BusID}
}

func withMIGLabels(labels map[string]string, profile string, giID, ciID uint, isCI bool) map[string]string {
	labels["GPU_I_ID"] = strconv.FormatUint(uint64(giID), 10)
	labels["GPU_I_PROFILE"] = profile
	if isCI {
		labels["GPU_CI_ID"] = strconv.FormatUint(uint64(ciID), 10)
	}
	return labels
}

func selectedExporterTarget(scenario string, index int) bool {
	switch scenario {
	case "plain":
		return index == 3
	case "mixed":
		return index < 4
	default:
		return index < 3
	}
}

type exporterOracle struct {
	group  dcgm.GroupHandle
	fields dcgm.FieldHandle
	closed bool
}

func newExporterOracle(targets []dcgmprobe.ExporterTarget) (*exporterOracle, error) {
	group, err := dcgm.CreateGroup(fmt.Sprintf("fec-oracle-%d", time.Now().UnixNano()))
	if err != nil {
		return nil, err
	}
	failed := func(err error) (*exporterOracle, error) { return nil, errors.Join(err, dcgm.DestroyGroup(group)) }
	for _, target := range targets {
		if err := dcgm.AddEntityToGroup(group, dcgm.Field_Entity_Group(target.Entity.Group), target.Entity.ID); err != nil {
			return failed(err)
		}
	}
	fields, err := dcgm.FieldGroupCreate(fmt.Sprintf("fec-oracle-fields-%d", time.Now().UnixNano()), []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS})
	if err != nil {
		return failed(err)
	}
	if err := dcgm.WatchFieldsWithGroupEx(fields, group, 1_000_000, 120, 100); err != nil {
		return failed(errors.Join(err, dcgm.FieldGroupDestroy(fields)))
	}
	return &exporterOracle{group: group, fields: fields}, nil
}

func (o *exporterOracle) close() error {
	if o.closed {
		return nil
	}
	o.closed = true
	return errors.Join(dcgm.UnwatchFields(o.fields, o.group), dcgm.FieldGroupDestroy(o.fields), dcgm.DestroyGroup(o.group))
}

func injectExporterFixture(report *dcgmprobe.ExporterAcceptance, socket string) error {
	report.HistorySinceUsec = time.Now().Add(-120 * time.Second).UnixMicro()
	for _, sample := range exporterInitialFixture(*report, time.Now().UnixMicro()) {
		if err := injectEntitySample(socket, sample); err != nil {
			return fmt.Errorf("inject exporter fixture: %w", err)
		}
		report.Injected = append(report.Injected, sample)
	}
	return nil
}

func exporterInitialFixture(report dcgmprobe.ExporterAcceptance, timestamp int64) []dcgmprobe.HistorySample {
	var samples []dcgmprobe.HistorySample
	// CI's codes are 45/46; different parent/plain/control codes detect scope substitution.
	for i, target := range report.Targets {
		if report.Scenario == "ci-only" && target.Selected && target.Entity.Group != uint(dcgm.FE_GPU_CI) {
			continue
		}
		code := []int64{31, 41, 45, 51, 61}[i]
		for j, value := range []int64{13, code, code, 0, code + 1} {
			ts := timestamp + int64(j)
			if j == 0 {
				ts = timestamp - 90_000_000
			}
			samples = append(samples, dcgmprobe.HistorySample{Entity: target.Entity, FieldID: uint(dcgm.DCGM_FI_DEV_XID_ERRORS), FieldType: dcgm.DCGM_FT_INT64, TimestampUsec: ts, Value: value})
		}
	}
	return samples
}

// exporterProgressFixture adds a distinct nonzero code to every selected entity.
func exporterProgressFixture(report dcgmprobe.ExporterAcceptance, timestamp int64) []dcgmprobe.HistorySample {
	var samples []dcgmprobe.HistorySample
	for i, target := range report.Targets {
		if target.Selected && (report.Scenario != "ci-only" || target.Entity.Group == uint(dcgm.FE_GPU_CI)) {
			samples = append(samples, dcgmprobe.HistorySample{Entity: target.Entity, FieldID: uint(dcgm.DCGM_FI_DEV_XID_ERRORS), FieldType: dcgm.DCGM_FT_INT64, TimestampUsec: timestamp, Value: 71 + int64(i)})
		}
	}
	return samples
}

type acceptanceSignals struct{ signals chan os.Signal }

func (s *acceptanceSignals) Signals() <-chan os.Signal { return s.signals }
func (*acceptanceSignals) Cleanup()                    {}

type acceptanceExporter struct {
	signals               *acceptanceSignals
	done                  chan error
	entered               chan struct{}
	client                *acceptanceClient
	finished              bool
	err                   error
	lastResponse          dcgmprobe.ExporterHTTPResponse
	stage                 string
	historyVerified       bool
	secondHistoryVerified bool
	cleanupErrors         []string
}

func startAcceptanceExporter(options exporterAcceptanceOptions, selector, csvPath, address string, client *acceptanceClient) *acceptanceExporter {
	run := &acceptanceExporter{signals: &acceptanceSignals{signals: make(chan os.Signal, 1)}, done: make(chan error, 1), entered: make(chan struct{}), client: client}
	app := exportercmd.NewApp()
	app.Action = func(c *cli.Context) error {
		close(run.entered)
		return exportercmd.StartDCGMExporterWithSignalSource(c, run.signals)
	}
	args := []string{"dcgm-exporter", "--collectors", csvPath, "--devices", selector, "--fake-gpus", "--no-hostname", "--address", address, "--collect-interval", strconv.Itoa(int(exporterPollInterval.Milliseconds())), "--xid-count-window-size", "60000"}
	if options.mode == "standalone" {
		args = append(args, "--remote-hostengine-info", options.ownedHostengine)
	}
	go func() {
		var err error
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("exporter panic: %v", recovered)
			}
			run.done <- err
		}()
		err = app.Run(args)
	}()
	return run
}

func (a *acceptanceExporter) completed() bool {
	if !a.finished {
		select {
		case a.err = <-a.done:
			a.finished = true
		default:
		}
	}
	return a.finished
}

func (a *acceptanceExporter) running() error {
	if a.completed() {
		return a.failure()
	}
	if a.client.cleanupRequested.Load() {
		return errors.New("exporter requested native cleanup before acceptance completed")
	}
	return nil
}

func (a *acceptanceExporter) failure() error {
	return fmt.Errorf("exporter stopped before acceptance completed: %w", errors.Join(a.err, errors.New("exporter is no longer running")))
}

func (a *acceptanceExporter) finishedBeforeAction() bool {
	if !a.completed() {
		return false
	}
	select {
	case <-a.entered:
		return false
	default:
		return true
	}
}

func (a *acceptanceExporter) stop() error {
	if a.completed() {
		return a.err
	}
	a.signals.signals <- syscall.SIGTERM
	select {
	case a.err = <-a.done:
		a.finished = true
		return a.err
	case <-time.After(10 * time.Second):
		return errors.New("exporter shutdown did not complete; provider may still own active watches")
	}
}

func (a *acceptanceExporter) waitScrape(address string, check func(string, int64) error, deadline time.Time) (string, int64, error) {
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	var last error
	for time.Now().Before(deadline) {
		if err := a.running(); err != nil {
			return "", 0, err
		}
		response, err := client.Get("http://" + address + "/metrics")
		if err == nil {
			err = a.checkResponse(response, check)
			if err == nil {
				return a.lastResponse.Body, a.lastResponse.TimestampUsec, nil
			}
		}
		last = err
		time.Sleep(50 * time.Millisecond)
	}
	return "", 0, fmt.Errorf("exporter metrics assertion timed out: %w", last)
}

func (a *acceptanceExporter) checkResponse(response *http.Response, check func(string, int64) error) error {
	const limit = 1 << 20
	data, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	err := errors.Join(readErr, response.Body.Close())
	observation := dcgmprobe.ExporterHTTPResponse{Status: response.StatusCode, TimestampUsec: time.Now().UnixMicro()}
	if len(data) > limit {
		data = data[:limit]
		observation.Truncated = true
		err = errors.Join(err, errors.New("metrics HTTP response exceeds 1 MiB"))
	}
	observation.Body = string(data)
	if response.StatusCode != http.StatusOK {
		err = errors.Join(err, fmt.Errorf("metrics HTTP status %d", response.StatusCode))
	}
	if err == nil {
		err = check(observation.Body, observation.TimestampUsec)
	}
	if err != nil {
		observation.Error = err.Error()
	}
	a.lastResponse = observation
	return err
}
