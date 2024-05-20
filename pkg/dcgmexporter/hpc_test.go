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
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	osmock "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/os"
	osinterface "github.com/NVIDIA/dcgm-exporter/internal/pkg/os"
)

func TestHPCProcess(t *testing.T) {
	realOS := osinterface.RealOS{}

	tests := []struct {
		name      string
		config    *Config
		fsState   func() func()
		assertion func(*testing.T, MetricsByCounter)
		wantErr   assert.ErrorAssertionFunc
	}{
		{
			name:   "When all GPU have job files",
			config: &Config{HPCJobMappingDir: "/var/run/nvidia/slurm"},
			fsState: func() func() {
				ctrl := gomock.NewController(t)
				mOS := osmock.NewMockOS(ctrl)
				mFileInfoGPU0 := osmock.NewMockFileInfo(ctrl)
				mFileInfoGPU0.EXPECT().IsDir().Return(false).AnyTimes()

				mDirEntryGPU0 := osmock.NewMockDirEntry(ctrl)
				mDirEntryGPU0.EXPECT().Info().Return(mFileInfoGPU0, nil).AnyTimes()
				mDirEntryGPU0.EXPECT().Name().Return("0").AnyTimes()

				mFileInfoGPU1 := osmock.NewMockFileInfo(ctrl)
				mFileInfoGPU1.EXPECT().IsDir().Return(false).AnyTimes()

				mDirEntryGPU1 := osmock.NewMockDirEntry(ctrl)
				mDirEntryGPU1.EXPECT().Info().Return(mFileInfoGPU1, nil).AnyTimes()
				mDirEntryGPU1.EXPECT().Name().Return("1").AnyTimes()

				mFileInfoDir := osmock.NewMockFileInfo(ctrl)
				mFileInfoDir.EXPECT().IsDir().Return(true).AnyTimes()

				mDirEntryDir := osmock.NewMockDirEntry(ctrl)
				mDirEntryDir.EXPECT().Info().Return(mFileInfoDir, nil).AnyTimes()
				mDirEntryDir.EXPECT().Name().Return("iamdir").AnyTimes()

				mDirEntryDamagedFile := osmock.NewMockDirEntry(ctrl)
				mDirEntryDamagedFile.EXPECT().Info().Return(nil, errors.New("boom")).AnyTimes()
				mDirEntryDamagedFile.EXPECT().Name().Return("iamerror").AnyTimes()

				mOS.EXPECT().Stat(gomock.Eq("/var/run/nvidia/slurm"))
				mOS.EXPECT().ReadDir(gomock.Eq("/var/run/nvidia/slurm")).
					Return([]fs.DirEntry{
						mDirEntryGPU0,
						mDirEntryGPU1,
						mDirEntryDir,
						mDirEntryDamagedFile,
					}, nil).AnyTimes()

				slurm0, err := realOS.CreateTemp("", "slurm0")
				require.NoError(t, err)
				_, _ = slurm0.WriteString("job1-0\n")
				slurm0.Close()

				slurm1, err := realOS.CreateTemp("", "slurm1")
				require.NoError(t, err)
				_, _ = slurm1.WriteString("job1-1\n")
				_, _ = slurm1.WriteString("job2-1\n")
				slurm1.Close()

				mOS.EXPECT().Open(gomock.Eq("/var/run/nvidia/slurm/0")).Return(realOS.Open(slurm0.Name()))
				mOS.EXPECT().Open(gomock.Eq("/var/run/nvidia/slurm/1")).Return(realOS.Open(slurm1.Name()))

				os = mOS
				return func() {
					os = osinterface.RealOS{}
					slurm0.Close()
					_ = realOS.Remove(slurm0.Name())
					slurm1.Close()
					_ = realOS.Remove(slurm1.Name())
				}
			},
			assertion: func(t *testing.T, mbc MetricsByCounter) {
				require.Len(t, mbc, 1, "metrics are expected for a single counter only.")
				// We get metric value with 0 index
				metricValues := mbc[reflect.ValueOf(mbc).MapKeys()[0].Interface().(Counter)]
				require.Len(t, metricValues, 4, "received unexpected number of metric values.")
				// Sort metrics by GPU ID
				slices.SortFunc(metricValues, func(a, b Metric) int {
					return cmp.Compare(a.GPU, b.GPU)
				})
				assert.Equal(t, "0", metricValues[0].GPU)
				assert.Equal(t, "42", metricValues[0].Value)
				assert.Equal(t, "job1-0", metricValues[0].Attributes[hpcJobAttribute])

				assert.Equal(t, "1", metricValues[1].GPU)
				assert.Equal(t, "451", metricValues[1].Value)
				assert.Equal(t, "job1-1", metricValues[1].Attributes[hpcJobAttribute])

				assert.Equal(t, "1", metricValues[2].GPU)
				assert.Equal(t, "451", metricValues[2].Value)
				assert.Equal(t, "job2-1", metricValues[2].Attributes[hpcJobAttribute])

				assert.Equal(t, "2", metricValues[3].GPU)
				assert.Equal(t, "1984", metricValues[3].Value)
				assert.NotContains(t, metricValues[3].Attributes, hpcJobAttribute)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fsState != nil {
				cleanup := tt.fsState()
				defer cleanup()
			}

			metrics := MetricsByCounter{}
			counter := Counter{
				FieldID:   155,
				FieldName: "DCGM_FI_DEV_POWER_USAGE",
				PromType:  "gauge",
			}

			metrics[counter] = append(metrics[counter], Metric{
				GPU:           "0",
				GPUUUID:       uuid.New().String(),
				GPUDevice:     "nvidia0",
				GPUInstanceID: "",
				Value:         "42",
				Counter: Counter{
					FieldID:   155,
					FieldName: "DCGM_FI_DEV_POWER_USAGE",
					PromType:  "gauge",
				},
				Attributes: map[string]string{},
			})

			metrics[counter] = append(metrics[counter], Metric{
				GPU:           "1",
				GPUUUID:       uuid.New().String(),
				GPUDevice:     "nvidia1",
				GPUInstanceID: "1",
				Value:         "451",
				Counter: Counter{
					FieldID:   155,
					FieldName: "DCGM_FI_DEV_POWER_USAGE",
					PromType:  "gauge",
				},
				Attributes: map[string]string{},
			})

			metrics[counter] = append(metrics[counter], Metric{
				GPU:           "2",
				GPUUUID:       uuid.New().String(),
				GPUDevice:     "nvidia3",
				GPUInstanceID: "2",
				Value:         "1984",
				Counter: Counter{
					FieldID:   155,
					FieldName: "DCGM_FI_DEV_POWER_USAGE",
					PromType:  "gauge",
				},
				Attributes: map[string]string{},
			})

			sysInfo := SystemInfo{
				GPUCount: 2,
				GPUs: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						DeviceInfo: dcgm.Device{
							UUID: "00000000-0000-0000-0000-000000000000",
							GPU:  0,
						},
					},
					{
						DeviceInfo: dcgm.Device{
							UUID: "00000000-0000-0000-0000-000000000001",
							GPU:  1,
						},
					},
				},
			}
			mapper := newHPCMapper(tt.config)
			err := mapper.Process(metrics, sysInfo)
			if tt.wantErr != nil && !tt.wantErr(t, err, fmt.Sprintf("hpcMapper.Process(%v,%v)", metrics, sysInfo)) {
				return
			}
			tt.assertion(t, metrics)
		})
	}
}

func TestHPCName(t *testing.T) {
	assert.Equal(t, "hpcMapper", newHPCMapper(&Config{}).Name())
}
