# Helm charts for GPU metrics

To collect and visualize NVIDIA GPU metrics in a Kubernetes cluster, use the provided Helm chart to deploy [DCGM-Exporter](https://github.com/nvidia/dcgm-exporter/).

For full instructions on setting up Prometheus (using `kube-prometheus-stack`) and Grafana with DCGM-Exporter, review the [documentation](https://docs.nvidia.com/datacenter/cloud-native/kubernetes/dcgme2e.html#gpu-telemetry)

#### Install Helm charts

First, install Helm v3 using the official script:

```console
curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 && \
    chmod 700 get_helm.sh && \
    ./get_helm.sh
```
Next, setup the Helm repo:

```console
helm repo add gpu-helm-charts \
    https://nvidia.github.io/dcgm-exporter/helm-charts
```
Update the repo:

```console
helm repo update
```

Install the official chart for DCGM-Exporter:

```console
helm install \
    --generate-name \
    gpu-helm-charts/dcgm-exporter
```

#### GPU Metrics Dashboard

We provide an official dashboard on Grafana: [https://grafana.com/grafana/dashboards/12239](https://grafana.com/grafana/dashboards/12239)
