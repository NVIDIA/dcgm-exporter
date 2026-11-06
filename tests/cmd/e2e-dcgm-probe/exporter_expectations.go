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
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	dto "github.com/prometheus/client_model/go"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

const (
	xidCountFamily = "DCGM_EXP_XID_ERRORS_COUNT"
	xidTotalFamily = "DCGM_EXP_XID_ERRORS_TOTAL"
	xidRawFamily   = "DCGM_FI_DEV_XID_ERROR"
)

// verifyExporterScrape derives the metric oracle from independently read
// native history, retaining typed entities and every sampled observation.
func verifyExporterScrape(report dcgmprobe.ExporterAcceptance, body string, scrapeUsec int64) error {
	families, err := metriccontract.ParseText([]byte(body))
	if err != nil {
		return fmt.Errorf("parse exporter scrape: %w", err)
	}
	for _, family := range []struct {
		name string
		kind dto.MetricType
	}{{xidCountFamily, dto.MetricType_GAUGE}, {xidTotalFamily, dto.MetricType_COUNTER}} {
		observed := families[family.name]
		if observed == nil || observed.GetType() != family.kind {
			return fmt.Errorf("missing or wrong type for %s", family.name)
		}
		expected := expectedEventMetrics(report, family.name, scrapeUsec)
		if err := compareEventMetrics(observed, expected); err != nil {
			return err
		}
	}
	raw := families[xidRawFamily]
	if report.Scenario != "ci-raw" {
		if raw != nil {
			return fmt.Errorf("derived-only CSV enabled raw XID gauge")
		}
		return nil
	}
	if raw == nil || raw.GetType() != dto.MetricType_GAUGE {
		return fmt.Errorf("raw XID gauge missing or wrong type")
	}
	return compareEventMetrics(raw, expectedRawMetrics(report))
}

type expectedEventMetric struct {
	labels map[string]string
	value  float64
}

func expectedEventMetrics(report dcgmprobe.ExporterAcceptance, family string, scrapeUsec int64) []expectedEventMetric {
	var expected []expectedEventMetric
	for _, target := range report.Targets {
		if !target.Selected {
			continue
		}
		counts := map[int64]int{}
		for _, sample := range report.History {
			if sample.Entity != target.Entity || !sampleContributesToFamily(report, family, sample, scrapeUsec) {
				continue
			}
			counts[sample.Value]++
		}
		if len(counts) == 0 && family == xidCountFamily {
			labels := eventMetricLabels(target.Labels, family, report.WindowSizeMS)
			expected = append(expected, expectedEventMetric{labels: labels})
		}
		for code, count := range counts {
			labels := eventMetricLabels(target.Labels, family, report.WindowSizeMS)
			labels["xid"] = strconv.FormatInt(code, 10)
			expected = append(expected, expectedEventMetric{labels: labels, value: float64(count)})
		}
	}
	return expected
}

func eventMetricLabels(base map[string]string, family string, windowSizeMS int) map[string]string {
	labels := maps.Clone(base)
	if family == xidCountFamily {
		labels["window_size_in_ms"] = strconv.Itoa(windowSizeMS)
	}
	return labels
}

func sampleContributesToFamily(report dcgmprobe.ExporterAcceptance, family string, sample dcgmprobe.HistorySample, scrapeUsec int64) bool {
	if family == xidCountFamily {
		return sample.TimestampUsec >= scrapeUsec-int64(report.WindowSizeMS)*1000
	}
	// TOTAL excludes zero and the deliberately pre-start observation.
	return sample.Value != 0 && sample.TimestampUsec >= report.TotalSinceUsec
}

func expectedRawMetrics(report dcgmprobe.ExporterAcceptance) []expectedEventMetric {
	var expected []expectedEventMetric
	for _, target := range report.Targets {
		if !target.Selected {
			continue
		}
		var latest dcgmprobe.HistorySample
		for _, sample := range report.History {
			if sample.Entity == target.Entity && sample.TimestampUsec > latest.TimestampUsec {
				latest = sample
			}
		}
		// Raw XID's err_msg is a maintained description, not an identity label.
		labels := maps.Clone(target.Labels)
		labels["err_code"] = strconv.FormatInt(latest.Value, 10)
		expected = append(expected, expectedEventMetric{labels: labels, value: float64(latest.Value)})
	}
	return expected
}

func compareEventMetrics(family *dto.MetricFamily, expected []expectedEventMetric) error {
	if len(family.Metric) != len(expected) {
		return fmt.Errorf("%s sample count=%d, want %d", family.GetName(), len(family.Metric), len(expected))
	}
	remaining := slices.Clone(expected)
	for _, sample := range family.Metric {
		labels := map[string]string{}
		for _, label := range sample.Label {
			labels[label.GetName()] = label.GetValue()
		}
		if family.GetName() == xidRawFamily {
			if labels["err_msg"] == "" {
				return fmt.Errorf("raw XID lacks error description")
			}
			delete(labels, "err_msg")
		}
		index := slices.IndexFunc(remaining, func(want expectedEventMetric) bool { return maps.Equal(want.labels, labels) })
		if index < 0 {
			return fmt.Errorf("%s unexpected identity/labels %v", family.GetName(), labels)
		}
		value := sample.GetGauge().GetValue()
		if family.GetType() == dto.MetricType_COUNTER {
			value = sample.GetCounter().GetValue()
		}
		if value != remaining[index].value {
			return fmt.Errorf("%s value=%v, want %v for %v", family.GetName(), value, remaining[index].value, labels)
		}
		remaining = slices.Delete(remaining, index, index+1)
	}
	return nil
}

func exporterCSV(scenario string) string {
	csv := xidCountFamily + ",gauge,Sampled XID observations in the configured window.\n" + xidTotalFamily + ",counter,Sampled XID observations since exporter startup.\n"
	if scenario == "ci-raw" {
		csv += xidRawFamily + ",gauge,Latest XID value.\n"
	}
	return csv
}

func validExporterScenario(scenario string) bool {
	return slices.Contains([]string{"ci-derived", "ci-raw", "ci-only", "plain", "mixed"}, scenario)
}

func sameExporterHistory(report dcgmprobe.ExporterAcceptance, actual []dcgmprobe.HistorySample) bool {
	expected := slices.Clone(report.Injected)
	if report.Scenario == "ci-only" {
		// Empty watched scopes return typed NO_DATA records rather than observations.
		for _, target := range report.Targets {
			if target.Selected && (target.Entity.Group == uint(dcgm.FE_GPU) || target.Entity.Group == uint(dcgm.FE_GPU_I)) {
				expected = append(expected, dcgmprobe.HistorySample{
					Entity: target.Entity, FieldID: uint(dcgm.DCGM_FI_DEV_XID_ERRORS), FieldType: dcgm.DCGM_FT_INT64,
					Status: dcgm.DCGM_ST_NO_DATA, Value: dcgm.DCGM_FT_INT64_BLANK,
				})
			}
		}
	}
	return sameHistory(expected, actual)
}
