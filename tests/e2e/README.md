# End-to-End tests

The end-to-tests required to maintain a confidence, that the dcgm-exporter works as expected correctly 
after the changes. The tests aim to reproduce a typical deployment scenario on k8s environment and tests 
how does the following components work together on K8S environment:
* Helm package - helm package can deploy the specified dcgm-exporter image;
* Docker image - docker image contains all necessary components to run the dcgm-exporter;
* dcgm-exporter - binary executable starts, reads GPU metrics and produces expected results.

The basic test executes the following scenario:

1. Connect to the Kubernetes cluster;
2. Create a namespace;
3. Install the dcgm-exporter helm package;
4. The E2E test waits until the dcgm-exporter is up and running;
5. When the dcgm-exporter is up and running, the test deploys a pod, that runs a workload on GPU.
6. The test reads `/metrics` endpoint output and verifies that the GPU metrics available and contains labels, such as 
`namespace`, `container` and `pod`.

If there aren't any errors during execution of steps from 1 to 7, the end-to-end test is considered as passed.

New e2e tests can be added in a future.

## Prerequisites

1. NVIDIA GPU-compatible hardware for use with DCGM (Requirements: https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/getting-started.html)
2. Kubernetes cluster with configured NVIDIA container tool kit (https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/index.html).
For the development or local environments, it is recommended to use [minikube](https://minikube.sigs.k8s.io/) with configured GPU support: https://minikube.sigs.k8s.io/docs/tutorials/nvidia/.

## How run E2E tests

### Scenario: Test the current DCGM-exporter release

The scenario installs the dcgm-exporter with default configuration, defined in the helm package [values](https://github.com/NVIDIA/dcgm-exporter/blob/main/deployment/values.yaml).

```shell
KUBECONFIG="~/.kube/config" make e2e-test
```

### Scenario build images, deploy and test DCGM-exporter after changes

1. Build local images;

```shell
cd ../../ # go to the project root directory  
make local
```

2. Run tests

```shell
cd tests/e2e # back to the e2e test directory 

KUBECONFIG="~/.kube/config" IMAGE_REPOSITORY="nvidia/dcgm-exporter"  make e2e-test
```
