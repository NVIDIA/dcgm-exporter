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
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	dto "github.com/prometheus/client_model/go"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// Framebuffer families whose sum is checked, as etc/default-counters.csv names
// them, and the driver version label row.
const (
	fbCapacityFamily   = "DCGM_FI_DEV_FB_CAPACITY_BYTES"
	fbFreeFamily       = "DCGM_FI_DEV_FB_FREE_BYTES"
	fbUsedFamily       = "DCGM_FI_DEV_FB_USED_BYTES"
	fbReservedFamily   = "DCGM_FI_DEV_FB_RESERVED_BYTES"
	driverVersionLabel = "DCGM_FI_SYSTEM_DRIVER_VERSION"
)

// Unit conversions from nvidia-smi units to exporter units, and the absolute
// part of the power tolerance.
const (
	bytesPerMiB         = 1 << 20
	hertzPerMHz         = 1e6
	powerToleranceWatts = 20
)

// GPUInventoryOptions says what the run expects beyond the nvidia-smi rows.
type GPUInventoryOptions struct {
	// CSVRows are the enabled rows of the CSV the exporter runs with. Only
	// families and label rows named there are compared.
	CSVRows []DefaultCounterRow
	// ExpectedHostname is the hostname label value every GPU sample must carry.
	// Empty skips the hostname check.
	ExpectedHostname string
	// HostnameSource names where ExpectedHostname came from, for messages.
	HostnameSource string
	// PodGPUsOnly says the rows come from nvidia-smi inside a workload pod.
	// Each listed GPU must be reported, the exporter may report more, and the
	// index is not compared because nvidia-smi numbers only the pod's GPUs.
	PodGPUsOnly bool
}

// GPUInventoryMismatch is one disagreement between exporter output and the
// expected GPU inventory.
type GPUInventoryMismatch struct {
	UUID     string
	Item     string
	Expected string
	Source   string
	Observed string
	Family   string
}

// String formats a mismatch for a test failure message.
func (m GPUInventoryMismatch) String() string {
	text := fmt.Sprintf("GPU %s %s: expected %s (%s), observed %s", m.UUID, m.Item, m.Expected, m.Source, m.Observed)
	if m.Family != "" {
		text += " in " + m.Family
	}
	return text
}

// GPUInventoryReport holds every mismatch and every check that was skipped
// because nvidia-smi or the GPU had no value, with the reason.
type GPUInventoryReport struct {
	Mismatches []GPUInventoryMismatch
	Skipped    []string
}

// Err returns one error listing every mismatch and every skipped check, or nil
// when there is no mismatch.
func (r GPUInventoryReport) Err() error {
	if len(r.Mismatches) == 0 {
		return nil
	}

	lines := make([]string, 0, len(r.Mismatches))
	for _, mismatch := range r.Mismatches {
		lines = append(lines, mismatch.String())
	}
	message := fmt.Sprintf("exporter output disagrees with nvidia-smi in %d place(s):\n%s",
		len(lines), strings.Join(lines, "\n"))

	// A skipped value can explain a mismatch, so a failure carries the reasons.
	if len(r.Skipped) > 0 {
		message += "\nskipped comparisons:\n" + strings.Join(r.Skipped, "\n")
	}
	return errors.New(message)
}

// CompareGPUInventory compares the identity labels and the values of every GPU
// sample with the nvidia-smi identities and readings, joined by GPU UUID.
// readings must hold the fields ValueQueryFields returns for opts.CSVRows.
func CompareGPUInventory(
	families map[string]*dto.MetricFamily, gpus []nvidiasmi.GPU, readings nvidiasmi.Readings, opts GPUInventoryOptions,
) GPUInventoryReport {
	report := compareGPUValues(families, gpus, readings, opts.CSVRows)
	report.Mismatches = append(compareGPUIdentity(families, gpus, opts), report.Mismatches...)
	return report
}

// gpuSample is one exporter sample that carries a GPU UUID label.
type gpuSample struct {
	family   string
	labels   map[string]string
	value    float64
	hasValue bool
}

// compareGPUIdentity checks the identity labels of every GPU sample in every
// family: modelName, pci_bus_id, gpu, device and hostname, one identity per
// UUID, and the set of reported UUIDs.
func compareGPUIdentity(
	families map[string]*dto.MetricFamily, gpus []nvidiasmi.GPU, opts GPUInventoryOptions,
) []GPUInventoryMismatch {
	expected := make(map[string]nvidiasmi.GPU, len(gpus))
	for _, gpu := range gpus {
		expected[gpu.UUID] = gpu
	}
	samples := gpuSamplesByUUID(families)
	var mismatches mismatchSet

	compareReportedGPUs(&mismatches, samples, gpus, expected, opts)

	for _, uuid := range slices.Sorted(maps.Keys(samples)) {
		gpu, known := expected[uuid]
		identities := map[string]string{}
		for _, sample := range samples[uuid] {
			identities[identityKey(sample.labels)] = sample.family
			if known {
				compareSampleIdentity(&mismatches, gpu, sample, opts)
			}
			compareSampleHostname(&mismatches, uuid, sample, opts)
		}

		// A UUID with two different identities means the exporter mixed up GPUs.
		if len(identities) > 1 {
			keys := slices.Sorted(maps.Keys(identities))
			mismatches.add(GPUInventoryMismatch{
				UUID: uuid, Item: "identity", Expected: "one identity per UUID", Source: "exporter contract",
				Observed: strings.Join(keys, " | "), Family: identities[keys[1]],
			})
		}
	}
	return mismatches.list
}

// compareReportedGPUs checks that every GPU nvidia-smi lists is reported and,
// with the full inventory, that no other GPU is.
func compareReportedGPUs(
	mismatches *mismatchSet, samples map[string][]gpuSample, gpus []nvidiasmi.GPU,
	expected map[string]nvidiasmi.GPU, opts GPUInventoryOptions,
) {
	for _, gpu := range gpus {
		if len(samples[gpu.UUID]) == 0 {
			mismatches.add(GPUInventoryMismatch{
				UUID: gpu.UUID, Item: "samples", Expected: "at least one", Source: "GPU listed by nvidia-smi",
				Observed: "none",
			})
		}
	}

	if opts.PodGPUsOnly {
		return
	}

	for _, uuid := range slices.Sorted(maps.Keys(samples)) {
		if _, ok := expected[uuid]; !ok {
			mismatches.add(GPUInventoryMismatch{
				UUID: uuid, Item: "samples", Expected: "none", Source: "GPU not listed by nvidia-smi",
				Observed: fmt.Sprintf("%d sample(s)", len(samples[uuid])), Family: samples[uuid][0].family,
			})
		}
	}
}

// compareSampleIdentity checks one sample's identity labels against its
// nvidia-smi row and the device naming contract.
func compareSampleIdentity(mismatches *mismatchSet, gpu nvidiasmi.GPU, sample gpuSample, opts GPUInventoryOptions) {
	labels := sample.labels
	add := func(item, expected, source, observed string) {
		mismatches.add(GPUInventoryMismatch{
			UUID: gpu.UUID, Item: item, Expected: strconv.Quote(expected), Source: source,
			Observed: strconv.Quote(observed), Family: sample.family,
		})
	}

	if gpu.Name != "" && labels["modelName"] != gpu.Name {
		add("modelName", gpu.Name, "nvidia-smi name", labels["modelName"])
	}
	if gpu.PCIBusID != "" && !strings.EqualFold(labels["pci_bus_id"], gpu.PCIBusID) {
		add("pci_bus_id", gpu.PCIBusID, "nvidia-smi pci.bus_id", labels["pci_bus_id"])
	}
	if !opts.PodGPUsOnly && labels["gpu"] != strconv.Itoa(gpu.Index) {
		add("gpu", strconv.Itoa(gpu.Index), "nvidia-smi index", labels["gpu"])
	}

	// The exporter names the device after the DCGM GPU ID it puts in the gpu label.
	if labels["device"] != "nvidia"+labels["gpu"] {
		add("device", "nvidia"+labels["gpu"], `"nvidia" + gpu label`, labels["device"])
	}
}

// compareSampleHostname checks the hostname label of one GPU sample.
func compareSampleHostname(mismatches *mismatchSet, uuid string, sample gpuSample, opts GPUInventoryOptions) {
	if opts.ExpectedHostname == "" {
		return
	}

	values := make([]string, 0, len(GPUHostnameLabels))
	for _, label := range GPUHostnameLabels {
		values = append(values, sample.labels[label])
	}
	observed := firstNonEmpty(values...)
	if observed == opts.ExpectedHostname {
		return
	}

	mismatches.add(GPUInventoryMismatch{
		UUID: uuid, Item: "hostname", Expected: strconv.Quote(opts.ExpectedHostname), Source: opts.HostnameSource,
		Observed: strconv.Quote(observed), Family: sample.family,
	})
}

// identityKey joins the identity labels of a sample so two samples of one
// UUID can be compared.
func identityKey(labels map[string]string) string {
	return fmt.Sprintf("gpu=%s device=%s modelName=%s pci_bus_id=%s",
		labels["gpu"], labels["device"], labels["modelName"], labels["pci_bus_id"])
}

// valueRow ties one exporter family to the nvidia-smi query field that
// measures the same thing.
type valueRow struct {
	family    string
	field     string
	scale     float64
	tolerance func(expected float64) float64
	describe  string
}

// gpuValueRows lists the value comparisons. A run checks only the rows whose
// family its CSV enables and its scrape contains.
//
// The table stays because nvidia-smi is the independent source: it has its own
// field names and units, so each row says how to convert. Comparing with
// dcgmi dmon instead would copy the exporter's own unit conversions and make
// the check circular. nvidia-smi and DCGM sample at different times, so live
// values get room to move.
var gpuValueRows = []valueRow{
	{
		family: fbCapacityFamily, field: "memory.total", scale: bytesPerMiB,
		tolerance: relativeTolerance(0.01), describe: "MiB x 1048576, within 1%",
	},
	{
		family: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", field: "temperature.gpu", scale: 1,
		tolerance: func(float64) float64 { return 5 }, describe: "°C, within 5 °C",
	},
	{
		family: "DCGM_FI_DEV_MEMORY_CLOCK_HERTZ", field: "clocks.mem", scale: hertzPerMHz,
		tolerance: relativeTolerance(0.01), describe: "MHz x 1e6, within 1%",
	},
	{
		family: "DCGM_FI_DEV_SM_CLOCK_HERTZ", field: "clocks.sm", scale: hertzPerMHz,
		tolerance: relativeTolerance(0.10), describe: "MHz x 1e6, within 10%",
	},
	{
		family: "DCGM_FI_DEV_BOARD_POWER_WATTS", field: "power.draw", scale: 1,
		tolerance: func(expected float64) float64 {
			return math.Max(powerToleranceWatts, 0.15*math.Abs(expected))
		},
		describe: "W, within the larger of 20 W or 15%",
	},
}

// ValueQueryFields returns the nvidia-smi query fields for the value rows the
// CSV rows enable, in table order. Pass them to the readings query so it asks
// only for what the comparison uses.
func ValueQueryFields(csvRows []DefaultCounterRow) []string {
	inCSV := csvFamilySet(csvRows)
	var fields []string
	for _, row := range gpuValueRows {
		if inCSV[row.family] {
			fields = append(fields, row.field)
		}
	}
	return fields
}

// csvFamilySet returns the set of names the CSV rows enable.
func csvFamilySet(csvRows []DefaultCounterRow) map[string]bool {
	names := make(map[string]bool, len(csvRows))
	for _, row := range csvRows {
		names[row.Name] = true
	}
	return names
}

// valueComparison holds the state of the value checks for one GPU.
type valueComparison struct {
	gpu        nvidiasmi.GPU
	readings   map[string]nvidiasmi.Reading
	samples    []gpuSample
	mismatches *mismatchSet
	skip       func(format string, args ...any)
	// compared counts the checks that ran to a result for this GPU.
	compared int
}

// compareGPUValues compares exporter values with nvidia-smi readings for every
// listed GPU. A value row or label row is checked only when the run's CSV
// enables its family and, for value rows, the scrape contains it; any other
// row is not checked and not reported. A value nvidia-smi does not report is
// skipped with a reason, and so is a GPU whose enabled rows all went unchecked.
// A GPU that lacks a sample of a checked family fails, unless the GPU is in MIG
// mode. A sample of a checked GPU family with no UUID label is a mismatch.
func compareGPUValues(
	families map[string]*dto.MetricFamily, gpus []nvidiasmi.GPU, readings nvidiasmi.Readings, csvRows []DefaultCounterRow,
) GPUInventoryReport {
	inCSV := csvFamilySet(csvRows)
	samples := gpuSamplesByUUID(families)
	var report GPUInventoryReport
	var mismatches mismatchSet
	skip := func(format string, args ...any) {
		report.Skipped = append(report.Skipped, fmt.Sprintf(format, args...))
	}

	// A family is checked only when the run's CSV enables it and the scrape has it.
	checked := map[string]bool{}
	for family := range inCSV {
		if _, scraped := families[family]; scraped {
			checked[family] = true
		}
	}

	for _, gpu := range gpus {
		comparison := &valueComparison{
			gpu: gpu, readings: readings[gpu.UUID], samples: samples[gpu.UUID], mismatches: &mismatches, skip: skip,
		}
		comparison.compareAll(checked, inCSV[driverVersionLabel])
	}

	compareUnlabeledSamples(&mismatches, families, checked)

	report.Mismatches = mismatches.list
	return report
}

// compareAll runs every enabled check for the GPU. When checks were enabled
// but none ran to a result, it records a skip reason so the unchecked GPU
// shows in the output.
func (c *valueComparison) compareAll(checked map[string]bool, driverVersionEnabled bool) {
	enabled := 0
	for _, row := range gpuValueRows {
		if checked[row.family] {
			enabled++
			c.compareReading(row)
		}
	}

	if checkedFramebufferFamilies(checked) {
		enabled++
		c.compareFramebufferSum()
	}

	if driverVersionEnabled {
		enabled++
		c.compareDriverVersion()
	}

	if enabled > 0 && c.compared == 0 {
		c.skip("GPU %s: no value was compared; every enabled check was skipped or found no sample", c.gpu.UUID)
	}
}

// compareUnlabeledSamples reports a mismatch for each checked GPU family that
// has a sample without a UUID label, because that sample belongs to no GPU and
// the per-GPU checks cannot see it.
func compareUnlabeledSamples(mismatches *mismatchSet, families map[string]*dto.MetricFamily, checked map[string]bool) {
	for _, family := range gpuFamilies() {
		if !checked[family] {
			continue
		}

		for _, metric := range families[family].GetMetric() {
			labels := metricLabels(metric)
			if firstNonEmpty(labels["UUID"], labels["uuid"]) != "" {
				continue
			}

			mismatches.add(GPUInventoryMismatch{
				UUID: "(none)", Item: family, Expected: "a UUID label", Source: "GPU family in the run's CSV and in the scrape",
				Observed: "a sample without one",
			})
		}
	}
}

// gpuFamilies lists the families the value checks read: every value row and
// the framebuffer families.
func gpuFamilies() []string {
	families := []string{fbFreeFamily, fbUsedFamily, fbReservedFamily}
	for _, row := range gpuValueRows {
		families = append(families, row.family)
	}
	return families
}

// checkedFramebufferFamilies reports whether all four framebuffer families are
// checked, which the framebuffer sum needs.
func checkedFramebufferFamilies(checked map[string]bool) bool {
	return checked[fbCapacityFamily] && checked[fbFreeFamily] && checked[fbUsedFamily] && checked[fbReservedFamily]
}

// compareReading compares one family's whole-GPU sample with one nvidia-smi
// reading after unit conversion.
func (c *valueComparison) compareReading(row valueRow) {
	reading := c.readings[row.field]
	if !reading.Reported {
		c.skip("GPU %s %s: nvidia-smi %s is %q", c.gpu.UUID, row.family, row.field, reading.Raw)
		return
	}

	observed, ok := c.wholeGPUValue(row.family)
	if !ok {
		return
	}

	c.compared++
	expected := reading.Value * row.scale
	if !withinTolerance(observed, expected, row.tolerance(expected)) {
		c.mismatches.add(GPUInventoryMismatch{
			UUID: c.gpu.UUID, Item: row.family, Expected: formatNumber(expected),
			Source:   fmt.Sprintf("nvidia-smi %s %s, %s", row.field, reading.Raw, row.describe),
			Observed: formatNumber(observed),
		})
	}
}

// compareFramebufferSum checks that free + used + reserved framebuffer equals
// the exporter's capacity within 1%. The caller runs it only when all four
// families are checked.
func (c *valueComparison) compareFramebufferSum() {
	families := []string{fbCapacityFamily, fbFreeFamily, fbUsedFamily, fbReservedFamily}

	values := make([]float64, 0, len(families))
	for _, family := range families {
		value, ok := c.wholeGPUValue(family)
		if !ok {
			return
		}
		values = append(values, value)
	}

	c.compared++
	capacity := values[0]
	sum := values[1] + values[2] + values[3]
	if !withinTolerance(sum, capacity, 0.01*capacity) {
		c.mismatches.add(GPUInventoryMismatch{
			UUID: c.gpu.UUID, Item: "framebuffer free + used + reserved", Expected: formatNumber(capacity),
			Source: fbCapacityFamily + ", within 1%", Observed: formatNumber(sum),
		})
	}
}

// compareDriverVersion requires the driver version label on the GPU's samples
// and checks every value it carries.
func (c *valueComparison) compareDriverVersion() {
	if c.gpu.DriverVersion == "" {
		c.skip("GPU %s %s: nvidia-smi driver_version is not reported", c.gpu.UUID, driverVersionLabel)
		return
	}

	c.compared++
	found := false
	for _, sample := range c.samples {
		observed, ok := sample.labels[driverVersionLabel]
		if !ok {
			continue
		}

		found = true
		if observed != c.gpu.DriverVersion {
			c.mismatches.add(GPUInventoryMismatch{
				UUID: c.gpu.UUID, Item: driverVersionLabel, Expected: strconv.Quote(c.gpu.DriverVersion),
				Source: "nvidia-smi driver_version", Observed: strconv.Quote(observed), Family: sample.family,
			})
		}
	}

	if !found {
		c.mismatches.add(GPUInventoryMismatch{
			UUID: c.gpu.UUID, Item: driverVersionLabel, Expected: strconv.Quote(c.gpu.DriverVersion),
			Source: "nvidia-smi driver_version; label row in the run's CSV", Observed: "no label",
		})
	}
}

// wholeGPUValue returns the value of the family's sample for the whole GPU,
// leaving out MIG instance samples. A missing sample is a mismatch, or a skip
// for a GPU in MIG mode.
func (c *valueComparison) wholeGPUValue(family string) (float64, bool) {
	for _, sample := range c.samples {
		if sample.family == family && sample.hasValue && sample.labels["GPU_I_ID"] == "" {
			return sample.value, true
		}
	}

	if c.gpu.MIGEnabled {
		c.skip("GPU %s %s: no whole-GPU sample on a GPU in MIG mode", c.gpu.UUID, family)
		return 0, false
	}

	c.mismatches.add(GPUInventoryMismatch{
		UUID: c.gpu.UUID, Item: family, Expected: "a whole-GPU sample",
		Source: "family in the run's CSV and in the scrape", Observed: "none",
	})
	return 0, false
}

// gpuSamplesByUUID groups every sample that carries a GPU UUID label, in every
// family, by that UUID. The lowercase uuid label is the --use-old-namespace
// name.
func gpuSamplesByUUID(families map[string]*dto.MetricFamily) map[string][]gpuSample {
	samples := map[string][]gpuSample{}
	for _, name := range slices.Sorted(maps.Keys(families)) {
		for _, metric := range families[name].GetMetric() {
			labels := metricLabels(metric)
			uuid := firstNonEmpty(labels["UUID"], labels["uuid"])
			if uuid == "" {
				continue
			}

			value, hasValue := metricSampleValue(metric)
			samples[uuid] = append(samples[uuid], gpuSample{family: name, labels: labels, value: value, hasValue: hasValue})
		}
	}
	return samples
}

// relativeTolerance returns a tolerance that is a fraction of the expected value.
func relativeTolerance(fraction float64) func(float64) float64 {
	return func(expected float64) float64 {
		return fraction * math.Abs(expected)
	}
}

// withinTolerance reports whether observed is at most tolerance away from
// expected. The edge counts as within.
func withinTolerance(observed, expected, tolerance float64) bool {
	return math.Abs(observed-expected) <= tolerance
}

// formatNumber prints a value without exponent notation so byte and hertz
// values stay readable.
func formatNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// mismatchSet collects mismatches and keeps one entry for the same mismatch
// seen in several families.
type mismatchSet struct {
	list []GPUInventoryMismatch
	seen map[string]bool
}

// add records a mismatch unless the same UUID, item, expected and observed
// values were already recorded.
func (s *mismatchSet) add(m GPUInventoryMismatch) {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}

	key := strings.Join([]string{m.UUID, m.Item, m.Expected, m.Observed}, "\x00")
	if s.seen[key] {
		return
	}

	s.seen[key] = true
	s.list = append(s.list, m)
}
