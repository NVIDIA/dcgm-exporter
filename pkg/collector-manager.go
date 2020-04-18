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
	"time"
	"strings"
	"sync"
	"fmt"

	"github.com/golang/glog"
)

func NewCollectorMgr(c *Config) (*CollectorMgr, func(), error) {
	fields, err := ExtractMetrics(c.FieldsFile)
	if err != nil {
		return nil, func() {}, err
	}

	gpuCollector, cleanup, err := NewDCGMCollector(fields)
	if err != nil {
		return nil, func() {}, err
	}

	return &CollectorMgr{
		Collectors: []Collector{gpuCollector},
		CollectInterval: c.CollectInterval,
		Out: make(chan string, 10),
	}, func() {
		cleanup()
	}, nil
}

func (c *CollectorMgr) Run(stop chan interface{}, wg *sync.WaitGroup) {
	defer wg.Done()

	out := make([]string, len(c.Collectors))

	fmt.Printf("%v\n", c.CollectInterval)
	t := time.NewTicker(time.Millisecond * time.Duration(c.CollectInterval))
	defer t.Stop()

	for {
		select{
		case <-stop:
			return
		case <-t.C:
			for i, c := range c.Collectors {
				m, err := c.GetMetrics()
				if err != nil {
					glog.Errorf("Failed to collect metrics for collector: %v", c)
					continue
				}

				out[i] = m
			}

			if len(c.Out) == cap(c.Out) {
				glog.Errorf("Channel is full skipping")
			} else {
				c.Out <- strings.Join(out, "\n")
			}
		}
	}
}
