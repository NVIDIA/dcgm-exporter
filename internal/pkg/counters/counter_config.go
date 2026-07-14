/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package counters

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/kubeclient"
)

// getKubeClient is an unexported test seam for ConfigMap counter loading.
// Production code leaves it bound to kubeclient.GetKubeClient.
var getKubeClient = kubeclient.GetKubeClient

func GetCounterSet(ctx context.Context, c *appconfig.Config) (*CounterSet, error) {
	var (
		err     error
		records [][]string
	)

	res := new(CounterSet)
	source, err := metricSource(c)
	if err != nil {
		return nil, err
	}

	switch source.Kind {
	case appconfig.MetricSourceConfigMap:
		var client kubernetes.Interface
		client, err = getKubeClient()
		if err != nil {
			return nil, err
		}
		records, err = readConfigMapSource(ctx, client, source.ConfigMap)
		if err != nil {
			return nil, err
		}
	case appconfig.MetricSourceInline:
		records = metricFieldsToRecords(source.Fields)
	default:
		slog.Info(fmt.Sprintf("Using metric file '%s'", source.File))
		records, err = ReadCSVFile(source.File)
		if err != nil {
			slog.Error(fmt.Sprintf("Could not read metrics file '%s'; err: %v", source.File, err))
			return nil, err
		}
	}

	res, err = ExtractCounters(records, c)
	if err != nil {
		return res, err
	}

	return res, err
}

// metricSource resolves the active metric source from explicit config, compatibility ConfigMap data, or file defaults.
func metricSource(c *appconfig.Config) (appconfig.MetricSource, error) {
	if c == nil {
		return appconfig.MetricSource{}, fmt.Errorf("config is nil")
	}
	if c.MetricSource.Kind != "" {
		return c.MetricSource, nil
	}

	if c.ConfigMapData != "" && c.ConfigMapData != appconfig.UndefinedConfigMapData {
		parts := strings.Split(c.ConfigMapData, ":")
		if len(parts) != 2 {
			return appconfig.MetricSource{}, fmt.Errorf("malformed configmap-data '%s'", c.ConfigMapData)
		}
		return appconfig.MetricSource{
			Kind: appconfig.MetricSourceConfigMap,
			ConfigMap: appconfig.ConfigMapMetricSource{
				Namespace: parts[0],
				Name:      parts[1],
			},
		}, nil
	}

	return appconfig.MetricSource{
		Kind: appconfig.MetricSourceFile,
		File: c.CollectorsFile,
	}, nil
}

// metricFieldsToRecords converts inline YAML metric fields to the existing three-column CSV record shape.
func metricFieldsToRecords(fields []appconfig.MetricField) [][]string {
	records := make([][]string, 0, len(fields))
	for _, field := range fields {
		records = append(records, []string{field.Name, field.PrometheusType, field.Help})
	}
	return records
}

func ReadCSVFile(filename string) ([][]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	defer file.Close()

	r := csv.NewReader(file)
	r.Comment = '#'
	records, err := r.ReadAll()

	return records, err
}

// ExtractCounters parses counter CSV rows into DCGM and exporter counter sets.
func ExtractCounters(records [][]string, c *appconfig.Config) (*CounterSet, error) {
	res := CounterSet{}

	for i, record := range records {
		if len(record) == 0 {
			continue
		}

		for j, r := range record {
			record[j] = strings.Trim(r, " ")
		}

		if len(record) != 3 {
			return nil, fmt.Errorf("malformed CSV record; err: failed to parse line %d (`%v`), "+
				"expected 3 fields", i,
				record)
		}

		// Config may only declare scalar shapes the renderer can actually emit.
		if _, ok := promMetricType[record[1]]; !ok {
			return nil, fmt.Errorf(
				"unsupported Prometheus metric type %q; supported types are counter, gauge, untyped, and label",
				record[1],
			)
		}

		fieldID, ok := dcgm.GetFieldID(record[0])
		isLegacyField := dcgm.IsLegacyField(record[0])

		if !ok && !isLegacyField {

			expField, err := IdentifyMetricType(record[0])
			if err != nil {
				return nil, fmt.Errorf("could not find DCGM field; err: %w", err)
			} else if expField != DCGMFIUnknown {
				res.ExporterCounters = append(res.ExporterCounters,
					Counter{
						FieldID:   dcgm.Short(expField),
						FieldName: record[0],
						PromType:  record[1],
						Help:      record[2],
					})
				continue
			}
		}

		if !fieldIsSupported(uint(fieldID), c) {
			slog.Warn(fmt.Sprintf("Skipping line %d ('%s'): metric not enabled", i, record[0]))
			continue
		}

		res.DCGMCounters = append(res.DCGMCounters,
			Counter{FieldID: fieldID, FieldName: record[0], PromType: record[1], Help: record[2]})
	}

	return &res, nil
}

func fieldIsSupported(fieldID uint, c *appconfig.Config) bool {
	if fieldID < dcpFieldsStart || fieldID >= cpuFieldsStart {
		return true
	}

	if !c.CollectDCP {
		return false
	}

	for i := int(0); i < len(c.MetricGroups); i++ {
		for j := int(0); j < len(c.MetricGroups[i].FieldIds); j++ {
			if fieldID == c.MetricGroups[i].FieldIds[j] {
				return true
			}
		}
	}

	return false
}

// readConfigMapSource loads the compatibility API-backed ConfigMap source from DefaultConfigMapKey.
func readConfigMapSource(
	ctx context.Context,
	kubeClient kubernetes.Interface,
	source appconfig.ConfigMapMetricSource,
) ([][]string, error) {
	var cm *corev1.ConfigMap
	cm, err := kubeClient.CoreV1().ConfigMaps(source.Namespace).Get(ctx, source.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not retrieve ConfigMap '%s:%s'; err: %w", source.Namespace, source.Name, err)
	}

	if _, ok := cm.Data[appconfig.DefaultConfigMapKey]; !ok {
		return nil, fmt.Errorf("malformed ConfigMap '%s:%s'; no '%s' key", source.Namespace, source.Name, appconfig.DefaultConfigMapKey)
	}

	r := csv.NewReader(strings.NewReader(cm.Data[appconfig.DefaultConfigMapKey]))
	r.Comment = '#'
	records, err := r.ReadAll()

	if len(records) == 0 {
		return nil, fmt.Errorf("malformed configmap contents; err: no metrics found")
	}

	return records, err
}
