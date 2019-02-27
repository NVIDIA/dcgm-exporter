# Pod Device Metrics

Collect per pod device metrics via pod-devices-exporter. pod-device-exporter connects to kubelet gRPC server (/var/lib/kubelet/pod-resources) to get GPU device's pod information and appends to metrics collected by [dcgm-exporter](https://github.com/NVIDIA/gpu-monitoring-tools/tree/master/exporters/prometheus-dcgm/dcgm-exporter). pod-device-exporter leverages Kubernetes [device assignment feature](https://github.com/vikaschoudhary16/community/blob/060a25c441269be476ade624ea0347ebc113e659/keps/sig-node/compute-device-assignment.md) and identifies the GPUs running on a pod.

### Prerequisites
* NVIDIA Tesla drivers = R384+ (download from [NVIDIA Driver Downloads page](http://www.nvidia.com/drivers))
* nvidia-docker version > 2.0 (see how to [install](https://github.com/NVIDIA/nvidia-docker) and it's [prerequisites](https://github.com/nvidia/nvidia-docker/wiki/Installation-\(version-2.0\)#prerequisites))
* Set the [default runtime](https://github.com/NVIDIA/nvidia-container-runtime#daemon-configuration-file) to nvidia
* Kubernetes version = 1.13
* Set KubeletPodResources in /etc/default/kubelet: KUBELET_EXTRA_ARGS=--feature-gates=KubeletPodResources=true

#### Deploy on Kubernetes cluster 
```sh
# Deploy nvidia-k8s-device-plugin

# Deploy GPU Pods

# Make sure to attach matching label to the GPU node
$ kubectl label nodes <gpu-node-name> hardware-type=NVIDIAGPU

# Check if the label is added
$ kubectl get nodes --show-labels

# Collect per pod device metrics
$ kubectl create -f node-device-exporter-daemonset.yaml

# Check if node-exporter is collecting the device metrics successfully
$ curl -s localhost:9100/metrics | grep dcgm

# Sample output

# HELP dcgm_gpu_temp GPU temperature (in C).
# TYPE dcgm_gpu_temp gauge
dcgm_gpu_temp{container_name="pod1-ctr",gpu="0",pod_name="pod1",pod_namespace="default",uuid="GPU-2b399198-c670-a848-173b-d3400051a200"} 33
dcgm_gpu_temp{container_name="pod1-ctr",gpu="1",pod_name="pod1",pod_namespace="default",uuid="GPU-9567a9e7-341e-bb7e-fcf5-788d8caa50f9"} 34
# HELP dcgm_gpu_utilization GPU utilization (in %).
# TYPE dcgm_gpu_utilization gauge
dcgm_gpu_utilization{container_name="pod1-ctr",gpu="0",pod_name="pod1",pod_namespace="default",uuid="GPU-2b399198-c670-a848-173b-d3400051a200"} 0
dcgm_gpu_utilization{container_name="pod1-ctr",gpu="1",pod_name="pod1",pod_namespace="default",uuid="GPU-9567a9e7-341e-bb7e-fcf5-788d8caa50f9"} 0
# HELP dcgm_low_util_violation Throttling duration due to low utilization (in us).
# TYPE dcgm_low_util_violation counter
dcgm_low_util_violation{container_name="pod1-ctr",gpu="0",pod_name="pod1",pod_namespace="default",uuid="GPU-2b399198-c670-a848-173b-d3400051a200"} 0
dcgm_low_util_violation{container_name="pod1-ctr",gpu="1",pod_name="pod1",pod_namespace="default",uuid="GPU-9567a9e7-341e-bb7e-fcf5-788d8caa50f9"} 0
# HELP dcgm_mem_copy_utilization Memory utilization (in %).
# TYPE dcgm_mem_copy_utilization gauge
dcgm_mem_copy_utilization{container_name="pod1-ctr",gpu="0",pod_name="pod1",pod_namespace="default",uuid="GPU-2b399198-c670-a848-173b-d3400051a200"} 0
dcgm_mem_copy_utilization{container_name="pod1-ctr",gpu="1",pod_name="pod1",pod_namespace="default",uuid="GPU-9567a9e7-341e-bb7e-fcf5-788d8caa50f9"} 0
# HELP dcgm_memory_clock Memory clock frequency (in MHz).
# TYPE dcgm_memory_clock gauge
dcgm_memory_clock{container_name="pod1-ctr",gpu="0",pod_name="pod1",pod_namespace="default",uuid="GPU-2b399198-c670-a848-173b-d3400051a200"} 810
dcgm_memory_clock{container_name="pod1-ctr",gpu="1",pod_name="pod1",pod_namespace="default",uuid="GPU-9567a9e7-341e-bb7e-fcf5-788d8caa50f9"} 810
```

#### Docker Build and Run

```sh
$ docker build -t nvidia/pod-devices-exporter .

# Make sure to run dcgm-exporter
$ docker run -d --runtime=nvidia --rm --name=nvidia-dcgm-exporter nvidia/dcgm-exporter

$ docker run -d --privileged --rm -v /var/lib/kubelet/pod-resources:/var/lib/kubelet/pod-resources --volumes-from nvidia-dcgm-exporter:ro nvidia/pod-devices-exporter
```

#### Build and Run locally
```sh
$ git clone
$ cd src && go build
$ sudo ./src
```
