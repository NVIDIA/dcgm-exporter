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

package dcgmexporter

import (
	"bufio"
	sysOS "os"
	"path"
	"strconv"

	"github.com/sirupsen/logrus"
)

type hpcMapper struct {
	Config *Config
}

func newHPCMapper(c *Config) *hpcMapper {
	logrus.Infof("HPC job mapping is enabled and watch for the %q directory", c.HPCJobMappingDir)
	return &hpcMapper{
		Config: c,
	}
}

func (p *hpcMapper) Name() string {
	return "hpcMapper"
}

func (p *hpcMapper) Process(metrics MetricsByCounter, sysInfo SystemInfo) error {
	_, err := os.Stat(p.Config.HPCJobMappingDir)
	if err != nil {
		logrus.WithError(err).Warnf("Unable to access HPC job mapping file directory '%s' - directory not found. Ignoring.", p.Config.HPCJobMappingDir)
		return nil
	}

	gpuFiles, err := getGPUFiles(p.Config.HPCJobMappingDir)
	if err != nil {
		return err
	}

	gpuToJobMap := make(map[string][]string)

	logrus.Debugf("HPC job mapping files: %#v", gpuFiles)

	for _, gpuFileName := range gpuFiles {
		jobs, err := readFile(path.Join(p.Config.HPCJobMappingDir, gpuFileName))
		if err != nil {
			return err
		}

		if _, exist := gpuToJobMap[gpuFileName]; !exist {
			gpuToJobMap[gpuFileName] = []string{}
		}
		gpuToJobMap[gpuFileName] = append(gpuToJobMap[gpuFileName], jobs...)
	}

	logrus.Debugf("GPU to job mapping: %+v", gpuToJobMap)

	for counter := range metrics {
		var modifiedMetrics []Metric
		for _, metric := range metrics[counter] {
			jobs, exists := gpuToJobMap[metric.GPU]
			if exists {
				for _, job := range jobs {
					modifiedMetric, err := deepCopy(metric)
					if err != nil {
						logrus.WithError(err).Errorf("Can not create deepCopy for the value: %v", metric)
						continue
					}
					modifiedMetric.Attributes[hpcJobAttribute] = job
					modifiedMetrics = append(modifiedMetrics, modifiedMetric)
				}
			} else {
				modifiedMetrics = append(modifiedMetrics, metric)
			}
		}
		metrics[counter] = modifiedMetrics
	}

	return nil
}

func readFile(path string) ([]string, error) {
	var jobs []string

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func(file *sysOS.File) {
		err := file.Close()
		if err != nil {
			logrus.WithError(err).Errorf("Failed for close the file: %s", file.Name())
		}
	}(file)

	// Example of the expected file format:
	// job1
	// job2
	// job3
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		jobs = append(jobs, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return jobs, nil
}

func getGPUFiles(dirPath string) ([]string, error) {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	logrus.Debugf("hpc mapper: %d files in the %q found", len(files), dirPath)

	var mappingFiles []string

	for _, file := range files {
		finfo, err := file.Info()
		if err != nil {
			logrus.Warnf("HPC mapper: can not get file info for the %s file.", file.Name())
			continue // Skip files that we can't read
		}

		if finfo.IsDir() {
			logrus.Debugf("HPC mapper: the %q file is directory", file.Name())
			continue // Skip directories
		}

		_, err = strconv.Atoi(file.Name())
		if err != nil {
			logrus.Debugf("HPC mapper: file %q name doesn't match with GPU ID convention", file.Name())
			continue
		}
		mappingFiles = append(mappingFiles, file.Name())
	}

	return mappingFiles, nil
}
