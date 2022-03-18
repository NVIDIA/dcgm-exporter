/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmexporter

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func ExtractCounters(c *Config) ([]Counter, error) {
	var err error
	var records [][]string

	if c.ConfigMapData != undefinedConfigMapData {
		var client kubernetes.Interface
		client, err = getKubeClient()
		if err == nil {
			records, err = readConfigMap(client, c)
		}
	} else {
		err = fmt.Errorf("No configmap data specified")
	}

	if err != nil {
		logrus.Infof("%v, falling back to metric file %s", err, c.CollectorsFile)

		records, err = ReadCSVFile(c.CollectorsFile)
		if err != nil {
			logrus.Errorf("Could not read metrics file '%s': %v\n", c.CollectorsFile, err)
			return nil, err
		}
	}

	counters, err := extractCounters(records, c.CollectDCP)
	if err != nil {
		return nil, err
	}

	return counters, err
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

func extractCounters(records [][]string, dcpAllowed bool) ([]Counter, error) {
	f := make([]Counter, 0, len(records))

	for i, record := range records {
		var useOld = false
		if len(record) == 0 {
			continue
		}

		for j, r := range record {
			record[j] = strings.Trim(r, " ")
		}

		if len(record) != 3 {
			return nil, fmt.Errorf("Malformed CSV record, failed to parse line %d (`%v`), expected 3 fields", i, record)
		}

		fieldID, ok := dcgm.DCGM_FI[record[0]]
		oldFieldID, oldOk := dcgm.OLD_DCGM_FI[record[0]]
		if !ok && !oldOk {
			return nil, fmt.Errorf("Could not find DCGM field %s", record[0])
		}

		if !ok && oldOk {
			useOld = true
		}

		if !useOld {
			if !dcpAllowed && fieldID >= 1000 {
				logrus.Warnf("Skipping line %d ('%s'): DCP metrics not enabled", i, record[0])
				continue
			}

			if _, ok := promMetricType[record[1]]; !ok {
				return nil, fmt.Errorf("Could not find Prometheus metry type %s", record[1])
			}

			f = append(f, Counter{fieldID, record[0], record[1], record[2]})
		} else {
			if !dcpAllowed && oldFieldID >= 1000 {
				logrus.Warnf("Skipping line %d ('%s'): DCP metrics not enabled", i, record[0])
				continue
			}

			if _, ok := promMetricType[record[1]]; !ok {
				return nil, fmt.Errorf("Could not find Prometheus metry type %s", record[1])
			}

			f = append(f, Counter{oldFieldID, record[0], record[1], record[2]})

		}
	}

	return f, nil
}

func readConfigMap(kubeClient kubernetes.Interface, c *Config) ([][]string, error) {
	parts := strings.Split(c.ConfigMapData, ":")
	if len(parts) != 2 {
		return nil, fmt.Errorf("Malformed configmap-data: %s", c.ConfigMapData)
	}

	var cm *corev1.ConfigMap
	cm, err := kubeClient.CoreV1().ConfigMaps(parts[0]).Get(context.TODO(), parts[1], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("Could not retrieve ConfigMap '%s': %v", c.ConfigMapData, err)
	}

	if _, ok := cm.Data["metrics"]; !ok {
		return nil, fmt.Errorf("Malformed ConfigMap '%s': no 'metrics' key", c.ConfigMapData)
	}

	r := csv.NewReader(strings.NewReader(cm.Data["metrics"]))

	records, err := r.ReadAll()

	if len(records) == 0 {
		return nil, fmt.Errorf("Malformed configmap contents. No metrics found")
	}

	return records, err
}

func getKubeClient() (kubernetes.Interface, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return client, err
}
