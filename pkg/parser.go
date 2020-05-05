/*
 * Copyright (c) 2020, NVIDIA CORPORATION.  All rights reserved.
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

package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
	"github.com/sirupsen/logrus"
)

func ExtractCounters(filename string) ([]Counter, error) {
	records, err := ReadCSVFile(filename)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return nil, err
	}

	counters, err := extractCounters(records)
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
	records, err := r.ReadAll()

	return records, err
}

func extractCounters(records [][]string) ([]Counter, error) {
	f := make([]Counter, 0, len(records))

	for i, record := range records {
		if len(record) == 0 {
			continue
		}

		for j, r := range record {
			record[j] = strings.Trim(r, " ")
		}

		if recordIsCommentOrEmpty(record) {
			logrus.Debugf("Skipping line %d (`%v`)", i, record)
			continue
		}

		if len(record) != 3 {
			return nil, fmt.Errorf("Malformed CSV record, failed to parse line %d (`%v`), expected 3 fields", i, record)
		}

		fieldID, ok := dcgm.DCGM_FI[record[0]]
		if !ok {
			return nil, fmt.Errorf("Could not find DCGM field %s", record[0])
		}

		if _, ok := promMetricType[record[1]]; !ok {
			return nil, fmt.Errorf("Could not find Prometheus metry type %s", record[1])
		}

		f = append(f, Counter{fieldID, record[0], record[1], record[2]})
	}

	return f, nil
}

func recordIsCommentOrEmpty(s []string) bool {
	if len(s) == 0 {
		return true
	}

	if len(s[0]) < 1 || s[0][0] == '#' {
		return true
	}

	return false
}
