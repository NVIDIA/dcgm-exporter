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

package metriccontract

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// Family names the fixtures use, as etc/default-counters.csv names them.
const (
	gpuTempFamily     = "DCGM_FI_DEV_GPU_TEMP_CELSIUS"
	memoryClockFamily = "DCGM_FI_DEV_MEMORY_CLOCK_HERTZ"
	smClockFamily     = "DCGM_FI_DEV_SM_CLOCK_HERTZ"
	boardPowerFamily  = "DCGM_FI_DEV_BOARD_POWER_WATTS"
)

// fixtureFamilies lists every family the scrape fixtures can carry.
var fixtureFamilies = []string{
	fbCapacityFamily, fbFreeFamily, fbUsedFamily, fbReservedFamily,
	gpuTempFamily, memoryClockFamily, smClockFamily, boardPowerFamily,
}

// inventoryGPU0 and inventoryGPU1 are the nvidia-smi identities the scrape fixtures match.
var (
	inventoryGPU0 = nvidiasmi.GPU{
		Index: 0, UUID: "GPU-0", Name: "NVIDIA GB300", PCIBusID: "00000008:01:00.0", DriverVersion: "580.82.07",
	}
	inventoryGPU1 = nvidiasmi.GPU{
		Index: 1, UUID: "GPU-1", Name: "NVIDIA GB300", PCIBusID: "00000009:01:00.0", DriverVersion: "580.82.07",
	}
)

// matchingReadings returns the nvidia-smi readings the scrape fixtures match,
// for the given GPUs.
func matchingReadings(gpus ...nvidiasmi.GPU) nvidiasmi.Readings {
	readings := nvidiasmi.Readings{}
	for _, gpu := range gpus {
		readings[gpu.UUID] = map[string]nvidiasmi.Reading{
			"memory.total":    smiReading(1000),
			"temperature.gpu": smiReading(40),
			"power.draw":      smiReading(200),
			"clocks.sm":       smiReading(1000),
			"clocks.mem":      smiReading(2000),
		}
	}
	return readings
}

// inventoryCSVRows enables every compared family and the driver version label row.
var inventoryCSVRows = func() []DefaultCounterRow {
	rows := []DefaultCounterRow{{Name: driverVersionLabel, Type: dto.MetricType_UNTYPED}}
	for _, family := range fixtureFamilies {
		rows = append(rows, DefaultCounterRow{Name: family, Type: dto.MetricType_GAUGE})
	}
	return rows
}()

// inventorySample describes one GPU's labels and values for a scrape fixture.
// An empty driver means no driver version label, and an empty migInstance
// means a whole-GPU sample rather than a MIG-instance one.
type inventorySample struct {
	gpu, uuid, device, model, pci, hostname, driver string
	uuidLabel, migInstance                          string
	values                                          map[string]float64
}

// matchingSample returns a fixture sample that agrees with the nvidia-smi row.
func matchingSample(gpu nvidiasmi.GPU) inventorySample {
	return inventorySample{
		gpu: fmt.Sprint(gpu.Index), uuid: gpu.UUID, device: fmt.Sprintf("nvidia%d", gpu.Index),
		model: gpu.Name, pci: strings.ToLower(gpu.PCIBusID), hostname: "node-a", driver: gpu.DriverVersion,
		uuidLabel: "UUID",
		values: map[string]float64{
			fbCapacityFamily:  1000 * bytesPerMiB,
			fbFreeFamily:      900 * bytesPerMiB,
			fbUsedFamily:      60 * bytesPerMiB,
			fbReservedFamily:  40 * bytesPerMiB,
			gpuTempFamily:     40,
			memoryClockFamily: 2000 * hertzPerMHz,
			smClockFamily:     1000 * hertzPerMHz,
			boardPowerFamily:  200,
		},
	}
}

// inventoryScrape renders fixture samples as Prometheus text and parses it.
func inventoryScrape(t *testing.T, samples ...inventorySample) map[string]*dto.MetricFamily {
	t.Helper()

	var text strings.Builder
	for _, family := range fixtureFamilies {
		// A family no sample has is left out entirely, as the exporter does.
		if !slices.ContainsFunc(samples, func(s inventorySample) bool { _, ok := s.values[family]; return ok }) {
			continue
		}

		fmt.Fprintf(&text, "# TYPE %s gauge\n", family)
		for _, s := range samples {
			value, ok := s.values[family]
			if !ok {
				continue
			}

			driver := ""
			if s.driver != "" {
				driver = fmt.Sprintf(",%s=%q", driverVersionLabel, s.driver)
			}
			if s.migInstance != "" {
				driver += fmt.Sprintf(",GPU_I_ID=%q", s.migInstance)
			}
			fmt.Fprintf(&text, "%s{gpu=%q,%s=%q,pci_bus_id=%q,device=%q,modelName=%q,hostname=%q%s} %v\n",
				family, s.gpu, s.uuidLabel, s.uuid, s.pci, s.device, s.model, s.hostname, driver, value)
		}
	}

	families, err := ParseText([]byte(text.String()))
	require.NoError(t, err)
	return families
}

// inventoryOptions returns the options the fixtures match.
func inventoryOptions() GPUInventoryOptions {
	return GPUInventoryOptions{CSVRows: inventoryCSVRows, ExpectedHostname: "node-a", HostnameSource: "host name"}
}

// mismatchItems lists the Item of every mismatch, for compact assertions.
func mismatchItems(mismatches []GPUInventoryMismatch) []string {
	items := make([]string, 0, len(mismatches))
	for _, mismatch := range mismatches {
		items = append(items, mismatch.Item)
	}
	return items
}

// TestCompareGPUInventoryAcceptsMatchingOutput accepts output that agrees with nvidia-smi.
func TestCompareGPUInventoryAcceptsMatchingOutput(t *testing.T) {
	families := inventoryScrape(t, matchingSample(inventoryGPU0), matchingSample(inventoryGPU1))

	report := CompareGPUInventory(families, []nvidiasmi.GPU{inventoryGPU0, inventoryGPU1},
		matchingReadings(inventoryGPU0, inventoryGPU1), inventoryOptions())

	require.NoError(t, report.Err())
	require.Empty(t, report.Skipped)
}

// TestCompareGPUInventoryReadsTheOldNamespaceUUIDLabel joins samples by the lowercase uuid label.
func TestCompareGPUInventoryReadsTheOldNamespaceUUIDLabel(t *testing.T) {
	sample := matchingSample(inventoryGPU0)
	sample.uuidLabel = "uuid"

	report := CompareGPUInventory(inventoryScrape(t, sample), []nvidiasmi.GPU{inventoryGPU0},
		matchingReadings(inventoryGPU0), inventoryOptions())

	require.NoError(t, report.Err())
}

// TestCompareGPUInventoryAcceptsMIGInstanceSamples accepts MIG-instance samples,
// which carry the parent GPU's UUID, beside the whole-GPU sample.
func TestCompareGPUInventoryAcceptsMIGInstanceSamples(t *testing.T) {
	instance := matchingSample(inventoryGPU0)
	instance.migInstance = "1"
	instance.values = map[string]float64{gpuTempFamily: 90}

	families := inventoryScrape(t, matchingSample(inventoryGPU0), instance)

	report := CompareGPUInventory(families, []nvidiasmi.GPU{inventoryGPU0},
		matchingReadings(inventoryGPU0), inventoryOptions())

	require.NoError(t, report.Err())
}

// TestCompareGPUIdentityReportsOneMismatchPerLabel changes one identity label at a time.
func TestCompareGPUIdentityReportsOneMismatchPerLabel(t *testing.T) {
	tests := []struct {
		name       string
		change     func(*inventorySample)
		wantItem   string
		wantSource string
	}{
		{name: "modelName", change: func(s *inventorySample) { s.model = "NVIDIA B200" }, wantItem: "modelName", wantSource: "nvidia-smi name"},
		{name: "pci_bus_id", change: func(s *inventorySample) { s.pci = "00000008:02:00.0" }, wantItem: "pci_bus_id", wantSource: "nvidia-smi pci.bus_id"},
		{
			name: "gpu and device together", change: func(s *inventorySample) { s.gpu = "1"; s.device = "nvidia1" },
			wantItem: "gpu", wantSource: "nvidia-smi index",
		},
		{name: "device", change: func(s *inventorySample) { s.device = "nvidia7" }, wantItem: "device", wantSource: `"nvidia" + gpu label`},
		{name: "hostname", change: func(s *inventorySample) { s.hostname = "node-b" }, wantItem: "hostname", wantSource: "host name"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sample := matchingSample(inventoryGPU0)
			tc.change(&sample)

			mismatches := compareGPUIdentity(inventoryScrape(t, sample), []nvidiasmi.GPU{inventoryGPU0}, inventoryOptions())

			require.Len(t, mismatches, 1, "%v", mismatches)
			require.Equal(t, "GPU-0", mismatches[0].UUID)
			require.Equal(t, tc.wantItem, mismatches[0].Item)
			require.Equal(t, tc.wantSource, mismatches[0].Source)
		})
	}
}

// TestCompareGPUIdentityChecksTheUUIDSet rejects missing and extra GPUs.
func TestCompareGPUIdentityChecksTheUUIDSet(t *testing.T) {
	families := inventoryScrape(t, matchingSample(inventoryGPU0))

	missing := compareGPUIdentity(families, []nvidiasmi.GPU{inventoryGPU0, inventoryGPU1}, GPUInventoryOptions{})
	require.Equal(t, []GPUInventoryMismatch{{
		UUID: "GPU-1", Item: "samples", Expected: "at least one", Source: "GPU listed by nvidia-smi", Observed: "none",
	}}, missing)

	extra := compareGPUIdentity(inventoryScrape(t, matchingSample(inventoryGPU0), matchingSample(inventoryGPU1)),
		[]nvidiasmi.GPU{inventoryGPU0}, GPUInventoryOptions{})
	require.Len(t, extra, 1)
	require.Equal(t, "GPU-1", extra[0].UUID)
	require.Equal(t, "samples", extra[0].Item)
	require.Equal(t, "none", extra[0].Expected)
	require.Equal(t, "GPU not listed by nvidia-smi", extra[0].Source)

	// A pod sees only its own GPU, so other reported GPUs are allowed and the
	// pod-local index is not compared.
	podGPU := inventoryGPU1
	podGPU.Index = 0
	pod := compareGPUIdentity(inventoryScrape(t, matchingSample(inventoryGPU0), matchingSample(inventoryGPU1)),
		[]nvidiasmi.GPU{podGPU}, GPUInventoryOptions{PodGPUsOnly: true})
	require.Empty(t, pod)
}

// TestCompareGPUIdentityRejectsTwoIdentitiesForOneUUID rejects a UUID reported as two GPUs.
func TestCompareGPUIdentityRejectsTwoIdentitiesForOneUUID(t *testing.T) {
	first := matchingSample(inventoryGPU0)
	second := matchingSample(inventoryGPU0)
	second.gpu = "1"
	second.device = "nvidia1"
	second.values = map[string]float64{gpuTempFamily: 40}
	first.values = map[string]float64{smClockFamily: 1000 * hertzPerMHz}

	mismatches := compareGPUIdentity(inventoryScrape(t, first, second), []nvidiasmi.GPU{inventoryGPU0}, GPUInventoryOptions{})

	require.ElementsMatch(t, []string{"gpu", "identity"}, mismatchItems(mismatches))
}

// TestCompareGPUValuesTolerances checks each value comparison at and just past its edge.
func TestCompareGPUValuesTolerances(t *testing.T) {
	tests := []struct {
		name     string
		family   string
		observed float64
		wantFail bool
	}{
		{name: "capacity at 1%", family: fbCapacityFamily, observed: 1010 * bytesPerMiB},
		{name: "capacity past 1%", family: fbCapacityFamily, observed: 1011 * bytesPerMiB, wantFail: true},
		{name: "temperature at 5 C", family: gpuTempFamily, observed: 35},
		{name: "temperature past 5 C", family: gpuTempFamily, observed: 45.5, wantFail: true},
		{name: "memory clock at 1%", family: memoryClockFamily, observed: 2020 * hertzPerMHz},
		{name: "memory clock past 1%", family: memoryClockFamily, observed: 1979 * hertzPerMHz, wantFail: true},
		{name: "SM clock at 10%", family: smClockFamily, observed: 1100 * hertzPerMHz},
		{name: "SM clock past 10%", family: smClockFamily, observed: 899 * hertzPerMHz, wantFail: true},
		{name: "power at 15%", family: boardPowerFamily, observed: 230},
		{name: "power past 15%", family: boardPowerFamily, observed: 231, wantFail: true},
		{name: "clock in MHz instead of Hz", family: smClockFamily, observed: 1000, wantFail: true},
		{name: "capacity in MiB instead of bytes", family: fbCapacityFamily, observed: 1000, wantFail: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sample := matchingSample(inventoryGPU0)
			sample.values[tc.family] = tc.observed
			// Keep the framebuffer sum consistent so only the value under test can fail.
			sample.values[fbFreeFamily] = sample.values[fbCapacityFamily] - 100*bytesPerMiB

			report := compareGPUValues(inventoryScrape(t, sample), []nvidiasmi.GPU{inventoryGPU0}, matchingReadings(inventoryGPU0), inventoryCSVRows)

			if !tc.wantFail {
				require.NoError(t, report.Err())
				return
			}

			require.Len(t, report.Mismatches, 1, "%v", report.Mismatches)
			require.Equal(t, tc.family, report.Mismatches[0].Item)
			require.Contains(t, report.Mismatches[0].Source, "nvidia-smi")
		})
	}
}

// TestCompareGPUValuesPowerUsesTheTwentyWattFloor uses 20 W when 15% of the draw is smaller.
func TestCompareGPUValuesPowerUsesTheTwentyWattFloor(t *testing.T) {
	gpu := inventoryGPU0
	readings := matchingReadings(gpu)
	readings[gpu.UUID]["power.draw"] = smiReading(100)
	sample := matchingSample(gpu)

	sample.values[boardPowerFamily] = 80
	require.NoError(t, compareGPUValues(inventoryScrape(t, sample), []nvidiasmi.GPU{gpu}, readings, inventoryCSVRows).Err())

	sample.values[boardPowerFamily] = 121
	require.Error(t, compareGPUValues(inventoryScrape(t, sample), []nvidiasmi.GPU{gpu}, readings, inventoryCSVRows).Err())
}

// TestCompareGPUValuesFramebufferSum checks free + used + reserved against capacity.
func TestCompareGPUValuesFramebufferSum(t *testing.T) {
	sample := matchingSample(inventoryGPU0)
	sample.values[fbFreeFamily] = 910 * bytesPerMiB
	require.NoError(t, compareGPUValues(inventoryScrape(t, sample), []nvidiasmi.GPU{inventoryGPU0}, matchingReadings(inventoryGPU0), inventoryCSVRows).Err())

	sample.values[fbFreeFamily] = 911 * bytesPerMiB
	report := compareGPUValues(inventoryScrape(t, sample), []nvidiasmi.GPU{inventoryGPU0}, matchingReadings(inventoryGPU0), inventoryCSVRows)
	require.Equal(t, []string{"framebuffer free + used + reserved"}, mismatchItems(report.Mismatches))
}

// TestCompareGPUValuesSkipsUnreportedValues skips a placeholder from nvidia-smi with a stated reason.
func TestCompareGPUValuesSkipsUnreportedValues(t *testing.T) {
	readings := matchingReadings(inventoryGPU0)
	readings["GPU-0"]["power.draw"] = nvidiasmi.Reading{Raw: "[N/A]"}
	sample := matchingSample(inventoryGPU0)
	sample.values[boardPowerFamily] = 9999

	report := compareGPUValues(inventoryScrape(t, sample), []nvidiasmi.GPU{inventoryGPU0}, readings, inventoryCSVRows)

	require.NoError(t, report.Err())
	require.Equal(t, []string{`GPU GPU-0 DCGM_FI_DEV_BOARD_POWER_WATTS: nvidia-smi power.draw is "[N/A]"`}, report.Skipped)
}

// TestCompareGPUValuesReportsAGPUWithNothingCompared records a skip reason,
// not a failure, for a GPU whose enabled value rows are all placeholders.
func TestCompareGPUValuesReportsAGPUWithNothingCompared(t *testing.T) {
	readings := nvidiasmi.Readings{"GPU-0": map[string]nvidiasmi.Reading{
		"temperature.gpu": {Raw: "[N/A]"},
	}}
	rows := []DefaultCounterRow{{Name: gpuTempFamily, Type: dto.MetricType_GAUGE}}

	report := compareGPUValues(inventoryScrape(t, matchingSample(inventoryGPU0)), []nvidiasmi.GPU{inventoryGPU0}, readings, rows)

	require.NoError(t, report.Err())
	require.Equal(t, []string{
		`GPU GPU-0 DCGM_FI_DEV_GPU_TEMP_CELSIUS: nvidia-smi temperature.gpu is "[N/A]"`,
		"GPU GPU-0: no value was compared; every enabled check was skipped or found no sample",
	}, report.Skipped)
}

// TestCompareGPUValuesRejectsUnlabeledSamples fails a sample of a checked GPU
// family that has no UUID label, even when labeled samples exist.
func TestCompareGPUValuesRejectsUnlabeledSamples(t *testing.T) {
	families, err := ParseText([]byte(`# TYPE DCGM_FI_DEV_GPU_TEMP_CELSIUS gauge
DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0",UUID="GPU-0"} 40
DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="1"} 40
# TYPE DCGM_FI_DEV_OTHER gauge
DCGM_FI_DEV_OTHER{gpu="1"} 1
`))
	require.NoError(t, err)
	rows := []DefaultCounterRow{{Name: gpuTempFamily, Type: dto.MetricType_GAUGE}}

	report := compareGPUValues(families, []nvidiasmi.GPU{inventoryGPU0}, matchingReadings(inventoryGPU0), rows)

	require.Equal(t, []GPUInventoryMismatch{{
		UUID: "(none)", Item: gpuTempFamily, Expected: "a UUID label",
		Source: "GPU family in the run's CSV and in the scrape", Observed: "a sample without one",
	}}, report.Mismatches)
}

// TestCompareGPUValuesChecksOnlyEnabledRows leaves a row alone, with no skip
// reason, when its family is not in the CSV or not in the scrape, even when
// the value would disagree.
func TestCompareGPUValuesChecksOnlyEnabledRows(t *testing.T) {
	tests := []struct {
		name       string
		badFamily  string
		badValue   float64
		withoutCSV bool
	}{
		{name: "value family not in the CSV", badFamily: smClockFamily, badValue: 1, withoutCSV: true},
		{name: "value family not in the scrape", badFamily: smClockFamily},
		{name: "framebuffer family not in the CSV", badFamily: fbReservedFamily, badValue: 1, withoutCSV: true},
		{name: "framebuffer family not in the scrape", badFamily: fbReservedFamily},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sample := matchingSample(inventoryGPU0)
			rows := inventoryCSVRows
			if tc.withoutCSV {
				sample.values[tc.badFamily] = tc.badValue // would fail if checked
				rows = rowsWithout(rows, tc.badFamily)
			} else {
				delete(sample.values, tc.badFamily)
			}

			report := compareGPUValues(inventoryScrape(t, sample), []nvidiasmi.GPU{inventoryGPU0}, matchingReadings(inventoryGPU0), rows)

			require.NoError(t, report.Err())
			require.Empty(t, report.Skipped)
		})
	}
}

// rowsWithout returns the CSV rows except the named family.
func rowsWithout(rows []DefaultCounterRow, name string) []DefaultCounterRow {
	var kept []DefaultCounterRow
	for _, row := range rows {
		if row.Name != name {
			kept = append(kept, row)
		}
	}
	return kept
}

// TestCompareGPUValuesChecksNothingWithoutMatchingRows passes when the CSV
// enables no value row.
func TestCompareGPUValuesChecksNothingWithoutMatchingRows(t *testing.T) {
	sample := matchingSample(inventoryGPU0)
	sample.values[gpuTempFamily] = 999

	report := compareGPUValues(inventoryScrape(t, sample), []nvidiasmi.GPU{inventoryGPU0}, nil, nil)

	require.NoError(t, report.Err())
	require.Empty(t, report.Skipped)
}

// TestValueQueryFields lists the nvidia-smi fields only for rows the CSV enables.
func TestValueQueryFields(t *testing.T) {
	tests := []struct {
		name string
		rows []string
		want []string
	}{
		{name: "no rows", want: nil},
		{name: "unrelated rows", rows: []string{"DCGM_FI_DEV_FB_USED_BYTES", driverVersionLabel}, want: nil},
		{name: "one row", rows: []string{gpuTempFamily, "DCGM_FI_PROF_GR_ENGINE_ACTIVE"}, want: []string{"temperature.gpu"}},
		{
			name: "every row in table order",
			rows: []string{boardPowerFamily, smClockFamily, memoryClockFamily, gpuTempFamily, fbCapacityFamily},
			want: []string{"memory.total", "temperature.gpu", "clocks.mem", "clocks.sm", "power.draw"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var rows []DefaultCounterRow
			for _, name := range tc.rows {
				rows = append(rows, DefaultCounterRow{Name: name})
			}

			require.Equal(t, tc.want, ValueQueryFields(rows))
		})
	}
}

// TestCompareGPUValuesRequiresEveryGPUInACompletedFamily fails a GPU missing
// from a family the scrape has for another GPU, unless the GPU is in MIG mode.
func TestCompareGPUValuesRequiresEveryGPUInACompletedFamily(t *testing.T) {
	gpu1Sample := matchingSample(inventoryGPU1)
	delete(gpu1Sample.values, gpuTempFamily)
	families := inventoryScrape(t, matchingSample(inventoryGPU0), gpu1Sample)

	report := compareGPUValues(families, []nvidiasmi.GPU{inventoryGPU0, inventoryGPU1}, matchingReadings(inventoryGPU0, inventoryGPU1), inventoryCSVRows)
	require.Equal(t, []GPUInventoryMismatch{{
		UUID: "GPU-1", Item: gpuTempFamily, Expected: "a whole-GPU sample",
		Source: "family in the run's CSV and in the scrape", Observed: "none",
	}}, report.Mismatches)

	migGPU := inventoryGPU1
	migGPU.MIGEnabled = true
	report = compareGPUValues(families, []nvidiasmi.GPU{inventoryGPU0, migGPU}, matchingReadings(inventoryGPU0, migGPU), inventoryCSVRows)
	require.NoError(t, report.Err())
	require.Equal(t, []string{"GPU GPU-1 DCGM_FI_DEV_GPU_TEMP_CELSIUS: no whole-GPU sample on a GPU in MIG mode"}, report.Skipped)
}

// TestCompareGPUValuesIgnoresMIGInstanceSamples compares only whole-GPU samples.
func TestCompareGPUValuesIgnoresMIGInstanceSamples(t *testing.T) {
	families, err := ParseText([]byte(`# TYPE DCGM_FI_DEV_GPU_TEMP_CELSIUS gauge
DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0",UUID="GPU-0",GPU_I_ID="1"} 90
DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0",UUID="GPU-0"} 41
`))
	require.NoError(t, err)

	report := compareGPUValues(families, []nvidiasmi.GPU{inventoryGPU0}, matchingReadings(inventoryGPU0),
		[]DefaultCounterRow{{Name: gpuTempFamily, Type: dto.MetricType_GAUGE}})

	require.NoError(t, report.Err())
}

// TestCompareGPUValuesDriverVersion compares the driver version label row and
// requires it on each GPU when the CSV enables it.
func TestCompareGPUValuesDriverVersion(t *testing.T) {
	sample := matchingSample(inventoryGPU0)
	sample.driver = "570.00"

	report := compareGPUValues(inventoryScrape(t, sample), []nvidiasmi.GPU{inventoryGPU0}, matchingReadings(inventoryGPU0), inventoryCSVRows)

	require.Len(t, report.Mismatches, 1)
	require.Equal(t, driverVersionLabel, report.Mismatches[0].Item)
	require.Equal(t, `"580.82.07"`, report.Mismatches[0].Expected)
	require.Equal(t, `"570.00"`, report.Mismatches[0].Observed)

	sample.driver = ""
	report = compareGPUValues(inventoryScrape(t, sample), []nvidiasmi.GPU{inventoryGPU0}, matchingReadings(inventoryGPU0), inventoryCSVRows)
	require.Len(t, report.Mismatches, 1)
	require.Equal(t, driverVersionLabel, report.Mismatches[0].Item)
	require.Equal(t, "no label", report.Mismatches[0].Observed)
}

// TestGPUInventoryReportErrListsEveryMismatch names UUID, item, expected value, source and observed value.
func TestGPUInventoryReportErrListsEveryMismatch(t *testing.T) {
	report := GPUInventoryReport{Mismatches: []GPUInventoryMismatch{
		{UUID: "GPU-0", Item: "modelName", Expected: `"A"`, Source: "nvidia-smi name", Observed: `"B"`, Family: "F"},
		{UUID: "GPU-1", Item: "gpu", Expected: `"1"`, Source: "nvidia-smi index", Observed: `"2"`},
	}}

	err := report.Err()

	require.EqualError(t, err, "exporter output disagrees with nvidia-smi in 2 place(s):\n"+
		`GPU GPU-0 modelName: expected "A" (nvidia-smi name), observed "B" in F`+"\n"+
		`GPU GPU-1 gpu: expected "1" (nvidia-smi index), observed "2"`)
	require.NoError(t, GPUInventoryReport{}.Err())
}

// smiReading builds a reported nvidia-smi reading.
func smiReading(value float64) nvidiasmi.Reading {
	return nvidiasmi.Reading{Value: value, Reported: true, Raw: fmt.Sprint(value)}
}

// TestGPUInventoryReportErrIncludesSkipReasons checks that a failing report
// carries the skip reasons its mismatches refer to.
func TestGPUInventoryReportErrIncludesSkipReasons(t *testing.T) {
	report := GPUInventoryReport{
		Mismatches: []GPUInventoryMismatch{{UUID: "GPU-0", Item: "values", Expected: "at least one value compared", Observed: "none; see the skip reasons"}},
		Skipped:    []string{"GPU-0 DCGM_FI_DEV_GPU_TEMP_CELSIUS: nvidia-smi reports [N/A]"},
	}

	err := report.Err()

	require.Error(t, err)
	require.Contains(t, err.Error(), "skipped comparisons:\nGPU-0 DCGM_FI_DEV_GPU_TEMP_CELSIUS: nvidia-smi reports [N/A]")
}
