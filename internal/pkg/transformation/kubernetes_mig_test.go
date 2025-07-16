/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

package transformation

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"

	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"
)

const sampleMetricsJSON = `{
  "metrics": {
    "DCGM_FI_DEV_FB_FREE": [
      {
        "counter": {
          "field_id": 251,
          "field_name": "DCGM_FI_DEV_FB_FREE",
          "prom_type": "gauge",
          "help": "Framebuffer memory free (in MiB)."
        },
        "value": "9969",
        "gpu": "0",
        "gpu_uuid": "GPU-be839661-c0f5-7452-284b-b875666df60c",
        "gpu_device": "nvidia0",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:1B:00.0",
        "uuid": "UUID",
        "mig_profile": "1g.10gb",
        "gpu_instance_id": "11",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 251,
          "field_name": "DCGM_FI_DEV_FB_FREE",
          "prom_type": "gauge",
          "help": "Framebuffer memory free (in MiB)."
        },
        "value": "9969",
        "gpu": "0",
        "gpu_uuid": "GPU-be839661-c0f5-7452-284b-b875666df60c",
        "gpu_device": "nvidia0",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:1B:00.0",
        "uuid": "UUID",
        "mig_profile": "1g.10gb",
        "gpu_instance_id": "13",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 251,
          "field_name": "DCGM_FI_DEV_FB_FREE",
          "prom_type": "gauge",
          "help": "Framebuffer memory free (in MiB)."
        },
        "value": "81079",
        "gpu": "1",
        "gpu_uuid": "GPU-21c6d9d7-46cd-7e91-99c3-7b6a06a3faea",
        "gpu_device": "nvidia1",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:43:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 251,
          "field_name": "DCGM_FI_DEV_FB_FREE",
          "prom_type": "gauge",
          "help": "Framebuffer memory free (in MiB)."
        },
        "value": "81079",
        "gpu": "2",
        "gpu_uuid": "GPU-5d9cc71f-b438-dc00-707d-c6c12bcfede1",
        "gpu_device": "nvidia2",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:52:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 251,
          "field_name": "DCGM_FI_DEV_FB_FREE",
          "prom_type": "gauge",
          "help": "Framebuffer memory free (in MiB)."
        },
        "value": "81079",
        "gpu": "3",
        "gpu_uuid": "GPU-81d888ca-dd11-328c-45fa-d6807a1afa6a",
        "gpu_device": "nvidia3",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:61:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 251,
          "field_name": "DCGM_FI_DEV_FB_FREE",
          "prom_type": "gauge",
          "help": "Framebuffer memory free (in MiB)."
        },
        "value": "81079",
        "gpu": "4",
        "gpu_uuid": "GPU-c4c7f4f8-af86-6966-c0b2-7c1e40c18347",
        "gpu_device": "nvidia4",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:9D:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 251,
          "field_name": "DCGM_FI_DEV_FB_FREE",
          "prom_type": "gauge",
          "help": "Framebuffer memory free (in MiB)."
        },
        "value": "81079",
        "gpu": "5",
        "gpu_uuid": "GPU-7845680c-0e07-1670-c2bb-9f018cd7864b",
        "gpu_device": "nvidia5",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:C3:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 251,
          "field_name": "DCGM_FI_DEV_FB_FREE",
          "prom_type": "gauge",
          "help": "Framebuffer memory free (in MiB)."
        },
        "value": "81079",
        "gpu": "6",
        "gpu_uuid": "GPU-f70b214f-9fe8-5a4e-0499-0ff9572959ff",
        "gpu_device": "nvidia6",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:D1:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 251,
          "field_name": "DCGM_FI_DEV_FB_FREE",
          "prom_type": "gauge",
          "help": "Framebuffer memory free (in MiB)."
        },
        "value": "81079",
        "gpu": "7",
        "gpu_uuid": "GPU-eb5c9999-ebc3-9a6e-58cc-494befb69b8a",
        "gpu_device": "nvidia7",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:DF:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      }
    ],
    "DCGM_FI_DEV_FB_USED": [
      {
        "counter": {
          "field_id": 252,
          "field_name": "DCGM_FI_DEV_FB_USED",
          "prom_type": "gauge",
          "help": "Framebuffer memory used (in MiB)."
        },
        "value": "14",
        "gpu": "0",
        "gpu_uuid": "GPU-be839661-c0f5-7452-284b-b875666df60c",
        "gpu_device": "nvidia0",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:1B:00.0",
        "uuid": "UUID",
        "mig_profile": "1g.10gb",
        "gpu_instance_id": "11",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 252,
          "field_name": "DCGM_FI_DEV_FB_USED",
          "prom_type": "gauge",
          "help": "Framebuffer memory used (in MiB)."
        },
        "value": "14",
        "gpu": "0",
        "gpu_uuid": "GPU-be839661-c0f5-7452-284b-b875666df60c",
        "gpu_device": "nvidia0",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:1B:00.0",
        "uuid": "UUID",
        "mig_profile": "1g.10gb",
        "gpu_instance_id": "13",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 252,
          "field_name": "DCGM_FI_DEV_FB_USED",
          "prom_type": "gauge",
          "help": "Framebuffer memory used (in MiB)."
        },
        "value": "0",
        "gpu": "1",
        "gpu_uuid": "GPU-21c6d9d7-46cd-7e91-99c3-7b6a06a3faea",
        "gpu_device": "nvidia1",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:43:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 252,
          "field_name": "DCGM_FI_DEV_FB_USED",
          "prom_type": "gauge",
          "help": "Framebuffer memory used (in MiB)."
        },
        "value": "0",
        "gpu": "2",
        "gpu_uuid": "GPU-5d9cc71f-b438-dc00-707d-c6c12bcfede1",
        "gpu_device": "nvidia2",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:52:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 252,
          "field_name": "DCGM_FI_DEV_FB_USED",
          "prom_type": "gauge",
          "help": "Framebuffer memory used (in MiB)."
        },
        "value": "0",
        "gpu": "3",
        "gpu_uuid": "GPU-81d888ca-dd11-328c-45fa-d6807a1afa6a",
        "gpu_device": "nvidia3",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:61:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 252,
          "field_name": "DCGM_FI_DEV_FB_USED",
          "prom_type": "gauge",
          "help": "Framebuffer memory used (in MiB)."
        },
        "value": "0",
        "gpu": "4",
        "gpu_uuid": "GPU-c4c7f4f8-af86-6966-c0b2-7c1e40c18347",
        "gpu_device": "nvidia4",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:9D:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 252,
          "field_name": "DCGM_FI_DEV_FB_USED",
          "prom_type": "gauge",
          "help": "Framebuffer memory used (in MiB)."
        },
        "value": "0",
        "gpu": "5",
        "gpu_uuid": "GPU-7845680c-0e07-1670-c2bb-9f018cd7864b",
        "gpu_device": "nvidia5",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:C3:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 252,
          "field_name": "DCGM_FI_DEV_FB_USED",
          "prom_type": "gauge",
          "help": "Framebuffer memory used (in MiB)."
        },
        "value": "0",
        "gpu": "6",
        "gpu_uuid": "GPU-f70b214f-9fe8-5a4e-0499-0ff9572959ff",
        "gpu_device": "nvidia6",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:D1:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      },
      {
        "counter": {
          "field_id": 252,
          "field_name": "DCGM_FI_DEV_FB_USED",
          "prom_type": "gauge",
          "help": "Framebuffer memory used (in MiB)."
        },
        "value": "0",
        "gpu": "7",
        "gpu_uuid": "GPU-eb5c9999-ebc3-9a6e-58cc-494befb69b8a",
        "gpu_device": "nvidia7",
        "gpu_model": "NVIDIA H100 80GB HBM3",
        "pci_bus_id": "00000000:DF:00.0",
        "uuid": "UUID",
        "hostname": "localhost",
        "labels": {},
        "attributes": {}
      }
    ]
  }
}`

const sampleDeviceInfoJSON = `{
  "cpu_options": {
    "Flex": false,
    "MajorRange": null,
    "MinorRange": null
  },
  "cpus": null,
  "gpu_count": 8,
  "gpu_options": {
    "Flex": true,
    "MajorRange": null,
    "MinorRange": null
  },
  "gpus": [
    {
      "DeviceInfo": {
        "GPU": 0,
        "DCGMSupported": "Yes",
        "UUID": "GPU-be839661-c0f5-7452-284b-b875666df60c",
        "Power": 700,
        "PCI": {
          "BusID": "00000000:1B:00.0",
          "BAR1": 131072,
          "FBTotal": 81559,
          "Bandwidth": 0
        },
        "Identifiers": {
          "Brand": "NVIDIA",
          "Model": "NVIDIA H100 80GB HBM3",
          "Serial": "1652024027919",
          "Vbios": "96.00.A5.00.01",
          "InforomImageVersion": "G520.0200.00.05",
          "DriverVersion": "575.51.03"
        },
        "Topology": [
          {
            "GPU": 1,
            "BusID": "00000000:1B:00.0",
            "Link": 2
          },
          {
            "GPU": 2,
            "BusID": "00000000:1B:00.0",
            "Link": 2
          },
          {
            "GPU": 3,
            "BusID": "00000000:1B:00.0",
            "Link": 2
          },
          {
            "GPU": 4,
            "BusID": "00000000:1B:00.0",
            "Link": 1
          },
          {
            "GPU": 5,
            "BusID": "00000000:1B:00.0",
            "Link": 1
          },
          {
            "GPU": 6,
            "BusID": "00000000:1B:00.0",
            "Link": 1
          },
          {
            "GPU": 7,
            "BusID": "00000000:1B:00.0",
            "Link": 1
          }
        ],
        "CPUAffinity": "{0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167}"
      },
      "GPUInstances": [
        {
          "Info": {
            "GpuUuid": "GPU-be839661-c0f5-7452-284b-b875666df60c",
            "NvmlGpuIndex": 0,
            "NvmlInstanceId": 11,
            "NvmlComputeInstanceId": 4294967295,
            "NvmlMigProfileId": 19,
            "NvmlProfileSlices": 1
          },
          "ProfileName": "1g.10gb",
          "EntityId": 0,
          "ComputeInstances": [
            {
              "InstanceInfo": {
                "GpuUuid": "GPU-be839661-c0f5-7452-284b-b875666df60c",
                "NvmlGpuIndex": 0,
                "NvmlInstanceId": 11,
                "NvmlComputeInstanceId": 0,
                "NvmlMigProfileId": 0,
                "NvmlProfileSlices": 1
              },
              "ProfileName": "",
              "EntityId": 0
            }
          ]
        },
        {
          "Info": {
            "GpuUuid": "GPU-be839661-c0f5-7452-284b-b875666df60c",
            "NvmlGpuIndex": 0,
            "NvmlInstanceId": 13,
            "NvmlComputeInstanceId": 4294967295,
            "NvmlMigProfileId": 19,
            "NvmlProfileSlices": 1
          },
          "ProfileName": "1g.10gb",
          "EntityId": 1,
          "ComputeInstances": [
            {
              "InstanceInfo": {
                "GpuUuid": "GPU-be839661-c0f5-7452-284b-b875666df60c",
                "NvmlGpuIndex": 0,
                "NvmlInstanceId": 13,
                "NvmlComputeInstanceId": 0,
                "NvmlMigProfileId": 0,
                "NvmlProfileSlices": 1
              },
              "ProfileName": "",
              "EntityId": 1
            }
          ]
        }
      ],
      "MigEnabled": true
    },
    {
      "DeviceInfo": {
        "GPU": 1,
        "DCGMSupported": "Yes",
        "UUID": "GPU-21c6d9d7-46cd-7e91-99c3-7b6a06a3faea",
        "Power": 700,
        "PCI": {
          "BusID": "00000000:43:00.0",
          "BAR1": 131072,
          "FBTotal": 81559,
          "Bandwidth": 0
        },
        "Identifiers": {
          "Brand": "NVIDIA",
          "Model": "NVIDIA H100 80GB HBM3",
          "Serial": "1652024002485",
          "Vbios": "96.00.A5.00.01",
          "InforomImageVersion": "G520.0200.00.05",
          "DriverVersion": "575.51.03"
        },
        "Topology": [
          {
            "GPU": 0,
            "BusID": "00000000:43:00.0",
            "Link": 2
          },
          {
            "GPU": 2,
            "BusID": "00000000:43:00.0",
            "Link": 0
          },
          {
            "GPU": 3,
            "BusID": "00000000:43:00.0",
            "Link": 0
          },
          {
            "GPU": 4,
            "BusID": "00000000:43:00.0",
            "Link": 0
          },
          {
            "GPU": 5,
            "BusID": "00000000:43:00.0",
            "Link": 0
          },
          {
            "GPU": 6,
            "BusID": "00000000:43:00.0",
            "Link": 0
          },
          {
            "GPU": 7,
            "BusID": "00000000:43:00.0",
            "Link": 0
          }
        ],
        "CPUAffinity": "{0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167}"
      },
      "GPUInstances": null,
      "MigEnabled": false
    },
    {
      "DeviceInfo": {
        "GPU": 2,
        "DCGMSupported": "Yes",
        "UUID": "GPU-5d9cc71f-b438-dc00-707d-c6c12bcfede1",
        "Power": 700,
        "PCI": {
          "BusID": "00000000:52:00.0",
          "BAR1": 131072,
          "FBTotal": 81559,
          "Bandwidth": 0
        },
        "Identifiers": {
          "Brand": "NVIDIA",
          "Model": "NVIDIA H100 80GB HBM3",
          "Serial": "1652024004421",
          "Vbios": "96.00.A5.00.01",
          "InforomImageVersion": "G520.0200.00.05",
          "DriverVersion": "575.51.03"
        },
        "Topology": [
          {
            "GPU": 0,
            "BusID": "00000000:52:00.0",
            "Link": 2
          },
          {
            "GPU": 1,
            "BusID": "00000000:52:00.0",
            "Link": 0
          },
          {
            "GPU": 3,
            "BusID": "00000000:52:00.0",
            "Link": 0
          },
          {
            "GPU": 4,
            "BusID": "00000000:52:00.0",
            "Link": 0
          },
          {
            "GPU": 5,
            "BusID": "00000000:52:00.0",
            "Link": 0
          },
          {
            "GPU": 6,
            "BusID": "00000000:52:00.0",
            "Link": 0
          },
          {
            "GPU": 7,
            "BusID": "00000000:52:00.0",
            "Link": 0
          }
        ],
        "CPUAffinity": "{0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167}"
      },
      "GPUInstances": null,
      "MigEnabled": false
    },
    {
      "DeviceInfo": {
        "GPU": 3,
        "DCGMSupported": "Yes",
        "UUID": "GPU-81d888ca-dd11-328c-45fa-d6807a1afa6a",
        "Power": 700,
        "PCI": {
          "BusID": "00000000:61:00.0",
          "BAR1": 131072,
          "FBTotal": 81559,
          "Bandwidth": 0
        },
        "Identifiers": {
          "Brand": "NVIDIA",
          "Model": "NVIDIA H100 80GB HBM3",
          "Serial": "1652024028442",
          "Vbios": "96.00.A5.00.01",
          "InforomImageVersion": "G520.0200.00.05",
          "DriverVersion": "575.51.03"
        },
        "Topology": [
          {
            "GPU": 0,
            "BusID": "00000000:61:00.0",
            "Link": 2
          },
          {
            "GPU": 1,
            "BusID": "00000000:61:00.0",
            "Link": 0
          },
          {
            "GPU": 2,
            "BusID": "00000000:61:00.0",
            "Link": 0
          },
          {
            "GPU": 4,
            "BusID": "00000000:61:00.0",
            "Link": 0
          },
          {
            "GPU": 5,
            "BusID": "00000000:61:00.0",
            "Link": 0
          },
          {
            "GPU": 6,
            "BusID": "00000000:61:00.0",
            "Link": 0
          },
          {
            "GPU": 7,
            "BusID": "00000000:61:00.0",
            "Link": 0
          }
        ],
        "CPUAffinity": "{0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,112,113,114,115,116,117,118,119,120,121,122,123,124,125,126,127,128,129,130,131,132,133,134,135,136,137,138,139,140,141,142,143,144,145,146,147,148,149,150,151,152,153,154,155,156,157,158,159,160,161,162,163,164,165,166,167}"
      },
      "GPUInstances": null,
      "MigEnabled": false
    },
    {
      "DeviceInfo": {
        "GPU": 4,
        "DCGMSupported": "Yes",
        "UUID": "GPU-c4c7f4f8-af86-6966-c0b2-7c1e40c18347",
        "Power": 700,
        "PCI": {
          "BusID": "00000000:9D:00.0",
          "BAR1": 131072,
          "FBTotal": 81559,
          "Bandwidth": 0
        },
        "Identifiers": {
          "Brand": "NVIDIA",
          "Model": "NVIDIA H100 80GB HBM3",
          "Serial": "1652024020193",
          "Vbios": "96.00.A5.00.01",
          "InforomImageVersion": "G520.0200.00.05",
          "DriverVersion": "575.51.03"
        },
        "Topology": [
          {
            "GPU": 0,
            "BusID": "00000000:9D:00.0",
            "Link": 1
          },
          {
            "GPU": 1,
            "BusID": "00000000:9D:00.0",
            "Link": 0
          },
          {
            "GPU": 2,
            "BusID": "00000000:9D:00.0",
            "Link": 0
          },
          {
            "GPU": 3,
            "BusID": "00000000:9D:00.0",
            "Link": 0
          },
          {
            "GPU": 5,
            "BusID": "00000000:9D:00.0",
            "Link": 0
          },
          {
            "GPU": 6,
            "BusID": "00000000:9D:00.0",
            "Link": 0
          },
          {
            "GPU": 7,
            "BusID": "00000000:9D:00.0",
            "Link": 0
          }
        ],
        "CPUAffinity": "{56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200,201,202,203,204,205,206,207,208,209,210,211,212,213,214,215,216,217,218,219,220,221,222,223}"
      },
      "GPUInstances": null,
      "MigEnabled": false
    },
    {
      "DeviceInfo": {
        "GPU": 5,
        "DCGMSupported": "Yes",
        "UUID": "GPU-7845680c-0e07-1670-c2bb-9f018cd7864b",
        "Power": 700,
        "PCI": {
          "BusID": "00000000:C3:00.0",
          "BAR1": 131072,
          "FBTotal": 81559,
          "Bandwidth": 0
        },
        "Identifiers": {
          "Brand": "NVIDIA",
          "Model": "NVIDIA H100 80GB HBM3",
          "Serial": "1652024002476",
          "Vbios": "96.00.A5.00.01",
          "InforomImageVersion": "G520.0200.00.05",
          "DriverVersion": "575.51.03"
        },
        "Topology": [
          {
            "GPU": 0,
            "BusID": "00000000:C3:00.0",
            "Link": 1
          },
          {
            "GPU": 1,
            "BusID": "00000000:C3:00.0",
            "Link": 0
          },
          {
            "GPU": 2,
            "BusID": "00000000:C3:00.0",
            "Link": 0
          },
          {
            "GPU": 3,
            "BusID": "00000000:C3:00.0",
            "Link": 0
          },
          {
            "GPU": 4,
            "BusID": "00000000:C3:00.0",
            "Link": 0
          },
          {
            "GPU": 6,
            "BusID": "00000000:C3:00.0",
            "Link": 0
          },
          {
            "GPU": 7,
            "BusID": "00000000:C3:00.0",
            "Link": 0
          }
        ],
        "CPUAffinity": "{56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200,201,202,203,204,205,206,207,208,209,210,211,212,213,214,215,216,217,218,219,220,221,222,223}"
      },
      "GPUInstances": null,
      "MigEnabled": false
    },
    {
      "DeviceInfo": {
        "GPU": 6,
        "DCGMSupported": "Yes",
        "UUID": "GPU-f70b214f-9fe8-5a4e-0499-0ff9572959ff",
        "Power": 700,
        "PCI": {
          "BusID": "00000000:D1:00.0",
          "BAR1": 131072,
          "FBTotal": 81559,
          "Bandwidth": 0
        },
        "Identifiers": {
          "Brand": "NVIDIA",
          "Model": "NVIDIA H100 80GB HBM3",
          "Serial": "1652124069319",
          "Vbios": "96.00.A5.00.01",
          "InforomImageVersion": "G520.0200.00.05",
          "DriverVersion": "575.51.03"
        },
        "Topology": [
          {
            "GPU": 0,
            "BusID": "00000000:D1:00.0",
            "Link": 1
          },
          {
            "GPU": 1,
            "BusID": "00000000:D1:00.0",
            "Link": 0
          },
          {
            "GPU": 2,
            "BusID": "00000000:D1:00.0",
            "Link": 0
          },
          {
            "GPU": 3,
            "BusID": "00000000:D1:00.0",
            "Link": 0
          },
          {
            "GPU": 4,
            "BusID": "00000000:D1:00.0",
            "Link": 0
          },
          {
            "GPU": 5,
            "BusID": "00000000:D1:00.0",
            "Link": 0
          },
          {
            "GPU": 7,
            "BusID": "00000000:D1:00.0",
            "Link": 0
          }
        ],
        "CPUAffinity": "{56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200,201,202,203,204,205,206,207,208,209,210,211,212,213,214,215,216,217,218,219,220,221,222,223}"
      },
      "GPUInstances": null,
      "MigEnabled": false
    },
    {
      "DeviceInfo": {
        "GPU": 7,
        "DCGMSupported": "Yes",
        "UUID": "GPU-eb5c9999-ebc3-9a6e-58cc-494befb69b8a",
        "Power": 700,
        "PCI": {
          "BusID": "00000000:DF:00.0",
          "BAR1": 131072,
          "FBTotal": 81559,
          "Bandwidth": 0
        },
        "Identifiers": {
          "Brand": "NVIDIA",
          "Model": "NVIDIA H100 80GB HBM3",
          "Serial": "1652024028462",
          "Vbios": "96.00.A5.00.01",
          "InforomImageVersion": "G520.0200.00.05",
          "DriverVersion": "575.51.03"
        },
        "Topology": [
          {
            "GPU": 0,
            "BusID": "00000000:DF:00.0",
            "Link": 1
          },
          {
            "GPU": 1,
            "BusID": "00000000:DF:00.0",
            "Link": 0
          },
          {
            "GPU": 2,
            "BusID": "00000000:DF:00.0",
            "Link": 0
          },
          {
            "GPU": 3,
            "BusID": "00000000:DF:00.0",
            "Link": 0
          },
          {
            "GPU": 4,
            "BusID": "00000000:DF:00.0",
            "Link": 0
          },
          {
            "GPU": 5,
            "BusID": "00000000:DF:00.0",
            "Link": 0
          },
          {
            "GPU": 6,
            "BusID": "00000000:DF:00.0",
            "Link": 0
          }
        ],
        "CPUAffinity": "{56,57,58,59,60,61,62,63,64,65,66,67,68,69,70,71,72,73,74,75,76,77,78,79,80,81,82,83,84,85,86,87,88,89,90,91,92,93,94,95,96,97,98,99,100,101,102,103,104,105,106,107,108,109,110,111,168,169,170,171,172,173,174,175,176,177,178,179,180,181,182,183,184,185,186,187,188,189,190,191,192,193,194,195,196,197,198,199,200,201,202,203,204,205,206,207,208,209,210,211,212,213,214,215,216,217,218,219,220,221,222,223}"
      },
      "GPUInstances": null,
      "MigEnabled": false
    }
  ],
  "info_type": 1,
  "switch_options": {
    "Flex": false,
    "MajorRange": null,
    "MinorRange": null
  },
  "switches": null
}`

const samplePodResourcesResponseJSON = `{
  "pod_resources": [
    {
      "name": "ollama-76f687f4cd-xcvvp",
      "namespace": "testing",
      "containers": [
        {
          "name": "ollama",
          "devices": [
            {
              "resource_name": "nvidia.com/gpu",
              "device_ids": [
                "GPU-be839661-c0f5-7452-284b-b875666df60c",
                "MIG-GPU-be839661-c0f5-7452-284b-b875666df60c"
              ],
              "topology": {
                "nodes": [
                  {
                    "id": 0
                  }
                ]
              }
            }
          ]
        }
      ]
    }
  ]
}`

// unmarshalSampleMetrics is an unexported reusable function to unmarshal sample metrics JSON
func unmarshalSampleMetrics() (collector.MetricsByCounter, error) {
	var metrics collector.MetricsByCounter
	err := json.Unmarshal([]byte(sampleMetricsJSON), &metrics)
	return metrics, err
}

// TestPodMapperProcess_WhenMIGDevicesAvailableAndPodRunning tests the PodMapper.Process function when MIG devices are available and pods are running
func TestPodMapperProcess_WhenMIGDevicesAvailableAndPodRunning(t *testing.T) {
	testutils.RequireLinux(t)

	// Unmarshal sample data
	metrics, err := unmarshalSampleMetrics()
	require.NoError(t, err)
	require.NotNil(t, metrics)

	// Create device info from sample JSON
	var deviceInfo deviceinfo.Info
	err = json.Unmarshal([]byte(sampleDeviceInfoJSON), &deviceInfo)
	require.NoError(t, err)
	require.NotNil(t, deviceInfo)

	// Create a temporary directory for the socket
	tmpDir, cleanup := testutils.CreateTmpDir(t)
	defer cleanup()
	socketPath := tmpDir + "/kubelet.sock"

	// Create a gRPC server with our sample pod resources
	server := grpc.NewServer()

	// Create a custom mock server that returns our sample data
	customMockServer := &CustomMockPodResourcesServer{
		sampleData: samplePodResourcesResponseJSON,
	}
	podresourcesapi.RegisterPodResourcesListerServer(server, customMockServer)

	// Start the mock server
	cleanupServer := testutils.StartMockServer(t, server, socketPath)
	defer cleanupServer()

	// Setup NVML provider mock
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockNVMLProvider := mocknvmlprovider.NewMockNVML(ctrl)

	// Mock MIG device info for any MIG device ID
	migDeviceInfo := &nvmlprovider.MIGDeviceInfo{
		ParentUUID:        "GPU-be839661-c0f5-7452-284b-b875666df60c",
		GPUInstanceID:     11,
		ComputeInstanceID: 0,
	}
	mockNVMLProvider.EXPECT().GetMIGDeviceInfoByID(gomock.Any()).Return(migDeviceInfo, nil).AnyTimes()
	nvmlprovider.SetClient(mockNVMLProvider)

	// Create PodMapper with socket path
	podMapper := NewPodMapper(&appconfig.Config{
		KubernetesGPUIdType:       appconfig.GPUUID,
		PodResourcesKubeletSocket: socketPath,
		KubernetesEnablePodLabels: false,
		KubernetesVirtualGPUs:     false,
	})
	require.NotNil(t, podMapper)

	// Store original metrics count for verification
	originalMetricsCount := len(metrics)

	// Process the metrics
	err = podMapper.Process(metrics, &deviceInfo)
	require.NoError(t, err)

	// Verify that metrics still exist after processing
	require.Equal(t, originalMetricsCount, len(metrics), "Number of metric types should remain the same")

	// Verify that metrics have been processed and maintain their structure
	for counter, metricList := range metrics {
		require.NotEmpty(t, metricList, "Metric list should not be empty")

		// Track which GPUs we've seen for this counter
		gpusSeen := make(map[string]bool)

		// Extract expected GPUs from the sample data at runtime
		expectedGPUs := make(map[string]bool)
		for _, metric := range metricList {
			expectedGPUs[metric.GPU] = true
		}

		// Verify that metrics still have their original structure
		for _, metric := range metricList {
			require.NotEmpty(t, metric.GPU, "GPU field should not be empty")
			require.NotEmpty(t, metric.GPUUUID, "GPUUUID field should not be empty")
			require.NotEmpty(t, metric.Value, "Value field should not be empty")
			require.NotNil(t, metric.Attributes, "Attributes map should not be nil")
			require.NotNil(t, metric.Labels, "Labels map should not be nil")

			// Track this GPU for this counter
			gpusSeen[metric.GPU] = true

			// MIG-specific validation
			if metric.MigProfile != "" {
				// This is a MIG metric, verify it has proper structure
				require.NotEmpty(t, metric.GPUInstanceID, "MIG metric should have GPU instance ID")
				require.NotEmpty(t, metric.MigProfile, "MIG metric should have MIG profile")
				require.NotEmpty(t, metric.GPU, "MIG metric should have GPU ID")

				// Verify the MIG profile matches what we expect from device info
				if metric.GPU == "0" {
					require.Equal(t, "1g.10gb", metric.MigProfile, "GPU 0 should have 1g.10gb MIG profile")
				}
			}
		}

		// Assert that each expected GPU has a metric for this counter
		for expectedGPU := range expectedGPUs {
			require.True(t, gpusSeen[expectedGPU],
				"Counter %s should have a metric for GPU %s", counter.FieldName, expectedGPU)
		}

		// Verify GPU instances are present for MIG metrics
		gpuInstancesSeen := make(map[string]bool)
		expectedGPUInstances := make(map[string]bool)

		for _, metric := range metricList {
			if metric.MigProfile != "" && metric.GPUInstanceID != "" {
				// Track GPU instances for MIG metrics
				gpuInstanceKey := fmt.Sprintf("%s-%s", metric.GPU, metric.GPUInstanceID)
				gpuInstancesSeen[gpuInstanceKey] = true
				expectedGPUInstances[gpuInstanceKey] = true
			}
		}

		// Assert that each expected GPU instance has a metric for this counter
		for expectedGPUInstance := range expectedGPUInstances {
			require.True(t, gpuInstancesSeen[expectedGPUInstance],
				"Counter %s should have a metric for GPU instance %s", counter.FieldName, expectedGPUInstance)
		}

		// Verify counter structure
		require.NotEmpty(t, counter.FieldName, "Counter field name should not be empty")
		require.NotZero(t, counter.FieldID, "Counter field ID should not be zero")
		require.NotEmpty(t, counter.PromType, "Counter prom type should not be empty")
	}
}

// CustomMockPodResourcesServer is a custom mock server that returns our sample pod resources data
type CustomMockPodResourcesServer struct {
	podresourcesapi.UnimplementedPodResourcesListerServer
	sampleData string
}

func (s *CustomMockPodResourcesServer) List(
	ctx context.Context, req *podresourcesapi.ListPodResourcesRequest,
) (*podresourcesapi.ListPodResourcesResponse, error) {
	var response podresourcesapi.ListPodResourcesResponse
	err := json.Unmarshal([]byte(s.sampleData), &response)
	if err != nil {
		return nil, err
	}
	return &response, nil
}

func (s *CustomMockPodResourcesServer) Get(
	ctx context.Context, req *podresourcesapi.GetPodResourcesRequest,
) (*podresourcesapi.GetPodResourcesResponse, error) {
	var listResponse podresourcesapi.ListPodResourcesResponse
	err := json.Unmarshal([]byte(s.sampleData), &listResponse)
	if err != nil {
		return nil, err
	}

	if len(listResponse.PodResources) > 0 {
		return &podresourcesapi.GetPodResourcesResponse{
			PodResources: listResponse.PodResources[0],
		}, nil
	}

	return &podresourcesapi.GetPodResourcesResponse{}, nil
}

func (s *CustomMockPodResourcesServer) GetAllocatableResources(
	ctx context.Context, req *podresourcesapi.AllocatableResourcesRequest,
) (*podresourcesapi.AllocatableResourcesResponse, error) {
	return &podresourcesapi.AllocatableResourcesResponse{}, nil
}
